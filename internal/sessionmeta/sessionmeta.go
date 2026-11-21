// Package sessionmeta 保存 dev-console 自己的 session 中繼資料：暱稱與封存紀錄。
//
// 為什麼不存在 tmux option：封存會結束 tmux session，已封存區仍要顯示暱稱與接手指令；
// Claude 以 --resume 重開後是新的 tmux session，暱稱也要能帶回。所以存成 DataDir 底下的 0600 JSON 檔
// （tmp＋rename 原子寫入），tmux option 只當 iTerm2 tab 標題的副本。
//
// key 慣例（由 snapshot 決定、這裡只當不透明字串）：
//   - "claude:<session id>"：Claude session，--resume 重開後同一個 key，暱稱自動帶回。
//   - "tmux:<session 名稱>"：tmux session（Codex、普通 shell，或 Claude 尚未知道 session id 時）。
//   - "proc:<pid>:<啟動 unix 秒>"：不在 tmux、又沒有 Claude session id 的唯讀卡片（Codex）。
//
// 暱稱：Claude 已知 session id 時只讀寫 claude:<sid>；tmux:<名稱> 只在 session id 還不知道時用，之後第一次對上
// session id 且 claude:<sid> 完全沒有紀錄時搬過去一次（搬完刪掉）。清空寫空字串墓碑，擋住備援 key 再被搬進來。
//
// 封存紀錄：tmux 卡片的紀錄 key 是 "tmux:<名稱>@<封存時間 ns>"，只當已封存區的歷史，不拿來隱藏卡片
// （同名新 session 要照常顯示）；只有唯讀卡片的紀錄（key＝proc:<pid>:<啟動秒>）會讓仍存活的同一程序維持隱藏。
package sessionmeta

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chiyum/remote-console/internal/validate"
)

// MaxNicknameRunes 是暱稱長度上限（以字元計）；手機卡片標題放得下、iTerm2 tab 也不會被擠爆。
const MaxNicknameRunes = 40

// maxNicknameEntries 是暱稱表上限：Claude session id 的 key 會隨 session 數累積，超過時淘汰最舊的。
const maxNicknameEntries = 2000

// ErrInvalid 標記使用者輸入錯誤（handler 回 400）。
var ErrInvalid = errors.New("invalid input")

// Nickname 是一筆暱稱（Value 為空字串＝已清空的墓碑）。
type Nickname struct {
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Archive 是一筆封存紀錄；session 已被結束後仍靠它顯示在已封存區。
type Archive struct {
	Key      string `json:"key"`
	Nickname string `json:"nickname,omitempty"`
	// Label 是原本的 tmux session 名稱或唯讀卡片名稱（沒有暱稱時當標題）。
	Label   string `json:"label"`
	Tool    string `json:"tool,omitempty"`
	Profile string `json:"profile,omitempty"`
	Dir     string `json:"dir,omitempty"`
	// Kind：tmux（已結束 tmux session）或 proc（唯讀卡片，只隱藏、程序不動）。
	Kind       string    `json:"kind"`
	SessionID  string    `json:"sessionId,omitempty"`
	ResumeCmd  string    `json:"resumeCommand,omitempty"`
	ArchivedAt time.Time `json:"archivedAt"`
}

type fileData struct {
	Version   int                 `json:"version"`
	Nicknames map[string]Nickname `json:"nicknames"`
	Archived  map[string]Archive  `json:"archived"`
}

// Store 是暱稱與封存紀錄的記憶體表＋檔案持久化；所有存取經 mu。
type Store struct {
	mu    sync.Mutex
	path  string // 空字串＝只在記憶體（測試用）
	data  fileData
	now   func() time.Time
	write func(path string, b []byte) error // 預設 writeAtomic；測試注入寫檔失敗
}

// Open 讀取既有檔案；檔案損毀時改名保留（不覆蓋使用者資料）並從空表開始。
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now, write: writeAtomic, data: fileData{Version: 1, Nicknames: map[string]Nickname{}, Archived: map[string]Archive{}}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	var d fileData
	if err := json.Unmarshal(b, &d); err != nil {
		bad := fmt.Sprintf("%s.bad-%d", path, time.Now().Unix())
		_ = os.Rename(path, bad)
		log.Printf("[sessionmeta] %s 解析失敗，已改名為 %s，從空表開始：%v", path, bad, err)
		return s, nil
	}
	if d.Nicknames != nil {
		s.data.Nicknames = d.Nicknames
	}
	if d.Archived != nil {
		s.data.Archived = d.Archived
	}
	return s, nil
}

