// Package ttydwatch 盯 ttyd 的健康狀態，fd 用量過高時在「沒有任何活著的終端機連線」才自動重啟（ADR 0006）。
//
// 為什麼要有：ttyd 1.7.7 在 macOS 上若子程序卡住，每條斷線漏 3 個 fd，撞到上限後 pty_spawn 一直
// EMFILE、所有人都開不了終端機。ttyd-attach.sh 已修掉已知的死結，這裡是兜底：未知的洩漏也不會讓服務
// 永久壞掉。連線數以 dev-console 反代自己追蹤的 WebSocket 數為準——ttyd 只開 0700 目錄內的 unix
// socket，唯一的連入者就是本服務的反代，所以這個計數就是 ttyd 的實際連線數；看 ttyd 子程序則會被
// 已卡死的殘留子程序誤判成「有人在用」，永遠不重啟。
package ttydwatch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chiyum/remote-console/internal/logthrottle"
)

// Gate 追蹤經反代進行中的 ttyd WebSocket 數，並在重啟期間擋下新連線。
// 「檢查沒人連線」與「開始重啟」在同一把鎖內完成，中間不會有新連線插進來。
type Gate struct {
	mu         sync.Mutex
	active     int
	restarting bool
}

// Acquire 在開始反代一條 WebSocket 前呼叫；重啟中回 false（呼叫端應回 503）。成功者必須呼叫 Release。
func (g *Gate) Acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.restarting {
		return false
	}
	g.active++
	return true
}

// Release 在 WebSocket 結束時呼叫。
func (g *Gate) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active > 0 {
		g.active--
	}
}

// TryBeginRestart 沒有活著的連線時進入重啟狀態並回 true；有連線時回 false 與目前連線數。
func (g *Gate) TryBeginRestart() (bool, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active > 0 || g.restarting {
		return false, g.active
	}
	g.restarting = true
	return true, 0
}

// EndRestart 結束重啟狀態，恢復接受新連線。
func (g *Gate) EndRestart() {
	g.mu.Lock()
	g.restarting = false
	g.mu.Unlock()
}

// State 回傳目前連線數與是否重啟中。
func (g *Gate) State() (active int, restarting bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active, g.restarting
}

// Status 是一次檢查的結果；同時給總覽頁、終端機頁健康檢查與 CLI 使用。
// OK=false 只留給「現在開終端機會失敗」的情況（沒在執行、socket 不回應、讀不到 fd、逼近上限、重啟中）；
// fd 超過自動重啟門檻時終端機仍可用，只放 Warning，否則開著的分頁會卡在異常提示、check.sh 也會假紅。
type Status struct {
	OK          bool      `json:"ok"`
	Reason      string    `json:"reason,omitempty"`
	Warning     string    `json:"warning,omitempty"`
	PID         int       `json:"pid"`
	FDs         int       `json:"fds"`
	Limit       int       `json:"limit"`
	Threshold   int       `json:"threshold"`
	ActiveConns int       `json:"activeConns"`
	Restarting  bool      `json:"restarting"`
	CheckedAt   time.Time `json:"checkedAt"`
}

// Line 是給人看的一行摘要（check.sh、log 共用）。
func (s Status) Line() string {
	if s.OK {
		line := fmt.Sprintf("ttyd：OK（pid %d，fd %d／上限 %d，自動重啟門檻 %d）", s.PID, s.FDs, s.Limit, s.Threshold)
		if s.Warning != "" {
			line += "；注意：" + s.Warning
		}
		return line
	}
	return fmt.Sprintf("ttyd：異常（%s；pid %d，fd %d／上限 %d，自動重啟門檻 %d）", s.Reason, s.PID, s.FDs, s.Limit, s.Threshold)
}

// Prober 取得 ttyd 的 pid 與 fd 數，並確認 socket 有回應（介面讓測試可替換）。
type Prober interface {
	PID(ctx context.Context) (int, error)
	FDCount(ctx context.Context, pid int) (int, error)
	Socket(ctx context.Context) error
}

// Restarter 重啟 ttyd。
type Restarter interface {
	Restart(ctx context.Context) error
}

