package procscan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const lstart = "Fri Oct 23 15:02:44 2026"

type fakePS struct {
	table string
	env   map[int]string
	calls []string
}

func (f *fakePS) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if len(args) > 0 && args[0] == "-E" {
		// 真實 ps -E：argv 原樣在前，環境接在後面。
		pid, _ := strconv.Atoi(args[len(args)-1])
		for _, p := range ParsePS([]byte(f.table), time.Local) {
			if p.PID == pid {
				return []byte(p.Args + " " + f.env[pid] + "\n"), nil
			}
		}
		return nil, fmt.Errorf("no such pid")
	}
	return []byte(f.table), nil
}

func row(pid, ppid, uid int, tty, cmd string) string {
	return fmt.Sprintf("%d %d %d %s %s %s\n", pid, ppid, uid, tty, lstart, cmd)
}

func setup(t *testing.T) (string, time.Time) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".ai-profiles")
	for _, p := range []string{"claude/work/sessions", "claude/second/sessions", "codex/team", "codex/--help"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	start, _ := time.ParseInLocation(lstartLayout, lstart, time.Local)
	return root, start
}

func writeSession(t *testing.T, root, profile string, pid int, sid, kind string, started time.Time) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": sid, "cwd": "/home/x", "startedAt": started.UnixMilli(), "kind": kind})
	if err := os.WriteFile(filepath.Join(root, "claude", profile, "sessions", strconv.Itoa(pid)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

const sidA = "aaaaaaaa-0000-4000-8000-000000000001"
const sidB = "bbbbbbbb-0000-4000-8000-000000000002"

func TestScanListsOnlyProvableNonTmuxSessions(t *testing.T) {
	root, start := setup(t)
	writeSession(t, root, "work", 100, sidA, "interactive", start.Add(500*time.Millisecond))
	// pid 重用：sessions 檔的啟動時間比 ps 早很多 → 不採信。
	writeSession(t, root, "second", 110, sidB, "interactive", start.Add(-time.Hour))
	// 非互動 kind。
	writeSession(t, root, "second", 120, sidB, "print", start)
	f := &fakePS{
		table: row(99, 1, 501, "ttys002", "bash /x/aiuse claude work") +
			row(100, 99, 501, "ttys002", "/home/x/.local/bin/claude --dangerously-skip-permissions") +
			row(110, 1, 501, "ttys004", "/home/x/.local/bin/claude") +
			row(120, 1, 501, "ttys005", "/home/x/.local/bin/claude") +
			// 在 tmux pane 裡：排除。
			row(130, 1, 501, "ttys007", "/opt/codex -c a=b -C /home/x") +
			// team 直接跑在 iTerm：列出。
			row(140, 1, 501, "ttys001", `/opt/homebrew/Caskroom/codex/0.162.1/bin/codex -c cli_auth_credentials_store="file" -C /home/x --remote unix:///tmp/s/tui.sock`) +
			// codex 在該 session 的 shell 工具裡跑的子 codex：祖先是候選 → 不重複列。
			row(141, 140, 501, "ttys001", "/bin/zsh -c x") +
			row(142, 141, 501, "ttys001", "/opt/codex exec hi") +
			// 沒有 tty（app-server）、別人的程序、非互動子命令、無法判定 profile 的 codex。
			row(150, 1, 501, "??", "/opt/codex app-server --listen unix://x") +
			row(160, 1, 502, "ttys008", "/opt/codex") +
			row(170, 1, 501, "ttys010", "/opt/codex -c a=b exec --json hi") +
			row(180, 1, 501, "ttys011", "/opt/codex") +
			row(190, 1, 501, "ttys012", "/opt/codex") +
			row(200, 1, 501, "ttys013", "/opt/codex -C /x resume abc") +
			// argv 夾帶 CODEX_HOME= 字樣、環境裡沒有：不得被當成 profile。
			row(210, 1, 501, "ttys014", "/opt/codex -- 把 CODEX_HOME="+root+"/codex/team 寫進文件") +
			// 大小寫變體：檔案系統找得到，但不是原樣目錄名。
			row(220, 1, 501, "ttys015", "/opt/codex") +
			// prompt 開頭的互動 TUI（連續空白也要保留原樣）：列出。
			row(230, 1, 501, "ttys016", "/opt/codex  -C /x 修這個  bug"),
		env: map[int]string{
			100: "/home/x/.local/bin/claude --dangerously-skip-permissions SECRET_TOKEN=abc CLAUDE_CONFIG_DIR=" + root + "/claude/work",
			110: "claude CLAUDE_CONFIG_DIR=" + root + "/claude/second",
			120: "claude CLAUDE_CONFIG_DIR=" + root + "/claude/second",
			140: "codex OPENAI_API_KEY=sk-x CODEX_HOME=" + root + "/codex/team",
			180: "codex CODEX_HOME=" + root + "/codex/--help",                                   // 不合法名稱
			190: "codex CODEX_HOME=" + root + "/codex/team CODEX_HOME=" + root + "/codex/other", // 兩個值
			200: "codex CODEX_HOME=" + root + "/codex/ghost",                                    // 目錄不存在
			210: "HOME=/home/x",
			220: "CODEX_HOME=" + root + "/codex/Team",
			230: "CODEX_HOME=" + root + "/codex/team",
		},
	}
	s := &Scanner{Root: root, Run: f.run, UID: 501}
	got, err := s.Scan(context.Background(), map[string]bool{"/dev/ttys007": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].PID != 230 || got[2].Profile != "team" {
		t.Fatalf("應只列 work、team 與 prompt 開頭的 team，got %+v", got)
	}
	if g := got[0]; g.Tool != "claude" || g.Profile != "work" || g.PID != 100 || g.TTY != "ttys002" || g.SessionID != sidA || g.Cwd != "/home/x" {
		t.Fatalf("claude 卡片不符：%+v", g)
	}
	if g := got[1]; g.Tool != "codex" || g.Profile != "team" || g.PID != 140 || g.TTY != "ttys001" || g.SessionID != "" {
		t.Fatalf("codex 卡片不符：%+v", g)
	}
	// 回傳結構不得帶任何環境變數內容。
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "SECRET_TOKEN") || strings.Contains(string(b), "sk-x") {
		t.Fatalf("環境變數外洩到結果：%s", b)
	}
	// 第二輪：env 結果走快取，不再對同一程序跑 ps -E。
	before := countEnv(f.calls)
	if _, err := s.Scan(context.Background(), map[string]bool{"/dev/ttys007": true}); err != nil {
		t.Fatal(err)
	}
	if countEnv(f.calls) != before {
		t.Fatalf("env 推導應快取：%v", f.calls)
	}
	for k, v := range s.cache {
		if v != "" && v != "work" && v != "second" && v != "team" {
			t.Fatalf("快取只能存 profile 名稱：%v=%q", k, v)
		}
	}
	// 程序結束：下一輪就不列，快取也清掉。
	f.table = row(100, 1, 501, "ttys002", "/home/x/.local/bin/claude")
	got, _ = s.Scan(context.Background(), nil)
	if len(got) != 1 || got[0].PID != 100 {
		t.Fatalf("team 結束後應消失：%+v", got)
	}
	if len(s.cache) != 1 {
		t.Fatalf("已結束程序的快取應清掉：%v", s.cache)
	}
}

func countEnv(calls []string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "-E") {
			n++
		}
	}
	return n
}

