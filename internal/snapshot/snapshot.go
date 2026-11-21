// Package snapshot 組出總覽頁的完整快照：tmux session、hook 燈號、transcript 摘要、state 任務、看門狗心跳。
// 每次都產生完整快照（不送增量），前端只要「拿到最新一份就重畫」，斷線重連不必補洞。
package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chiyum/remote-console/internal/config"
	"github.com/chiyum/remote-console/internal/hookstore"
	"github.com/chiyum/remote-console/internal/procscan"
	"github.com/chiyum/remote-console/internal/sessionmeta"
	"github.com/chiyum/remote-console/internal/tasks"
	"github.com/chiyum/remote-console/internal/tmux"
	"github.com/chiyum/remote-console/internal/transcript"
	"github.com/chiyum/remote-console/internal/validate"
)

// Card 是總覽頁的一張 session 卡片。
type Card struct {
	tmux.Session
	TermURL    string              `json:"termUrl,omitempty"`
	Status     *hookstore.Status   `json:"status,omitempty"`
	Transcript *transcript.Summary `json:"transcript,omitempty"`
	// Process 只在「不在 tmux 的既有 session」唯讀卡片出現；這種卡片沒有 termUrl，不能從網頁操作。
	Process *procscan.Session `json:"process,omitempty"`
	// Nickname 是使用者設定的暱稱（空＝沒設，前端顯示 tmux 名稱）。
	Nickname string `json:"nickname,omitempty"`
	// MetaKey 是卡片的 meta key：tmux:<名稱> 或 proc:<pid>:<啟動秒>（後者也是唯讀卡片封存後的隱藏 key）。
	MetaKey string `json:"metaKey,omitempty"`
	// ResumeCommand 是重開這段對話的 aiuse 指令；空字串＝不是 aiuse 管的 session，封存後無法找回。
	ResumeCommand string `json:"resumeCommand,omitempty"`
	// ClaudeSessionID 是這張卡片目前對應的 Claude session id（hook 或 sessions/<pid>.json），封存紀錄用。
	ClaudeSessionID string `json:"-"`
	// nickKey 是暱稱的權威 key（已知 Claude session id 時是 claude:<sid>，否則是 MetaKey）；
	// nickFallback 只在 tmux 卡片已知 session id 時是 tmux:<名稱>（session id 還不知道時設的暱稱由它搬過來）。
	nickKey      string
	nickFallback string
}

// ProcScanner 找不在 tmux 的 aiuse session（測試可注入；nil 表示不掃）。
type ProcScanner interface {
	Scan(ctx context.Context, tmuxTTYs map[string]bool) ([]procscan.Session, error)
}

// TmuxSessionResolver 是 ProcScanner 的選配能力：查 tmux pane 上 claude 的 session id（procscan.Scanner 有實作）。
type TmuxSessionResolver interface {
	TmuxClaudeSession(profile, tty string) (string, bool)
}

// Snapshot 是推給前端的完整資料。
type Snapshot struct {
	GeneratedAt time.Time      `json:"generatedAt"`
	Host        string         `json:"host"`
	Sessions    []Card         `json:"sessions"`
	Tasks       []tasks.Task   `json:"tasks"`
	Watchdog    tasks.Watchdog `json:"watchdog"`
	// Archived 是已封存區的紀錄（最新的在前）；被封存的卡片不會出現在 Sessions。
	Archived []ArchivedItem `json:"archived"`
	Errors   []string       `json:"errors,omitempty"`
}

// ArchivedItem 是推給前端的封存紀錄：持久化的 sessionmeta.Archive 加上只在快照時判定的欄位。
// Alive 不放進 sessionmeta.Archive，是因為程序死活是即時狀態，寫進封存檔會變成過期資料。
type ArchivedItem struct {
	sessionmeta.Archive
	// Alive 只對 proc 紀錄有意義：被隱藏的唯讀卡片本輪仍在程序表裡。
	// 程序表本輪查詢失敗時無法判定，保守當作仍在執行（不誤報「已結束」）。
	Alive bool `json:"alive"`
}