// Config 是門檻與週期設定（來自環境變數，見 config 套件與 README）。
type Config struct {
	Interval  time.Duration // 偵測週期；<=0 表示不自動重啟（仍可查狀態）
	Threshold int           // fd 數達此值就想重啟
	Limit     int           // launchd 給 ttyd 的 NumberOfFiles（僅顯示與判斷「快滿」用）
}

// Watcher 週期檢查 ttyd，必要時重啟。
type Watcher struct {
	Cfg       Config
	Gate      *Gate
	Probe     Prober
	Restarter Restarter
	// Now 與 Logf 可在測試替換。
	Now  func() time.Time
	Logf func(format string, args ...any)

	// RestartWait 是重啟後等新 socket 的上限（0＝15 秒）。
	RestartWait time.Duration
	// errLog 節流「讀不到 fd 數」：健康 API 每次呼叫都會檢查，失敗時不能每次寫一行。
	errLogOnce sync.Once
	errLog     *logthrottle.Throttle

	mu   sync.Mutex
	last Status
}

// throttled 以 30 秒窗口節流寫 log（輸出仍走 logf，測試可攔）。
func (w *Watcher) throttled(key, format string, args ...any) {
	w.errLogOnce.Do(func() {
		w.errLog = logthrottle.New(30 * time.Second)
		w.errLog.Logf = w.logf
	})
	w.errLog.Printf(key, format, args...)
}

func (w *Watcher) restartWait() time.Duration {
	if w.RestartWait > 0 {
		return w.RestartWait
	}
	return 15 * time.Second
}

func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Watcher) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// nearLimit 判斷 fd 是否已逼近上限：剩不到 5% 或 16 個就視為不健康（新連線大概會 EMFILE）。
func nearLimit(fds, limit int) bool {
	if limit <= 0 {
		return false
	}
	margin := limit / 20
	if margin < 16 {
		margin = 16
	}
	return fds >= limit-margin
}

// Check 做一次檢查並記下結果（不重啟）。
func (w *Watcher) Check(ctx context.Context) Status {
	st := Status{Limit: w.Cfg.Limit, Threshold: w.Cfg.Threshold, CheckedAt: w.now()}
	if w.Gate != nil {
		st.ActiveConns, st.Restarting = w.Gate.State()
	}
	st.OK, st.Reason = w.probe(ctx, &st)
	w.mu.Lock()
	w.last = st
	w.mu.Unlock()
	return st
}

func (w *Watcher) probe(ctx context.Context, st *Status) (bool, string) {
	if st.Restarting {
		return false, "重啟中"
	}
	pid, err := w.Probe.PID(ctx)
	if err != nil || pid <= 0 {
		return false, "ttyd 沒有在執行"
	}
	st.PID = pid
	if err := w.Probe.Socket(ctx); err != nil {
		return false, "ttyd socket 沒有回應"
	}
	// 讀不到 fd 數就無法判斷是否在漏、自動恢復也等於失效，不能當成 0 回 OK（假綠）。
	n, err := w.Probe.FDCount(ctx, pid)
	if err != nil {
		w.throttled("fdcount", "[ttyd] 讀不到 fd 數（pid %d）：%v", pid, err)
		return false, "無法讀取 fd 數"
	}
	st.FDs = n
	if nearLimit(st.FDs, st.Limit) {
		return false, "fd 用量逼近上限"
	}
	if w.Cfg.Threshold > 0 && st.FDs >= w.Cfg.Threshold {
		st.Warning = "fd 用量超過自動重啟門檻，等沒有連線時自動重啟"
	}
	return true, ""
}

