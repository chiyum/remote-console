// Package procscan 找出「已在跑、但不在 tmux 裡」的 aiuse Claude／Codex 互動 session（ADR 0003 A10 段）。
//
// 這些程序無法搬進 tmux，總覽頁只以唯讀卡片顯示。判定原則是「寧可不顯示也不誤標」：
//   - Claude：必須同時有 <profiles>/claude/<p>/sessions/<pid>.json（kind=interactive、pid 相符、啟動時間對得上 ps）
//     且該程序環境的 CLAUDE_CONFIG_DIR 正好是 <profiles>/claude/<p>。
//   - Codex：程序環境的 CODEX_HOME 正好是 <profiles>/codex/<p>，argv 是互動 TUI（不是 exec／app-server 等子命令）。
//   - 兩者都要有 tty、uid 是自己、tty 不屬於任何 tmux pane、祖先不是另一個候選（避免 shell 工具裡的子程序被重複列出）。
//
// 資安：讀程序環境（ps -E）時只抽 CLAUDE_CONFIG_DIR／CODEX_HOME 兩個 key，原始輸出不保存、不回傳；
// 快取只存「pid＋啟動時間 → profile」這個推導結果。
package procscan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Session 是一個不在 tmux 的互動 session（唯讀卡片的資料）。
type Session struct {
	Tool      string    `json:"tool"`
	Profile   string    `json:"profile"`
	PID       int       `json:"pid"`
	TTY       string    `json:"tty"`
	Started   time.Time `json:"started"`
	Cwd       string    `json:"cwd,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	// TranscriptPath 只在 Claude 且檔案存在時填入，供 snapshot 讀最後回覆；不送前端。
	TranscriptPath string `json:"-"`
}

// Proc 是 ps 的一列。
type Proc struct {
	PID, PPID, UID int
	TTY            string
	Start          time.Time
	Args           string
}

// Runner 執行外部指令（測試可注入）。
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner 以 exec 陣列執行（不經 shell）；ps 一律用 C locale，lstart 才是固定英文格式。
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"LC_ALL=C", "PATH=/bin:/usr/bin"}
	return cmd.Output()
}

var (
	profileRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`) // 與 validate／aiuse 同一套

	uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ValidSessionID 判斷 Claude session id 格式（hook 以它為非 tmux session 的 key）。
func ValidSessionID(s string) bool { return uuidRe.MatchString(s) }

type envKey struct {
	pid   int
	start int64
}

// Scanner 依序掃程序表；env 推導結果以 pid＋啟動時間快取，程序結束即清掉。
type Scanner struct {
	Root string // ~/.ai-profiles
	Run  Runner
	UID  int

	mu    sync.Mutex
	cache map[envKey]string // 值是 profile；"" 代表判定不出（也快取，避免每輪重讀）
	// inTmux 是最近一次 Scan 看到、跑在 tmux pane tty 上的最外層互動 claude（tty → 程序）；
	// 給 TmuxClaudeSession 查 session id，讓還沒收到任何 hook 的 tmux 卡片（例如剛 --resume 回來）也認得出是哪段對話。
	inTmux map[string]Proc
}

// New 建立使用真實 ps 的 Scanner。
func New(root string) *Scanner {
	return &Scanner{Root: root, Run: ExecRunner, UID: os.Getuid()}
}

const lstartLayout = "Mon Jan _2 15:04:05 2006"

// ParsePS 解析 `ps -axww -o pid=,ppid=,uid=,tty=,lstart=,command=`；lstart 固定 5 個欄位。
func ParsePS(out []byte, loc *time.Location) []Proc {
	var res []Proc
	for _, line := range strings.Split(string(out), "\n") {
		f, command := splitFields(line, 9)
		if len(f) < 9 || command == "" {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		uid, e3 := strconv.Atoi(f[2])
		start, e4 := time.ParseInLocation(lstartLayout, strings.Join(f[4:9], " "), loc)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue
		}
		// command 保留 ps 輸出的原樣（含連續空白）：envProfile 要拿它當 `ps -E` 輸出的前綴切掉 argv。
		res = append(res, Proc{PID: pid, PPID: ppid, UID: uid, TTY: f[3], Start: start, Args: command})
	}
	return res
}

