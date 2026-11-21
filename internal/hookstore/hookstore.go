// Package hookstore 保存 Claude hooks 回報的即時狀態（等你允許／問你問題／工作中／做完），
// 以 tmux pane id 為 key 對回總覽頁的 session 卡片；不在 tmux 的 claude（沒有 TMUX_PANE）改以
// "sid:<session_id>" 為 key，由 snapshot 依程序表找到的 sessions/<pid>.json 對回唯讀卡片（ADR 0003）。
//
// 狀態會原子寫入磁碟，服務重啟後燈號不會全部歸零；但它只是「最後一次聽到的事件」，
// 真正的畫面仍以終端機為準。
package hookstore

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 燈號狀態。
const (
	StateWorking    = "working"
	StatePermission = "waiting_permission"
	StateQuestion   = "question"
	StateIdle       = "idle"
	StateDone       = "done"
	StateEnded      = "ended"
)

// SessionKeyPrefix 是非 tmux session 的 key 前綴；pane id 一律以 % 開頭，兩者不會撞。
const SessionKeyPrefix = "sid:"

var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// SessionKey 回傳某 Claude session id 的 key；格式不合法回空字串（不收）。
func SessionKey(id string) string {
	if !sessionIDRe.MatchString(id) {
		return ""
	}
	return SessionKeyPrefix + id
}

// IsPaneKey 判斷 key 是不是 tmux pane（只有 pane 才能抓畫面）。
func IsPaneKey(k string) bool { return strings.HasPrefix(k, "%") }

// Event 是一次 hook 回報（payload 由 Claude Code 從 stdin 給，profile/pane 由 hook 指令補上）。
type Event struct {
	Profile          string `json:"-"`
	Pane             string `json:"-"`
	HookEventName    string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
	Cwd              string `json:"cwd"`
	ToolName         string `json:"tool_name"`
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
	// LastAssistantMessage 是 Stop payload 帶的最後回覆（claude 2.1.309 實測有此欄位），
	// 讓卡片不必等 transcript 被重讀就能顯示。
	LastAssistantMessage string `json:"last_assistant_message"`
}

// Status 是某個 pane 目前的燈號。
type Status struct {
	State          string `json:"state"`
	Tool           string `json:"tool,omitempty"`
	Message        string `json:"message,omitempty"`
	Event          string `json:"event"`
	SessionID      string `json:"sessionId"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
	Profile        string `json:"profile"`
	Cwd            string `json:"cwd,omitempty"`
	// LastMessage 是最近一次 Stop 帶的最後回覆；新一輪開始（UserPromptSubmit）時清掉。
	LastMessage string    `json:"lastMessage,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Store 是 pane → Status 的記憶體表，所有存取都經 mu。
type Store struct {
	mu     sync.Mutex
	byPane map[string]*Status
	// misses 記錄「畫面檢查第一次沒看到該有的東西」，連續兩次才降級（見 probe.go）；只存記憶體。
	misses map[string]miss
	// probed 是各 pane 上次列入畫面檢查的時間，用來限制抓畫面頻率。
	probed map[string]time.Time
	path   string
	now    func() time.Time
}

// New 建立 Store；path 非空時啟動即載入既有狀態。
func New(path string) *Store {
	s := &Store{byPane: map[string]*Status{}, misses: map[string]miss{}, probed: map[string]time.Time{}, path: path, now: time.Now}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &s.byPane)
		}
	}
	return s
}

