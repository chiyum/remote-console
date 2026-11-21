package config

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 不合法的 ttyd 監看設定要用預設值並寫 log，不可靜默。
func TestTtydWatchFromEnv(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	cases := []struct {
		name     string
		in       map[string]string
		interval time.Duration
		restart  int
		nofile   int
		logHas   string
	}{
		{"預設", map[string]string{}, 60 * time.Second, 2048, 8192, ""},
		{"合法", map[string]string{"DC_TTYD_WATCH_INTERVAL": "10s", "DC_TTYD_FD_RESTART": "20", "DC_TTYD_NOFILE": "4096"}, 10 * time.Second, 20, 4096, ""},
		{"關閉", map[string]string{"DC_TTYD_WATCH_INTERVAL": "0"}, 0, 2048, 8192, ""},
		{"少單位", map[string]string{"DC_TTYD_WATCH_INTERVAL": "10"}, 60 * time.Second, 2048, 8192, "不是 Go duration"},
		{"太短", map[string]string{"DC_TTYD_WATCH_INTERVAL": "1s"}, 5 * time.Second, 2048, 8192, "太短"},
		{"門檻 0", map[string]string{"DC_TTYD_FD_RESTART": "0"}, 60 * time.Second, 2048, 8192, "不是正整數"},
		{"門檻不小於上限", map[string]string{"DC_TTYD_FD_RESTART": "9000"}, 60 * time.Second, 9000, 8192, "不小於"},
	}
	for _, c := range cases {
		buf.Reset()
		iv, r, n := ttydWatchFromEnv(env(c.in))
		if iv != c.interval || r != c.restart || n != c.nofile {
			t.Errorf("%s：得 %v %d %d", c.name, iv, r, n)
		}
		if c.logHas == "" && buf.Len() != 0 {
			t.Errorf("%s：不應寫 log：%s", c.name, buf.String())
		}
		if c.logHas != "" && !strings.Contains(buf.String(), c.logHas) {
			t.Errorf("%s：log 應含 %q，得 %q", c.name, c.logHas, buf.String())
		}
	}
}

// 「有沒有 aiuse」只看設定路徑是否為存在、可執行的一般檔案；不存在、沒有執行權、是目錄都算直接模式。
func TestAiuseAvailable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "aiuse")
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{exe: true, link: true, plain: false, dir: false, filepath.Join(dir, "missing"): false, "": false} {
		if got := (Config{AiusePath: p}).AiuseAvailable(); got != want {
			t.Errorf("AiuseAvailable(%q)=%v want %v", p, got, want)
		}
	}
}

// ToolBin 從 SessionPath 找執行檔：略過相對路徑項目與沒有執行權的同名檔；找不到回 ErrToolNotFound。
func TestToolBin(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "claude"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(b, "claude")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Config{SessionPath: "relative:" + a + "::" + b}
	if got, err := c.ToolBin("claude"); err != nil || got != want {
		t.Fatalf("ToolBin=%q %v want %q", got, err, want)
	}
	if _, err := c.ToolBin("codex"); !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("找不到 codex 應回 ErrToolNotFound：%v", err)
	}
	c.ClaudeBin = filepath.Join(a, "claude") // 存在但不可執行
	if _, err := c.ToolBin("claude"); !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("覆寫路徑不可執行應回 ErrToolNotFound：%v", err)
	}
}
