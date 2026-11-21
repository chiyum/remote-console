package progress

import (
	"os"
	"path/filepath"
	"testing"
)

func TestItemIDs(t *testing.T) {
	md := []byte("# t\n### A1 x\n### A2 y\n#### A3 不算\n ### A4 不算（前有空白）\n### B1 算\n### A10 z\n### A1 重複\n")
	got := ItemIDs(md)
	want := []string{"A1", "A2", "B1", "A10"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestHasEvidence(t *testing.T) {
	cases := []struct {
		name, id string
		ok       bool
	}{
		{"A1-a.png", "A1", true},
		{"qa-A3-x.png", "A3", true},
		{"pm-A12-y.txt", "A12", true},
		{"qa-live-A6-m4.txt", "A6", true},
		{"DATA10-x.txt", "A10", false}, // 前一個字元是英數
		{"A12-x.txt", "A1", false},     // A1 不得誤配 A12
		{"A1x.txt", "A1", false},
		{"xDATA1-y_A1-z.txt", "A1", true}, // 第一個命中被擋，後面的合法命中仍算
	}
	for _, c := range cases {
		if got := HasEvidence(c.name, c.id); got != c.ok {
			t.Errorf("HasEvidence(%q,%q)=%v want %v", c.name, c.id, got, c.ok)
		}
	}
}

func TestLoadAndEvidence(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "task.md")
	_ = os.WriteFile(md, []byte("### A1\n### A2\n### A3\n### A10\n"), 0o644)
	ev := EvidenceDir(md)
	_ = os.MkdirAll(filepath.Join(ev, "pm-A10-dir"), 0o755) // 目錄不算
	for _, f := range []string{"A1-a.png", "qa-A1-b.txt", "pm-A3-x.txt", "DATA10-z.txt", "notes.txt"} {
		_ = os.WriteFile(filepath.Join(ev, f), nil, 0o644)
	}
	p := Load(md)
	if p == nil || p.Items != 4 || p.Verified != 2 {
		t.Fatalf("got %+v（應為 4 條、A1 與 A3 有證據）", p)
	}
	if Load(filepath.Join(dir, "none.md")) != nil {
		t.Fatal("不存在的清單應回 nil")
	}
}

func TestResolvePath(t *testing.T) {
	if got := ResolvePath("/h/.claude", "acceptance/x.md"); got != "/h/.claude/acceptance/x.md" {
		t.Fatal(got)
	}
	if got := ResolvePath("/h/.claude", "/abs/x.md"); got != "/abs/x.md" {
		t.Fatal(got)
	}
	if ResolvePath("/h", "") != "" {
		t.Fatal("空字串應回空")
	}
}
