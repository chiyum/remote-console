package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiyum/remote-console/internal/config"
	"github.com/chiyum/remote-console/internal/hookstore"
	"github.com/chiyum/remote-console/internal/procscan"
	"github.com/chiyum/remote-console/internal/sessionmeta"
	"github.com/chiyum/remote-console/internal/tmux"
)

// fakeTmux 模擬 tmux：一個 session「dc-t」底下的 pane 清單，capture-pane 依 pane 回傳畫面或錯誤。
type fakeTmux struct {
	mu        sync.Mutex
	panes     []string
	screens   map[string]string
	errs      map[string]error
	onCapture func(pane string) // 抓畫面途中要做的事（模擬期間有新 hook 進來）
	captured  []string
	calls     [][]string
}

func (f *fakeTmux) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	switch args[0] {
	case "list-sessions":
		return "$1\tdc-t\t1700000000\t1700000000\t0\tclaude-v1\twork\tclaude\t1\n", nil
	case "list-panes":
		var b strings.Builder
		for i, p := range f.panes {
			active := "0"
			if i == 0 {
				active = "1"
			}
			b.WriteString("dc-t\t" + p + "\t" + active + "\tclaude\t/tmp\t0\n")
		}
		return b.String(), nil
	case "capture-pane":
		pane := args[len(args)-1]
		f.captured = append(f.captured, pane)
		if f.onCapture != nil {
			f.mu.Unlock()
			f.onCapture(pane)
			f.mu.Lock()
		}
		if err := f.errs[pane]; err != nil {
			return "", err
		}
		return f.screens[pane], nil
	}
	return "", nil
}

func (f *fakeTmux) capturedCount(pane string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.captured {
		if p == pane {
			n++
		}
	}
	return n
}

// fixture 讀 hookstore/testdata 的實測畫面。
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "hookstore", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type harness struct {
	c     *Collector
	hooks *hookstore.Store
	tmux  *fakeTmux
	now   time.Time
	mu    sync.Mutex
}

func newHarness(t *testing.T, panes ...string) *harness {
	home := t.TempDir()
	cfg := config.Config{Home: home, ProfilesRoot: filepath.Join(home, ".ai-profiles"), ClaudeHome: filepath.Join(home, ".claude"), PollInterval: time.Second}
	h := &harness{hooks: hookstore.New(""), tmux: &fakeTmux{panes: panes, screens: map[string]string{}, errs: map[string]error{}}, now: time.Unix(1000, 0)}
	h.hooks.SetClock(func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now })
	h.c = New(cfg, h.tmux, h.hooks, nil)
	return h
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func (h *harness) permission(pane string) {
	h.hooks.Apply(hookstore.Event{Pane: pane, SessionID: "s-" + pane, HookEventName: "PermissionRequest", ToolName: "Bash"})
}

// poll 模擬一次輪詢：前進 2 秒後重算快照，回傳卡片燈號。
func (h *harness) poll(t *testing.T) string {
	t.Helper()
	h.advance(2 * time.Second)
	snap := h.c.Build(context.Background())
	if len(snap.Sessions) != 1 || snap.Sessions[0].Status == nil {
		t.Fatalf("應有一張帶燈號的卡片：%+v", snap.Sessions)
	}
	return snap.Sessions[0].Status.State
}

func TestEscCancelDowngradesOnSecondPoll(t *testing.T) {
	h := newHarness(t, "%1")
	h.permission("%1")
	h.tmux.screens["%1"] = fixture(t, "screen-bash-after-esc.txt")
	if got := h.poll(t); got != hookstore.StatePermission {
		t.Fatalf("第一次沒看到提示不應降級，got %s", got)
	}
	if got := h.poll(t); got != hookstore.StateIdle {
		t.Fatalf("第二次仍沒看到應降為等你輸入，got %s", got)
	}
}

func TestPromptStillShownStaysLit(t *testing.T) {
	h := newHarness(t, "%1")
	h.permission("%1")
	h.tmux.screens["%1"] = fixture(t, "screen-bash-prompt.txt")
	for i := 0; i < 30; i++ { // 提示掛 1 分鐘
		if got := h.poll(t); got != hookstore.StatePermission {
			t.Fatalf("提示還在畫面上，第 %d 輪燈號變成 %s", i, got)
		}
	}
	if h.tmux.capturedCount("%1") == 0 {
		t.Fatal("應有實際抓畫面")
	}
}