// Collector 定期與事件觸發時重算快照，內容有變才 publish。
type Collector struct {
	cfg      config.Config
	tmux     tmux.Runner
	hooks    *hookstore.Store
	tracker  *transcript.Tracker
	procs    ProcScanner
	meta     *sessionmeta.Store
	publish  func([]byte)
	trigger  chan struct{}
	mu       sync.Mutex
	recheck  atomic.Bool // 已排定「第一次看到變化後補看一次」
	last     []byte
	lastHash [32]byte
}

// New 建立 Collector。
func New(cfg config.Config, r tmux.Runner, hooks *hookstore.Store, publish func([]byte)) *Collector {
	return &Collector{cfg: cfg, tmux: r, hooks: hooks, tracker: transcript.NewTracker(), publish: publish, trigger: make(chan struct{}, 1)}
}

// SetProcScanner 啟用「不在 tmux 的既有 session」唯讀卡片（main 於啟動時設定一次）。
func (c *Collector) SetProcScanner(p ProcScanner) { c.procs = p }

// SetMeta 啟用暱稱與封存（main 於啟動時設定一次；nil＝不啟用）。
func (c *Collector) SetMeta(m *sessionmeta.Store) { c.meta = m }

// TriggerAfterStop 在收到 Stop 時呼叫：強制重讀該 transcript，並在 1 秒、3 秒後各補算一次，
// 涵蓋 claude 在 Stop 之後才把最後幾行寫進 transcript 的情況。
func (c *Collector) TriggerAfterStop(transcriptPath string) {
	if transcriptPath != "" {
		c.tracker.Invalidate(transcriptPath)
	}
	c.Trigger()
	for _, d := range []time.Duration{time.Second, 3 * time.Second} {
		time.AfterFunc(d, func() {
			if transcriptPath != "" {
				c.tracker.Invalidate(transcriptPath)
			}
			c.Trigger()
		})
	}
}

// Trigger 要求盡快重算（hook 事件、新開 session 後呼叫），合併多次觸發。
func (c *Collector) Trigger() {
	select {
	case c.trigger <- struct{}{}:
	default:
	}
}

// Run 主迴圈：每 PollInterval 或被觸發時重算。
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(c.cfg.PollInterval)
	defer t.Stop()
	c.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.trigger:
		}
		c.refresh(ctx)
	}
}

// Latest 回傳最後一份快照 JSON。
func (c *Collector) Latest(ctx context.Context) []byte {
	c.mu.Lock()
	b := c.last
	c.mu.Unlock()
	if b == nil {
		c.refresh(ctx)
		c.mu.Lock()
		b = c.last
		c.mu.Unlock()
	}
	return b
}

func (c *Collector) refresh(ctx context.Context) {
	defer func() {
		// 單次收集 panic 不拖垮服務，下一輪再試。
		if r := recover(); r != nil {
			log.Printf("[snapshot] panic: %v", r)
		}
	}()
	snap := c.Build(ctx)
	// 雜湊排除 generatedAt，內容沒變就不推，避免每 2 秒白白重畫。
	snap.GeneratedAt = time.Time{}
	body, _ := json.Marshal(snap)
	h := sha256.Sum256(body)
	snap.GeneratedAt = time.Now()
	out, _ := json.Marshal(snap)
	c.mu.Lock()
	changed := h != c.lastHash
	c.lastHash, c.last = h, out
	c.mu.Unlock()
	if changed && c.publish != nil {
		c.publish(out)
	}
}

