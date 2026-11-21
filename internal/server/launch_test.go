package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiyum/remote-console/internal/config"
)

// claimTmux 模擬「aiuse 在臨時 pane 內建立另一個 session」：臨時 session 建立後，
// list-sessions 會多出 extra 列出的 session（格式同 tmux.sessionFmt）。
type claimTmux struct {
	mu      sync.Mutex
	calls   [][]string
	created bool
	extra   []string
	// deadAfter 是 helper pane 被問幾次之後才回報 pane_dead=1（模擬 aiuse 收尾中）。
	deadAfter int
	asked     int
	// afterDead 是 helper 回報 dead 之後才出現在 list-sessions 的 session（模擬 aiuse 剛好在兩次查詢之間寫 ready 並結束）。
	afterDead []string
	// deadStatus 是 helper pane 結束時的 exit status（空＝"0"）。
	deadStatus string
	// token 是從 new-session 的 -s dc-launch-<token> 擷取的本次 token；列 session 時把列內的 thisToken 換成它。
	token string
}

// thisToken 是測試列裡「本次 helper 的 token」的佔位字；實際值在 Launch 內隨機產生，由假 tmux 代入。
const thisToken = "{TOKEN}"

func (f *claimTmux) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	switch args[0] {
	case "new-session":
		f.created = true
		for i, a := range args {
			if a == "-s" && i+1 < len(args) {
				f.token = strings.TrimPrefix(args[i+1], "dc-launch-")
			}
		}
		return "$7\n", nil
	case "list-sessions":
		if !f.created {
			return "", nil
		}
		rows := f.extra
		if f.asked > f.deadAfter {
			rows = append(append([]string{}, rows...), f.afterDead...)
		}
		return strings.ReplaceAll(strings.Join(rows, "\n"), thisToken, f.token) + "\n", nil
	case "display-message":
		if args[len(args)-1] == "#{pane_dead}:#{pane_dead_status}" {
			f.asked++
			if f.asked > f.deadAfter {
				st := f.deadStatus
				if st == "" {
					st = "0"
				}
				return "1:" + st + "\n", nil
			}
			return "0:\n", nil
		}
	}
	return "", nil
}

// sessionRow 是本次 helper 建出、已 ready 的 session 列。
func sessionRow(name, managed, profile string, created time.Time) string {
	return tokenRow(name, managed, profile, created, "1", thisToken)
}

func readyRow(name, managed, profile string, created time.Time, ready string) string {
	return tokenRow(name, managed, profile, created, ready, thisToken)
}

func tokenRow(name, managed, profile string, created time.Time, ready, token string) string {
	return fmt.Sprintf("$1\t%s\t%d\t%d\t0\t%s\t%s\tcodex\t1\t%s\t%s", name, created.Unix(), created.Unix(), managed, profile, ready, token)
}

