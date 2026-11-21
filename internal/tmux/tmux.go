// Package tmux 用 exec 陣列呼叫 tmux（不經 shell），列 session / pane 與建立新 session。
package tmux

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runner 抽象 tmux 執行，測試可注入假實作。
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// ExecRunner 直接 exec tmux binary。
type ExecRunner struct{ Path string }

// Run 執行 tmux 並回傳 stdout；失敗時把 stderr 併入錯誤訊息。
func (r ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Path, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Session 是 tmux session 摘要；@aiuse_* 是 aiuse 與 dev-console 寫入的標記。
type Session struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	Activity time.Time `json:"activity"`
	Attached int       `json:"attached"`
	Managed  string    `json:"managed,omitempty"`
	Profile  string    `json:"profile,omitempty"`
	Tool     string    `json:"tool,omitempty"`
	Windows  int       `json:"windows"`
	// Ready 是 aiuse 寫完所有標記後最後設定的 @aiuse_ready；dev-console 認領新 session 的條件之一。
	Ready string `json:"-"`
	// LaunchToken 是 aiuse 替遠端工作台代建時寫的 @aiuse_launch_token（＝helper dc-launch-<token> 的 token）；
	// 同時開兩個 Codex session 時靠它認領自己那個，不送前端。
	LaunchToken string `json:"-"`
	// DCNickname／DCTitle 是 dev-console 為了 iTerm2 tab 標題寫的副本（@dc_nickname／@dc_title），
	// 權威資料在 sessionmeta；只用來判斷 tmux 端要不要補設，不送前端。
	DCNickname string `json:"-"`
	DCTitle    string `json:"-"`
	ActivePane *Pane  `json:"activePane,omitempty"`
}

// Pane 是 pane 摘要。
type Pane struct {
	Session string `json:"-"`
	ID      string `json:"id"`
	Active  bool   `json:"-"`
	Command string `json:"command"`
	Path    string `json:"path"`
	Dead    bool   `json:"dead"`
	// TTY 是 pane 的終端機（/dev/ttysNNN）；用來排除「其實在 tmux 裡」的程序，不送前端。
	TTY string `json:"-"`
}

const sessionFmt = "#{session_id}\t#{session_name}\t#{session_created}\t#{session_activity}\t#{session_attached}\t#{@aiuse_managed}\t#{@aiuse_profile}\t#{@aiuse_tool}\t#{session_windows}\t#{@aiuse_ready}\t#{@aiuse_launch_token}\t#{@dc_nickname}\t#{@dc_title}"
const paneFmt = "#{session_name}\t#{pane_id}\t#{&&:#{pane_active},#{window_active}}\t#{pane_current_command}\t#{pane_current_path}\t#{pane_dead}\t#{pane_tty}"

// noServer 判斷「tmux server 沒在跑」，這不是錯誤，只是沒有 session。
func noServer(err error) bool {
	s := err.Error()
	return strings.Contains(s, "no server running") || strings.Contains(s, "error connecting to") || strings.Contains(s, "No such file or directory")
}

// ListSessions 列出所有 session，並把每個 session 的 active pane 掛上去。
func ListSessions(ctx context.Context, r Runner) ([]Session, error) {
	out, err := r.Run(ctx, "list-sessions", "-F", sessionFmt)
	if err != nil {
		if noServer(err) {
			return nil, nil
		}
		return nil, err
	}
	sessions := ParseSessions(out)
	pout, err := r.Run(ctx, "list-panes", "-a", "-F", paneFmt)
	if err == nil {
		panes := ParsePanes(pout)
		for i := range sessions {
			for j := range panes {
				if panes[j].Session == sessions[i].Name && panes[j].Active {
					p := panes[j]
					sessions[i].ActivePane = &p
				}
			}
		}
	}
	return sessions, nil
}

