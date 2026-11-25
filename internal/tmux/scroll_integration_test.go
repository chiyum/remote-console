package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// isoRunner 只對測試自己的 tmux server（-L <name>）下指令，並清掉 TMUX／TMUX_PANE：
// 測試若在 tmux 裡跑，tmux 會優先用 $TMUX 指到使用者正在用的 server，只靠 TMUX_TMPDIR 隔不開。
type isoRunner struct{ name string }

func (r isoRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-L", r.name, "-f", "/dev/null"}, args...)...)
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TMUX=") && !strings.HasPrefix(e, "TMUX_PANE=") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// newIsoTmux 起一個只給本測試用的 tmux server；收尾只 kill 這個 -L server。
func newIsoTmux(t *testing.T) isoRunner {
	t.Helper()
	if testing.Short() {
		t.Skip("short 模式略過真 tmux 測試")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("沒有 tmux")
	}
	r := isoRunner{name: fmt.Sprintf("dc-scrolltest-%d", os.Getpid())}
	t.Cleanup(func() {
		// kill-server 不會刪 socket 檔；先問出自己這個 server 的路徑，結束後刪掉。
		sock, _ := r.Run(context.Background(), "display-message", "-p", "#{socket_path}")
		_, _ = r.Run(context.Background(), "kill-server")
		if sock = strings.TrimSpace(sock); strings.HasSuffix(sock, "/"+r.name) {
			_ = os.Remove(sock)
		}
	})
	return r
}

// waitFor 輪詢 cond 直到成立或逾時。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

func TestScrollRealTmuxShellUsesCopyMode(t *testing.T) {
	r := newIsoTmux(t)
	ctx := context.Background()
	if _, err := r.Run(ctx, "new-session", "-d", "-s", "sh", "-x", "100", "-y", "40", "--", "sh", "-c", "i=1; while [ $i -le 200 ]; do echo L$i; i=$((i+1)); done; exec cat"); err != nil {
		t.Fatal(err)
	}
	// 負向對照：這是全新的隔離 server，只能看到本測試建的 session，不得出現使用者的任何 session。
	out, _ := r.Run(ctx, "list-sessions", "-F", "#{session_name}")
	if strings.Contains(out, "aiuse-") || strings.TrimSpace(out) != "sh" {
		t.Fatalf("隔離 server 不乾淨：%q", out)
	}
	id := strings.TrimSpace(mustRun(t, r, "display-message", "-p", "-t", "=sh:", "#{session_id}"))
	pos := func() string {
		return strings.TrimSpace(mustRun(t, r, "display-message", "-p", "-t", id, "#{pane_in_mode}:#{scroll_position}"))
	}
	waitFor(t, "輸出 200 行", func() bool { return strings.Contains(mustRun(t, r, "capture-pane", "-p", "-t", id), "L200") })

	if m, err := SendScroll(ctx, r, id, ScrollLineDown, 3); err != nil || m != "noop" || pos() != "0:" {
		t.Fatalf("在底部往下應不動作：%s %v %s", m, err, pos())
	}
	if _, err := SendScroll(ctx, r, id, ScrollLineUp, 5); err != nil || pos() != "1:5" {
		t.Fatalf("逐列上 5 應進 copy-mode 停在 5：%v %s", err, pos())
	}
	// tmux 的 page-up 捲「pane 高度 − 2」列（保留兩列重疊）。
	h, _ := strconv.Atoi(strings.TrimSpace(mustRun(t, r, "display-message", "-p", "-t", id, "#{pane_height}")))
	if _, err := SendScroll(ctx, r, id, ScrollPageUp, 1); err != nil || pos() != fmt.Sprintf("1:%d", 5+h-2) {
		t.Fatalf("翻頁上應再往上 %d 列：%v %s", h-2, err, pos())
	}
	if _, err := SendScroll(ctx, r, id, ScrollPageDown, 1); err != nil || pos() != "1:5" {
		t.Fatalf("翻頁下應回到 5：%v %s", err, pos())
	}
	if _, err := SendScroll(ctx, r, id, ScrollLineDown, 30); err != nil || pos() != "0:" {
		t.Fatalf("捲到底應自動離開 copy-mode：%v %s", err, pos())
	}
	// 整個過程沒有任何位元組送進程式：cat 不會回顯任何東西，最後一行仍是 L200。
	if cap := mustRun(t, r, "capture-pane", "-p", "-t", id); strings.Contains(cap, "^[") || strings.Contains(cap, "[<6") {
		t.Fatalf("shell 畫面不得出現跳脫序列：%q", cap)
	}
}

func TestScrollRealTmuxAlternateScreen(t *testing.T) {
	r := newIsoTmux(t)
	ctx := context.Background()
	// 模擬全螢幕程式：進 alternate screen，可選擇開 SGR 滑鼠回報，之後用 cat -v 把收到的位元組印出來。
	start := func(name, modes string) string {
		if _, err := r.Run(ctx, "new-session", "-d", "-s", name, "-x", "80", "-y", "24", "--", "sh", "-c", "printf '\\033[?1049h"+modes+"'; stty -echo; exec cat -v"); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(mustRun(t, r, "display-message", "-p", "-t", "="+name+":", "#{session_id}"))
		waitFor(t, name+" 進入 alternate screen", func() bool {
			return strings.HasPrefix(mustRun(t, r, "display-message", "-p", "-t", id, "#{alternate_on}#{mouse_sgr_flag}"), "1")
		})
		return id
	}
	plain := start("plain", "")
	mouse := start("mouse", "\\033[?1000h\\033[?1006h")
	waitFor(t, "mouse 開啟 SGR 回報", func() bool {
		return strings.TrimSpace(mustRun(t, r, "display-message", "-p", "-t", mouse, "#{mouse_any_flag}#{mouse_sgr_flag}")) == "11"
	})

	if _, err := SendScroll(ctx, r, plain, ScrollLineUp, 2); !errors.Is(err, ErrScrollUnsupported) {
		t.Fatalf("沒開滑鼠回報的全螢幕程式不得送滾輪序列：%v", err)
	}
	if _, err := SendScroll(ctx, r, mouse, ScrollLineUp, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := SendScroll(ctx, r, mouse, ScrollLineDown, 1); err != nil {
		t.Fatal(err)
	}
	// 只送 Enter 讓 cat -v 把這一行吐出來；cat -v 把 ESC 印成 ^[。
	mustRun(t, r, "send-keys", "-t", mouse, "Enter")
	want := "^[[<64;41;13M^[[<64;41;13M^[[<65;41;13M"
	waitFor(t, "收到滾輪序列", func() bool { return strings.Contains(mustRun(t, r, "capture-pane", "-p", "-t", mouse), want) })
	if cap := strings.TrimSpace(mustRun(t, r, "capture-pane", "-p", "-t", plain)); cap != "" {
		t.Fatalf("被拒的捲動不得在畫面留下字元：%q", cap)
	}
	if mode := strings.TrimSpace(mustRun(t, r, "display-message", "-p", "-t", plain, "#{pane_in_mode}")); mode != "0" {
		t.Fatalf("全螢幕程式不應進 copy-mode：%s", mode)
	}
}

func mustRun(t *testing.T, r Runner, args ...string) string {
	t.Helper()
	out, err := r.Run(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