func TestCodexIsTUI(t *testing.T) {
	cases := map[string]bool{
		"/x/codex":                              true,
		"/x/codex --yolo":                       true,
		"/x/codex -c a=b -C /h --remote unix:x": true,
		"/x/codex -c a=b resume abc":            true,
		"/x/codex fork abc":                     true,
		"/x/codex -C /h -- 幫我修 bug":             true,
		"/x/codex exec hi":                      false,
		"/x/codex -c exec app-server":           false,
		"/x/codex login status":                 false,
		"/x/codex 修這個 bug":                      true,
		"/x/codex e hi":                         false,
		"/x/codex -m gpt features list":         false,
		"/x/codex mcp-server":                   false,
	}
	for in, want := range cases {
		if got := CodexIsTUI(in); got != want {
			t.Errorf("CodexIsTUI(%q)=%v want %v", in, got, want)
		}
	}
}

func TestProfileFromHome(t *testing.T) {
	root, _ := setup(t)
	ok := []string{root + "/codex/team"}
	bad := []string{root + "/codex/team/", root + "/codex/../codex/team", root + "/codex/--help", root + "/claude/work", "/tmp/codex/team", root + "/codex/ghost", ""}
	for _, h := range ok {
		if _, good := ProfileFromHome(root, "codex", h); !good {
			t.Errorf("%q 應接受", h)
		}
	}
	for _, h := range bad {
		if _, good := ProfileFromHome(root, "codex", h); good {
			t.Errorf("%q 應拒絕", h)
		}
	}
}