// splitFields 取前 n 個以空白分隔的欄位，其餘部分原樣回傳（只去掉開頭空白）。
func splitFields(line string, n int) ([]string, string) {
	var f []string
	rest := line
	for len(f) < n {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return f, ""
		}
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			return append(f, rest), ""
		}
		f = append(f, rest[:end])
		rest = rest[end:]
	}
	return f, strings.TrimLeft(rest, " \t")
}

// toolOf 依執行檔名判斷工具；argv0 可能是完整路徑或只有名稱。
func toolOf(args string) string {
	argv0 := args
	if i := strings.IndexByte(args, ' '); i >= 0 {
		argv0 = args[:i]
	}
	switch filepath.Base(argv0) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	}
	return ""
}

// codexValueOptions 是 codex 根層會吃下一個參數的選項（跳過值，才能找到第一個非選項字）；
// 必須等於 aiuse codex_root_option_takes_value 再加 -i／--image，procscan 測試會讀 aiuse 原始碼比對。
var codexValueOptions = map[string]bool{
	"-c": true, "--config": true, "-C": true, "--cd": true, "--remote": true, "-m": true, "--model": true,
	"-p": true, "--profile": true, "-s": true, "--sandbox": true, "-a": true, "--ask-for-approval": true,
	"--add-dir": true, "-i": true, "--image": true, "--enable": true, "--disable": true, "--local-provider": true,
	"--remote-auth-token-env": true,
}

// codexSubcommands 是 codex 0.162.1 `codex --help` 的非互動子命令（含別名）；必須與 aiuse 的 codex_known_subcommand
// 內建清單相同（aiuse 另會在執行時解析 `codex --help`，procscan 只用內建）。Codex 升版時兩份一起更新，兩個 repo 的測試互相比對。
// resume／fork 是互動 TUI，不在這裡。
var codexSubcommands = map[string]bool{
	"agents": true, "exec": true, "e": true, "review": true, "login": true, "logout": true, "mcp": true, "mcp-server": true,
	"plugin": true, "app-server": true, "remote-control": true, "app": true, "completion": true, "update": true, "doctor": true,
	"sandbox": true, "debug": true, "apply": true, "a": true, "queue": true, "archive": true, "delete": true,
	"migrate-rollouts": true, "unarchive": true, "cloud": true, "exec-server": true, "features": true, "help": true,
}

// CodexIsTUI 判斷 codex argv 是否為互動 TUI：第一個非選項字不存在、是 resume／fork、是 `--`，或不是已知子命令
// （也就是 prompt，例如 `codex "修這個 bug"`）。prompt 以空白切開只影響第一個字，判斷結果不變。
func CodexIsTUI(args string) bool {
	f := strings.Fields(args)
	for i := 1; i < len(f); i++ {
		a := f[i]
		switch {
		case a == "--":
			return true
		case codexValueOptions[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return !codexSubcommands[a]
		}
	}
	return true
}

// claudeIsInteractive 排除 print／背景模式（sessions 檔另有 kind 判斷，這裡是第二道）。
func claudeIsInteractive(args string) bool {
	for _, a := range strings.Fields(args)[1:] {
		if a == "--" {
			break
		}
		switch a {
		case "-p", "--print", "--bg", "--background", "-v", "--version", "-h", "--help":
			return false
		}
	}
	return true
}

// EnvPart 從 `ps -E -o command=` 的輸出切掉 argv（必須正好以 command 開頭），只回傳後面的環境段；
// argv 裡夾帶的 `CODEX_HOME=...` 字樣因此不會被當成環境。對不上（程序在兩次 ps 之間改了 argv）就回空。
func EnvPart(out []byte, command string) []byte {
	out = bytes.TrimRight(out, "\n")
	if command == "" || !bytes.HasPrefix(out, []byte(command)) {
		return nil
	}
	rest := out[len(command):]
	if len(rest) > 0 && rest[0] != ' ' {
		return nil
	}
	return rest
}

// EnvValue 從環境段抽出唯一一個 key=value；出現兩個不同值就當判定不出。
func EnvValue(out []byte, key string) (string, bool) {
	prefix := key + "="
	val := ""
	for _, tok := range bytes.Fields(out) {
		if !bytes.HasPrefix(tok, []byte(prefix)) {
			continue
		}
		v := string(tok[len(prefix):])
		if val != "" && v != val {
			return "", false
		}
		val = v
	}
	return val, val != ""
}

// ProfileFromHome 只接受 <root>/<tool>/<profile> 這個精確形狀，且 profile 目錄實際存在。
func ProfileFromHome(root, tool, home string) (string, bool) {
	if home == "" || filepath.Clean(home) != home {
		return "", false
	}
	dir, name := filepath.Split(home)
	if filepath.Clean(dir) != filepath.Join(root, tool) || !profileRe.MatchString(name) || name == "shared" {
		return "", false
	}
	fi, err := os.Lstat(home)
	if err != nil || !fi.IsDir() {
		return "", false
	}
	// macOS 檔案系統不分大小寫：Lstat("…/Team") 也會成功，必須是目錄裡原樣存在的名稱才算。
	entries, err := os.ReadDir(filepath.Join(root, tool))
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.Name() == name {
			return name, true
		}
	}
	return "", false
}