// Build 收集一份快照（已封存的卡片不列）。
func (c *Collector) Build(ctx context.Context) Snapshot {
	snap, all, listOK := c.collect(ctx)
	snap.Sessions = []Card{}
	// hiddenAlive 記下本輪仍活著、但因封存而隱藏的唯讀卡片 key，供已封存區顯示程序是否已結束。
	hiddenAlive := map[string]bool{}
	for _, card := range all {
		// 只有唯讀卡片會因封存而隱藏（程序還活著、無法結束）；tmux 卡片封存時已被 kill，
		// 之後同名出現的是新 session，照常顯示。
		if c.meta != nil && card.Process != nil && c.meta.IsArchived(card.MetaKey) {
			hiddenAlive[card.MetaKey] = true
			continue
		}
		snap.Sessions = append(snap.Sessions, card)
	}
	snap.Archived = []ArchivedItem{}
	if c.meta != nil {
		for _, a := range c.meta.Archived() {
			alive := false
			if a.Kind == "proc" {
				alive = !listOK || hiddenAlive[a.Key]
			}
			snap.Archived = append(snap.Archived, ArchivedItem{Archive: a, Alive: alive})
		}
	}
	return snap
}

// FindCard 以當下的 tmux／程序表重新列一次，找出 id 相符的卡片（含已封存隱藏的）。
// 寫入 API 一律經它驗證 id，不信任前端傳來的 session 名稱或 pid。
func (c *Collector) FindCard(ctx context.Context, id string) (Card, bool) {
	_, all, _ := c.collect(ctx)
	for _, card := range all {
		if card.ID == id {
			return card, true
		}
	}
	return Card{}, false
}

// collect 列出所有卡片（不分封存與否），並補上暱稱、隱藏 key、接手指令與 iTerm2 標題同步。
// 第三個回傳值＝tmux 與程序表本輪都查詢成功（失敗時「卡片不在」不代表程序已結束）。
func (c *Collector) collect(ctx context.Context) (Snapshot, []Card, bool) {
	snap := Snapshot{Host: c.cfg.PublicHost, Sessions: []Card{}}
	sessions, err := tmux.ListSessions(ctx, c.tmux)
	if err != nil {
		snap.Errors = append(snap.Errors, "tmux: "+err.Error())
	}
	panes, _ := tmux.ListPanes(ctx, c.tmux)
	alive := map[string]bool{}
	paneSession := map[string]string{}
	paneTTYs := map[string]bool{}
	for _, p := range panes {
		alive[p.ID] = true
		paneSession[p.ID] = p.Session
		if p.TTY != "" {
			paneTTYs[p.TTY] = true
		}
	}
	procs, procErr := c.scanProcs(ctx, paneTTYs, err)
	for _, ps := range procs {
		if k := hookstore.SessionKey(ps.SessionID); k != "" {
			alive[k] = true
		}
	}
	if procErr != nil {
		// 掃程序失敗時不知道誰還活著：保留所有非 tmux 燈號，下一輪再判斷。
		for _, k := range c.hooks.Keys(hookstore.SessionKeyPrefix) {
			alive[k] = true
		}
		snap.Errors = append(snap.Errors, "程序表："+procErr.Error())
	}
	// tmux 正常回應時才清理已不存在 pane 的燈號；tmux 查詢失敗時不動，避免誤清。
	if err == nil {
		c.hooks.Prune(alive)
		c.resolveFromScreens(ctx, alive)
	}
	keepTranscripts := map[string]bool{}
	for _, s := range sessions {
		card := Card{Session: s}
		if validate.SessionName(s.Name) {
			card.TermURL = "/term/?arg=" + s.Name
		}
		if card.Tool == "" {
			card.Tool = guessTool(s)
		}
		// 一個 session 可能有多個 pane；要人處理的燈優先，同級取最後事件較新的代表整張卡。
		for pid, sname := range paneSession {
			if sname != s.Name {
				continue
			}
			if st := c.hooks.Get(pid); st != nil && betterStatus(st, card.Status) {
				card.Status = st
			}
		}
		if card.Status != nil {
			if card.Profile == "" {
				card.Profile = card.Status.Profile
			}
			card.Tool = "claude"
			if p := card.Status.TranscriptPath; c.transcriptAllowed(p) {
				if sum, err := c.tracker.Get(p); err == nil {
					// Stop payload 帶的最後回覆最準（不受 transcript 寫入時序影響），本輪有就優先用。
					if card.Status.LastMessage != "" {
						sum.LastText = card.Status.LastMessage
					}
					sum.LastText = truncate(sum.LastText, 600)
					card.Transcript = &sum
					keepTranscripts[p] = true
				}
			}
		}
		snap.Sessions = append(snap.Sessions, card)
	}
	for _, ps := range procs {
		snap.Sessions = append(snap.Sessions, c.processCard(ps, keepTranscripts))
	}
	c.tracker.Forget(keepTranscripts)
	listOK := err == nil && procErr == nil
	c.applyMeta(ctx, snap.Sessions, listOK)
	var badState []string
	snap.Tasks, badState = tasks.LoadTasks(filepath.Join(c.cfg.ClaudeHome, "state"), c.cfg.ClaudeHome)
	for _, b := range badState {
		snap.Errors = append(snap.Errors, "state 檔解析失敗（未修改）："+b)
	}
	if snap.Tasks == nil {
		snap.Tasks = []tasks.Task{}
	}
	snap.Watchdog = tasks.LoadWatchdog(filepath.Join(c.cfg.ClaudeHome, "state"), time.Local)
	all := snap.Sessions
	return snap, all, listOK
}

