package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestValidScroll(t *testing.T) {
	ok := []struct {
		a    string
		c, n int
	}{
		{ScrollPageUp, 0, 1}, {ScrollPageUp, 1, 1}, {ScrollPageDown, 0, 1},
		{ScrollLineUp, 1, 1}, {ScrollLineDown, MaxScrollLines, MaxScrollLines},
	}
	for _, c := range ok {
		if n, good := ValidScroll(c.a, c.c); !good || n != c.n {
			t.Errorf("ValidScroll(%q,%d) = %d,%v，應為 %d,true", c.a, c.c, n, good, c.n)
		}
	}
	bad := []struct {
		a string
		c int
	}{
		{ScrollPageUp, 2}, {ScrollPageDown, -1}, {ScrollLineUp, 0}, {ScrollLineUp, -3},
		{ScrollLineDown, MaxScrollLines + 1}, {ScrollLineUp, 1 << 30},
		{"", 1}, {"up", 1}, {"PageUp", 1}, {"page-up;", 1}, {"line-up ", 1}, {"PPage", 1},
	}
	for _, c := range bad {
		if _, good := ValidScroll(c.a, c.c); good {
			t.Errorf("ValidScroll(%q,%d) 應被拒", c.a, c.c)
		}
	}
}

func TestParseScrollState(t *testing.T) {
	st, err := ParseScrollState("1\t1\t1\t0\t\t0\t100\t40\t2.1.332\n")
	if err != nil || !st.Alternate || !st.MouseAny || !st.MouseSGR || st.InMode || st.Width != 100 || st.Height != 40 || st.Command != "2.1.332" {
		t.Fatalf("解析錯誤：%+v %v", st, err)
	}
	if _, err := ParseScrollState("1\t1\n"); err == nil {
		t.Fatal("欄位不足應回錯")
	}
}

func joinPlan(cmds [][]string) string {
	var out []string
	for _, c := range cmds {
		out = append(out, strings.Join(c, " "))
	}
	return strings.Join(out, " | ")
}

