package validate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func setupRoot(t *testing.T) (home, root string) {
	t.Helper()
	home = t.TempDir()
	root = filepath.Join(home, ".ai-profiles")
	for _, p := range []string{"claude/work", "claude/shared", "codex/alpha", "codex/--help", "codex/Zz", "codex/a_b", "codex/_x"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home, root
}

func TestProfile(t *testing.T) {
	_, root := setupRoot(t)
	cases := []struct {
		tool, name string
		ok         bool
	}{
		{"claude", "work", true},
		{"codex", "alpha", true},
		{"claude", "shared", false},
		{"codex", "--help", false},
		{"claude", "work;touch /tmp/dc-pwn", false},
		{"claude", "../work", false},
		{"claude", "notexist", false},
		{"bash", "work", false},
		{"claude", "WORK", false},
		{"codex", "work", false},
		// 與 aiuse 同規則：大寫、底線可用，但必須是目錄原樣名稱（檔案系統不分大小寫）。
		{"codex", "Zz", true},
		{"codex", "a_b", true},
		{"codex", "zz", false},
		{"codex", "ALPHA", false},
		{"claude", "Shared", false},
		{"codex", "_x", true},
	}
	for _, c := range cases {
		err := Profile(root, c.tool, c.name)
		if (err == nil) != c.ok {
			t.Errorf("Profile(%q,%q) err=%v want ok=%v", c.tool, c.name, err, c.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("Profile error 應包 ErrInvalid：%v", err)
		}
	}
}

func TestListProfilesSkipsReserved(t *testing.T) {
	_, root := setupRoot(t)
	got := ListProfiles(root, "claude")
	if len(got) != 1 || got[0] != "work" {
		t.Fatalf("got %v", got)
	}
	if got := ListProfiles(root, "codex"); len(got) != 4 || got[0] != "Zz" || got[1] != "_x" || got[2] != "a_b" || got[3] != "alpha" {
		t.Fatalf("codex got %v", got)
	}
}

func TestDir(t *testing.T) {
	home, _ := setupRoot(t)
	realHome, _ := filepath.EvalSymlinks(home)
	sub := filepath.Join(home, "proj")
	_ = os.Mkdir(sub, 0o755)
	outside := t.TempDir()
	_ = os.Symlink(outside, filepath.Join(home, "escape"))

	ok := map[string]string{
		"":       realHome,
		"~":      realHome,
		"~/proj": filepath.Join(realHome, "proj"),
		sub:      filepath.Join(realHome, "proj"),
	}
	for in, want := range ok {
		got, err := Dir(home, in)
		if err != nil || got != want {
			t.Errorf("Dir(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	bad := []string{
		"/nonexistent",
		"~/$(touch /tmp/dc-pwn2)",
		"/tmp;touch /tmp/dc-pwn3",
		"/tmp",
		"relative/path",
		"~/escape", // symlink 指到 home 外
		"~/../",
	}
	for _, in := range bad {
		if _, err := Dir(home, in); err == nil {
			t.Errorf("Dir(%q) 應被拒絕", in)
		}
	}
}

func TestSessionNameAndPane(t *testing.T) {
	for _, s := range []string{"dc-a3", "aiuse-claude-work-20261023T010203Z-abcdef", "rc-work"} {
		if !SessionName(s) {
			t.Errorf("%q 應合法", s)
		}
	}
	for _, s := range []string{"", "a;b", "a b", "$(x)", "a:b", "a.b", "../x", "-t"} {
		if SessionName(s) {
			t.Errorf("%q 應不合法", s)
		}
	}
	if !PaneID("%12") || PaneID("12") || PaneID("%1;x") {
		t.Error("PaneID 規則錯")
	}
}