func newLaunchTestLauncher(t *testing.T, ft *claimTmux, profiles ...string) *Launcher {
	t.Helper()
	home := t.TempDir()
	for _, p := range profiles {
		if err := os.MkdirAll(filepath.Join(home, ".ai-profiles/codex", p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Home: home, ProfilesRoot: filepath.Join(home, ".ai-profiles"), AiusePath: fakeExecutable(t, t.TempDir(), "aiuse"), SessionPath: "/usr/bin", Lang: "C"}
	return &Launcher{Cfg: cfg, Tmux: ft}
}

// 任一 Codex profile（含日後新增的）都交給 aiuse 建 session，dev-console 不再自建 dc-codex-*。
func TestLaunchCodexAnyProfileGoesThroughAiuse(t *testing.T) {
	now := time.Now()
	cases := []struct{ profile, managed string }{
		{"team", "codex-direct-v1"},
		{"zz-new", "codex-direct-v1"},
		{"Work", "codex-direct-v1"},
	}
	for _, c := range cases {
		t.Run(c.profile, func(t *testing.T) {
			want := "aiuse-codex-" + c.profile + "-20261024T000000Z-abcdef"
			ft := &claimTmux{extra: []string{
				// 不能被認領：沒有 aiuse 標記、profile 不同、標記不明。
				sessionRow("aiuse-codex-"+c.profile+"-foreign", "", c.profile, now),
				sessionRow("aiuse-codex-other-x", c.managed, "other", now),
				sessionRow("aiuse-codex-"+c.profile+"-spoof", "codex-v2", c.profile, now),
				// codex-v1 也是合法協定，但帶的是別次新開的 token：不認領。
				tokenRow("aiuse-codex-"+c.profile+"-v1", "codex-v1", c.profile, now, "1", "bbbbbbbbbbbb"),
				// aiuse 還沒寫完（沒有 @aiuse_ready）：不認領。
				readyRow("aiuse-codex-"+c.profile+"-notready", c.managed, c.profile, now, ""),
				// 同時進行的另一次新開（別的 helper token）或沒帶 token 的 session：不認領。
				tokenRow("aiuse-codex-"+c.profile+"-othertoken", c.managed, c.profile, now, "1", "aaaaaaaaaaaa"),
				tokenRow("aiuse-codex-"+c.profile+"-notoken", c.managed, c.profile, now, "1", ""),
				sessionRow(want, c.managed, c.profile, now),
			}, deadAfter: 3}
			l := newLaunchTestLauncher(t, ft, c.profile)
			res, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: c.profile})
			if err != nil {
				t.Fatal(err)
			}
			if res.Name != want || res.TermURL != "/term/?arg="+want {
				t.Fatalf("認領錯 session：%+v", res)
			}
			var ns []string
			for _, call := range ft.calls {
				if call[0] == "new-session" {
					if ns != nil {
						t.Fatal("只應建立一個臨時 session")
					}
					ns = call
				}
			}
			joined := strings.Join(ns, " ")
			// token 必須同時出現在 helper 名稱與 AIUSE_TMUX_NO_ATTACH（aiuse 以兩者相符判斷是 dev-console 代建）。
			token := ""
			for i, a := range ns {
				if a == "-s" && i+1 < len(ns) {
					token = strings.TrimPrefix(ns[i+1], "dc-launch-")
				}
			}
			if len(token) != 12 || !strings.Contains(joined, "-e AIUSE_TMUX_NO_ATTACH="+token) ||
				!strings.HasSuffix(joined, "-- "+l.Cfg.AiusePath+" codex "+c.profile) {
				t.Fatalf("臨時 session 參數不符：%v", ns)
			}
			if strings.Contains(joined, "dc-codex-") {
				t.Fatalf("不應再自建 dc-codex-* session：%v", ns)
			}
			// helper 必須在 aiuse 結束（pane_dead=1）之後才刪。
			killedAt := -1
			for i, call := range ft.calls {
				if call[0] == "kill-session" && call[len(call)-1] == "$7" {
					killedAt = i
				}
			}
			if killedAt < 0 {
				t.Fatal("臨時 session 應在認領後刪除")
			}
			if ft.asked <= ft.deadAfter {
				t.Fatalf("應等到 helper pane 結束才刪，只問了 %d 次", ft.asked)
			}
			last := -1
			for i, call := range ft.calls {
				if call[0] == "display-message" {
					last = i
				}
			}
			if last > killedAt {
				t.Fatal("刪除 helper 之後不應再查 pane 狀態")
			}
		})
	}
}

// helper 裡的 aiuse 已結束、且再列一次仍沒有 ready session：不必等滿 15 秒，立刻帶畫面上的錯誤回報。
func TestLaunchCodexReportsEarlyHelperFailure(t *testing.T) {
	// 有 session 但沒 ready（aiuse 中途失敗）也算失敗。
	ft := &claimTmux{deadAfter: 0, afterDead: []string{readyRow("aiuse-codex-team-x", "codex-direct-v1", "team", time.Now(), "")}}
	l := newLaunchTestLauncher(t, ft, "team")
	start := time.Now()
	_, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "team"})
	if err == nil || !strings.Contains(err.Error(), "exit 0") {
		t.Fatalf("應回報 helper 已結束：%v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("應立刻回報，實際等了 %s", time.Since(start))
	}
}

// aiuse 在第一次列 session 之後才寫 ready 並結束：看到 pane 已 dead 時要再列一次，照常認領，不能誤判成失敗。
func TestLaunchCodexClaimsReadySessionAfterHelperDied(t *testing.T) {
	want := "aiuse-codex-team-20261024T000000Z-abcdef"
	ft := &claimTmux{deadAfter: 0, afterDead: []string{sessionRow(want, "codex-direct-v1", "team", time.Now())}}
	l := newLaunchTestLauncher(t, ft, "team")
	res, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "team"})
	if err != nil || res.Name != want {
		t.Fatalf("pane 已 dead 但 session 已 ready，應照常認領：%+v %v", res, err)
	}
}

