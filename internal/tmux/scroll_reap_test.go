package tmux

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
)

// scrollTestPrefix 是整合測試隔離 tmux server 的名稱前綴；名稱後綴是建立它的測試程序 pid。
const scrollTestPrefix = "dc-scrolltest-"

var scrollTestNameRe = regexp.MustCompile(`^` + scrollTestPrefix + `([0-9]{1,9})$`)

// TestMain 先清掉以前被 SIGKILL／逾時中斷而殘留的隔離 server，再跑測試。
func TestMain(m *testing.M) {
	reapStaleScrollTestServers(tmuxSocketDir(), pidAlive, func(name string) {
		_, _ = isoRunner{name: name}.Run(context.Background(), "kill-server")
	})
	os.Exit(m.Run())
}

// tmuxSocketDir 與 tmux 找 -L socket 的規則相同：$TMUX_TMPDIR（預設 /tmp）下的 tmux-<uid>。
func tmuxSocketDir() string {
	base := os.Getenv("TMUX_TMPDIR")
	if base == "" {
		base = "/tmp"
	}
	return filepath.Join(base, "tmux-"+strconv.Itoa(os.Getuid()))
}

// pidAlive 回報程序是否還在；EPERM 代表存在但不是自己的，同樣算還在（寧可不清）。
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// reapStaleScrollTestServers 只處理名稱完全符合 dc-scrolltest-<pid>、是 unix socket、且 pid 已不存在的項目：
// 先 kill 那個 -L server（若還活著），再刪 socket。其他 socket（default、別的 session 的 -L 名稱）一律不碰；
// pid 被重用時會被當成「還在」而略過，方向是安全的。回傳清掉的名稱（測試用）。
func reapStaleScrollTestServers(dir string, alive func(int) bool, kill func(name string)) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var reaped []string
	for _, e := range entries {
		m := scrollTestNameRe.FindStringSubmatch(e.Name())
		if m == nil || e.Type()&os.ModeSocket == 0 {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid == os.Getpid() || alive(pid) {
			continue
		}
		kill(e.Name())
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil || errors.Is(err, os.ErrNotExist) {
			reaped = append(reaped, e.Name())
		}
	}
	return reaped
}

func TestReapStaleScrollTestServersOnlyTouchesDeadOwnPrefix(t *testing.T) {
	// unix socket 路徑長度有限（macOS 104 bytes），不用 t.TempDir() 的長路徑。
	dir, err := os.MkdirTemp("/tmp", "dcr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := func(name string) {
		ln, err := net.Listen("unix", filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		// 關掉 listener 但保留檔案，模擬「server 已死、socket 檔還在」。
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = ln.Close()
	}
	mine := scrollTestPrefix + strconv.Itoa(os.Getpid())
	for _, n := range []string{scrollTestPrefix + "111", scrollTestPrefix + "222", mine, "default", "other-session-83344", scrollTestPrefix + "333-x", scrollTestPrefix + "abc", "x" + scrollTestPrefix + "444"} {
		sock(n)
	}
	// 同名但不是 socket 的一般檔不碰。
	if err := os.WriteFile(filepath.Join(dir, scrollTestPrefix+"555"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{222: true}
	var killed []string
	got := reapStaleScrollTestServers(dir, func(pid int) bool { return alive[pid] }, func(n string) { killed = append(killed, n) })
	if len(got) != 1 || got[0] != scrollTestPrefix+"111" || len(killed) != 1 || killed[0] != scrollTestPrefix+"111" {
		t.Fatalf("只應清 pid 已不存在的 dc-scrolltest-111：清了 %v、kill 了 %v", got, killed)
	}
	for _, n := range []string{scrollTestPrefix + "222", mine, "default", "other-session-83344", scrollTestPrefix + "333-x", scrollTestPrefix + "abc", "x" + scrollTestPrefix + "444", scrollTestPrefix + "555"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s 不應被刪：%v", n, err)
		}
	}
}

func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatal("自己的 pid 應為存在")
	}
}