func TestCaptureErrorDoesNothing(t *testing.T) {
	h := newHarness(t, "%1")
	h.permission("%1")
	h.tmux.errs["%1"] = errors.New("tmux capture-pane: can't find pane")
	for i := 0; i < 5; i++ {
		if got := h.poll(t); got != hookstore.StatePermission {
			t.Fatalf("抓畫面失敗不應動燈號，got %s", got)
		}
	}
}

func TestEmptyScreenDoesNothing(t *testing.T) {
	h := newHarness(t, "%1")
	h.permission("%1")
	h.tmux.screens["%1"] = "\n\n   \n"
	for i := 0; i < 5; i++ {
		if got := h.poll(t); got != hookstore.StatePermission {
			t.Fatalf("空畫面不應動燈號，got %s", got)
		}
	}
}

func TestDeadPaneNotCaptured(t *testing.T) {
	h := newHarness(t, "%1")
	h.permission("%1")
	h.permission("%9") // %9 不在 list-panes 裡
	h.tmux.screens["%1"] = fixture(t, "screen-bash-prompt.txt")
	h.poll(t)
	h.poll(t)
	if n := h.tmux.capturedCount("%9"); n != 0 {
		t.Fatalf("不存在的 pane 不應抓畫面，抓了 %d 次", n)
	}
}

func TestNewHookDuringCaptureWins(t *testing.T) {
	h := newHarness(t, "%1")
	h.permission("%1")
	h.tmux.screens["%1"] = fixture(t, "screen-bash-after-esc.txt")
	h.poll(t) // 第一次沒看到
	// 第二次抓畫面途中，使用者在終端機按了允許、PostToolUse 送到。
	h.tmux.onCapture = func(pane string) {
		h.hooks.Apply(hookstore.Event{Pane: pane, SessionID: "s-" + pane, HookEventName: "PostToolUse", ToolName: "Bash"})
	}
	if got := h.poll(t); got != hookstore.StateWorking {
		t.Fatalf("抓畫面期間的新 hook 應優先（CAS 失敗），got %s", got)
	}
	if st := h.hooks.Get("%1"); st.Event != "PostToolUse" {
		t.Fatalf("不應被畫面檢查覆寫：%+v", st)
	}
}

func TestWorkingInterruptedDowngrades(t *testing.T) {
	h := newHarness(t, "%1")
	h.hooks.Apply(hookstore.Event{Pane: "%1", SessionID: "s", HookEventName: "UserPromptSubmit"})
	h.tmux.screens["%1"] = fixture(t, "screen-working-streaming.txt")
	for i := 0; i < 10; i++ {
		if got := h.poll(t); got != hookstore.StateWorking {
			t.Fatalf("還在轉圈不應熄，got %s", got)
		}
	}
	h.tmux.screens["%1"] = fixture(t, "screen-text-interrupted.txt")
	h.poll(t)
	if got := h.poll(t); got != hookstore.StateIdle {
		t.Fatalf("中斷後應降為等你輸入，got %s", got)
	}
}

func TestPriorityOverNewerPane(t *testing.T) {
	h := newHarness(t, "%1", "%2")
	h.permission("%1")
	h.tmux.screens["%1"] = fixture(t, "screen-bash-prompt.txt")
	h.advance(5 * time.Second)
	// 另一個 pane 之後才有事件（較新），也不能蓋掉真正在等的提示。
	h.hooks.Apply(hookstore.Event{Pane: "%2", SessionID: "s2", HookEventName: "Stop"})
	if got := h.poll(t); got != hookstore.StatePermission {
		t.Fatalf("等你允許應優先於較新的做完，got %s", got)
	}
	// 被畫面檢查降級的 pane（UpdatedAt 不變）不能因為檢查時間較新而蓋掉另一個 pane。
	h.tmux.screens["%1"] = fixture(t, "screen-bash-after-esc.txt")
	h.poll(t)
	if got := h.poll(t); got != hookstore.StateDone {
		t.Fatalf("降級後應由事件較新的 %%2（做完）代表，got %s", got)
	}
}

