package hookstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	s := New(path)
	steps := []struct {
		ev   Event
		want string
	}{
		{Event{HookEventName: "UserPromptSubmit"}, StateWorking},
		{Event{HookEventName: "PreToolUse", ToolName: "Bash"}, StateWorking},
		{Event{HookEventName: "PermissionRequest", ToolName: "Bash"}, StatePermission},
		{Event{HookEventName: "Notification", NotificationType: "permission_prompt", Message: "Claude needs your permission to use Bash"}, StatePermission},
		{Event{HookEventName: "PostToolUse", ToolName: "Bash"}, StateWorking},
		{Event{HookEventName: "Stop", LastAssistantMessage: "完成了"}, StateDone},
		{Event{HookEventName: "Notification", NotificationType: "idle_prompt", Message: "Claude is waiting for your input"}, StateDone},
		{Event{HookEventName: "PreToolUse", ToolName: "AskUserQuestion"}, StateQuestion},
	}
	for i, st := range steps {
		st.ev.Pane, st.ev.SessionID, st.ev.Profile = "%1", "s1", "work"
		if !s.Apply(st.ev) {
			t.Fatal("apply 失敗")
		}
		if got := s.Get("%1").State; got != st.want {
			t.Fatalf("step %d %s: got %s want %s", i, st.ev.HookEventName, got, st.want)
		}
	}
	if s.Get("%1").Tool != "AskUserQuestion" {
		t.Fatal("tool 應記錄")
	}
	if s.Get("%1").LastMessage != "完成了" {
		t.Fatal("Stop 的 last_assistant_message 應保留到下一輪開始")
	}
	s.Apply(Event{Pane: "%1", SessionID: "s1", HookEventName: "UserPromptSubmit"})
	if s.Get("%1").LastMessage != "" {
		t.Fatal("新一輪開始應清掉上一輪的最後回覆")
	}
	// 重新載入應保留狀態（服務重啟不歸零）。
	if New(path).Get("%1") == nil {
		t.Fatal("應持久化")
	}
	s.Prune(map[string]bool{})
	if s.Get("%1") != nil {
		t.Fatal("prune 後應消失")
	}
}

func TestNoPaneUsesSessionKey(t *testing.T) {
	s := New("")
	if s.Apply(Event{HookEventName: "Stop"}) {
		t.Fatal("無 pane 也無合法 session id 的事件應略過")
	}
	if s.Apply(Event{HookEventName: "Stop", SessionID: "../../x"}) {
		t.Fatal("session id 格式不合法應略過")
	}
	sid := "aaaaaaaa-0000-4000-8000-000000000001"
	if !s.Apply(Event{HookEventName: "UserPromptSubmit", SessionID: sid}) {
		t.Fatal("不在 tmux 的 claude 應以 session id 為 key")
	}
	if st := s.Get(SessionKey(sid)); st == nil || st.State != StateWorking {
		t.Fatalf("sid key 狀態不符：%+v", st)
	}
	if k := s.Keys(SessionKeyPrefix); len(k) != 1 || k[0] != SessionKey(sid) {
		t.Fatalf("Keys 不符：%v", k)
	}
	s.Prune(map[string]bool{"%1": true})
	if s.Get(SessionKey(sid)) != nil {
		t.Fatal("程序不在了，sid key 應被清掉")
	}
}

func TestNewSessionInSamePaneResets(t *testing.T) {
	s := New("")
	s.Apply(Event{Pane: "%2", SessionID: "a", HookEventName: "PreToolUse", ToolName: "Bash", TranscriptPath: "/a.jsonl"})
	s.Apply(Event{Pane: "%2", SessionID: "b", HookEventName: "Notification", Message: "x"})
	if st := s.Get("%2"); st.Tool != "" || st.TranscriptPath != "" {
		t.Fatalf("換 session 不應沿用：%+v", st)
	}
}