// SetWriter 讓測試注入寫檔行為（例如模擬磁碟滿）。
func (s *Store) SetWriter(w func(path string, b []byte) error) {
	s.mu.Lock()
	s.write = w
	s.mu.Unlock()
}

// SetClock 讓測試控制時間。
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

// NormalizeNickname 去掉前後空白與控制／格式字元（含 bidi 覆寫），檢查長度與 tmux 不安全的結尾。
// 回傳空字串代表「清空暱稱」。
func NormalizeNickname(in string) (string, error) {
	if !utf8.ValidString(in) {
		return "", fmt.Errorf("%w: 暱稱不是合法 UTF-8", ErrInvalid)
	}
	var b strings.Builder
	for _, r := range in {
		// 換行、tab、ESC 等會弄亂 iTerm2 標題與 tmux 指令；U+202E 這類格式字元會讓畫面顯示與實際內容不同。
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > MaxNicknameRunes {
		return "", fmt.Errorf("%w: 暱稱最多 %d 個字", ErrInvalid, MaxNicknameRunes)
	}
	// tmux 以 argv 執行時，結尾是 ; 的參數會被當成指令分隔（實測 3.7c 會吃掉分號），寫進 @dc_nickname 會走樣。
	if strings.HasSuffix(out, ";") {
		return "", fmt.Errorf("%w: 暱稱結尾不可是分號", ErrInvalid)
	}
	return out, nil
}

// Resolve 回傳卡片目前的暱稱。primary 是權威 key（Claude 已知 session id 時是 claude:<sid>，否則是 tmux:／proc:）；
// fallback 只在 primary 完全沒有紀錄（連清空墓碑都沒有）時採用一次：把值搬到 primary 並刪掉 fallback。
// 搬完就刪是為了避免同一個 tmux 裡 /resume 切到另一段對話時，又把舊暱稱複製過去（reviewer 第 1 輪）。
func (s *Store) Resolve(primary, fallback string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.data.Nicknames[primary]; ok {
		return n.Value
	}
	if fallback == "" || fallback == primary {
		return ""
	}
	n, ok := s.data.Nicknames[fallback]
	if !ok || n.Value == "" {
		return ""
	}
	next := s.cloneLocked()
	next.Nicknames[primary] = n
	delete(next.Nicknames, fallback)
	// 寫檔失敗時記憶體不變，這次仍回傳 fallback 的值，下一輪再搬。
	_ = s.commitLocked(next)
	return n.Value
}

// SetNickname 設定 key 的暱稱（value 已經 NormalizeNickname；空字串＝清空，寫墓碑擋住 fallback 再被搬進來）。
func (s *Store) SetNickname(key, value string) error {
	if key == "" {
		return fmt.Errorf("%w: 沒有可用的 key", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cloneLocked()
	next.Nicknames[key] = Nickname{Value: value, UpdatedAt: s.now()}
	evict(next.Nicknames)
	return s.commitLocked(next)
}

// AddArchive 新增一筆封存紀錄，回傳實際存下的內容（含封存時間）。key 已存在時拒絕，不覆寫舊紀錄。
func (s *Store) AddArchive(a Archive) (Archive, error) {
	if a.Key == "" {
		return a, fmt.Errorf("%w: 封存紀錄缺 key", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.data.Archived[a.Key]; dup {
		return a, fmt.Errorf("%w: 這筆已封存", ErrInvalid)
	}
	if a.ArchivedAt.IsZero() {
		a.ArchivedAt = s.now()
	}
	next := s.cloneLocked()
	next.Archived[a.Key] = a
	return a, s.commitLocked(next)
}

// RemoveArchive 移除封存紀錄；不存在回 false（handler 回 404，不讓前端任意字串觸發寫檔）。
func (s *Store) RemoveArchive(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Archived[key]; !ok {
		return false, nil
	}
	next := s.cloneLocked()
	delete(next.Archived, key)
	if err := s.commitLocked(next); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveResumedClaude 移除「這段 Claude 對話以新的活著的卡片重新出現」的封存紀錄（A10），回傳移除的 key。
// keep(rec) 回 true 的不移除：呼叫端用它排除「封存時那個 proc 程序本身還活著」與「同名 tmux session 還沒被 kill 完」。
func (s *Store) RemoveResumedClaude(sessionID string, keep func(Archive) bool) []string {
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var drop []string
	for k, a := range s.data.Archived {
		if a.Tool == "claude" && a.SessionID == sessionID && !keep(a) {
			drop = append(drop, k)
		}
	}
	if len(drop) == 0 {
		return nil
	}
	next := s.cloneLocked()
	for _, k := range drop {
		delete(next.Archived, k)
	}
	if s.commitLocked(next) != nil {
		return nil
	}
	sort.Strings(drop)
	return drop
}

// IsArchived 判斷這個 key 是否有封存紀錄（總覽頁只拿它隱藏唯讀卡片）。
func (s *Store) IsArchived(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data.Archived[key]
	return ok
}

// Archived 回傳所有封存紀錄，最新封存的排前面。
func (s *Store) Archived() []Archive {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Archive, 0, len(s.data.Archived))
	for _, a := range s.data.Archived {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ArchivedAt.Equal(out[j].ArchivedAt) {
			return out[i].ArchivedAt.After(out[j].ArchivedAt)
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// PruneEphemeral 清掉已不存在的 tmux:／proc: 暱稱（session 結束後這兩種 key 不會再出現）；
// claude: key 要留給 --resume，只靠數量上限淘汰。呼叫端只在 tmux 與程序表都查詢成功時呼叫。
func (s *Store) PruneEphemeral(alive map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var dead []string
	for k := range s.data.Nicknames {
		if (strings.HasPrefix(k, "tmux:") || strings.HasPrefix(k, "proc:")) && !alive[k] {
			dead = append(dead, k)
		}
	}
	if len(dead) == 0 {
		return
	}
	next := s.cloneLocked()
	for _, k := range dead {
		delete(next.Nicknames, k)
	}
	_ = s.commitLocked(next)
}

// evict 超過上限時淘汰最舊的暱稱（墓碑優先，因為它們只用來擋舊值被搬回來）。
func evict(m map[string]Nickname) {
	over := len(m) - maxNicknameEntries
	if over <= 0 {
		return
	}
	type kv struct {
		k string
		n Nickname
	}
	all := make([]kv, 0, len(m))
	for k, n := range m {
		all = append(all, kv{k, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if (all[i].n.Value == "") != (all[j].n.Value == "") {
			return all[i].n.Value == ""
		}
		return all[i].n.UpdatedAt.Before(all[j].n.UpdatedAt)
	})
	for _, e := range all[:over] {
		delete(m, e.k)
	}
}

// cloneLocked 複製一份表；所有修改都改在複本上，寫檔成功才換上（寫檔失敗時記憶體與磁碟一致）。
func (s *Store) cloneLocked() fileData {
	next := fileData{Version: s.data.Version, Nicknames: make(map[string]Nickname, len(s.data.Nicknames)+1), Archived: make(map[string]Archive, len(s.data.Archived)+1)}
	for k, v := range s.data.Nicknames {
		next.Nicknames[k] = v
	}
	for k, v := range s.data.Archived {
		next.Archived[k] = v
	}
	return next
}

// commitLocked 先寫檔，成功才把 next 換成目前的表。
func (s *Store) commitLocked(next fileData) error {
	if s.path != "" {
		b, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		if err := s.write(s.path, b); err != nil {
			log.Printf("[sessionmeta] 寫入 %s 失敗（記憶體維持原狀）：%v", s.path, err)
			return err
		}
	}
	s.data = next
	return nil
}

// writeAtomic 以 0600 寫入（tmp＋rename），寫到一半當機也不會留下半份檔案。
func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dc-meta-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
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

var claudeSessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ResumeCommand 組出「在終端機重開這段對話」的 aiuse 指令；總覽頁的接手提示與封存紀錄共用這一份。
// profile／session id 來自 tmux option 或程序表，使用者會把指令貼進終端機，所以格式不合法就不給指令
// （避免 option 被寫成 `x; rm -rf ~` 後變成可複製的指令）。
//   - Claude：`aiuse claude <profile> --resume <session id>`；session id 未知時退回 `--resume`（Claude 會列清單讓你選）。
//   - Codex：`aiuse codex <profile> resume`（Codex 不回報 session id，由它列清單選）。
func ResumeCommand(tool, profile, sessionID string) string {
	if !validate.ProfileName(profile) {
		return ""
	}
	switch tool {
	case "claude":
		if claudeSessionIDRe.MatchString(sessionID) {
			return "aiuse claude " + profile + " --resume " + sessionID
		}
		return "aiuse claude " + profile + " --resume"
	case "codex":
		return "aiuse codex " + profile + " resume"
	}
	return ""
}