func TestNarrowLongRunningNeverDowngrades(t *testing.T) {
	// 手機（40 欄）上跑長指令：狀態列沒有 esc to interrupt，中途打 / 跳補全清單、按 ctrl+o，燈都要維持工作中。
	h := newHarness(t, "%1")
	h.hooks.Apply(hookstore.Event{Pane: "%1", SessionID: "s", HookEventName: "PreToolUse", ToolName: "Bash"})
	for _, f := range []string{"screen-narrow40-auto-running.txt", "screen-narrow40-acceptedits-running.txt",
		"screen-narrow40-working-slash-menu.txt", "screen-narrow40-working-at-menu.txt",
		"screen-narrow40-working-ctrl-o.txt", "screen-narrow40-todos-running.txt"} {
		h.tmux.screens["%1"] = fixture(t, f)
		for i := 0; i < 10; i++ {
			if got := h.poll(t); got != hookstore.StateWorking {
				t.Fatalf("%s 第 %d 輪：工作中被誤熄成 %s", f, i, got)
			}
		}
	}
	// 真的中斷（窄畫面殘影「Interrupte· What should」）才熄。
	h.tmux.screens["%1"] = fixture(t, "screen-narrow40-acceptedits-interrupted.txt")
	h.poll(t)
	if got := h.poll(t); got != hookstore.StateIdle {
		t.Fatalf("中斷後應降為等你輸入，got %s", got)
	}
}

func TestCaptureRateLimitedPerPane(t *testing.T) {
	h := newHarness(t, "%1")
	h.permission("%1")
	h.tmux.screens["%1"] = fixture(t, "screen-bash-prompt.txt")
	h.poll(t)
	// hook 爆量：同一秒內再重算 5 次，不應再抓畫面。
	for i := 0; i < 5; i++ {
		h.c.Build(context.Background())
	}
	if n := h.tmux.capturedCount("%1"); n != 1 {
		t.Fatalf("1 秒內同一 pane 只應抓 1 次，抓了 %d 次", n)
	}
}

func TestEarlyEscInterruptGoesIdleQuickly(t *testing.T) {
	// 送出 prompt 後第 3 秒就按 Esc（QA 實測曾要等到約 13 秒）：以 1 秒為步距重算，應在 5 秒內熄燈。
	h := newHarness(t, "%1")
	h.hooks.Apply(hookstore.Event{Pane: "%1", SessionID: "s", HookEventName: "UserPromptSubmit"})
	h.tmux.screens["%1"] = fixture(t, "screen-working-streaming.txt")
	for i := 0; i < 3; i++ {
		h.advance(time.Second)
		h.c.Build(context.Background())
	}
	h.tmux.screens["%1"] = fixture(t, "screen-text-interrupted.txt")
	for elapsed := 1; elapsed <= 5; elapsed++ {
		h.advance(time.Second)
		h.c.Build(context.Background())
		if st := h.hooks.Get("%1").State; st == hookstore.StateIdle {
			t.Logf("Esc 後 %d 秒熄燈", elapsed)
			return
		}
	}
	t.Fatalf("Esc 後 5 秒內應熄燈，仍是 %s", h.hooks.Get("%1").State)
}

func TestPendingSchedulesRecheck(t *testing.T) {
	h := newHarness(t, "%1")
	h.permission("%1")
	h.tmux.screens["%1"] = fixture(t, "screen-bash-after-esc.txt")
	h.poll(t) // 第一次看到提示消失 → 應排一次補看
	select {
	case <-h.c.trigger:
	case <-time.After(recheckDelay + 500*time.Millisecond):
		t.Fatal("第一次看到變化後應在 recheckDelay 後觸發重算")
	}
}

// fakeProcs 回傳固定的「不在 tmux」session，並記下收到的 pane tty。
type fakeProcs struct {
	res  []procscan.Session
	err  error
	ttys map[string]bool
}

func (f *fakeProcs) Scan(_ context.Context, ttys map[string]bool) ([]procscan.Session, error) {
	f.ttys = ttys
	return f.res, f.err
}