func TestPlanScroll(t *testing.T) {
	claude := PaneScrollState{Alternate: true, MouseAny: true, MouseSGR: true, Width: 100, Height: 40, Command: "2.1.332"}
	codex := PaneScrollState{Alternate: true, Width: 100, Height: 40, Command: "codex"}
	vim := PaneScrollState{Alternate: true, Width: 80, Height: 24, Command: "vim"}
	x10 := PaneScrollState{Alternate: true, MouseAny: true, Width: 80, Height: 24, Command: "old"}
	shell := PaneScrollState{History: 163, Width: 100, Height: 40, Command: "zsh"}
	empty := PaneScrollState{Width: 100, Height: 40, Command: "zsh"}
	copyMode := PaneScrollState{InMode: true, Mode: "copy-mode", History: 163, Command: "zsh"}
	treeMode := PaneScrollState{InMode: true, Mode: "tree-mode", Command: "zsh"}
	cases := []struct {
		name   string
		st     PaneScrollState
		action string
		count  int
		want   string
		method string
		err    error
	}{
		{"claude 翻頁上", claude, ScrollPageUp, 1, "send-keys -t $3 PPage", "page-key", nil},
		{"claude 翻頁下", claude, ScrollPageDown, 1, "send-keys -t $3 NPage", "page-key", nil},
		{"claude 逐列上 2", claude, ScrollLineUp, 2, "send-keys -t $3 -l \x1b[<64;51;21M\x1b[<64;51;21M", "sgr-wheel", nil},
		{"claude 逐列下 1", claude, ScrollLineDown, 1, "send-keys -t $3 -l \x1b[<65;51;21M", "sgr-wheel", nil},
		{"codex 翻頁", codex, ScrollPageUp, 1, "send-keys -t $3 PPage", "page-key", nil},
		{"codex 逐列（名單內）", codex, ScrollLineUp, 1, "send-keys -t $3 -l \x1b[<64;51;21M", "sgr-wheel", nil},
		{"vim 翻頁", vim, ScrollPageDown, 1, "send-keys -t $3 NPage", "page-key", nil},
		{"vim 逐列不送序列", vim, ScrollLineUp, 1, "", "", ErrScrollUnsupported},
		{"非 SGR 滑鼠不送序列", x10, ScrollLineDown, 1, "", "", ErrScrollUnsupported},
		{"shell 翻頁上進 copy-mode", shell, ScrollPageUp, 1, "copy-mode -e -t $3 ; send-keys -t $3 -X page-up", "copy-mode", nil},
		{"shell 逐列上進 copy-mode", shell, ScrollLineUp, 4, "copy-mode -e -t $3 ; send-keys -t $3 -X -N 4 scroll-up", "copy-mode", nil},
		{"shell 在底部往下不動作", shell, ScrollPageDown, 1, "", "noop", nil},
		{"shell 在底部逐列下不動作", shell, ScrollLineDown, 3, "", "noop", nil},
		{"shell 沒有 scrollback 不動作", empty, ScrollPageUp, 1, "", "noop", nil},
		{"copy-mode 中翻頁下", copyMode, ScrollPageDown, 1, "send-keys -t $3 -X page-down", "copy-mode", nil},
		{"copy-mode 中逐列下", copyMode, ScrollLineDown, 5, "send-keys -t $3 -X -N 5 scroll-down", "copy-mode", nil},
		{"其他 tmux 模式不動", treeMode, ScrollPageUp, 1, "", "", ErrScrollUnsupported},
	}
	for _, c := range cases {
		cmds, method, err := PlanScroll(c.st, "$3", c.action, c.count)
		if !errors.Is(err, c.err) || err == nil && c.err != nil {
			t.Errorf("%s：err=%v，應為 %v", c.name, err, c.err)
			continue
		}
		if got := joinPlan(cmds); got != c.want || method != c.method {
			t.Errorf("%s：得到 %q（%s），應為 %q（%s）", c.name, got, method, c.want, c.method)
		}
		// 不是 copy-mode 指令的 send-keys 只能是 PPage／NPage 或 -l 加純 SGR 滾輪序列。
		for _, cmd := range cmds {
			if cmd[0] == "send-keys" && len(cmd) == 5 && cmd[3] == "-l" {
				if strings.Trim(strings.NewReplacer("\x1b[<64;", "", "\x1b[<65;", "").Replace(cmd[4]), "0123456789;M") != "" {
					t.Errorf("%s：-l 只能送滾輪序列：%q", c.name, cmd[4])
				}
			}
		}
	}
}

// fakeStateRunner 對 display-message 回固定的 pane 狀態，其他指令只記錄。
type fakeStateRunner struct {
	state string
	calls []string
}

func (f *fakeStateRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == "display-message" {
		return f.state, nil
	}
	return "", nil
}

func TestSendScroll(t *testing.T) {
	f := &fakeStateRunner{state: "1\t1\t1\t0\t\t0\t100\t40\t2.1.332\n"}
	for _, c := range []struct {
		id, action string
		count      int
	}{{"aiuse-claude-x", ScrollPageUp, 1}, {"=name", ScrollPageUp, 1}, {"$1;", ScrollPageUp, 1}, {"$3", "hello", 1}, {"$3", ScrollLineUp, 0}, {"$3", ScrollLineUp, MaxScrollLines + 1}, {"$3", ScrollPageUp, 2}} {
		if _, err := SendScroll(context.Background(), f, c.id, c.action, c.count); err == nil {
			t.Errorf("%v 應被拒", c)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("不合法輸入不應呼叫 tmux：%v", f.calls)
	}
	m, err := SendScroll(context.Background(), f, "$3", ScrollPageUp, 0)
	if err != nil || m != "page-key" {
		t.Fatal(m, err)
	}
	want := "display-message -p -t $3 " + scrollStateFmt + ",send-keys -t $3 PPage"
	if strings.Join(f.calls, ",") != want {
		t.Fatalf("呼叫順序不對：%q", f.calls)
	}
}
