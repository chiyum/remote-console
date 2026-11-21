package sessionmeta

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeNickname(t *testing.T) {
	ok := map[string]string{
		"  客服 bug 調查  ":                        "客服 bug 調查",
		"":                                     "",
		"   ":                                  "",
		"a\nb\tc\x1b[31m":                      "abc[31m", // 換行、tab、ESC 被拿掉
		"‮evil":                                "evil",    // bidi 覆寫（格式字元）被拿掉
		"<img src=x onerror=alert(1)><b>粗</b>": "<img src=x onerror=alert(1)><b>粗</b>",
		"#(touch /tmp/x) #{session_name}":      "#(touch /tmp/x) #{session_name}", // tmux 標題只引用 option，不會展開
		strings.Repeat("字", MaxNicknameRunes):  strings.Repeat("字", MaxNicknameRunes),
		"a;b":                                  "a;b",
	}
	for in, want := range ok {
		got, err := NormalizeNickname(in)
		if err != nil || got != want {
			t.Errorf("NormalizeNickname(%q) = %q, %v；want %q", in, got, err, want)
		}
	}
	for _, in := range []string{strings.Repeat("字", MaxNicknameRunes+1), "結尾分號;", "x;", "\xff\xfe"} {
		if _, err := NormalizeNickname(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("NormalizeNickname(%q) 應回 ErrInvalid，實際 %v", in, err)
		}
	}
}

func TestNicknamePrimaryKeyWithOneTimeFallback(t *testing.T) {
	s, _ := Open("")
	// session id 還不知道時設在 tmux key；之後認出 session id，claude key 沒紀錄 → 搬過去一次並刪掉 tmux key。
	_ = s.SetNickname("tmux:T1", "先設的")
	if got := s.Resolve("claude:A", "tmux:T1"); got != "先設的" {
		t.Fatalf("claude key 沒紀錄時應採用 tmux 備援，got %q", got)
	}
	if got := s.Resolve("tmux:T1", ""); got != "" {
		t.Fatalf("搬過去後 tmux 備援應刪掉，got %q", got)
	}
	// 同一個 tmux 裡 /resume 切到已有暱稱的對話 B：B 維持自己的暱稱，不被 A 改寫。
	_ = s.SetNickname("claude:B", "對話 B")
	_ = s.SetNickname("tmux:T1", "殘留")
	if got := s.Resolve("claude:B", "tmux:T1"); got != "對話 B" {
		t.Fatalf("已有暱稱的 claude key 不得被 tmux 備援蓋掉，got %q", got)
	}
	if got := s.Resolve("claude:A", "tmux:T9"); got != "先設的" {
		t.Fatalf("A 的暱稱不應被改，got %q", got)
	}
	// 清空寫墓碑：之後備援 key 有值也不搬進來。
	_ = s.SetNickname("claude:A", "")
	_ = s.SetNickname("tmux:T2", "別的")
	if got := s.Resolve("claude:A", "tmux:T2"); got != "" {
		t.Fatalf("清空後不應被備援 key 帶回，got %q", got)
	}
	// --resume 回來（新的 tmux 名稱、同一個 session id）：暱稱帶回。
	if got := s.Resolve("claude:B", "tmux:T3"); got != "對話 B" {
		t.Fatalf("resume 後應帶回暱稱，got %q", got)
	}
}

func TestWriteFailureLeavesMemoryUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-meta.json")
	s, _ := Open(path)
	_ = s.SetNickname("claude:A", "原本")
	_, _ = s.AddArchive(Archive{Key: "tmux:keep@1", Kind: "tmux"})
	s.SetWriter(func(string, []byte) error { return errors.New("磁碟滿") })
	if err := s.SetNickname("claude:A", "新的"); err == nil {
		t.Fatal("寫檔失敗應回錯誤")
	}
	if _, err := s.AddArchive(Archive{Key: "tmux:x@2", Kind: "tmux"}); err == nil {
		t.Fatal("寫檔失敗應回錯誤")
	}
	if ok, err := s.RemoveArchive("tmux:keep@1"); ok || err == nil {
		t.Fatal("寫檔失敗時取消封存應回錯誤")
	}
	if s.Resolve("claude:A", "") != "原本" || s.IsArchived("tmux:x@2") || !s.IsArchived("tmux:keep@1") {
		t.Fatal("寫檔失敗時記憶體必須維持原狀")
	}
	// 重開檔案也一樣（磁碟上沒有半套狀態）。
	s2, _ := Open(path)
	if s2.Resolve("claude:A", "") != "原本" || len(s2.Archived()) != 1 {
		t.Fatal("磁碟內容應維持寫檔失敗前的狀態")
	}
}

func TestRemoveResumedClaude(t *testing.T) {
	s, _ := Open("")
	_, _ = s.AddArchive(Archive{Key: "tmux:a@1", Kind: "tmux", Tool: "claude", SessionID: "S", Label: "a"})
	_, _ = s.AddArchive(Archive{Key: "proc:9:1", Kind: "proc", Tool: "claude", SessionID: "S"})
	_, _ = s.AddArchive(Archive{Key: "tmux:c@1", Kind: "tmux", Tool: "codex"})
	got := s.RemoveResumedClaude("S", func(a Archive) bool { return a.Key == "proc:9:1" })
	if len(got) != 1 || got[0] != "tmux:a@1" || !s.IsArchived("proc:9:1") || !s.IsArchived("tmux:c@1") {
		t.Fatalf("只應移除同 session id、未被 keep 的 Claude 紀錄：%v", got)
	}
	if s.RemoveResumedClaude("", func(Archive) bool { return false }) != nil {
		t.Fatal("沒有 session id 不應移除任何紀錄")
	}
}

