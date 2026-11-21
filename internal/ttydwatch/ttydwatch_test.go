package ttydwatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProbe 模擬 ttyd：重啟後換 pid、fd 歸零。
type fakeProbe struct {
	mu      sync.Mutex
	pid     int
	fds     int
	sockErr error
	pidErr  error
	fdErr   error
}

func (f *fakeProbe) PID(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pid, f.pidErr
}
func (f *fakeProbe) FDCount(context.Context, int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fdErr != nil {
		return 0, f.fdErr
	}
	return f.fds, nil
}
func (f *fakeProbe) Socket(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sockErr
}

type fakeRestarter struct {
	probe *fakeProbe
	calls int
	err   error
}

func (r *fakeRestarter) Restart(context.Context) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	r.probe.mu.Lock()
	r.probe.pid++
	r.probe.fds = 12
	r.probe.mu.Unlock()
	return nil
}

func newWatcher(p *fakeProbe, r *fakeRestarter, logs *[]string) *Watcher {
	return &Watcher{
		Cfg:         Config{Interval: time.Minute, Threshold: 100, Limit: 8192},
		Gate:        &Gate{},
		Probe:       p,
		Restarter:   r,
		RestartWait: 2 * time.Second,
		Logf:        func(f string, a ...any) { *logs = append(*logs, fmt.Sprintf(f, a...)) },
	}
}

func TestTickRestartsOnlyWhenIdle(t *testing.T) {
	p := &fakeProbe{pid: 500, fds: 150}
	r := &fakeRestarter{probe: p}
	var logs []string
	w := newWatcher(p, r, &logs)

	// 有連線：只記延後，不重啟。
	if !w.Gate.Acquire() {
		t.Fatal("Acquire 應成功")
	}
	if w.Tick(context.Background()) {
		t.Fatal("有連線時不應重啟")
	}
	if r.calls != 0 || !strings.Contains(strings.Join(logs, "\n"), "延後重啟（有連線 1 條）") {
		t.Fatalf("calls=%d logs=%v", r.calls, logs)
	}

	// 連線結束：重啟，log 記原因與 fd 數，新 pid 生效。
	w.Gate.Release()
	if !w.Tick(context.Background()) {
		t.Fatal("閒置時應重啟")
	}
	all := strings.Join(logs, "\n")
	if r.calls != 1 || !strings.Contains(all, "觸發自動重啟：fd 150 ≥ 門檻 100（pid 500") || !strings.Contains(all, "已重啟（新 pid 501）") {
		t.Fatalf("calls=%d logs=%v", r.calls, logs)
	}
	// 重啟結束後恢復接受連線。
	if !w.Gate.Acquire() {
		t.Fatal("重啟後應恢復接受連線")
	}
}

func TestTickBelowThresholdNoRestart(t *testing.T) {
	p := &fakeProbe{pid: 500, fds: 99}
	r := &fakeRestarter{probe: p}
	var logs []string
	w := newWatcher(p, r, &logs)
	if w.Tick(context.Background()) || r.calls != 0 {
		t.Fatal("未達門檻不應重啟")
	}
	if st := w.Last(); !st.OK || st.FDs != 99 || st.PID != 500 {
		t.Fatalf("status=%+v", st)
	}
}

func TestTickRestartFailureReleasesGate(t *testing.T) {
	p := &fakeProbe{pid: 500, fds: 150}
	r := &fakeRestarter{probe: p, err: errors.New("boom")}
	var logs []string
	w := newWatcher(p, r, &logs)
	if w.Tick(context.Background()) {
		t.Fatal("重啟失敗應回 false")
	}
	if _, restarting := w.Gate.State(); restarting {
		t.Fatal("失敗後不可卡在重啟中")
	}
}

func TestGateBlocksDuringRestart(t *testing.T) {
	g := &Gate{}
	ok, _ := g.TryBeginRestart()
	if !ok || g.Acquire() {
		t.Fatal("重啟中應拒絕新連線")
	}
	if again, _ := g.TryBeginRestart(); again {
		t.Fatal("不可重入")
	}
	g.EndRestart()
	if !g.Acquire() {
		t.Fatal("結束重啟後應接受")
	}
	g.Release()
	g.Release() // 多放一次不得變負數
	if a, _ := g.State(); a != 0 {
		t.Fatalf("active=%d", a)
	}
}