func normTTY(t string) string { return strings.TrimPrefix(t, "/dev/") }

// envProfile 讀該程序環境裡的單一 key 推導 profile；結果快取，原始 env 輸出不留存。
func (s *Scanner) envProfile(ctx context.Context, p Proc, tool, key string) string {
	k := envKey{p.PID, p.Start.Unix()}
	s.mu.Lock()
	if v, ok := s.cache[k]; ok {
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()
	profile := ""
	if out, err := s.Run(ctx, "/bin/ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(p.PID)); err == nil {
		if home, ok := EnvValue(EnvPart(out, p.Args), key); ok {
			if name, ok := ProfileFromHome(s.Root, tool, home); ok {
				profile = name
			}
		}
		// out 含完整環境（可能有 token）：離開這裡就不再被引用。
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[envKey]string{}
	}
	s.cache[k] = profile
	s.mu.Unlock()
	return profile
}

// claudeSessionFile 是 Claude 寫在 <config>/sessions/<pid>.json 的欄位（只取需要的）。
type claudeSessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"`
	Kind      string `json:"kind"`
}

func (s *Scanner) readClaudeSession(profile string, p Proc) (claudeSessionFile, bool) {
	var f claudeSessionFile
	path := filepath.Join(s.Root, "claude", profile, "sessions", strconv.Itoa(p.PID)+".json")
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 64<<10 || json.Unmarshal(b, &f) != nil {
		return f, false
	}
	if f.PID != p.PID || f.Kind != "interactive" || !ValidSessionID(f.SessionID) {
		return f, false
	}
	// pid 可能被重用：sessions 檔的啟動時間必須落在 ps 啟動時間之後 10 分鐘內。
	started := time.UnixMilli(f.StartedAt)
	if started.Before(p.Start.Add(-2*time.Second)) || started.After(p.Start.Add(10*time.Minute)) {
		return f, false
	}
	return f, true
}