// ListPanes 列出所有 pane。
func ListPanes(ctx context.Context, r Runner) ([]Pane, error) {
	out, err := r.Run(ctx, "list-panes", "-a", "-F", paneFmt)
	if err != nil {
		if noServer(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParsePanes(out), nil
}

// ParseSessions 解析 list-sessions 輸出。
func ParseSessions(out string) []Session {
	var res []Session
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 9 {
			continue
		}
		s := Session{ID: f[0], Name: f[1], Created: unix(f[2]), Activity: unix(f[3]), Managed: f[5], Profile: f[6], Tool: f[7]}
		s.Attached, _ = strconv.Atoi(f[4])
		s.Windows, _ = strconv.Atoi(f[8])
		if len(f) >= 10 {
			s.Ready = f[9]
		}
		if len(f) >= 11 {
			s.LaunchToken = f[10]
		}
		if len(f) >= 13 {
			s.DCNickname, s.DCTitle = f[11], f[12]
		}
		res = append(res, s)
	}
	return res
}

// ParsePanes 解析 list-panes 輸出。
func ParsePanes(out string) []Pane {
	var res []Pane
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 6 {
			continue
		}
		p := Pane{Session: f[0], ID: f[1], Active: f[2] == "1", Command: f[3], Path: f[4], Dead: f[5] == "1"}
		if len(f) >= 7 {
			p.TTY = f[6]
		}
		res = append(res, p)
	}
	return res
}