// 網頁同時開兩個同 profile 的 Codex session：兩個 helper 建出的 session 都新出現且 ready，
// 各自只能認領帶自己 token 的那個（舊版只看 profile／ready，會互相認領錯）。
func TestLaunchCodexConcurrentClaimsOwnToken(t *testing.T) {
	now := time.Now()
	mine := "aiuse-codex-team-20261024T000001Z-bbbbbb"
	// 別人的 session 排在前面，舊邏輯會先撿到它。
	ft := &claimTmux{extra: []string{
		tokenRow("aiuse-codex-team-20261024T000000Z-aaaaaa", "codex-direct-v1", "team", now, "1", "111111111111"),
		sessionRow(mine, "codex-direct-v1", "team", now),
	}, deadAfter: 0}
	l := newLaunchTestLauncher(t, ft, "team")
	res, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "team"})
	if err != nil || res.Name != mine {
		t.Fatalf("應認領帶本次 token 的 session：%+v %v", res, err)
	}
}

// ---- A9：兩種協定（codex-v1／codex-direct-v1）、有無 token 的相容與並發保護 ----

// 舊版 aiuse 的 standalone 路徑：標 codex-v1、不寫 token、把 profile 正規化成小寫。profile 名稱不寫死，只靠標記判斷。
func TestLaunchCodexStrictProtocolWithoutToken(t *testing.T) {
	want := "aiuse-codex-solo-20261024T000000Z-abcdef"
	ft := &claimTmux{deadAfter: 1, afterDead: []string{tokenRow(want, "codex-v1", "solo-x", time.Now(), "1", "")}}
	l := newLaunchTestLauncher(t, ft, "Solo-X")
	res, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "Solo-X"})
	if err != nil || res.Name != want {
		t.Fatalf("codex-v1 無 token 應認領成功：%+v %v", res, err)
	}
}

// 舊版 aiuse 的一般 profile：標 codex-direct-v1、不寫 token；profile 要完全相同。
func TestLaunchCodexDirectProtocolWithoutToken(t *testing.T) {
	want := "aiuse-codex-team-20261024T000000Z-abcdef"
	ft := &claimTmux{deadAfter: 1, afterDead: []string{
		// codex-direct-v1 的 profile 大小寫不同：不是同一個帳號，不認領。
		tokenRow("aiuse-codex-team-case", "codex-direct-v1", "TEAM", time.Now(), "1", ""),
		tokenRow(want, "codex-direct-v1", "team", time.Now(), "1", ""),
	}}
	l := newLaunchTestLauncher(t, ft, "team")
	res, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "team"})
	if err != nil || res.Name != want {
		t.Fatalf("codex-direct-v1 無 token 應認領成功：%+v %v", res, err)
	}
}

// 新版 aiuse：codex-direct-v1＋@aiuse_launch_token 等於本次 token；不必等 helper 結束就能認領。
func TestLaunchCodexDirectProtocolWithMatchingToken(t *testing.T) {
	want := "aiuse-codex-team-20261024T000000Z-abcdef"
	ft := &claimTmux{deadAfter: 5, extra: []string{sessionRow(want, "codex-direct-v1", "team", time.Now())}}
	l := newLaunchTestLauncher(t, ft, "team")
	res, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "team"})
	if err != nil || res.Name != want {
		t.Fatalf("token 相符應認領成功：%+v %v", res, err)
	}
}

// token 不符＝別次新開建的 session：即使 aiuse 以 0 結束、這是唯一的新 session，也絕不認領。
func TestLaunchCodexRejectsMismatchedToken(t *testing.T) {
	for _, managed := range []string{"codex-direct-v1", "codex-v1"} {
		t.Run(managed, func(t *testing.T) {
			ft := &claimTmux{deadAfter: 1, afterDead: []string{tokenRow("aiuse-codex-team-other", managed, "team", time.Now(), "1", "999999999999")}}
			l := newLaunchTestLauncher(t, ft, "team")
			res, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "team"})
			if err == nil {
				t.Fatalf("token 不符應拒絕，卻認領了 %+v", res)
			}
		})
	}
}

