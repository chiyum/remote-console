// Package authstore 管理「只有讀得到本機 0600 檔案的人才拿得到」的憑證。
//
// 為什麼不能只信 header：UI 只能走 TCP 127.0.0.1（App 版 Tailscale 在沙箱內，serve 連不到 unix socket，
// 實測 502），而本機任何程序——包括 Docker Desktop 容器經 host.docker.internal——都能直連 127.0.0.1
// 並偽造 Host、X-Forwarded-For、Tailscale-User-Login。所以身分一律落在檔案秘密上：
//   - 瀏覽器：用一次性配對碼（`dev-console pair` 寫進 0600 檔、10 分鐘、最多試 5 次）換 HttpOnly cookie。
//   - hook：每次帶 0600 hook-token 檔的內容。
//
// 伺服器只存 token 的 SHA-256；檔案都在 0700 的 DataDir，以 tmp + rename 原子寫入。
package authstore

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	pairFile     = "pair-code.json"
	sessionsFile = "sessions.json"
	hookFile     = "hook-token"
	// hookHeaderFile 是給 `curl -H @檔案` 用的 header 檔：token 不出現在 hook 指令的命令列（ps 看不到）。
	hookHeaderFile = "hook-header"
	// 配對碼字母表去掉易混淆的 0/O/1/I/L；10 碼約 2^49，加上每碼 5 次上限，無法暴力猜。
	codeAlphabet  = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	codeLen       = 10
	maxCodeTries  = 5
	maxSessions   = 20
	SessionMaxAge = 90 * 24 * time.Hour
	// LocalSessionMaxAge 是本機（dev-console.localhost）cookie 的效期：本機瀏覽器多半是驗收或臨時用，縮短暴露窗。
	LocalSessionMaxAge = 12 * time.Hour
	defaultPairTTL     = 10 * time.Minute
)

// 錯誤型別：handler 依此決定訊息，但一律不透露細節給 client。
var (
	ErrNoCode   = errors.New("沒有有效的配對碼（請在 Mac 執行 dev-console pair）")
	ErrBadCode  = errors.New("配對碼錯誤")
	ErrLockedUp = errors.New("錯誤次數過多，此配對碼已作廢")
)

type pairRecord struct {
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expiresAt"`
	Tries     int       `json:"tries"`
}

