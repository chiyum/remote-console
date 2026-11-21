package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/chiyum/remote-console/internal/config"
	"github.com/chiyum/remote-console/internal/tmux"
	"github.com/chiyum/remote-console/internal/validate"
)

// LaunchRequest 是網頁「新開 session」的輸入。
type LaunchRequest struct {
	Tool    string `json:"tool"`
	Profile string `json:"profile"`
	Dir     string `json:"dir"`
}

// LaunchResult 是建立結果。
type LaunchResult struct {
	Name    string `json:"name"`
	TermURL string `json:"termUrl"`
	Dir     string `json:"dir"`
}

// Launcher 負責驗證輸入並以 exec 陣列建立 tmux session。
type Launcher struct {
	Cfg  config.Config
	Tmux tmux.Runner
	Now  func() time.Time
	// Settle 是建立後確認 session 沒有立即結束的等待時間（測試可設 0）。
	Settle time.Duration
	// codexLocks：profile（小寫）→ 容量 1 的 channel，串行化同 profile 的 Codex 新開（見 codexLaunchLock）。
	codexLocks sync.Map
}

// 有 aiuse 時，所有 Codex profile 一律交給 aiuse 建 session 再認領（見 launchViaAiuse），
// 命名、標記與 provenance 只由 aiuse 一處決定，新增 profile 不必改 dev-console。

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Launch 驗證後建立 session。所有錯誤若包 validate.ErrInvalid，handler 回 400。
// 設定的 aiuse 不存在或不可執行時走「直接模式」（launchDirect），否則照 aiuse 的多 profile 流程。
func (l *Launcher) Launch(ctx context.Context, req LaunchRequest) (LaunchResult, error) {
	if err := validate.Tool(req.Tool); err != nil {
		return LaunchResult{}, err
	}
	if !l.Cfg.AiuseAvailable() {
		return l.launchDirect(ctx, req)
	}
	if err := validate.Profile(l.Cfg.ProfilesRoot, req.Tool, req.Profile); err != nil {
		return LaunchResult{}, err
	}
	dir, err := validate.Dir(l.Cfg.Home, req.Dir)
	if err != nil {
		return LaunchResult{}, err
	}
	stamp := l.stamp()
	env := []string{"PATH=" + l.Cfg.SessionPath, "LANG=" + l.Cfg.Lang}
	argv := []string{l.Cfg.AiusePath, req.Tool, req.Profile}

	if req.Tool == "codex" {
		return l.launchViaAiuse(ctx, req, dir, env, argv)
	}

	// Claude：名稱與標記和 aiuse claude 自己開的 session 一致，`aiuse sessions list` 也認得。
	// pane 內 TMUX 已設定，aiuse claude 會直接跑 claude，不會再包一層 tmux。
	spec := tmux.NewSessionSpec{Dir: dir, Env: env, Argv: argv}
	spec.Name = fmt.Sprintf("aiuse-claude-%s-%s-%s", req.Profile, stamp, randHex(6))
	spec.Options = map[string]string{
		"@aiuse_managed": "claude-v1", "@aiuse_schema": "1", "@aiuse_name": spec.Name,
		"@aiuse_profile": req.Profile, "@aiuse_tool": "claude", "@dc_created": "1",
	}
	return l.startAndSettle(ctx, spec, dir, fmt.Sprintf("aiuse %s %s", req.Tool, req.Profile))
}

func (l *Launcher) stamp() string {
	now := l.Now
	if now == nil {
		now = time.Now
	}
	return now().UTC().Format("20060102T150405Z")
}

// startAndSettle 建 session、等 Settle 後確認它沒有立即結束。manual 是錯誤訊息裡建議使用者在本機手動跑的指令。
func (l *Launcher) startAndSettle(ctx context.Context, spec tmux.NewSessionSpec, dir, manual string) (LaunchResult, error) {
	id, err := tmux.NewSession(ctx, l.Tmux, spec)
	if err != nil {
		if id != "" {
			tmux.KillSession(ctx, l.Tmux, id)
		}
		return LaunchResult{}, fmt.Errorf("建立 tmux session 失敗：%w", err)
	}
	time.Sleep(l.Settle)
	if !tmux.HasSession(ctx, l.Tmux, spec.Name) {
		return LaunchResult{}, fmt.Errorf("session 啟動後立即結束（請到本機手動執行 %s 查看錯誤）", manual)
	}
	return LaunchResult{Name: spec.Name, TermURL: "/term/?arg=" + spec.Name, Dir: dir}, nil
}