func unix(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// NewSessionSpec 描述要建立的 session；Argv 直接交給 tmux exec，不經 shell。
type NewSessionSpec struct {
	Name    string
	Dir     string
	Env     []string // KEY=VALUE
	Argv    []string
	Options map[string]string // 建立後 set-option 的 session 選項
}

// NewSession 建立 detached session 並回傳 session id（$N）。
func NewSession(ctx context.Context, r Runner, spec NewSessionSpec) (string, error) {
	args := []string{"new-session", "-d", "-P", "-F", "#{session_id}", "-s", spec.Name, "-c", spec.Dir}
	for _, e := range spec.Env {
		args = append(args, "-e", e)
	}
	// tmux 3.x：-- 之後的多個參數會被直接 execvp，不經 /bin/sh。
	args = append(args, "--")
	args = append(args, spec.Argv...)
	out, err := r.Run(ctx, args...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if !strings.HasPrefix(id, "$") {
		return "", fmt.Errorf("tmux 未回傳 session id：%q", id)
	}
	for k, v := range spec.Options {
		if _, err := r.Run(ctx, "set-option", "-q", "-t", id, k, v); err != nil {
			return id, err
		}
	}
	return id, nil
}

// HasSession 以精確名稱（=name）檢查 session 是否存在。
func HasSession(ctx context.Context, r Runner, name string) bool {
	_, err := r.Run(ctx, "has-session", "-t", "="+name)
	return err == nil
}

// CapturePane 取 pane 目前畫面文字（錯誤回報用）。
func CapturePane(ctx context.Context, r Runner, target string) string {
	out, _ := r.Run(ctx, "capture-pane", "-p", "-t", target)
	return strings.TrimSpace(out)
}

// CaptureVisible 取 pane 目前可見畫面並回傳錯誤；用來判定燈號時不能把「抓不到」當成「畫面是空的」。
// target 用 pane id（%N），在同一 tmux server 內唯一，不會有 session 名稱前綴比對的問題。
func CaptureVisible(ctx context.Context, r Runner, target string) (string, error) {
	return r.Run(ctx, "capture-pane", "-p", "-t", target)
}

// KillSession 以 id 刪除 session（清理用，失敗不回報）。
func KillSession(ctx context.Context, r Runner, id string) {
	_, _ = r.Run(ctx, "kill-session", "-t", id)
}

// KillSessionChecked 以 id（$N）刪除 session 並回報錯誤；封存要知道有沒有真的結束，才能決定要不要留紀錄。
// 只接受 $N 形式：名稱會有前綴比對問題，呼叫端也必須先從 ListSessions 結果驗證過這個 id。
func KillSessionChecked(ctx context.Context, r Runner, id string) error {
	if !sessionIDRe.MatchString(id) {
		return fmt.Errorf("tmux session id 格式不合法：%q", id)
	}
	_, err := r.Run(ctx, "kill-session", "-t", id)
	return err
}

var sessionIDRe = regexp.MustCompile(`^\$[0-9]{1,9}$`)

// TitleOn 是 @dc_title 的「dev-console 正在管 tab 標題」值；清空中的值為 "clearing:<unix 秒>"。
const TitleOn = "on"

// titleClearingPrefix 標記「已推送空標題、等下一輪再 unset」。
const titleClearingPrefix = "clearing:"

// titleClearDelay 是推送空標題後多久才 unset：太早 unset，tmux 還沒把空標題送到 iTerm2，tab 會停在舊暱稱
// （2026-10-26 iTerm2 實測：直接 unset 後 tab 維持舊標題；先送空標題，iTerm2 會回到預設名稱）。
const titleClearDelay = 2 * time.Second

// SyncTitle 讓 session 層的 iTerm2 tab 標題跟暱稱一致；只對 dev-console 設過暱稱的 session 動手。
//
//	有暱稱：set-titles on、set-titles-string '#{@dc_nickname}'、@dc_nickname=暱稱。標題字串只引用 option，
//	        暱稱裡的 #(...)／#{...} 不會被 tmux 展開（實測 3.7c）。
//	清空：先把 @dc_nickname 設成空字串（tmux 送空標題，iTerm2 回到預設名稱），titleClearDelay 後再 unset 三個
//	      option 回到全域預設。沒設過暱稱的 session（@dc_title 為空）完全不動。
//
// 以 session id（$N）為目標，不用 =名稱：tmux 的 =名稱 精確比對只在 session 目標指令有效，
// set-option／display-message 這類以 pane 為目標的指令要寫成 =名稱: 才找得到，寫錯時 -q 會默默失敗。
func SyncTitle(ctx context.Context, r Runner, s Session, nickname string, now time.Time) error {
	if !sessionIDRe.MatchString(s.ID) {
		return nil
	}
	set := func(args ...string) error {
		_, err := r.Run(ctx, append([]string{"set-option", "-t", s.ID}, args...)...)
		return err
	}
	if nickname != "" {
		if s.DCTitle == TitleOn && s.DCNickname == nickname {
			return nil
		}
		for _, kv := range [][]string{{"@dc_nickname", nickname}, {"set-titles-string", "#{@dc_nickname}"}, {"set-titles", "on"}, {"@dc_title", TitleOn}} {
			if err := set(kv...); err != nil {
				return err
			}
		}
		return nil
	}
	switch {
	case s.DCTitle == TitleOn:
		if err := set("@dc_nickname", ""); err != nil {
			return err
		}
		return set("@dc_title", titleClearingPrefix+strconv.FormatInt(now.Unix(), 10))
	case strings.HasPrefix(s.DCTitle, titleClearingPrefix):
		since, err := strconv.ParseInt(strings.TrimPrefix(s.DCTitle, titleClearingPrefix), 10, 64)
		if err == nil && now.Sub(time.Unix(since, 0)) < titleClearDelay {
			return nil
		}
		for _, k := range []string{"set-titles", "set-titles-string", "@dc_nickname", "@dc_title"} {
			if err := set("-u", k); err != nil {
				return err
			}
		}
	}
	return nil
}

// webKeys 是終端機頁按鍵列可送的鍵：API 鍵名 → tmux send-keys 鍵名。只有這 13 個，
// 不接受任意字串、不用 -l 送文字（按鍵列只解決「手機鍵盤沒有方向鍵／Esc」，不是遠端輸入通道）。
// 鍵名都不以 ";" 結尾（tmux 以 argv 執行時，結尾是分號的參數會被當成指令分隔）。
var webKeys = map[string]string{
	"esc":    "Escape",
	"tab":    "Tab",
	"btab":   "BTab",
	"up":     "Up",
	"down":   "Down",
	"left":   "Left",
	"right":  "Right",
	"enter":  "Enter",
	"1":      "1",
	"2":      "2",
	"3":      "3",
	"4":      "4",
	"ctrl-c": "C-c",
}

// WebKey 把 API 鍵名換成 tmux 鍵名；不在白名單回 false。
func WebKey(name string) (string, bool) {
	k, ok := webKeys[name]
	return k, ok
}

// SendWebKey 對 session id（$N）的作用中 pane 送一個白名單鍵。
// 目標只收 $N：名稱會有前綴比對問題，且 send-keys 的 -t 是 pane 目標，=名稱 會找不到；
// 呼叫端必須先從當下的 ListSessions 以精確名稱解出 id。
func SendWebKey(ctx context.Context, r Runner, sessionID, webKey string) error {
	if !sessionIDRe.MatchString(sessionID) {
		return fmt.Errorf("tmux session id 格式不合法：%q", sessionID)
	}
	key, ok := webKeys[webKey]
	if !ok {
		return fmt.Errorf("不在白名單的鍵：%q", webKey)
	}
	_, err := r.Run(ctx, "send-keys", "-t", sessionID, key)
	return err
}
