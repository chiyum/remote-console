package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadTasksOrderAndProgress(t *testing.T) {
	claude := t.TempDir()
	state := filepath.Join(claude, "state")
	acc := filepath.Join(claude, "acceptance")
	_ = os.MkdirAll(state, 0o755)
	_ = os.MkdirAll(filepath.Join(acc, "t1", "evidence"), 0o755)
	_ = os.WriteFile(filepath.Join(acc, "t1.md"), []byte("### A1\n### A2\n"), 0o644)
	_ = os.WriteFile(filepath.Join(acc, "t1", "evidence", "A2-x.txt"), nil, 0o644)

	_ = os.WriteFile(filepath.Join(state, "a.json"), []byte(`{"task":"a","status":"running","acceptance":"acceptance/t1.md"}`), 0o644)
	_ = os.WriteFile(filepath.Join(state, "b.json"), []byte(`{"task":"b","status":"awaiting_user","next_action":"問使用者"}`), 0o644)
	_ = os.WriteFile(filepath.Join(state, "old.json"), []byte(`{"task":"old","status":"done"}`), 0o644)
	old := time.Now().Add(-100 * time.Hour)
	_ = os.Chtimes(filepath.Join(state, "old.json"), old, old)
	_ = os.WriteFile(filepath.Join(state, "bad.json"), []byte(`not json`), 0o644)

	got, bad := LoadTasks(state, claude)
	if len(bad) != 1 || !strings.HasPrefix(bad[0], "bad.json：") {
		t.Fatalf("壞掉的 JSON 應回報檔名：%v", bad)
	}
	if len(got) != 3 {
		t.Fatalf("got %d tasks: %+v", len(got), got)
	}
	if got[0].Task != "b" || got[0].Status != "awaiting_user" {
		t.Fatalf("awaiting_user 應排第一：%+v", got[0])
	}
	if got[2].Task != "old" {
		t.Fatalf("舊的 done 也要列出（由前端摺疊）：%+v", got)
	}
	if p := got[1].Progress; p == nil || p.Items != 2 || p.Verified != 1 {
		t.Fatalf("progress=%+v", p)
	}
}

func TestWatchdog(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "watchdog.log"), []byte("[2026-10-23 10:00:00] a\n[2026-10-23 16:06:02] last line\n\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".last-sweep"), nil, 0o644)
	w := LoadWatchdog(dir, time.UTC)
	if w.LastLine != "[2026-10-23 16:06:02] last line" || w.LastLineAt == nil || w.LastLineAt.Hour() != 16 {
		t.Fatalf("got %+v", w)
	}
	if w.LastSweepAt == nil {
		t.Fatal("缺 last sweep")
	}
}