func TestPersistAtomic0600AndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-meta.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetNickname("claude:S", "暱稱")
	_, _ = s.AddArchive(Archive{Key: "tmux:T1", Label: "T1", Kind: "tmux", ResumeCmd: "aiuse claude second --resume S"})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("檔案權限應為 0600，實際 %v", fi.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".dc-meta-*")); len(left) != 0 {
		t.Fatalf("不應殘留暫存檔：%v", left)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Resolve("claude:S", ""); got != "暱稱" {
		t.Fatalf("重開後暱稱應還在，got %q", got)
	}
	if !s2.IsArchived("tmux:T1") || len(s2.Archived()) != 1 || s2.Archived()[0].ArchivedAt.IsZero() {
		t.Fatalf("重開後封存紀錄應還在且有封存時間：%+v", s2.Archived())
	}
}

func TestCorruptFileIsKeptAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-meta.json")
	_ = os.WriteFile(path, []byte("{壞掉"), 0o600)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Archived()) != 0 {
		t.Fatal("損毀檔應從空表開始")
	}
	if bad, _ := filepath.Glob(path + ".bad-*"); len(bad) != 1 {
		t.Fatalf("損毀檔應改名保留，不得覆蓋：%v", bad)
	}
}

func TestArchiveRemoveAndOrder(t *testing.T) {
	s, _ := Open("")
	_, _ = s.AddArchive(Archive{Key: "tmux:old", Kind: "tmux", ArchivedAt: time.Unix(100, 0)})
	_, _ = s.AddArchive(Archive{Key: "proc:1:2", Kind: "proc", ArchivedAt: time.Unix(200, 0)})
	if l := s.Archived(); len(l) != 2 || l[0].Key != "proc:1:2" {
		t.Fatalf("最新封存應排前面：%+v", l)
	}
	if _, err := s.AddArchive(Archive{Key: "tmux:old", Kind: "tmux"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("同 key 已存在時不得覆寫舊紀錄")
	}
	if ok, _ := s.RemoveArchive("tmux:nope"); ok {
		t.Fatal("不存在的 key 應回 false")
	}
	if ok, err := s.RemoveArchive("tmux:old"); !ok || err != nil || s.IsArchived("tmux:old") {
		t.Fatal("取消封存應移除紀錄")
	}
	if _, err := s.AddArchive(Archive{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("缺 key 的紀錄應拒絕")
	}
}

func TestPruneEphemeralKeepsClaudeKeys(t *testing.T) {
	s, _ := Open("")
	for _, k := range []string{"claude:S", "tmux:dead", "proc:1:2"} {
		_ = s.SetNickname(k, "x")
	}
	_ = s.SetNickname("tmux:alive", "y")
	s.PruneEphemeral(map[string]bool{"tmux:alive": true})
	if s.Resolve("tmux:dead", "") != "" || s.Resolve("proc:1:2", "") != "" {
		t.Fatal("已消失的 tmux:／proc: 暱稱應清掉")
	}
	if s.Resolve("claude:S", "") != "x" || s.Resolve("tmux:alive", "") != "y" {
		t.Fatal("claude: 與仍存活的 key 不能清")
	}
}

func TestEvictOldestOverCap(t *testing.T) {
	s, _ := Open("")
	base := time.Unix(0, 0)
	i := 0
	s.SetClock(func() time.Time { i++; return base.Add(time.Duration(i) * time.Second) })
	for n := 0; n < maxNicknameEntries+5; n++ {
		_ = s.SetNickname(fmt.Sprintf("claude:%d", n), "v")
	}
	s.mu.Lock()
	n := len(s.data.Nicknames)
	_, oldest := s.data.Nicknames["claude:0"]
	_, newest := s.data.Nicknames[fmt.Sprintf("claude:%d", maxNicknameEntries+4)]
	s.mu.Unlock()
	if n > maxNicknameEntries || oldest || !newest {
		t.Fatalf("暱稱表應被限制在 %d 筆內且淘汰最舊的，實際 %d（最舊還在=%v，最新在=%v）", maxNicknameEntries, n, oldest, newest)
	}
}

func TestResumeCommand(t *testing.T) {
	sid := "aaaaaaaa-0000-4000-8000-000000000001"
	cases := []struct{ tool, profile, sid, want string }{
		{"claude", "second", sid, "aiuse claude second --resume " + sid},
		{"claude", "second", "", "aiuse claude second --resume"},
		{"claude", "second", "not-a-uuid; rm -rf ~", "aiuse claude second --resume"},
		{"codex", "alpha", "", "aiuse codex alpha resume"},
		{"codex", "x; rm -rf ~", "", ""},
		{"claude", "", sid, ""},
		{"", "second", "", ""},
	}
	for _, c := range cases {
		if got := ResumeCommand(c.tool, c.profile, c.sid); got != c.want {
			t.Errorf("ResumeCommand(%q,%q,%q) = %q；want %q", c.tool, c.profile, c.sid, got, c.want)
		}
	}
}