// DirectSessionPrefix 是直接模式建立的 session 名稱前綴（dc-claude-*／dc-codex-*），總覽頁據此判斷工具。
const DirectSessionPrefix = "dc-"

// launchDirect 是「直接模式」（機器上沒有 aiuse）：不使用 profile，直接以 exec 陣列執行 claude／codex。
//   - profile 必須是空字串：直接模式沒有多帳號，帶值多半是前端與伺服器模式不一致，拒絕比默默忽略安全。
//   - 執行檔先解析成絕對路徑再建 session：找不到時在建立任何 tmux session 之前就回錯，不留殘餘 session。
//   - 名稱 dc-<tool>-<時間>-<隨機>，標 @dc_created=1；不寫任何 @aiuse_* 標記，避免冒充 aiuse 管理的 session。
func (l *Launcher) launchDirect(ctx context.Context, req LaunchRequest) (LaunchResult, error) {
	if req.Profile != "" {
		return LaunchResult{}, fmt.Errorf("%w: 這台沒有 aiuse（直接模式），不使用 profile", validate.ErrInvalid)
	}
	dir, err := validate.Dir(l.Cfg.Home, req.Dir)
	if err != nil {
		return LaunchResult{}, err
	}
	bin, err := l.Cfg.ToolBin(req.Tool)
	if err != nil {
		return LaunchResult{}, err
	}
	spec := tmux.NewSessionSpec{
		Name: fmt.Sprintf("%s%s-%s-%s", DirectSessionPrefix, req.Tool, l.stamp(), randHex(6)),
		Dir:  dir,
		Env:  []string{"PATH=" + l.Cfg.SessionPath, "LANG=" + l.Cfg.Lang},
		Argv: []string{bin},
		// 選項在 session 建好後才設；dc_created 讓解除與排查時分得出是本服務開的。
		Options: map[string]string{"@dc_created": "1"},
	}
	return l.startAndSettle(ctx, spec, dir, bin)
}

// aiuse 開的 Codex session 有兩種協定標記（@aiuse_managed），dev-console 兩種都認，不靠 profile 名單判斷：
//   - codex-direct-v1：一般 profile。新版 aiuse 另在 @aiuse_launch_token 寫入本次 helper 的 token；舊版不寫。
//   - codex-v1：standalone profile 的嚴格 provenance 路徑（舊版 aiuse）；aiuse 會把 @aiuse_profile 正規化成小寫。
//
// 哪個 profile 走哪種協定由 aiuse 決定，這裡只看 session 上實際寫的標記。
const (
	codexDirectMarker = "codex-direct-v1"
	codexStrictMarker = "codex-v1"
)

// codexProfileMatches：codex-direct-v1 寫的是目錄原樣名稱（validate.Profile 已確保 req.Profile 就是原樣），要完全相同；
// codex-v1 會被 aiuse 正規化成小寫，比對時不分大小寫。其他標記一律不認。
func codexProfileMatches(s tmux.Session, profile string) bool {
	switch s.Managed {
	case codexDirectMarker:
		return s.Profile == profile
	case codexStrictMarker:
		return strings.EqualFold(s.Profile, profile)
	}
	return false
}

// helperExitWait 是認領後等 helper pane 裡的 aiuse 自然結束的上限。
var helperExitWait = 10 * time.Second

