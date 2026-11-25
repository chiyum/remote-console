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
	st, err := ParseScrollState("1\t1\t1\t0\t\t0\t100\t40\t%12\t2.1.332\n")
	if err != nil || !st.Alternate || !st.MouseAny || !st.MouseSGR || st.InMode || st.Width != 100 || st.Height != 40 || st.PaneID != "%12" || st.Command != "2.1.332" {
		t.Fatalf("解析錯誤：%+v %v", st, err)
	}
	for _, bad := range []string{"1\t1\n", "1\t1\t1\t0\t\t0\t100\t40\t$3\tzsh\n", "1\t1\t1\t0\t\t0\t100\t40\t%1;\tzsh\n"} {
		if _, err := ParseScrollState(bad); err == nil {
			t.Fatalf("%q 應回錯", bad)
		}
	}
}

// hexOf 是測試用的期望值：把序列寫成 send-keys -H 的參數。
func hexOf(s string) string { return hexBytes(s) }

func TestPlanScroll(t *testing.T) {
	claude := PaneScrollState{Alternate: true, MouseAny: true, MouseSGR: true, Width: 100, Height: 40, PaneID: "%7", Command: "2.1.332"}
	codex := PaneScrollState{Alternate: true, Width: 100, Height: 40, PaneID: "%7", Command: "codex"}
	vim := PaneScrollState{Alternate: true, Width: 80, Height: 24, PaneID: "%7", Command: "vim"}
	x10 := PaneScrollState{Alternate: true, MouseAny: true, Width: 80, Height: 24, PaneID: "%7", Command: "old"}
	shell := PaneScrollState{History: 163, Width: 100, Height: 40, PaneID: "%7", Command: "zsh"}
	empty := PaneScrollState{Width: 100, Height: 40, PaneID: "%7", Command: "zsh"}
	copyMode := PaneScrollState{InMode: true, Mode: "copy-mode", History: 163, PaneID: "%7", Command: "zsh"}
	treeMode := PaneScrollState{InMode: true, Mode: "tree-mode", PaneID: "%7", Command: "zsh"}
	same := "#{==:#{pane_id},%7}"
	notIn := "#{?pane_in_mode,0,1}"
	wheel := "#{||:#{&&:#{mouse_any_flag},#{mouse_sgr_flag}},#{==:#{pane_current_command},codex}}"
	cases := []struct {
		name         string
		st           PaneScrollState
		action       string
		count        int
		cond, cmds   string
		method       string
		err          error
		expectNoPlan bool
	}{
		{"claude 翻頁上", claude, ScrollPageUp, 1, allOf(same, notIn, "#{alternate_on}"), "send-keys -t %7 PPage", "page-key", nil, false},
		{"claude 翻頁下", claude, ScrollPageDown, 1, allOf(same, notIn, "#{alternate_on}"), "send-keys -t %7 NPage", "page-key", nil, false},
		{"claude 逐列上 2", claude, ScrollLineUp, 2, allOf(same, notIn, "#{alternate_on}", wheel), "send-keys -t %7 -H " + hexOf("\x1b[<64;51;21M\x1b[<64;51;21M"), "sgr-wheel", nil, false},
		{"claude 逐列下 1", claude, ScrollLineDown, 1, allOf(same, notIn, "#{alternate_on}", wheel), "send-keys -t %7 -H " + hexOf("\x1b[<65;51;21M"), "sgr-wheel", nil, false},
		{"codex 翻頁", codex, ScrollPageUp, 1, allOf(same, notIn, "#{alternate_on}"), "send-keys -t %7 PPage", "page-key", nil, false},
		{"codex 逐列（名單內）", codex, ScrollLineUp, 1, allOf(same, notIn, "#{alternate_on}", wheel), "send-keys -t %7 -H " + hexOf("\x1b[<64;51;21M"), "sgr-wheel", nil, false},
		{"vim 翻頁", vim, ScrollPageDown, 1, allOf(same, notIn, "#{alternate_on}"), "send-keys -t %7 NPage", "page-key", nil, false},
		{"vim 逐列不送序列", vim, ScrollLineUp, 1, "", "", "", ErrScrollUnsupported, true},
		{"非 SGR 滑鼠不送序列", x10, ScrollLineDown, 1, "", "", "", ErrScrollUnsupported, true},
		{"shell 翻頁上進 copy-mode", shell, ScrollPageUp, 1, allOf(same, notIn, "#{?alternate_on,0,1}", "#{?history_size,1,0}"), "copy-mode -e -t %7 ; send-keys -t %7 -X page-up", "copy-mode", nil, false},
		{"shell 逐列上進 copy-mode", shell, ScrollLineUp, 4, allOf(same, notIn, "#{?alternate_on,0,1}", "#{?history_size,1,0}"), "copy-mode -e -t %7 ; send-keys -t %7 -X -N 4 scroll-up", "copy-mode", nil, false},
		{"shell 在底部往下不動作", shell, ScrollPageDown, 1, "", "", "", nil, true},
		{"shell 在底部逐列下不動作", shell, ScrollLineDown, 3, "", "", "", nil, true},
		{"shell 沒有 scrollback 不動作", empty, ScrollPageUp, 1, "", "", "", nil, true},
		{"copy-mode 中翻頁下", copyMode, ScrollPageDown, 1, allOf(same, "#{==:#{pane_mode},copy-mode}"), "send-keys -t %7 -X page-down", "copy-mode", nil, false},
		{"copy-mode 中逐列下", copyMode, ScrollLineDown, 5, allOf(same, "#{==:#{pane_mode},copy-mode}"), "send-keys -t %7 -X -N 5 scroll-down", "copy-mode", nil, false},
		{"其他 tmux 模式不動", treeMode, ScrollPageUp, 1, "", "", "", ErrScrollUnsupported, true},
	}
	for _, c := range cases {
		plan, err := PlanScroll(c.st, c.action, c.count)
		if !errors.Is(err, c.err) || err == nil && c.err != nil {
			t.Errorf("%s：err=%v，應為 %v", c.name, err, c.err)
			continue
		}
		if c.expectNoPlan {
			if plan != nil {
				t.Errorf("%s：不應有計畫：%+v", c.name, plan)
			}
			continue
		}
		if plan == nil || plan.Cond != c.cond || plan.Cmds != c.cmds || plan.Method != c.method {
			t.Errorf("%s：得到 %+v，應為 cond=%q cmds=%q（%s）", c.name, plan, c.cond, c.cmds, c.method)
		}
	}
	if _, err := PlanScroll(PaneScrollState{Alternate: true, PaneID: "$3"}, ScrollPageUp, 1); err == nil {
		t.Fatal("pane id 不是 %N 應回錯")
	}
}