// Codex 升版時，aiuse 與 procscan 的內建子命令／吃值選項清單必須一起更新。
// aiuse 不在本 repo，預設比對 testdata 內的節錄（aiuse-cli-surface.sh）；設 DC_AIUSE_SOURCE 則改比對指定的那份 aiuse，
// 用來確認實際安裝的 aiuse 與 procscan 仍同步。指定的檔讀不到或格式不符一律 FAIL，不 Skip（避免設錯路徑卻以為驗過）。
func TestBuiltinCLISurfaceMatchesAiuse(t *testing.T) {
	src := os.Getenv("DC_AIUSE_SOURCE")
	if src == "" {
		src = filepath.Join("testdata", "aiuse-cli-surface.sh")
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("讀不到 aiuse 原始碼 %s：%v", src, err)
	}
	t.Logf("比對來源：%s", src)
	script := string(b)
	caseWords := func(fn string) map[string]bool {
		i := strings.Index(script, fn+"() {")
		if i < 0 {
			t.Fatalf("%s 裡找不到函式 %s", src, fn)
		}
		body := script[i:]
		j := strings.Index(body, `case "$1" in`)
		if j < 0 {
			t.Fatalf("%s 的 %s 裡找不到 case \"$1\" in", src, fn)
		}
		start := j + len(`case "$1" in`)
		end := start + strings.Index(body[start:], ")")
		out := map[string]bool{}
		for _, w := range strings.Split(strings.ReplaceAll(body[start:end], "\\\n", ""), "|") {
			if w = strings.TrimSpace(w); w != "" {
				out[w] = true
			}
		}
		return out
	}
	sub := caseWords("codex_known_subcommand")
	if fmt.Sprint(sortedKeys(sub)) != fmt.Sprint(sortedKeys(codexSubcommands)) {
		t.Errorf("子命令清單不同步：aiuse=%v procscan=%v", sortedKeys(sub), sortedKeys(codexSubcommands))
	}
	opts := caseWords("codex_root_option_takes_value")
	opts["-i"], opts["--image"] = true, true
	if fmt.Sprint(sortedKeys(opts)) != fmt.Sprint(sortedKeys(codexValueOptions)) {
		t.Errorf("吃值選項不同步：aiuse=%v procscan=%v", sortedKeys(opts), sortedKeys(codexValueOptions))
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestTmuxClaudeSessionFromSessionsFile(t *testing.T) {
	root, start := setup(t)
	writeSession(t, root, "work", 300, sidA, "interactive", start.Add(time.Second))
	f := &fakePS{table: row(299, 1, 501, "ttys020", "bash /x/aiuse claude work --resume "+sidA) +
		row(300, 299, 501, "ttys020", "/home/x/.local/bin/claude --resume "+sidA) +
		// session 內 shell 工具跑的子 claude（同 tty、父程序是 claude）：不能蓋掉外層。
		row(301, 300, 501, "ttys020", "/home/x/.local/bin/claude") +
		// 別人的程序。
		row(310, 1, 502, "ttys021", "/home/x/.local/bin/claude")}
	s := &Scanner{Root: root, Run: f.run, UID: 501}
	if _, err := s.Scan(context.Background(), map[string]bool{"/dev/ttys020": true, "/dev/ttys021": true}); err != nil {
		t.Fatal(err)
	}
	if sid, ok := s.TmuxClaudeSession("work", "/dev/ttys020"); !ok || sid != sidA {
		t.Fatalf("應從 sessions/300.json 取得 session id，got %q %v", sid, ok)
	}
	// profile 不符（檔案不在該 profile 底下）、不合法 profile、別人的 tty：都不採信。
	for _, c := range [][2]string{{"second", "ttys020"}, {"../work", "ttys020"}, {"work", "ttys021"}, {"work", "ttys099"}} {
		if sid, ok := s.TmuxClaudeSession(c[0], c[1]); ok {
			t.Errorf("%v 不應取得 session id：%q", c, sid)
		}
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "-E") {
			t.Fatalf("tmux 內的 claude 不需讀程序環境：%s", call)
		}
	}
}