// Session 是一個已配對裝置（只存雜湊）。
type Session struct {
	Hash      string    `json:"hash"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// expired 以 ExpiresAt 為準；舊檔沒有此欄位時退回 CreatedAt＋SessionMaxAge。
func (x Session) expired(now time.Time) bool {
	if !x.ExpiresAt.IsZero() {
		return !now.Before(x.ExpiresAt)
	}
	return now.Sub(x.CreatedAt) >= SessionMaxAge
}

// Store 是檔案為準的憑證庫；sessions.json 被 CLI（revoke）改過時依 mtime 重新載入。
type Store struct {
	dir string
	now func() time.Time

	mu       sync.Mutex
	sessions map[string]Session
	modTime  time.Time
	hookHash [32]byte
	hookSet  bool
}

// Open 開啟（必要時建立）DataDir，並確保 hook token 存在。
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	s := &Store{dir: dir, now: time.Now, sessions: map[string]Session{}}
	tok, err := EnsureHookToken(dir)
	if err != nil {
		return nil, err
	}
	s.hookHash, s.hookSet = sha256.Sum256([]byte(tok)), true
	s.reloadLocked()
	return s, nil
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// writeAtomic 以 0600 寫入（tmp + rename）。
func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dc-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Chmod(0o600)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// EnsureHookToken 讀取或建立 hook token（0600），並確保對應的 curl header 檔存在且一致。
func EnsureHookToken(dir string) (string, error) {
	p := filepath.Join(dir, hookFile)
	t := ""
	if b, err := os.ReadFile(p); err == nil {
		if v := strings.TrimSpace(string(b)); len(v) >= 32 {
			t = v
		}
	}
	if t == "" {
		t = randToken(32)
		if err := writeAtomic(p, []byte(t+"\n")); err != nil {
			return "", err
		}
	}
	hdr := []byte("X-DC-Hook-Token: " + t + "\n")
	hp := filepath.Join(dir, hookHeaderFile)
	if b, err := os.ReadFile(hp); err != nil || string(b) != string(hdr) {
		if err := writeAtomic(hp, hdr); err != nil {
			return "", err
		}
	}
	return t, nil
}

// HookTokenOK 以常數時間比對 hook token。
func (s *Store) HookTokenOK(tok string) bool {
	if !s.hookSet || tok == "" {
		return false
	}
	h := sha256.Sum256([]byte(strings.TrimSpace(tok)))
	return subtle.ConstantTimeCompare(h[:], s.hookHash[:]) == 1
}

// IssuePairCode 產生一次性配對碼並寫入 0600 檔（CLI 呼叫；會取代舊碼）。
func IssuePairCode(dir string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = defaultPairTTL
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	raw := make([]byte, codeLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, b := range raw {
		if i == codeLen/2 {
			sb.WriteByte('-')
		}
		// 256 % 31 的偏差可忽略（只影響極少數字元機率，不影響不可猜性）。
		sb.WriteByte(codeAlphabet[int(b)%len(codeAlphabet)])
	}
	code := sb.String()
	rec, _ := json.Marshal(pairRecord{Hash: hashHex(normalizeCode(code)), ExpiresAt: time.Now().Add(ttl)})
	return code, writeAtomic(filepath.Join(dir, pairFile), rec)
}

func normalizeCode(c string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
}

// Redeem 驗證配對碼，成功回傳新 session token（單次使用）。錯誤 5 次即作廢該碼。
// maxAge 是這個 session 的效期（serve 90 天、本機 12 小時；0 代表最長效期）。
func (s *Store) Redeem(code, label string, maxAge time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := filepath.Join(s.dir, pairFile)
	b, err := os.ReadFile(p)
	if err != nil {
		return "", ErrNoCode
	}
	var rec pairRecord
	if json.Unmarshal(b, &rec) != nil || s.now().After(rec.ExpiresAt) || rec.Tries >= maxCodeTries {
		_ = os.Remove(p)
		return "", ErrNoCode
	}
	want, _ := hex.DecodeString(rec.Hash)
	got := sha256.Sum256([]byte(normalizeCode(code)))
	if len(want) != len(got) || subtle.ConstantTimeCompare(want, got[:]) != 1 {
		rec.Tries++
		if rec.Tries >= maxCodeTries {
			_ = os.Remove(p)
			return "", ErrLockedUp
		}
		nb, _ := json.Marshal(rec)
		_ = writeAtomic(p, nb)
		return "", ErrBadCode
	}
	_ = os.Remove(p) // 單次使用
	s.reloadLocked()
	tok := randToken(32)
	if len(label) > 120 {
		label = label[:120]
	}
	if maxAge <= 0 || maxAge > SessionMaxAge {
		maxAge = SessionMaxAge
	}
	s.sessions[hashHex(tok)] = Session{Hash: hashHex(tok), Label: label, CreatedAt: s.now(), ExpiresAt: s.now().Add(maxAge)}
	s.pruneLocked()
	if err := s.persistLocked(); err != nil {
		return "", err
	}
	return tok, nil
}

// Valid 檢查 cookie token 是否為有效 session。
func (s *Store) Valid(tok string) bool {
	if len(tok) != 64 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChangedLocked()
	sess, ok := s.sessions[hashHex(tok)]
	return ok && !sess.expired(s.now())
}

// RevokeAll 清掉所有已配對裝置（CLI 用）。
func RevokeAll(dir string) error {
	return writeAtomic(filepath.Join(dir, sessionsFile), []byte("[]"))
}

// List 列出已配對裝置（CLI 用）。
func List(dir string) []Session {
	b, err := os.ReadFile(filepath.Join(dir, sessionsFile))
	if err != nil {
		return nil
	}
	var out []Session
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Store) reloadIfChangedLocked() {
	fi, err := os.Stat(filepath.Join(s.dir, sessionsFile))
	if err != nil {
		if len(s.sessions) > 0 {
			s.sessions = map[string]Session{}
		}
		return
	}
	if !fi.ModTime().Equal(s.modTime) {
		s.reloadLocked()
	}
}

func (s *Store) reloadLocked() {
	p := filepath.Join(s.dir, sessionsFile)
	s.sessions = map[string]Session{}
	fi, err := os.Stat(p)
	if err != nil {
		return
	}
	s.modTime = fi.ModTime()
	for _, sess := range List(s.dir) {
		s.sessions[sess.Hash] = sess
	}
}

// pruneLocked 刪過期者；超過上限時丟最舊的。
func (s *Store) pruneLocked() {
	for h, sess := range s.sessions {
		if sess.expired(s.now()) {
			delete(s.sessions, h)
		}
	}
	for len(s.sessions) > maxSessions {
		var oldest string
		for h, sess := range s.sessions {
			if oldest == "" || sess.CreatedAt.Before(s.sessions[oldest].CreatedAt) {
				oldest = h
			}
		}
		delete(s.sessions, oldest)
	}
}

func (s *Store) persistLocked() error {
	list := make([]Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		list = append(list, sess)
	}
	b, _ := json.Marshal(list)
	p := filepath.Join(s.dir, sessionsFile)
	if err := writeAtomic(p, b); err != nil {
		return err
	}
	if fi, err := os.Stat(p); err == nil {
		s.modTime = fi.ModTime()
	}
	return nil
}