func TestProcessCardsAreReadOnlyAndLitBySessionID(t *testing.T) {
	cfg := config.Config{ProfilesRoot: t.TempDir(), ClaudeHome: t.TempDir(), PollInterval: time.Second}
	hooks := hookstore.New("")
	c := New(cfg, &fakeTmux{panes: []string{"%1"}}, hooks, nil)
	sid := "aaaaaaaa-0000-4000-8000-000000000001"
	started := time.Unix(1792738964, 0)
	fp := &fakeProcs{res: []procscan.Session{
		{Tool: "claude", Profile: "work", PID: 2432, TTY: "ttys002", Started: started, SessionID: sid, Cwd: "/home/tester"},
		{Tool: "codex", Profile: "team", PID: 30449, TTY: "ttys001", Started: started},
	}}
	c.SetProcScanner(fp)
	hooks.Apply(hookstore.Event{SessionID: sid, Profile: "work", HookEventName: "Stop", LastAssistantMessage: "做好了"})
	// 一個已不存在的非 tmux session 燈號：應被清掉。
	hooks.Apply(hookstore.Event{SessionID: "bbbbbbbb-0000-4000-8000-000000000002", HookEventName: "UserPromptSubmit"})

	snap := c.Build(context.Background())
	var cards []Card
	for _, s := range snap.Sessions {
		if s.Process != nil {
			cards = append(cards, s)
		}
	}
	if len(cards) != 2 {
		t.Fatalf("應有 2 張唯讀卡片：%+v", snap.Sessions)
	}
	for _, card := range cards {
		if card.TermURL != "" || !strings.HasPrefix(card.ID, "proc:") {
			t.Fatalf("唯讀卡片不得有 termUrl：%+v", card)
		}
	}
	if cards[0].Status == nil || cards[0].Status.State != hookstore.StateDone || cards[0].Tool != "claude" || cards[0].Profile != "work" {
		t.Fatalf("claude 卡片應以 session id 對到燈號：%+v", cards[0])
	}
	if cards[1].Status != nil || cards[1].Tool != "codex" {
		t.Fatalf("codex 卡片不該有燈號：%+v", cards[1])
	}
	if hooks.Get(hookstore.SessionKey("bbbbbbbb-0000-4000-8000-000000000002")) != nil {
		t.Fatal("程序表沒有的 session 燈號應清掉")
	}
	if hooks.Get(hookstore.SessionKey(sid)) == nil {
		t.Fatal("仍存活 session 的燈號不能清")
	}

	// 別的 profile 送來同一個 session id 的 hook：不掛到 work 的卡片上。
	hooks.Apply(hookstore.Event{SessionID: sid, Profile: "second", HookEventName: "UserPromptSubmit"})
	snap = c.Build(context.Background())
	for _, s := range snap.Sessions {
		if s.Process != nil && s.Process.PID == 2432 && s.Status != nil {
			t.Fatalf("profile 不符的 hook 不應亮燈：%+v", s.Status)
		}
	}

	// 掃程序失敗：不列卡片，但也不清非 tmux 燈號。
	fp.res, fp.err = nil, errors.New("ps 失敗")
	snap = c.Build(context.Background())
	for _, s := range snap.Sessions {
		if s.Process != nil {
			t.Fatal("掃描失敗時不列唯讀卡片")
		}
	}
	if hooks.Get(hookstore.SessionKey(sid)) == nil {
		t.Fatal("掃描失敗時不得誤清燈號")
	}
}

// resolverProcs 另外實作 TmuxSessionResolver：模擬 tmux pane 上 claude 的 sessions/<pid>.json。
type resolverProcs struct {
	fakeProcs
	sid string
}

func (r *resolverProcs) TmuxClaudeSession(profile, _ string) (string, bool) {
	return r.sid, profile == "work" && r.sid != ""
}

func TestNicknameFollowsClaudeSessionAcrossResume(t *testing.T) {
	h := newHarness(t, "%1")
	meta, _ := sessionmeta.Open("")
	h.c.SetMeta(meta)
	sid := "aaaaaaaa-0000-4000-8000-000000000001"
	// 封存前的暱稱存在 claude:<sid>；新 tmux session（dc-t）還沒收到任何 hook。
	_ = meta.SetNickname("claude:"+sid, "客服 bug 調查")
	_ = meta.SetNickname("tmux:old-session", "已結束的 session")
	h.c.SetProcScanner(&resolverProcs{sid: sid})
	snap := h.c.Build(context.Background())
	if len(snap.Sessions) != 1 || snap.Sessions[0].Nickname != "客服 bug 調查" {
		t.Fatalf("--resume 回來的 tmux 卡片應帶回暱稱：%+v", snap.Sessions)
	}
	if card := snap.Sessions[0]; card.ResumeCommand != "aiuse claude work --resume "+sid || card.MetaKey != "tmux:dc-t" {
		t.Fatalf("接手指令或隱藏 key 不對：%+v", card)
	}
	// 帶回暱稱時同步補設 iTerm2 標題（以 session id 為目標）。
	h.tmux.mu.Lock()
	var titled bool
	for _, c := range h.tmux.calls {
		if len(c) >= 5 && c[0] == "set-option" && c[2] == "$1" && c[3] == "@dc_nickname" && c[4] == "客服 bug 調查" {
			titled = true
		}
	}
	h.tmux.mu.Unlock()
	if !titled {
		t.Fatal("帶回暱稱時應補設 @dc_nickname")
	}
	// 已消失的 tmux: key 被清掉，claude: key 保留。
	if meta.Resolve("tmux:old-session", "") != "" || meta.Resolve("claude:"+sid, "") == "" {
		t.Fatal("PruneEphemeral 應只清已消失的 tmux: key")
	}
	// tmux 封存紀錄只是歷史，不隱藏同名 session；同一段 Claude 對話重新出現時自動移除該紀錄。
	_, _ = meta.AddArchive(sessionmeta.Archive{Key: "tmux:dc-t@1", Kind: "tmux", Label: "old-session", Tool: "claude", SessionID: sid})
	// 同名 tmux session 還活著（封存流程寫完紀錄、kill 之前）：不算重開，紀錄保留。
	_, _ = meta.AddArchive(sessionmeta.Archive{Key: "tmux:dc-t@2", Kind: "tmux", Label: "dc-t", Tool: "claude", SessionID: sid})
	snap = h.c.Build(context.Background())
	if len(snap.Sessions) != 1 || len(snap.Archived) != 1 || snap.Archived[0].Key != "tmux:dc-t@2" {
		t.Fatalf("tmux 紀錄不應隱藏卡片、重開的 Claude 紀錄應移除：%+v %+v", snap.Sessions, snap.Archived)
	}
}