// applyMeta 為每張卡片決定 key、取暱稱、組接手指令，並讓 tmux session 的 iTerm2 標題跟上暱稱。
// listOK＝tmux 與程序表本輪都查詢成功；只有這時才清掉已消失 session 的 tmux:／proc: 暱稱，避免查詢失敗誤清。
func (c *Collector) applyMeta(ctx context.Context, cards []Card, listOK bool) {
	resolver, _ := c.procs.(TmuxSessionResolver)
	alive := map[string]bool{}
	for i := range cards {
		card := &cards[i]
		sid := ""
		if card.Process != nil {
			sid = card.Process.SessionID
			card.MetaKey = fmt.Sprintf("proc:%d:%d", card.Process.PID, card.Process.Started.Unix())
		} else {
			card.MetaKey = "tmux:" + card.Name
			if card.Status != nil {
				sid = card.Status.SessionID
			}
			// 還沒收到 hook（剛 --resume 回來）時改從 sessions/<pid>.json 找，暱稱才能立刻帶回。
			if hookstore.SessionKey(sid) == "" && resolver != nil && card.ActivePane != nil && (card.Tool == "claude" || card.Tool == "") {
				if found, ok := resolver.TmuxClaudeSession(card.Profile, card.ActivePane.TTY); ok {
					sid = found
					card.Tool = "claude"
				}
			}
		}
		if hookstore.SessionKey(sid) == "" {
			sid = ""
		}
		card.ClaudeSessionID = sid
		card.nickKey = card.MetaKey
		if sid != "" {
			card.nickKey = "claude:" + sid
			if card.Process == nil {
				card.nickFallback = card.MetaKey
			}
		}
		alive[card.MetaKey] = true
		if card.Profile != "" && (card.Managed != "" || card.Process != nil) {
			card.ResumeCommand = sessionmeta.ResumeCommand(card.Tool, card.Profile, sid)
		}
		if c.meta == nil {
			continue
		}
		card.Nickname = c.meta.Resolve(card.nickKey, card.nickFallback)
		if card.Process == nil {
			if err := tmux.SyncTitle(ctx, c.tmux, card.Session, card.Nickname, time.Now()); err != nil {
				log.Printf("[snapshot] %s 同步 tab 標題失敗：%v", card.Name, err)
			}
		}
		if sid != "" {
			c.dropResumedArchives(*card)
		}
	}
	if c.meta != nil && listOK {
		c.meta.PruneEphemeral(alive)
	}
}

// dropResumedArchives：同一段 Claude 對話以「新的活著的卡片」重新出現時，移除它的封存紀錄（A10，使用者裁決）。
// 不算重開的兩種情況：封存時那個 proc 程序本身還活著（同一個隱藏 key）；同名 tmux session 還在
// （封存流程寫完紀錄、kill 之前被別的輪詢看到）。Codex 沒有 session id，不適用。
func (c *Collector) dropResumedArchives(card Card) {
	removed := c.meta.RemoveResumedClaude(card.ClaudeSessionID, func(a sessionmeta.Archive) bool {
		if card.Process != nil {
			return a.Key == card.MetaKey
		}
		return a.Kind == "tmux" && a.Label == card.Name
	})
	for _, k := range removed {
		log.Printf("[snapshot] Claude session %s 已以 %s 重新出現，移除封存紀錄 %s", card.ClaudeSessionID, card.Name, k)
	}
}