// 畫面樣本皆為 claude 2.1.309 在 tmux 內實測 capture-pane 的輸出（testdata/）。
func readScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReadScreenFixtures(t *testing.T) {
	type want struct{ input, dialog, busy, idle bool }
	var (
		prompt  = want{false, true, false, false} // 對話框取代輸入框
		idle    = want{true, false, false, true}  // 有閒置正面證據
		running = want{true, false, true, false}
		unknown = want{true, false, false, false} // 看得到輸入框但判斷不出在不在跑
		hidden  = want{false, false, false, false}
	)
	cases := []struct {
		file string
		want want
	}{
		{"screen-bash-prompt.txt", prompt},
		{"screen-write-prompt.txt", prompt},
		{"screen-edit-prompt.txt", prompt},
		{"screen-webfetch-prompt.txt", prompt}, // 沒有 Esc to cancel 列
		{"screen-mcp-prompt.txt", prompt},
		{"screen-exitplanmode.txt", prompt}, // Would you like to proceed?，底部是 ctrl+g 列
		{"screen-write-tab-amend.txt", prompt},
		{"screen-write-tab-amend-typed.txt", prompt},      // 補充輸入中：「1. Yes, 改成 bye」
		{"screen-narrow40-bash-prompt.txt", prompt},       // 40 欄，選項折行、文字殘影
		{"screen-askuserquestion.txt", prompt},            // 有兩條 ─ 分隔線但中間不是輸入列
		{"screen-narrow40-prompt-with-tasks.txt", prompt}, // 任務清單畫在對話框下方

		{"screen-after-esc.txt", idle}, // 上一輪結束行
		{"screen-done.txt", idle},
		{"screen-write-after-esc.txt", idle},
		{"screen-bash-after-esc.txt", idle},
		{"screen-askuserquestion-after-esc.txt", idle},
		{"screen-exitplanmode-after-esc.txt", idle},
		{"screen-write-tab-amend-esc1.txt", idle},
		{"screen-tool-interrupted.txt", idle},                 // ⎿ Interrupted · What should Claude do instead?
		{"screen-text-interrupted.txt", idle},                 // 原 prompt 回填到輸入列
		{"screen-narrow40-interrupted.txt", idle},             // 40 欄殘影「Interrupte· What should」
		{"screen-narrow40-acceptedits-interrupted.txt", idle}, // 同上，狀態列被截斷
		{"screen-long-running-tip-interrupted.txt", idle},
		{"screen-todos-interrupted-wide.txt", idle},               // 任務清單在 Interrupted 與輸入框之間
		{"screen-narrow40-prompt-with-tasks-after-esc.txt", idle}, // 任務清單＋tmux 提示在結束行與輸入框之間

		{"screen-allowed-long-running.txt", running},
		{"screen-working-streaming.txt", running},
		{"screen-narrow40-running.txt", running},
		{"screen-long-running-tip.txt", running},             // 轉圈行下方有 Tip 與 tmux 提示
		{"screen-narrow40-acceptedits-running.txt", running}, // 狀態列截成「(shift+tab to  ·」，靠轉圈行
		{"screen-narrow40-auto-running.txt", running},        // auto 模式狀態列整段沒有 esc to interrupt
		{"screen-narrow40-auto-running2.txt", running},
		{"screen-narrow40-todos-running.txt", running}, // 任務清單畫在轉圈行與輸入框之間
		{"screen-todos-running-wide.txt", running},
		{"screen-narrow40-working-after-ctrl-o.txt", running},

		{"screen-narrow40-working-slash-menu.txt", unknown},          // 工作中打「/」：補全清單蓋住轉圈行
		{"screen-narrow40-working-at-menu.txt", unknown},             // 工作中打「@」：「* claude (agent) – …」不是轉圈行
		{"screen-narrow40-auto-idle-start.txt", idle},                // 剛啟動：輸入框正上方是啟動畫面
		{"screen-narrow40-auto-early-esc-prompt-restored.txt", idle}, // 送出 4 秒就 Esc：prompt 撤回輸入框，上方只剩啟動畫面
		{"screen-narrow40-auto-early-esc-interrupted.txt", idle},
		{"screen-early-esc-long-reply.txt", idle},
		// 高 pane：啟動畫面與輸入框之間隔幾十行空白（QA 實測 50 行 pane 隔 41 行；65 行為本輪實測）
		{"screen-tall50-fresh-early-esc-prompt-restored.txt", idle},
		{"screen-rows65-fresh-early-esc-prompt-restored.txt", idle},
		{"screen-tall50-warm-early-esc-prompt-restored.txt", idle}, // 上方是上一輪結束行、輸入框有撤回的 prompt
		{"screen-rows28-fresh-early-esc-prompt-restored.txt", idle},
		{"screen-tall50-fresh-streaming.txt", running},
		{"screen-rows50-fresh-0.5s-after-send.txt", running}, // 送出後 0.5 秒已不是只剩啟動畫面
		{"screen-rows65-fresh-0.5s-after-send.txt", running},
		{"screen-rows65-fresh-1s-after-send.txt", running},
		// ctrl+o 全畫面 transcript，對話內容含對話框字樣（QA 實測誤亮成等你允許）：判斷不出來
		{"screen-ctrl-o-transcript-dialog-words.txt", hidden},
		{"screen-narrow40-ctrl-o-transcript-dialog-words.txt", hidden},
		{"screen-working-output-has-dialog-words.txt", running}, // 同一輪按 ctrl+o 之前
		{"screen-narrow40-working-ctrl-o.txt", hidden},          // ctrl+o 全畫面 transcript：沒有輸入框
	}
	for _, c := range cases {
		v := ReadScreen(readScreen(t, c.file))
		got := want{v.InputBox, v.Dialog, v.Busy, v.Idle}
		if got != c.want {
			t.Errorf("%s：got %+v，want %+v", c.file, got, c.want)
		}
	}
}

