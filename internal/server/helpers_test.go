package server

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeExecutable 在 dir 底下建立一個可執行的假檔案並回傳絕對路徑。
// 「有沒有 aiuse」與直接模式找 claude／codex 都以檔案是否存在且可執行判斷，測試要用真檔案才走得到對應分支。
func fakeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}