// NickKey 回傳卡片暱稱的權威 key（server 設定暱稱時用；以 collect 當下的判定為準）。
func (card Card) NickKey() string { return card.nickKey }

// scanProcs 找不在 tmux 的 session。tmux 查詢失敗時拿不到 pane tty，可能把 tmux 裡的程序誤當成
// 「不在 tmux」，所以這輪不列（回傳錯誤讓燈號保留）。
func (c *Collector) scanProcs(ctx context.Context, paneTTYs map[string]bool, tmuxErr error) ([]procscan.Session, error) {
	if c.procs == nil {
		return nil, nil
	}
	if tmuxErr != nil {
		return nil, fmt.Errorf("tmux 查詢失敗，本輪不列不在 tmux 的 session")
	}
	return c.procs.Scan(ctx, paneTTYs)
}

// processCard 組出不在 tmux 的唯讀卡片：ID／名稱帶 proc 前綴、沒有 termUrl（不可能接到別的 session）。
func (c *Collector) processCard(ps procscan.Session, keep map[string]bool) Card {
	p := ps
	card := Card{
		Session: tmux.Session{
			ID:      fmt.Sprintf("proc:%d", ps.PID),
			Name:    fmt.Sprintf("%s-%s @ %s", ps.Tool, ps.Profile, ps.TTY),
			Created: ps.Started, Activity: ps.Started,
			Profile: ps.Profile, Tool: ps.Tool,
		},
		Process: &p,
	}
	transcriptPath := ps.TranscriptPath
	if ps.Tool == "claude" {
		// hook 的 profile（hook 指令帶的 query）必須和程序表判定的 profile 相同，才把燈號掛上這張卡；
		// 不同 profile 的 hook 冒用同一個 session id 時不採信。
		if st := c.hooks.Get(hookstore.SessionKey(ps.SessionID)); st != nil && st.Profile == ps.Profile {
			card.Status = st
			card.Activity = st.UpdatedAt
			if st.TranscriptPath != "" {
				transcriptPath = st.TranscriptPath
			}
		}
	}
	if ps.Tool == "claude" && c.transcriptAllowed(transcriptPath) {
		if sum, err := c.tracker.Get(transcriptPath); err == nil {
			if card.Status != nil && card.Status.LastMessage != "" {
				sum.LastText = card.Status.LastMessage
			}
			sum.LastText = truncate(sum.LastText, 600)
			card.Transcript = &sum
			keep[transcriptPath] = true
		}
	}
	return card
}

// promptGrace 是提示亮燈後多久才開始看畫面：PermissionRequest 比對話框早約 40ms 送達，留足餘裕。
const promptGrace = 1500 * time.Millisecond

// workingIdle 是「工作中」最後一個 hook 之後多久開始看畫面。工作中按 Esc 中斷不送 Stop，要靠畫面；
// 降成等你輸入必須看到閒置正面證據（Interrupted、上一輪結束行、? for shortcuts），剛送出 prompt
// 的畫面已有新的 prompt 行與轉圈行，不會被當成閒置，所以只留 1.5 秒避開 hook 與畫面之間的時間差。
// 原本 10 秒會讓「送出後幾秒就中斷」要等到約 13 秒才熄燈（QA 實測）。
const workingIdle = 1500 * time.Millisecond

// recheckDelay 是第一次看到變化後補看一次的延遲：略大於 MinMissGap，不必等下一個 2 秒輪詢。
// 取消／中斷後最慢約「一個輪詢間隔＋recheckDelay」熄燈。
const recheckDelay = hookstore.MinMissGap + 150*time.Millisecond

// captureBudget 是一輪畫面檢查的總時間上限：tmux 卡住時不拖慢整份快照。
const captureBudget = 3 * time.Second

