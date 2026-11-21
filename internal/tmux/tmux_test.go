package tmux

import (
	"context"
	"strings"
	"testing"
	"time"
)

type recRunner struct{ calls [][]string }

func (r *recRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return "", nil
}

func (r *recRunner) joined() []string {
	var out []string
	for _, c := range r.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func TestSyncTitleSetsOptionsAsSingleArgs(t *testing.T) {
	r := &recRunner{}
	nick := "客服 #(touch /tmp/x) -x"
	if err := SyncTitle(context.Background(), r, Session{ID: "$7"}, nick, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 4 {
		t.Fatalf("應下 4 個 set-option：%v", r.joined())
	}
	// 暱稱必須是單一 argv 元素、放在 option 名稱之後；標題字串只引用 option。
	if c := r.calls[0]; c[0] != "set-option" || c[2] != "$7" || c[3] != "@dc_nickname" || c[4] != nick || len(c) != 5 {
		t.Fatalf("@dc_nickname 參數不對：%q", c)
	}
	if c := r.calls[1]; c[3] != "set-titles-string" || c[4] != "#{@dc_nickname}" {
		t.Fatalf("set-titles-string 應只引用 option：%q", c)
	}
	// 已同步就不再動。
	r.calls = nil
	_ = SyncTitle(context.Background(), r, Session{ID: "$7", DCTitle: TitleOn, DCNickname: nick}, nick, time.Unix(100, 0))
	if len(r.calls) != 0 {
		t.Fatalf("已同步不應再呼叫 tmux：%v", r.joined())
	}
}

func TestSyncTitleClearTwoSteps(t *testing.T) {
	r := &recRunner{}
	now := time.Unix(1000, 0)
	_ = SyncTitle(context.Background(), r, Session{ID: "$7", DCTitle: TitleOn, DCNickname: "舊"}, "", now)
	if got := r.joined(); len(got) != 2 || got[0] != "set-option -t $7 @dc_nickname " || got[1] != "set-option -t $7 @dc_title clearing:1000" {
		t.Fatalf("清空第一步應送空標題並標記清空中：%q", got)
	}
	// 還沒到延遲：不 unset（空標題要先送到 iTerm2）。
	r.calls = nil
	_ = SyncTitle(context.Background(), r, Session{ID: "$7", DCTitle: "clearing:1000"}, "", now.Add(time.Second))
	if len(r.calls) != 0 {
		t.Fatalf("延遲內不應 unset：%v", r.joined())
	}
	r.calls = nil
	_ = SyncTitle(context.Background(), r, Session{ID: "$7", DCTitle: "clearing:1000"}, "", now.Add(3*time.Second))
	want := []string{"set-option -t $7 -u set-titles", "set-option -t $7 -u set-titles-string", "set-option -t $7 -u @dc_nickname", "set-option -t $7 -u @dc_title"}
	if strings.Join(r.joined(), "|") != strings.Join(want, "|") {
		t.Fatalf("第二步應 unset 回全域預設：%q", r.joined())
	}
}

func TestSyncTitleLeavesUnmanagedSessionsAlone(t *testing.T) {
	r := &recRunner{}
	_ = SyncTitle(context.Background(), r, Session{ID: "$7"}, "", time.Now())
	_ = SyncTitle(context.Background(), r, Session{ID: "proc:12"}, "x", time.Now())
	if len(r.calls) != 0 {
		t.Fatalf("沒設過暱稱、或不是 tmux id 的不應動：%v", r.joined())
	}
}

func TestKillSessionCheckedOnlyAcceptsIDs(t *testing.T) {
	r := &recRunner{}
	for _, bad := range []string{"aiuse-claude-x", "=name", "$", "$1 ; kill-server", "proc:1", ""} {
		if err := KillSessionChecked(context.Background(), r, bad); err == nil {
			t.Errorf("%q 應被拒", bad)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("格式不合法不應呼叫 tmux：%v", r.joined())
	}
	if err := KillSessionChecked(context.Background(), r, "$12"); err != nil || r.joined()[0] != "kill-session -t $12" {
		t.Fatalf("合法 id 應 kill：%v %v", err, r.joined())
	}
}

func TestParseSessionsReadsDCOptions(t *testing.T) {
	out := "$1\tn\t1\t1\t0\tcodex-direct-v1\twork\tcodex\t1\t1\tabc123\t暱稱\ton\n$2\tm\t1\t1\t0\t\t\t\t1\n"
	s := ParseSessions(out)
	if len(s) != 2 || s[0].LaunchToken != "abc123" || s[0].DCNickname != "暱稱" || s[0].DCTitle != TitleOn ||
		s[1].DCTitle != "" || s[1].LaunchToken != "" {
		t.Fatalf("解析錯誤：%+v", s)
	}
}

func TestWebKeyWhitelist(t *testing.T) {
	want := map[string]string{
		"esc": "Escape", "tab": "Tab", "btab": "BTab", "up": "Up", "down": "Down", "left": "Left", "right": "Right",
		"enter": "Enter", "1": "1", "2": "2", "3": "3", "4": "4", "ctrl-c": "C-c",
	}
	for k, v := range want {
		if got, ok := WebKey(k); !ok || got != v {
			t.Errorf("WebKey(%q) = %q,%v，應為 %q", k, got, ok, v)
		}
	}
	if len(webKeys) != len(want) {
		t.Fatalf("白名單應剛好 %d 個鍵，實際 %d", len(want), len(webKeys))
	}
	for _, bad := range []string{"", "a", "rm -rf /", "C-d", "down down", "Down", "Escape", "5", "0", "C-c", "enter;", "-l", "Up;kill-server"} {
		if _, ok := WebKey(bad); ok {
			t.Errorf("%q 不應在白名單", bad)
		}
	}
	for _, v := range webKeys {
		if strings.HasSuffix(v, ";") || strings.HasPrefix(v, "-") {
			t.Errorf("tmux 鍵名 %q 不可以 ; 結尾或 - 開頭", v)
		}
	}
}

func TestSendWebKeyTargetsIDOnly(t *testing.T) {
	r := &recRunner{}
	for _, c := range [][2]string{{"aiuse-claude-x", "down"}, {"=name", "down"}, {"=name:", "down"}, {"$1;", "down"}, {"$3", "a"}, {"$3", "rm -rf /"}} {
		if err := SendWebKey(context.Background(), r, c[0], c[1]); err == nil {
			t.Errorf("%v 應被拒", c)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("不合法輸入不應呼叫 tmux：%v", r.joined())
	}
	if err := SendWebKey(context.Background(), r, "$3", "btab"); err != nil {
		t.Fatal(err)
	}
	if got := r.calls[0]; len(got) != 4 || strings.Join(got, "|") != "send-keys|-t|$3|BTab" {
		t.Fatalf("argv 應為 send-keys -t $3 BTab（不帶 -l），實際 %q", got)
	}
}