func TestReadScreenIgnoresQuotedTextAboveInputBox(t *testing.T) {
	// 由實測閒置畫面在對話區插入引用字樣：看得到輸入框就不算對話框，對話裡的 esc to interrupt 也不算在跑。
	lines := strings.Split(readScreen(t, "screen-done.txt"), "\n")
	quoted := []string{"Do you want to proceed?", " ❯ 1. Yes", " Esc to cancel · Tab to amend", "✢ Flowing… (7s · esc to interrupt)", "結論如上。"}
	lines = append(lines[:3], append(quoted, lines[3:]...)...)
	if v := ReadScreen(strings.Join(lines, "\n")); v.Dialog || v.Busy || !v.Idle {
		t.Fatalf("引用字樣不應影響判讀：%+v", v)
	}
	// 回覆最後一段就是問句（緊貼在輸入框上方、落在底部 12 行內）：看得到輸入框就不是對話框。
	lines = strings.Split(strings.TrimRight(readScreen(t, "screen-bash-after-esc.txt"), "\n"), "\n")
	n := len(lines) - 6
	ask := []string{"⏺ Do you want to proceed with the migration?", "  1. Yes", "  2. No (Esc to cancel)"}
	lines = append(lines[:n:n], append(ask, lines[n:]...)...)
	if v := ReadScreen(strings.Join(lines, "\n")); v.Dialog || !v.InputBox {
		t.Fatalf("輸入框上方的問句不應判定為對話框：%+v", v)
	}
}

func TestSpinnerAndAuxLines(t *testing.T) {
	for l, want := range map[string]bool{
		"✢ Flowing… (7s · ↓ 128 tokens)":     true,
		"· Cogitating… (12s · ↓ 238 tokens)": true,
		"✻ Churning… (8s · thought for 1s)":  true,
		"✽ Julienning… (1m 3s · thinking)":   true,
		"✻ Sautéed for 2s · done 下午10:39":    false,
		"* claude (agent) – Catch-all for …": false, // @ 補全清單
		"* 重點整理… (見下方)":                      false, // markdown 項目
		"⎿  Running… (12s)":                  false, // ctrl+o transcript 內的工具列
	} {
		if got := spinnerRe.MatchString(l); got != want {
			t.Errorf("spinner %q：got %v want %v", l, got, want)
		}
	}
	for l, want := range map[string]bool{
		"                    ◐ medium · /effort": true,
		"  tmux focus-events off · add 'set -g…": true,
		"  ⎿  Tip: Continue your session":        true,
		"  ⎿  ◻ 檢查環境":                            true,
		"  5 tasks (0 done, 5 open)":             true,
		"  ⎿  Interrupted · What should Claude":  false,
		"✻ Sautéed for 2s · done":                false,
	} {
		if got := auxLine(l); got != want {
			t.Errorf("aux %q：got %v want %v", l, got, want)
		}
	}
}

func TestNormalizeLine(t *testing.T) {
	for in, want := range map[string]string{" ❯ 1. Yes": "1. yes", "│ Esc to cancel": "esc to cancel", "╰─x": "─x"} {
		if got := normalizeLine(in); got != want {
			t.Errorf("%q → %q，want %q", in, got, want)
		}
	}
}