// Last 回傳最近一次檢查結果（還沒檢查過時為零值）。
func (w *Watcher) Last() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// Tick 是一次週期工作：檢查，fd 達門檻時沒有連線才重啟。回傳是否有重啟。
func (w *Watcher) Tick(ctx context.Context) bool {
	st := w.Check(ctx)
	if st.PID <= 0 || w.Cfg.Threshold <= 0 || st.FDs < w.Cfg.Threshold {
		return false
	}
	ok, active := w.Gate.TryBeginRestart()
	if !ok {
		w.logf("[ttyd] fd %d ≥ 門檻 %d，延後重啟（有連線 %d 條）", st.FDs, w.Cfg.Threshold, active)
		return false
	}
	defer w.Gate.EndRestart()
	w.logf("[ttyd] 觸發自動重啟：fd %d ≥ 門檻 %d（pid %d，上限 %d），目前沒有任何終端機連線", st.FDs, w.Cfg.Threshold, st.PID, st.Limit)
	if err := w.Restarter.Restart(ctx); err != nil {
		w.logf("[ttyd] 重啟失敗：%v", err)
		return false
	}
	// 等新的 ttyd 開好 socket 再放行新連線，避免第一條連線撞到「socket 不存在」。
	// 這裡用真實時間（不是可替換的 Now），測試固定時鐘時才不會無限等。
	deadline := time.Now().Add(w.restartWait())
	for time.Now().Before(deadline) {
		if pid, err := w.Probe.PID(ctx); err == nil && pid > 0 && pid != st.PID && w.Probe.Socket(ctx) == nil {
			w.logf("[ttyd] 已重啟（新 pid %d）", pid)
			return true
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(300 * time.Millisecond):
		}
	}
	w.logf("[ttyd] 已送出重啟，但 %v 內沒等到新的 socket", w.restartWait())
	return true
}

// Run 啟動時先檢查一次，之後每 Interval 執行 Tick；Interval<=0 時只在狀態查詢時檢查。
func (w *Watcher) Run(ctx context.Context) {
	st := w.Check(ctx)
	w.logf("[ttyd] 監看啟動：%s；週期 %v", st.Line(), w.Cfg.Interval)
	if w.Cfg.Interval <= 0 {
		return
	}
	t := time.NewTicker(w.Cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx)
		}
	}
}

// ---------- 正式實作（launchctl／lsof／unix socket） ----------

// Launchd 以 launchctl 查 pid 與重啟、以 lsof 數 fd、以 HTTP 打 ttyd socket 確認有回應。
type Launchd struct {
	Label string // 例如 <label 前綴>.ttyd
	UID   int
	Sock  string // ttyd 的 unix socket 路徑
}

func (l Launchd) target() string { return fmt.Sprintf("gui/%d/%s", l.UID, l.Label) }

var pidLine = regexp.MustCompile(`(?m)^\tpid = (\d+)$`)

// ParsePID 從 `launchctl print` 的輸出取頂層的 pid 行（沒在執行時沒有這行）。
func ParsePID(out string) int {
	m := pidLine.FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// PID 實作 Prober。
func (l Launchd) PID(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/launchctl", "print", l.target()).Output()
	if err != nil {
		return 0, fmt.Errorf("launchctl print：%w", err)
	}
	return ParsePID(string(out)), nil
}

// CountFDs 數 `lsof -F f` 輸出中的數字 fd（cwd、txt 等非數字項不算）。
func CountFDs(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 1 && line[0] == 'f' && line[1] >= '0' && line[1] <= '9' {
			n++
		}
	}
	return n
}

// FDCount 實作 Prober。
func (l Launchd) FDCount(ctx context.Context, pid int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-p", strconv.Itoa(pid), "-F", "f").Output()
	if err != nil && len(out) == 0 {
		return 0, fmt.Errorf("lsof：%w", err)
	}
	return CountFDs(string(out)), nil
}

// Socket 實作 Prober：對 ttyd 打 GET /term/token（不會開 pty），確認 event loop 有在回應。
func (l Launchd) Socket(ctx context.Context) error {
	return ProbeSocket(ctx, l.Sock)
}

// ProbeSocket 對 ttyd 的 unix socket 打一次 GET /term/token，2 秒內要回 200。
func ProbeSocket(ctx context.Context, sock string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ttyd/term/token", nil)
	if err != nil {
		return err
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("ttyd 回 " + resp.Status)
	}
	return nil
}

// Restart 實作 Restarter：kickstart -k 會先結束現有 ttyd 再由 launchd 啟動新的（ttyd-run.sh 會清掉舊 socket）。
func (l Launchd) Restart(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/launchctl", "kickstart", "-k", l.target()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart：%w：%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
