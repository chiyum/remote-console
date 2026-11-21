package authstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPairRedeemSingleUseAndPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	code, err := IssuePairCode(dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{pairFile, hookFile, hookHeaderFile} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s 權限應為 0600：%v %v", f, fi.Mode(), err)
		}
	}
	// 小寫、去掉連字號也接受。
	tok, err := s.Redeem("  "+lower(code)+" ", "test", 0)
	if err != nil || !s.Valid(tok) {
		t.Fatalf("redeem 失敗：%v", err)
	}
	if _, err := s.Redeem(code, "again", 0); err != ErrNoCode {
		t.Fatalf("配對碼應單次使用，got %v", err)
	}
	if s.Valid("x") || s.Valid(randToken(32)) {
		t.Fatal("亂數 token 不應有效")
	}
	// 另一個 Store（模擬服務重啟）讀得到 session。
	s2, _ := Open(dir)
	if !s2.Valid(tok) {
		t.Fatal("session 應持久化")
	}
	// CLI revoke 後，執行中的 Store 依 mtime 重新載入而失效。
	time.Sleep(10 * time.Millisecond)
	if err := RevokeAll(dir); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	_ = os.Chtimes(filepath.Join(dir, sessionsFile), future, future)
	if s.Valid(tok) {
		t.Fatal("revoke 後應失效")
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestBruteForceLocksCode(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	code, _ := IssuePairCode(dir, time.Minute)
	for i := 0; i < maxCodeTries-1; i++ {
		if _, err := s.Redeem("AAAAA-AAAAA", "", 0); err != ErrBadCode {
			t.Fatalf("第 %d 次應 ErrBadCode：%v", i, err)
		}
	}
	if _, err := s.Redeem("AAAAA-AAAAA", "", 0); err != ErrLockedUp {
		t.Fatalf("第 5 次應作廢：%v", err)
	}
	if _, err := s.Redeem(code, "", 0); err != ErrNoCode {
		t.Fatalf("作廢後正確碼也不能用：%v", err)
	}
}

func TestExpiredCode(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	code, _ := IssuePairCode(dir, time.Minute)
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := s.Redeem(code, "", 0); err != ErrNoCode {
		t.Fatalf("過期碼應拒絕：%v", err)
	}
}

func TestHookToken(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	tok, _ := EnsureHookToken(dir)
	if !s.HookTokenOK(tok) || !s.HookTokenOK(tok+"\n") || s.HookTokenOK("") || s.HookTokenOK("nope") {
		t.Fatal("hook token 比對錯")
	}
	again, _ := EnsureHookToken(dir)
	if again != tok {
		t.Fatal("已存在的 token 不應被換掉")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, hookHeaderFile)); string(b) != "X-DC-Hook-Token: "+tok+"\n" {
		t.Fatalf("header 檔內容錯：%q", b)
	}
}

func TestLocalSessionExpires(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	code, _ := IssuePairCode(dir, time.Minute)
	tok, err := s.Redeem(code, "local", LocalSessionMaxAge)
	if err != nil || !s.Valid(tok) {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().Add(LocalSessionMaxAge + time.Minute) }
	if s.Valid(tok) {
		t.Fatal("本機 session 超過 12 小時應失效")
	}
}