func TestCheckReasons(t *testing.T) {
	cases := []struct {
		name string
		p    *fakeProbe
		want string
	}{
		{"沒在跑", &fakeProbe{pid: 0}, "沒有在執行"},
		{"launchctl 失敗", &fakeProbe{pidErr: errors.New("x")}, "沒有在執行"},
		{"socket 沒回應", &fakeProbe{pid: 1, sockErr: errors.New("x")}, "socket 沒有回應"},
		{"逼近上限", &fakeProbe{pid: 1, fds: 8000}, "逼近上限"},
		{"讀不到 fd", &fakeProbe{pid: 1, fdErr: errors.New("lsof 壞了")}, "無法讀取 fd 數"},
		{"正常", &fakeProbe{pid: 1, fds: 30}, ""},
	}
	for _, c := range cases {
		var logs []string
		w := newWatcher(c.p, &fakeRestarter{probe: c.p}, &logs)
		st := w.Check(context.Background())
		if c.want == "" {
			if !st.OK || !strings.Contains(st.Line(), "OK（pid 1，fd 30／上限 8192") {
				t.Errorf("%s：%+v %s", c.name, st, st.Line())
			}
			continue
		}
		if st.OK || !strings.Contains(st.Reason, c.want) || !strings.Contains(st.Line(), "異常") {
			t.Errorf("%s：%+v", c.name, st)
		}
	}
}

// 超過重啟門檻但終端機仍可用：OK 維持 true（不讓開著的分頁卡在異常提示），只帶 Warning。
func TestOverThresholdIsWarningNotFailure(t *testing.T) {
	p := &fakeProbe{pid: 1, fds: 100}
	var logs []string
	w := newWatcher(p, &fakeRestarter{probe: p}, &logs)
	st := w.Check(context.Background())
	if !st.OK || st.Reason != "" || !strings.Contains(st.Warning, "超過自動重啟門檻") || !strings.Contains(st.Line(), "OK") || !strings.Contains(st.Line(), "注意") {
		t.Fatalf("%+v %s", st, st.Line())
	}
	// 負向對照：門檻以下沒有 Warning。
	p.fds = 99
	if st := w.Check(context.Background()); st.Warning != "" {
		t.Fatalf("門檻以下不應有 warning：%+v", st)
	}
}

// 讀不到 fd 數：不可假綠、不可自動重啟，連續失敗的 log 要節流。
func TestFDReadFailureIsNotOKAndLogThrottled(t *testing.T) {
	p := &fakeProbe{pid: 7, fds: 5000, fdErr: errors.New("lsof 壞了")}
	r := &fakeRestarter{probe: p}
	var logs []string
	w := newWatcher(p, r, &logs)
	for i := 0; i < 5; i++ {
		if st := w.Check(context.Background()); st.OK || st.Reason != "無法讀取 fd 數" || st.FDs != 0 {
			t.Fatalf("讀不到 fd 應為異常：%+v", st)
		}
	}
	if w.Tick(context.Background()) || r.calls != 0 {
		t.Fatal("讀不到 fd 時不應重啟")
	}
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "讀不到 fd 數") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("6 次失敗應只記 1 行，得 %d：%v", n, logs)
	}
	// 恢復後回 OK。
	p.fdErr = nil
	p.fds = 20
	if st := w.Check(context.Background()); !st.OK || st.FDs != 20 {
		t.Fatalf("恢復後應 OK：%+v", st)
	}
}

func TestParsePIDAndCountFDs(t *testing.T) {
	out := "gui/501/x = {\n\tactive count = 1\n\tpid = 52232\n\tresource coalition = {\n\t\tpid = 9\n\t}\n}\n"
	if got := ParsePID(out); got != 52232 {
		t.Fatalf("pid=%d", got)
	}
	if got := ParsePID("gui/501/x = {\n\tstate = not running\n}\n"); got != 0 {
		t.Fatalf("沒在跑應為 0，得 %d", got)
	}
	lsof := "p52232\nfcwd\nftxt\nf0\nf1\nf2\nf15\n"
	if got := CountFDs(lsof); got != 4 {
		t.Fatalf("fds=%d", got)
	}
}