// claudeProjectDir 是 Claude 存 transcript 的專案目錄名：cwd 的非英數字元換成 '-'。
func claudeProjectDir(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Scan 回傳不在 tmux 的互動 session；tmuxTTYs 是所有 tmux pane 的 tty（含或不含 /dev/ 前綴皆可）。
func (s *Scanner) Scan(ctx context.Context, tmuxTTYs map[string]bool) ([]Session, error) {
	out, err := s.Run(ctx, "/bin/ps", "-axww", "-o", "pid=,ppid=,uid=,tty=,lstart=,command=")
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	procs := ParsePS(out, time.Local)
	inTmux := map[string]bool{}
	for t := range tmuxTTYs {
		inTmux[normTTY(t)] = true
	}
	byPID := map[int]Proc{}
	for _, p := range procs {
		byPID[p.PID] = p
	}
	// 候選：自己的、有 tty、tty 不在 tmux、執行檔是 claude／codex。
	cand := map[int]string{}
	for _, p := range procs {
		if p.UID != s.UID || p.TTY == "" || p.TTY == "??" || inTmux[normTTY(p.TTY)] {
			continue
		}
		if tool := toolOf(p.Args); tool != "" {
			cand[p.PID] = tool
		}
	}
	isNested := func(p Proc) bool {
		seen := 0
		for cur := p.PPID; cur > 1 && seen < 64; seen++ {
			if _, ok := cand[cur]; ok {
				return true
			}
			parent, ok := byPID[cur]
			if !ok {
				break
			}
			cur = parent.PPID
		}
		return false
	}
	// tmux pane 裡的互動 claude：同一個 tty 只留最外層那個（祖先不是 claude），shell 工具跑的子 claude 不算。
	tmuxClaude := map[string]Proc{}
	for _, p := range procs {
		tty := normTTY(p.TTY)
		if p.UID != s.UID || !inTmux[tty] || toolOf(p.Args) != "claude" || !claudeIsInteractive(p.Args) {
			continue
		}
		if parent, ok := byPID[p.PPID]; ok && normTTY(parent.TTY) == tty && toolOf(parent.Args) == "claude" {
			continue
		}
		if cur, ok := tmuxClaude[tty]; !ok || p.PID < cur.PID {
			tmuxClaude[tty] = p
		}
	}
	s.mu.Lock()
	s.inTmux = tmuxClaude
	s.mu.Unlock()
	live := map[envKey]bool{}
	var res []Session
	for _, p := range procs {
		tool, ok := cand[p.PID]
		if !ok || isNested(p) {
			continue
		}
		live[envKey{p.PID, p.Start.Unix()}] = true
		sess := Session{Tool: tool, PID: p.PID, TTY: normTTY(p.TTY), Started: p.Start}
		switch tool {
		case "claude":
			if !claudeIsInteractive(p.Args) {
				continue
			}
			profile := s.envProfile(ctx, p, "claude", "CLAUDE_CONFIG_DIR")
			if profile == "" {
				continue
			}
			f, ok := s.readClaudeSession(profile, p)
			if !ok {
				continue
			}
			sess.Profile, sess.SessionID, sess.Cwd = profile, f.SessionID, f.Cwd
			if f.Cwd != "" {
				tp := filepath.Join(s.Root, "claude", profile, "projects", claudeProjectDir(f.Cwd), f.SessionID+".jsonl")
				if fi, err := os.Stat(tp); err == nil && fi.Mode().IsRegular() {
					sess.TranscriptPath = tp
				}
			}
		case "codex":
			if !CodexIsTUI(p.Args) {
				continue
			}
			profile := s.envProfile(ctx, p, "codex", "CODEX_HOME")
			if profile == "" {
				continue
			}
			sess.Profile = profile
		}
		res = append(res, sess)
	}
	// 只留仍存活程序的快取。
	s.mu.Lock()
	for k := range s.cache {
		if !live[k] {
			delete(s.cache, k)
		}
	}
	s.mu.Unlock()
	return res, nil
}

// TmuxClaudeSession 回傳某 tmux pane tty 上 claude 目前的 session id（讀 <profile>/sessions/<pid>.json，
// pid 與啟動時間都要對得上）；profile 由呼叫端從 tmux 的 @aiuse_profile 帶入，只有該 profile 的檔案才採信。
func (s *Scanner) TmuxClaudeSession(profile, tty string) (string, bool) {
	if !profileRe.MatchString(profile) {
		return "", false
	}
	s.mu.Lock()
	p, ok := s.inTmux[normTTY(tty)]
	s.mu.Unlock()
	if !ok {
		return "", false
	}
	f, ok := s.readClaudeSession(profile, p)
	if !ok {
		return "", false
	}
	return f.SessionID, true
}