// helper 的 aiuse 以非 0 結束（例如新版 aiuse 驗證失敗）：不退而認領別人剛開的無 token session。
func TestLaunchCodexLegacyNotClaimedWhenHelperFailed(t *testing.T) {
	ft := &claimTmux{deadAfter: 1, deadStatus: "1", afterDead: []string{tokenRow("aiuse-codex-team-manual", "codex-direct-v1", "team", time.Now(), "1", "")}}
	l := newLaunchTestLauncher(t, ft, "team")
	if res, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "team"}); err == nil {
		t.Fatalf("aiuse 失敗時不應認領無 token session：%+v", res)
	}
}

// 同時出現兩個新的無 token session（例如本機另一個終端機剛好也開了同 profile）：無法判定歸屬，拒絕而不是猜一個。
func TestLaunchCodexLegacyAmbiguousRejected(t *testing.T) {
	now := time.Now()
	ft := &claimTmux{deadAfter: 1, afterDead: []string{
		tokenRow("aiuse-codex-team-a", "codex-direct-v1", "team", now, "1", ""),
		tokenRow("aiuse-codex-team-b", "codex-direct-v1", "team", now, "1", ""),
	}}
	l := newLaunchTestLauncher(t, ft, "team")
	_, err := l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "team"})
	if err == nil || !strings.Contains(err.Error(), "無法判定") {
		t.Fatalf("多個候選應拒絕：%v", err)
	}
}

// simTmux 模擬真實的 aiuse（不寫 token 的舊版）：每個 dc-launch-<token> helper 在第一次被問 pane 狀態時
// 建出自己的 codex session（名稱帶 helper token 方便核對，但 @aiuse_launch_token 是空的），第二次起回報 exit 0。
// new-session 刻意慢一點，讓兩個並發的 Launch 都在對方 session 出現前拍下「建立前」集合——沒有串行化就會互相干擾。
type simTmux struct {
	mu      sync.Mutex
	nextID  int
	helpers map[string]*simHelper // helper session id → 狀態
	rows    []string
	created []string
}

type simHelper struct {
	token string
	polls int
}

func (f *simTmux) Run(_ context.Context, args ...string) (string, error) {
	if args[0] == "new-session" {
		time.Sleep(30 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch args[0] {
	case "new-session":
		f.nextID++
		id := fmt.Sprintf("$%d", f.nextID)
		for i, a := range args {
			if a == "-s" && i+1 < len(args) {
				f.helpers[id] = &simHelper{token: strings.TrimPrefix(args[i+1], "dc-launch-")}
			}
		}
		return id + "\n", nil
	case "list-sessions":
		return strings.Join(f.rows, "\n") + "\n", nil
	case "has-session":
		if _, ok := f.helpers[args[len(args)-1]]; ok {
			return "", nil
		}
		return "", fmt.Errorf("no such session")
	case "kill-session":
		delete(f.helpers, args[len(args)-1])
	case "display-message":
		h := f.helpers[args[3]]
		if h == nil {
			return "", fmt.Errorf("no such session")
		}
		h.polls++
		if h.polls == 1 {
			name := "aiuse-codex-team-" + h.token
			f.created = append(f.created, name)
			f.rows = append(f.rows, tokenRow(name, "codex-direct-v1", "team", time.Now(), "1", ""))
			return "0:\n", nil
		}
		return "1:0\n", nil
	}
	return "", nil
}

// 網頁同時開兩個同 profile 的 Codex（舊版 aiuse，沒有 token 可比對）：同 profile 串行化後，
// 每次都只認領自己 helper 建的那個 session，兩次結果不同、且正好是實際建出的兩個。
func TestLaunchCodexConcurrentSameProfileWithoutToken(t *testing.T) {
	ft := &simTmux{helpers: map[string]*simHelper{}}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ai-profiles/codex/team"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Home: home, ProfilesRoot: filepath.Join(home, ".ai-profiles"), AiusePath: fakeExecutable(t, t.TempDir(), "aiuse"), SessionPath: "/usr/bin", Lang: "C"}
	l := &Launcher{Cfg: cfg, Tmux: ft}
	var wg sync.WaitGroup
	results := make([]LaunchResult, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = l.Launch(context.Background(), LaunchRequest{Tool: "codex", Profile: "team"})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 次新開失敗：%v", i, err)
		}
	}
	if results[0].Name == results[1].Name {
		t.Fatalf("兩次新開認領到同一個 session：%s", results[0].Name)
	}
	got := []string{results[0].Name, results[1].Name}
	sort.Strings(got)
	want := append([]string{}, ft.created...)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("認領結果 %v 與實際建出的 session %v 不符", got, want)
	}
}