// codexLaunchLock 取得「同一個 profile 的 Codex 新開」互斥鎖（不分大小寫，因 codex-v1 會把名稱轉小寫）；ctx 取消時放棄等待。
// 為什麼要鎖：舊版 aiuse 不寫 @aiuse_launch_token，同 profile 同時開兩個時，兩個 helper 建出的 session 都是「新出現且 ready」，
// 只靠 profile／ready 會互相認領錯。串行化之後，後一次的「建立前 session 集合」一定已含前一次的 session，
// 新出現的就只會是自己 helper 建的那一個；新版 aiuse 有 token 時鎖是多餘但無害的保險。
func (l *Launcher) codexLaunchLock(ctx context.Context, profile string) (func(), error) {
	v, _ := l.codexLocks.LoadOrStore(strings.ToLower(profile), make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// launchViaAiuse 讓 aiuse 自己建 codex tmux session（保留它的 provenance 驗證），再認領新出現的那個。
//   - helper 名稱 dc-launch-<token>、環境 AIUSE_TMUX_NO_ATTACH=<token>：aiuse 只在名稱與 token 對得上時才「建了不 attach」，
//     使用者 shell 裡 export 的同名變數不會誤觸；建出的是 new-session -d 的兄弟 session，不是巢狀 tmux。
//   - 只認領帶 @aiuse_ready=1 的 session：aiuse 寫完所有標記與 proof 後最後才標 ready，未 ready 前標記可能只寫一半。
//   - token：session 帶 @aiuse_launch_token 時必須等於本次 token（不符＝別次新開的，絕不認領）；
//     沒帶 token（舊版 aiuse）時，靠同 profile 串行化＋建立前後 session 集合差異判定，只在 helper 的 aiuse 以 exit 0 結束後才接受，
//     且候選超過一個就拒絕認領（無法判定歸屬）。
//   - 認領後先等 helper pane 結束（remain-on-exit 保留 pane_dead）再刪 helper：helper pane 是 aiuse 的終端機，
//     太早刪會 SIGHUP 還沒退出的 aiuse。ready 之後 aiuse 還有一次 no-attach 驗證（tmux display-message 重讀 token），
//     驗證失敗會刪掉本次剛標 ready 的 session，其餘只剩輸出與結束；等 aiuse 結束才刪 helper，這段驗證與刪除才不會被 SIGHUP 截斷，
//     也不讓 dev-console 依賴「ready 後沒有其他步驟」這個隱含假設。aiuse 標 ready 後幾乎立即結束，這個等待通常很短。
func (l *Launcher) launchViaAiuse(ctx context.Context, req LaunchRequest, dir string, env, argv []string) (LaunchResult, error) {
	unlock, err := l.codexLaunchLock(ctx, req.Profile)
	if err != nil {
		return LaunchResult{}, err
	}
	// 鎖要涵蓋到 helper 刪除為止：helper 還在時下一次新開若開始，aiuse 可能仍在建立中。
	defer unlock()
	before := map[string]bool{}
	existing, _ := tmux.ListSessions(ctx, l.Tmux)
	for _, s := range existing {
		before[s.Name] = true
	}
	start := time.Now().Add(-2 * time.Second)
	token := randHex(6)
	helper := "dc-launch-" + token
	spec := tmux.NewSessionSpec{
		Name: helper, Dir: dir,
		Env:  append(env, "AIUSE_TMUX_NO_ATTACH="+token),
		Argv: argv,
		// 失敗時保留畫面供擷取錯誤訊息；成功時用來判斷 aiuse 已正常結束。
		Options: map[string]string{"remain-on-exit": "on", "@dc_created": "1"},
	}
	id, err := tmux.NewSession(ctx, l.Tmux, spec)
	if err != nil {
		if id != "" {
			tmux.KillSession(ctx, l.Tmux, id)
		}
		return LaunchResult{}, fmt.Errorf("建立 tmux session 失敗：%w", err)
	}
	defer tmux.KillSession(context.Background(), l.Tmux, id)
	// findReady 找本次新出現、標記與 profile 相符、aiuse 已標 ready 的 session。帶別的 token 的一律略過。
	//   - 帶本次 token 的（新版 aiuse）隨時可認領。
	//   - 不帶 token 的（舊版 aiuse）只在 allowLegacy 時考慮，且必須唯一：呼叫端只在 helper 裡的 aiuse 已以 exit 0 結束後才允許。
	//     新版 aiuse 一定在結束前寫好 token 並標 ready，所以它成功時看到的必定是帶 token 的那個；
	//     它失敗（非 0）時不會退而認領別人手動開的無 token session。B 版「只認 token」的保證因此不被相容路徑削弱。
	findReady := func(allowLegacy bool) (tmux.Session, bool, error) {
		sessions, _ := tmux.ListSessions(ctx, l.Tmux)
		var legacy []tmux.Session
		for _, s := range sessions {
			if before[s.Name] || s.Ready != "1" || s.Created.Before(start) || !validate.SessionName(s.Name) ||
				!codexProfileMatches(s, req.Profile) {
				continue
			}
			switch s.LaunchToken {
			case token:
				return s, true, nil
			case "":
				legacy = append(legacy, s)
			}
		}
		if !allowLegacy || len(legacy) == 0 {
			return tmux.Session{}, false, nil
		}
		if len(legacy) == 1 {
			return legacy[0], true, nil
		}
		names := make([]string, len(legacy))
		for i, s := range legacy {
			names[i] = s.Name
		}
		return tmux.Session{}, false, fmt.Errorf("同時出現多個新的 %s Codex session（%s），無法判定哪個是這次開的；請在總覽頁直接點選", req.Profile, strings.Join(names, "、"))
	}
	claim := func(s tmux.Session) (LaunchResult, error) {
		l.waitHelperExit(ctx, id)
		return LaunchResult{Name: s.Name, TermURL: "/term/?arg=" + s.Name, Dir: dir}, nil
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok, _ := findReady(false); ok {
			return claim(s)
		}
		// helper 裡的 aiuse 已結束：立刻判定，不必等滿 15 秒。aiuse 可能剛好在上面列 session 之後才寫 ready 並結束，
		// 所以看到 dead 之後要再列一次——有 ready 就照常認領，仍沒有才是失敗（驗證失敗、找不到 tmux…）。
		// 舊版 aiuse 不寫 token：只有 exit 0（aiuse 自認成功）時才接受唯一的無 token session。
		if dead, status := l.helperDead(ctx, id); dead {
			s, ok, err := findReady(status == "0")
			if err != nil {
				return LaunchResult{}, err
			}
			if ok {
				return claim(s)
			}
			return LaunchResult{}, fmt.Errorf("aiuse 結束（exit %s）但沒有建立可認領的 codex session：%s", status, tmux.CapturePane(ctx, l.Tmux, id))
		}
		select {
		case <-ctx.Done():
			return LaunchResult{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return LaunchResult{}, fmt.Errorf("aiuse 未在 15 秒內建立 codex session：%s", tmux.CapturePane(ctx, l.Tmux, id))
}

// helperDead 回報 helper pane 是否已結束與其 exit status；查不到 pane 視為未結束（交給逾時處理）。
func (l *Launcher) helperDead(ctx context.Context, id string) (bool, string) {
	out, err := l.Tmux.Run(ctx, "display-message", "-p", "-t", id, "#{pane_dead}:#{pane_dead_status}")
	if err != nil {
		return false, ""
	}
	dead, status, _ := strings.Cut(strings.TrimSpace(out), ":")
	return dead == "1", status
}

// waitHelperExit 等 helper pane 的 aiuse 結束（pane_dead=1）；逾時或非 0 結束只記 log，認領結果不變
// （session 已 ready，代表 aiuse 的關鍵步驟都已完成）。
func (l *Launcher) waitHelperExit(ctx context.Context, id string) {
	deadline := time.Now().Add(helperExitWait)
	for time.Now().Before(deadline) {
		if _, err := l.Tmux.Run(ctx, "has-session", "-t", id); err != nil {
			return // helper 已不在（例如被手動關掉），不必再等
		}
		if dead, status := l.helperDead(ctx, id); dead {
			if status != "0" {
				log.Printf("[launch] helper %s 的 aiuse 以 %s 結束（session 已 ready，仍認領）", id, status)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	log.Printf("[launch] helper %s 的 aiuse %s 內未結束，照常刪除", id, helperExitWait)
}