// Next 依事件推算下一個狀態（純函式，方便測試）。
func Next(prev *Status, ev Event) (state, tool, msg string) {
	prevState := ""
	if prev != nil {
		prevState = prev.State
		tool = prev.Tool
	}
	switch ev.HookEventName {
	case "UserPromptSubmit":
		return StateWorking, "", ""
	case "PermissionRequest":
		// 權限對話框出現的同時觸發（實測比提示畫面早約 40ms）；Notification(permission_prompt)
		// 要再晚約 6 秒，只留作備援。
		// AskUserQuestion 與 ExitPlanMode 也會觸發 PermissionRequest（2.1.309 實測）；問答介面要亮「問你問題」。
		if ev.ToolName == "AskUserQuestion" {
			return StateQuestion, ev.ToolName, "Claude 在問你問題"
		}
		msg := "等你允許"
		if ev.ToolName != "" {
			msg = "要求使用 " + ev.ToolName
		}
		return StatePermission, ev.ToolName, msg
	case "PreToolUse":
		if ev.ToolName == "AskUserQuestion" {
			return StateQuestion, ev.ToolName, "Claude 在問你問題"
		}
		return StateWorking, ev.ToolName, ""
	case "PostToolUse":
		return StateWorking, "", ""
	case "Notification":
		lm := strings.ToLower(ev.Message)
		switch {
		case ev.NotificationType == "permission_prompt" || strings.Contains(lm, "permission"):
			// 這個備援比對話框晚約 6 秒，它對應的提示早已由 PermissionRequest 亮過燈：
			// 已是「問你問題」就維持；若畫面檢查已判定提示消失（使用者恰在 6 秒前後按 Esc），它是過期提醒，不再點亮。
			// 取捨：假設每個真正的新提示都先有 PermissionRequest（2.1.309 實測如此，含 AskUserQuestion／
			// ExitPlanMode）。若某次 PermissionRequest 沒送到（服務重啟或 curl 逾時），畫面檢查之後的這個
			// 備援會被忽略，該次提示不亮燈，直到下一個 hook。為了避免「熄了又被過期提醒點回去」而接受這個風險。
			if prev != nil && (prevState == StateQuestion || prev.Event == EventScreenCheck) {
				return prevState, tool, prev.Message
			}
			return StatePermission, tool, ev.Message
		case ev.NotificationType == "idle_prompt" || strings.Contains(lm, "waiting for your input"):
			// 回覆結束後閒置的提醒：已是「做完」就維持，不把綠燈蓋掉。
			if prevState == StateDone {
				return StateDone, "", prev.Message
			}
			return StateIdle, "", ev.Message
		case ev.NotificationType == "elicitation_dialog":
			return StateQuestion, tool, ev.Message
		default:
			return StateQuestion, tool, ev.Message
		}
	case "Stop":
		return StateDone, "", ""
	case "SessionEnd":
		return StateEnded, "", ""
	}
	return prevState, tool, ""
}

// Apply 套用一個事件。Pane 為空（不在 tmux 內的 claude）時改用 session id 當 key；
// 兩者都沒有就略過。-p 腳本等不會出現在卡片上的 session 也會暫存，下一輪 Prune 就清掉。
func (s *Store) Apply(ev Event) bool {
	if ev.Pane == "" {
		ev.Pane = SessionKey(ev.SessionID)
	}
	if ev.Pane == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.byPane[ev.Pane]
	// 新的 hook 代表狀態已由 claude 明確回報，前一次畫面檢查的「沒看到」不再算數。
	delete(s.misses, ev.Pane)
	// 同一個 pane 換了一個 claude session（關掉重開），舊狀態不沿用。
	if prev != nil && ev.SessionID != "" && prev.SessionID != ev.SessionID {
		prev = nil
	}
	state, tool, msg := Next(prev, ev)
	st := &Status{State: state, Tool: tool, Message: msg, Event: ev.HookEventName, SessionID: ev.SessionID,
		TranscriptPath: ev.TranscriptPath, Profile: ev.Profile, Cwd: ev.Cwd, UpdatedAt: s.now()}
	if st.TranscriptPath == "" && prev != nil {
		st.TranscriptPath = prev.TranscriptPath
	}
	switch {
	case ev.HookEventName == "Stop":
		st.LastMessage = ev.LastAssistantMessage
	case ev.HookEventName == "UserPromptSubmit":
		st.LastMessage = ""
	case prev != nil:
		st.LastMessage = prev.LastMessage
	}
	s.byPane[ev.Pane] = st
	s.persistLocked()
	return true
}

// Get 取某 pane 的狀態副本。
func (s *Store) Get(pane string) *Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.byPane[pane]; st != nil {
		c := *st
		return &c
	}
	return nil
}

// Keys 列出指定前綴的 key（snapshot 掃程序失敗時用來保留非 tmux 的燈號，不誤清）。
func (s *Store) Keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res []string
	for k := range s.byPane {
		if strings.HasPrefix(k, prefix) {
			res = append(res, k)
		}
	}
	return res
}

// Prune 刪除已不存在的 pane（pane 關掉後 id 不會重用於同一 server，但 server 重啟會重用）。
func (s *Store) Prune(alive map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for p := range s.byPane {
		if !alive[p] {
			delete(s.byPane, p)
			delete(s.misses, p)
			delete(s.probed, p)
			changed = true
		}
	}
	if changed {
		s.persistLocked()
	}
}

// persistLocked 原子寫檔（tmp + rename）；失敗只打 log，不影響即時燈號。
func (s *Store) persistLocked() {
	if s.path == "" {
		return
	}
	b, err := json.Marshal(s.byPane)
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err == nil {
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, s.path)
		}
		if err != nil {
			log.Printf("[hookstore] 持久化失敗：%v", err)
		}
	}
}