// resolveFromScreens 對需要確認的 pane 並行抓畫面，交給 hookstore 判定是否改燈。
// 抓畫面失敗、逾時或畫面為空一律不動，寧可多亮也不把真的在等的燈熄掉。
func (c *Collector) resolveFromScreens(ctx context.Context, alive map[string]bool) {
	probes := c.hooks.Probes(promptGrace, workingIdle)
	if len(probes) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, captureBudget)
	defer cancel()
	var wg sync.WaitGroup
	var pending atomic.Bool
	for _, p := range probes {
		// Prune 已先刪掉不存在的 pane，這裡是防呆：不對已消失的 pane 下 tmux 指令。
		// 非 tmux session（sid: key）沒有畫面可抓，只靠 hook。
		if !alive[p.Pane] || !hookstore.IsPaneKey(p.Pane) {
			continue
		}
		wg.Add(1)
		go func(p hookstore.Probe) {
			defer wg.Done()
			screen, err := tmux.CaptureVisible(ctx, c.tmux, p.Pane)
			if err != nil || ctx.Err() != nil || strings.TrimSpace(screen) == "" {
				return
			}
			switch c.hooks.Observe(p, hookstore.ReadScreen(screen)) {
			case hookstore.OutcomeChanged:
				log.Printf("[snapshot] pane %s 依畫面改燈（原 %s）", p.Pane, p.State)
			case hookstore.OutcomePending:
				pending.Store(true)
			}
		}(p)
	}
	wg.Wait()
	if pending.Load() {
		c.scheduleRecheck()
	}
}

// scheduleRecheck 在 recheckDelay 後觸發一次重算；已有排程就不重複排。
func (c *Collector) scheduleRecheck() {
	if !c.recheck.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(recheckDelay, func() {
		c.recheck.Store(false)
		c.Trigger()
	})
}

// statusRank 是卡片彙整多個 pane 時的優先序：等你允許＞問你問題＞其他。
// 「要人處理」的燈不能因為另一個 pane 的事件較新就被蓋掉。
func statusRank(st *hookstore.Status) int {
	switch st.State {
	case hookstore.StatePermission:
		return 0
	case hookstore.StateQuestion:
		return 1
	}
	return 2
}

// betterStatus 判斷 cand 是否該取代 cur 代表整張卡：先比優先序，同級才比最後事件時間。
func betterStatus(cand, cur *hookstore.Status) bool {
	if cur == nil {
		return true
	}
	if rc, ru := statusRank(cand), statusRank(cur); rc != ru {
		return rc < ru
	}
	return cand.UpdatedAt.After(cur.UpdatedAt)
}

// transcriptAllowed 只讀 ~/.ai-profiles/claude/<p>/projects/ 底下的 .jsonl；
// hook payload 來自本機，但仍不讓它指定任意檔案給總覽頁讀。
func (c *Collector) transcriptAllowed(p string) bool {
	if p == "" || !strings.HasSuffix(p, ".jsonl") || strings.Contains(p, "..") {
		return false
	}
	rel, err := filepath.Rel(filepath.Join(c.cfg.ProfilesRoot, "claude"), filepath.Clean(p))
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) >= 3 && parts[1] == "projects" && parts[0] != ".."
}

func guessTool(s tmux.Session) string {
	switch {
	case s.Managed == "codex-v1" || s.Managed == "codex-direct-v1" || strings.HasPrefix(s.Name, "aiuse-codex-") || strings.HasPrefix(s.Name, "dc-codex-"):
		return "codex"
	// dc-claude-*／dc-codex-* 是沒有 aiuse 時網頁直接開的 session（直接模式），不帶任何 @aiuse_* 標記。
	case s.Managed == "claude-v1" || strings.HasPrefix(s.Name, "aiuse-claude-") || strings.HasPrefix(s.Name, "dc-claude-") || strings.HasPrefix(s.Name, "rc-"):
		return "claude"
	}
	if s.ActivePane != nil {
		switch s.ActivePane.Command {
		case "codex":
			return "codex"
		case "claude":
			return "claude"
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