// obs 回傳這次判讀是否改了燈。
func obs(s *Store, p Probe, v ScreenView) bool { return s.Observe(p, v) == OutcomeChanged }

// probeOnce 模擬 snapshot 一輪：列出 probe 後對指定 pane 套用畫面。
func probeOnce(s *Store, pane string, v ScreenView) bool {
	for _, p := range s.Probes(1500*time.Millisecond, 10*time.Second) {
		if p.Pane == pane {
			return obs(s, p, v)
		}
	}
	return false
}

func TestObserveNeedsTwoMisses(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%3", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Bash", TranscriptPath: "/t.jsonl"})
	gone := ReadScreen(readScreen(t, "screen-bash-after-esc.txt"))
	if probeOnce(s, "%3", gone) {
		t.Fatal("寬限期內不應檢查")
	}
	hookAt := s.Get("%3").UpdatedAt
	now = now.Add(2 * time.Second)
	if probeOnce(s, "%3", gone) || s.Get("%3").State != StatePermission {
		t.Fatal("第一次沒看到不應降級")
	}
	// 同一輪重算被觸發兩次（相隔不到 MinMissGap）：不算兩次獨立確認。
	now = now.Add(500 * time.Millisecond)
	if obs(s, Probe{Pane: "%3", State: StatePermission, UpdatedAt: hookAt}, gone) {
		t.Fatal("兩次間隔不足 MinMissGap 不算兩次確認")
	}
	// 中間看到一次對話框（例如重畫拍到半張畫面後恢復）就重新計數。
	now = now.Add(time.Second)
	probeOnce(s, "%3", ReadScreen(readScreen(t, "screen-bash-prompt.txt")))
	now = now.Add(2 * time.Second)
	if probeOnce(s, "%3", gone) {
		t.Fatal("看到過對話框後要重新計數")
	}
	now = now.Add(2 * time.Second)
	if !probeOnce(s, "%3", gone) {
		t.Fatal("連續兩次沒看到應降級")
	}
	st := s.Get("%3")
	if st.State != StateIdle || st.Event != EventScreenCheck || st.TranscriptPath != "/t.jsonl" || st.SessionID != "s" {
		t.Fatalf("降級後應保留 session 資訊：%+v", st)
	}
	if !st.UpdatedAt.Equal(hookAt) {
		t.Fatal("降級不得改 UpdatedAt（它代表最後一個真正的事件）")
	}
}

func TestObserveCASAndNewHookResets(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%3", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Bash"})
	now = now.Add(2 * time.Second)
	gone := ScreenView{InputBox: true, Idle: true}
	probeOnce(s, "%3", gone)
	now = now.Add(2 * time.Second)
	p := s.Probes(0, time.Hour)[0]
	// 抓畫面期間又來了新的 hook：舊 probe 一律失敗。
	s.Apply(Event{Pane: "%3", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Edit"})
	now = now.Add(2 * time.Second)
	if obs(s, p, gone) {
		t.Fatal("UpdatedAt 已變，不應降級")
	}
	// 同一個過期 probe 被套用兩次（例如兩個重算並行時各拿到舊 probe）也不能把新狀態蓋掉。
	now = now.Add(2 * time.Second)
	if obs(s, p, gone) || s.Get("%3").Tool != "Edit" {
		t.Fatalf("過期 probe 不應降級：%+v", s.Get("%3"))
	}
	// 新 hook 也清掉了先前的「第一次沒看到」，要重新累積兩次。
	if probeOnce(s, "%3", gone) {
		t.Fatal("新 hook 後的第一次沒看到不應降級")
	}
}

func TestObserveAllowedGoesWorkingThenInterruptGoesIdle(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%4", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Bash"})
	running := ReadScreen(readScreen(t, "screen-allowed-long-running.txt"))
	for i := 0; i < 2; i++ {
		now = now.Add(2 * time.Second)
		probeOnce(s, "%4", running)
	}
	if s.Get("%4").State != StateWorking {
		t.Fatalf("按允許後 tool 執行中應為工作中：%+v", s.Get("%4"))
	}
	// 長指令跑很久、狀態列一直有 esc to interrupt：不能熄。
	for i := 0; i < 20; i++ {
		now = now.Add(2 * time.Second)
		if probeOnce(s, "%4", running) {
			t.Fatal("工作中且在轉圈不應降級")
		}
	}
	// 工作中按 Esc 中斷：不送 Stop，靠畫面兩次確認降為等你輸入。
	stopped := ReadScreen(readScreen(t, "screen-tool-interrupted.txt"))
	now = now.Add(2 * time.Second)
	probeOnce(s, "%4", stopped)
	now = now.Add(2 * time.Second)
	if !probeOnce(s, "%4", stopped) || s.Get("%4").State != StateIdle {
		t.Fatalf("中斷後應降為等你輸入：%+v", s.Get("%4"))
	}
}

func TestWorkingProbeWaitsForIdleWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%5", SessionID: "s", HookEventName: "UserPromptSubmit"})
	now = now.Add(time.Second)
	if len(s.Probes(1500*time.Millisecond, 1500*time.Millisecond)) != 0 {
		t.Fatal("工作中未滿 workingIdle 不應檢查")
	}
	now = now.Add(time.Second)
	if len(s.Probes(1500*time.Millisecond, 1500*time.Millisecond)) != 1 {
		t.Fatal("工作中滿 workingIdle 應檢查")
	}
}

func TestWorkingWithDialogRestoresPromptLight(t *testing.T) {
	// 工作中卻看到對話框（漏收 PermissionRequest，或單次升級誤判）：連續兩次後改回對應的提示燈，不會熄。
	for _, c := range []struct{ file, want string }{
		{"screen-write-prompt.txt", StatePermission},
		{"screen-askuserquestion.txt", StateQuestion},
	} {
		now := time.Unix(1000, 0)
		s := New("")
		s.now = func() time.Time { return now }
		s.Apply(Event{Pane: "%5", SessionID: "s", HookEventName: "PreToolUse", ToolName: "Bash"})
		v := ReadScreen(readScreen(t, c.file))
		now = now.Add(11 * time.Second)
		if probeOnce(s, "%5", v) {
			t.Fatalf("%s：第一次不應改燈", c.file)
		}
		now = now.Add(2 * time.Second)
		if !probeOnce(s, "%5", v) || s.Get("%5").State != c.want {
			t.Fatalf("%s：應改回 %s，got %+v", c.file, c.want, s.Get("%5"))
		}
	}
}