// 已封存區的 proc 紀錄要反映程序死活：隱藏中的程序還在＝alive，程序消失＝已結束；
// 程序表查詢失敗時無法判定，保守當作仍在執行。tmux 紀錄一律 alive=false（本來就已結束）。
func TestArchivedProcAliveFollowsProcessTable(t *testing.T) {
	h := newHarness(t, "%1")
	meta, _ := sessionmeta.Open("")
	h.c.SetMeta(meta)
	started := time.Unix(1792738964, 0)
	fp := &fakeProcs{res: []procscan.Session{{Tool: "codex", Profile: "team", PID: 30449, TTY: "ttys001", Started: started}}}
	h.c.SetProcScanner(fp)
	key := fmt.Sprintf("proc:%d:%d", 30449, started.Unix())
	_, _ = meta.AddArchive(sessionmeta.Archive{Key: key, Kind: "proc", Tool: "codex", Label: "codex-team @ ttys001"})
	_, _ = meta.AddArchive(sessionmeta.Archive{Key: "tmux:dc-x@1", Kind: "tmux", Tool: "codex", Label: "dc-x"})

	aliveOf := func(snap Snapshot) map[string]bool {
		out := map[string]bool{}
		for _, a := range snap.Archived {
			out[a.Key] = a.Alive
		}
		return out
	}
	snap := h.c.Build(context.Background())
	for _, s := range snap.Sessions {
		if s.Process != nil {
			t.Fatalf("封存的唯讀卡片應隱藏：%+v", s)
		}
	}
	if got := aliveOf(snap); len(got) != 2 || !got[key] || got["tmux:dc-x@1"] {
		t.Fatalf("程序存活時 proc 紀錄應 alive=true、tmux 紀錄 alive=false：%+v", snap.Archived)
	}

	// 程序表查詢失敗：不知道程序死活，維持 alive=true。
	fp.res, fp.err = nil, errors.New("ps 失敗")
	if got := aliveOf(h.c.Build(context.Background())); !got[key] {
		t.Fatalf("程序表查詢失敗時不得誤報已結束：%+v", got)
	}

	// 程序結束（程序表正常但沒有它）：alive=false，紀錄仍留著。
	fp.err = nil
	snap = h.c.Build(context.Background())
	if got := aliveOf(snap); len(got) != 2 || got[key] {
		t.Fatalf("程序消失後 proc 紀錄應 alive=false 且仍保留：%+v", snap.Archived)
	}
	body, _ := json.Marshal(snap.Archived)
	if !strings.Contains(string(body), `"alive":false`) || !strings.Contains(string(body), `"kind":"proc"`) {
		t.Fatalf("快照 JSON 應攤平 Archive 欄位並帶 alive：%s", body)
	}
}

// 工具判斷：aiuse 標記、aiuse 名稱、直接模式名稱（dc-claude-*／dc-codex-*）、最後才看 pane 指令。
func TestGuessTool(t *testing.T) {
	cases := map[string]tmux.Session{
		"codex":  {Name: "dc-codex-20261121T000000Z-abcdef"},
		"claude": {Name: "dc-claude-20261121T000000Z-abcdef"},
		"":       {Name: "plain"},
	}
	cases["codex"+"/v1"] = tmux.Session{Name: "x", Managed: "codex-v1"}
	cases["claude"+"/pane"] = tmux.Session{Name: "y", ActivePane: &tmux.Pane{Command: "claude"}}
	for want, s := range cases {
		want, _, _ = strings.Cut(want, "/")
		if got := guessTool(s); got != want {
			t.Errorf("guessTool(%+v)=%q want %q", s, got, want)
		}
	}
}