func TestAtomicScrollArgsIsSingleCall(t *testing.T) {
	plan := &ScrollPlan{Cond: "#{alternate_on}", Cmds: "send-keys -t %7 PPage"}
	got := AtomicScrollArgs("$3", plan)
	want := []string{"if-shell", "-F", "-t", "$3", "#{alternate_on}", "send-keys -t %7 PPage ; display-message -p dc-scroll-ok", "display-message -p dc-scroll-skip"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv 不對：%q", got)
	}
	// tmux 以 argv 執行時，結尾是分號的參數會被當成指令分隔（見 webKeys 註解）。
	for _, a := range got {
		if strings.HasSuffix(a, ";") {
			t.Fatalf("參數不可以 ; 結尾：%q", a)
		}
	}
}

// fakeStateRunner 對 display-message 回固定的 pane 狀態，對 if-shell 回 ifResult，其他指令只記錄。
type fakeStateRunner struct {
	state    string
	ifResult string
	calls    [][]string
}

func (f *fakeStateRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "display-message":
		return f.state, nil
	case "if-shell":
		return f.ifResult, nil
	}
	return "", nil
}

func TestSendScroll(t *testing.T) {
	f := &fakeStateRunner{state: "1\t1\t1\t0\t\t0\t100\t40\t%9\t2.1.332\n", ifResult: "dc-scroll-ok\n"}
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
	// 讀旗標一次、送出一次；送出只有一個 if-shell 呼叫，沒有單獨的 send-keys。
	if len(f.calls) != 2 || f.calls[0][0] != "display-message" || f.calls[1][0] != "if-shell" || f.calls[1][3] != "$3" {
		t.Fatalf("呼叫順序不對：%q", f.calls)
	}
	f.calls, f.ifResult = nil, "dc-scroll-skip\n"
	if _, err := SendScroll(context.Background(), f, "$3", ScrollLineUp, 3); !errors.Is(err, ErrScrollStateChanged) {
		t.Fatalf("tmux 回 skip 應為 ErrScrollStateChanged：%v", err)
	}
	f.ifResult = "something else"
	if _, err := SendScroll(context.Background(), f, "$3", ScrollLineUp, 3); err == nil || errors.Is(err, ErrScrollStateChanged) {
		t.Fatalf("非預期回應應為一般錯誤：%v", err)
	}
	// 已在底部的 shell：只讀旗標，不送任何東西。
	f.calls, f.state = nil, "0\t0\t0\t0\t\t50\t100\t40\t%9\tzsh\n"
	if m, err := SendScroll(context.Background(), f, "$3", ScrollPageDown, 1); err != nil || m != "noop" || len(f.calls) != 1 {
		t.Fatalf("在底部往下應只讀旗標：%s %v %q", m, err, f.calls)
	}
}