func TestAllowedPromotesToWorkingOnFirstSight(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%6", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Bash"})
	now = now.Add(2 * time.Second)
	// 按了允許：輸入框回來而且在轉圈，只看一次就改成工作中。
	if !probeOnce(s, "%6", ReadScreen(readScreen(t, "screen-allowed-long-running.txt"))) || s.Get("%6").State != StateWorking {
		t.Fatalf("看到在跑應立即改成工作中：%+v", s.Get("%6"))
	}
	// 輸入框回來但判斷不出在跑或閒置（補全清單蓋住轉圈行）：兩次後先當工作中，不熄成等你輸入。
	s.Apply(Event{Pane: "%6", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Bash"})
	unknown := ReadScreen(readScreen(t, "screen-narrow40-working-slash-menu.txt"))
	now = now.Add(2 * time.Second)
	if probeOnce(s, "%6", unknown) {
		t.Fatal("判斷不出來時第一次不應改燈")
	}
	now = now.Add(2 * time.Second)
	if !probeOnce(s, "%6", unknown) || s.Get("%6").State != StateWorking {
		t.Fatalf("沒有閒置正面證據應改成工作中而非等你輸入：%+v", s.Get("%6"))
	}
}

func TestQuestionFromPermissionRequestAndEscape(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%6", SessionID: "s", HookEventName: "PreToolUse", ToolName: "AskUserQuestion"})
	// 2.1.309 實測 AskUserQuestion 也會送 PermissionRequest，6 秒後再送 Notification(permission_prompt)。
	s.Apply(Event{Pane: "%6", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "AskUserQuestion"})
	if s.Get("%6").State != StateQuestion {
		t.Fatalf("AskUserQuestion 應亮問你問題：%+v", s.Get("%6"))
	}
	s.Apply(Event{Pane: "%6", SessionID: "s", HookEventName: "Notification", NotificationType: "permission_prompt", Message: "Claude needs your permission to use AskUserQuestion"})
	if s.Get("%6").State != StateQuestion {
		t.Fatal("權限 Notification 不應把問你問題蓋成等你允許")
	}
	asking := ReadScreen(readScreen(t, "screen-askuserquestion.txt"))
	for i := 0; i < 3; i++ {
		now = now.Add(2 * time.Second)
		if probeOnce(s, "%6", asking) {
			t.Fatal("問答畫面還在不應熄")
		}
	}
	gone := ReadScreen(readScreen(t, "screen-askuserquestion-after-esc.txt"))
	now = now.Add(2 * time.Second)
	probeOnce(s, "%6", gone)
	now = now.Add(2 * time.Second)
	if !probeOnce(s, "%6", gone) || s.Get("%6").State != StateIdle {
		t.Fatalf("Esc 取消問答後應降為等你輸入：%+v", s.Get("%6"))
	}
}

func TestLateNotificationAfterScreenCheckIgnored(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%7", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Bash"})
	for i := 0; i < 2; i++ {
		now = now.Add(2 * time.Second)
		probeOnce(s, "%7", ScreenView{InputBox: true, Idle: true})
	}
	if s.Get("%7").State != StateIdle {
		t.Fatal("前置：應已降級")
	}
	// 使用者恰在 6 秒前後按 Esc，過期的權限 Notification 晚到：不應把燈點回去。
	s.Apply(Event{Pane: "%7", SessionID: "s", HookEventName: "Notification", NotificationType: "permission_prompt", Message: "Claude needs your permission to use Bash"})
	if s.Get("%7").State != StateIdle {
		t.Fatalf("過期提醒不應再點亮：%+v", s.Get("%7"))
	}
	// 下一個真正的提示一定先有 PermissionRequest。
	s.Apply(Event{Pane: "%7", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Bash"})
	if s.Get("%7").State != StatePermission {
		t.Fatal("新的 PermissionRequest 應亮燈")
	}
}

func TestNoPositiveEvidenceKeepsLight(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%8", SessionID: "s", HookEventName: "PreToolUse", ToolName: "Bash"})
	// 工作中：窄畫面狀態列沒有 esc to interrupt、補全清單、ctrl+o——都判斷不出閒置，不能熄。
	// （這些畫面都沒有對話框，也不會被改成提示燈。）
	for _, f := range []string{"screen-narrow40-auto-running.txt", "screen-narrow40-working-slash-menu.txt",
		"screen-narrow40-working-at-menu.txt", "screen-narrow40-working-ctrl-o.txt"} {
		v := ReadScreen(readScreen(t, f))
		for i := 0; i < 5; i++ {
			now = now.Add(11 * time.Second)
			if probeOnce(s, "%8", v) {
				t.Fatalf("%s：工作中沒有閒置正面證據卻降級", f)
			}
		}
	}
	// 等你允許：ctrl+o 看不到輸入框，不能當成提示已消失。
	s.Apply(Event{Pane: "%8", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Bash"})
	v := ReadScreen(readScreen(t, "screen-narrow40-working-ctrl-o.txt"))
	for i := 0; i < 5; i++ {
		now = now.Add(2 * time.Second)
		if probeOnce(s, "%8", v) {
			t.Fatal("看不到輸入框時不應判定提示已消失")
		}
	}
}

func TestProbeRateLimit(t *testing.T) {
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%9", SessionID: "s", HookEventName: "PermissionRequest", ToolName: "Bash"})
	now = now.Add(2 * time.Second)
	if len(s.Probes(time.Second, time.Hour)) != 1 {
		t.Fatal("應列入")
	}
	now = now.Add(300 * time.Millisecond)
	if len(s.Probes(time.Second, time.Hour)) != 0 {
		t.Fatal("同一個 pane 1 秒內不應再抓畫面")
	}
	now = now.Add(time.Second)
	if len(s.Probes(time.Second, time.Hour)) != 1 {
		t.Fatal("過了 MinProbeGap 應再列入")
	}
}

func TestDialogTailSkipsTasksAndIgnoresUpperText(t *testing.T) {
	// 由實測「對話框下方畫任務清單」畫面再多接 6 項任務：清單變長不能把 Esc to cancel 擠出底部範圍。
	s := strings.TrimRight(readScreen(t, "screen-narrow40-prompt-with-tasks.txt"), "\n") +
		strings.Repeat("\n  ◻ 額外任務項目…", 6)
	if v := ReadScreen(s); !v.Dialog {
		t.Fatal("任務清單變長後仍應看到對話框")
	}
	// 由實測 ctrl+o 畫面去掉底部說明列（模擬重畫途中、沒有輸入框也沒有說明列的半張畫面），
	// 在上方插入引用的對話框字樣：不在底部 12 行內就不算。
	lines := strings.Split(strings.TrimRight(readScreen(t, "screen-narrow40-working-ctrl-o.txt"), "\n"), "\n")
	lines = lines[:len(lines)-1]
	quoted := []string{"Do you want to proceed?", " ❯ 1. Yes", " Esc to cancel"}
	lines = append(lines[:2], append(quoted, lines[2:]...)...)
	if v := ReadScreen(strings.Join(lines, "\n")); v.Dialog {
		t.Fatal("上方的引用字樣不應判定為對話框")
	}
}

func TestMissTiedToTarget(t *testing.T) {
	// 第一次看到對話框、第二次看到閒置：兩次看到的不是同一件事，不能湊成「連續兩次」。
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%7", SessionID: "s", HookEventName: "PreToolUse", ToolName: "Bash"})
	now = now.Add(11 * time.Second)
	if probeOnce(s, "%7", ReadScreen(readScreen(t, "screen-write-prompt.txt"))) {
		t.Fatal("第一次不應改燈")
	}
	now = now.Add(2 * time.Second)
	if probeOnce(s, "%7", ReadScreen(readScreen(t, "screen-tool-interrupted.txt"))) {
		t.Fatalf("目標不同的兩次觀察不應改燈：%+v", s.Get("%7"))
	}
}

func TestDoneOnlyForCleanFinishedTurn(t *testing.T) {
	for f, want := range map[string]bool{
		"screen-done.txt": true, // 正常結束：結束行、輸入框空
		"screen-tall50-warm-early-esc-prompt-restored.txt": false, // 結束行是上一輪的，輸入框有撤回的 prompt
		"screen-bash-after-esc.txt":                        false, // 取消權限提示後也畫結束行，但這一輪有 Interrupted
		"screen-write-after-esc.txt":                       false, // User rejected write
		"screen-askuserquestion-after-esc.txt":             false, // User declined to answer
		"screen-exitplanmode-after-esc.txt":                false, // User rejected Claude's plan
	} {
		if got := ReadScreen(readScreen(t, f)).Done; got != want {
			t.Errorf("%s：Done=%v，want %v", f, got, want)
		}
	}
	// 工作中（例如 Stop 之後又來一個不明 PreToolUse）而畫面是正常結束：回到做完，不是等你輸入。
	now := time.Unix(1000, 0)
	s := New("")
	s.now = func() time.Time { return now }
	s.Apply(Event{Pane: "%9", SessionID: "s", HookEventName: "Stop", LastAssistantMessage: "完成"})
	s.Apply(Event{Pane: "%9", SessionID: "s", HookEventName: "PreToolUse", ToolName: "Bash"})
	v := ReadScreen(readScreen(t, "screen-done.txt"))
	for i := 0; i < 2; i++ {
		now = now.Add(11 * time.Second)
		probeOnce(s, "%9", v)
	}
	if st := s.Get("%9"); st.State != StateDone || st.LastMessage != "完成" {
		t.Fatalf("應回到做完並保留最後回覆：%+v", st)
	}
}

func TestStaleInputBoxAboveDialogIgnored(t *testing.T) {
	// 由實測權限提示畫面在上方插入一個舊輸入框（重畫殘影）：下框線之下遠超過狀態列行數，不是目前的輸入框。
	lines := strings.Split(readScreen(t, "screen-bash-prompt.txt"), "\n")
	border := strings.Repeat("─", 80)
	stale := []string{border, "❯ ", border, "  ⏸ manual mode on · ? for shortcuts"}
	lines = append(lines[:2], append(stale, lines[2:]...)...)
	if v := ReadScreen(strings.Join(lines, "\n")); v.InputBox || !v.Dialog {
		t.Fatalf("殘影輸入框不應讓提示被判定消失：%+v", v)
	}
}
