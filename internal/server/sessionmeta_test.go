package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiyum/remote-console/internal/hookstore"
	"github.com/chiyum/remote-console/internal/procscan"
	"github.com/chiyum/remote-console/internal/sessionmeta"
	"github.com/chiyum/remote-console/internal/snapshot"
)

// metaTmux 模擬 tmux：預設 $5 是 aiuse claude、$6 是普通 shell；可用 add 加 session，kill-session 會真的移除。
type metaTmux struct {
	mu      sync.Mutex
	calls   [][]string
	killErr error
	order   []string
	sess    map[string][2]string // id → {list-sessions 列, list-panes 列}
}

func newMetaTmux() *metaTmux {
	f := &metaTmux{sess: map[string][2]string{}}
	f.add("$5", "aiuse-claude-work-x", "claude-v1\twork\tclaude", "claude", "/home/x/proj", "/dev/ttys090", "%5")
	f.add("$6", "plain", "\t\t", "zsh", "/home/x", "/dev/ttys091", "%6")
	return f
}

// add 加一個 session；marks 是 "@aiuse_managed\t@aiuse_profile\t@aiuse_tool"。
func (f *metaTmux) add(id, name, marks, cmd, path, tty, pane string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, id)
	f.sess[id] = [2]string{
		id + "\t" + name + "\t1700000000\t1700000000\t0\t" + marks + "\t1\t1\t\t\n",
		name + "\t" + pane + "\t1\t" + cmd + "\t" + path + "\t0\t" + tty + "\n",
	}
}

func (f *metaTmux) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	col := -1
	switch args[0] {
	case "list-sessions":
		col = 0
	case "list-panes":
		col = 1
	case "kill-session":
		if f.killErr != nil {
			return "", f.killErr
		}
		delete(f.sess, args[2])
	}
	if col < 0 {
		return "", nil
	}
	var b strings.Builder
	for _, id := range f.order {
		if v, ok := f.sess[id]; ok {
			b.WriteString(v[col])
		}
	}
	return b.String(), nil
}

func (f *metaTmux) kills() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c[0] == "kill-session" {
			out = append(out, c[2])
		}
	}
	return out
}

// metaProcs 回傳一張不在 tmux 的 Claude 唯讀卡片。
type metaProcs struct{}

func (metaProcs) Scan(context.Context, map[string]bool) ([]procscan.Session, error) {
	return []procscan.Session{{Tool: "claude", Profile: "work", PID: 4242, TTY: "ttys002", Started: time.Unix(1792738964, 0), SessionID: procSID, Cwd: "/home/x/p"}}, nil
}

const procSID = "aaaaaaaa-0000-4000-8000-000000000001"
const tmuxSID = "bbbbbbbb-0000-4000-8000-000000000002"

func newMetaServer(t *testing.T) (*Server, *metaTmux) {
	s, _ := newTestServer(t)
	mt := newMetaTmux()
	meta, err := sessionmeta.Open(filepath.Join(t.TempDir(), "session-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := snapshot.New(s.Cfg, mt, s.Hooks, nil)
	c.SetProcScanner(metaProcs{})
	c.SetMeta(meta)
	s.Collector, s.Tmux, s.Meta = c, mt, meta
	// tmux 卡片的 Claude session id 來自 hook。
	s.Hooks.Apply(hookEvent("%5", tmuxSID))
	return s, mt
}

func hookEvent(pane, sid string) hookstore.Event {
	return hookstore.Event{Pane: pane, SessionID: sid, Profile: "work", HookEventName: "UserPromptSubmit"}
}

func postTo(h http.Handler, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:1", "dev-console.localhost:7690"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Dev-Console", "1")
	r.Header.Set("Origin", "http://dev-console.localhost:7690")
	if hdr["Cookie"] != "none" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: testCookie})
	}
	for k, v := range hdr {
		if k == "Cookie" {
			continue
		}
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func snapCards(t *testing.T, s *Server) snapshot.Snapshot {
	t.Helper()
	return s.Collector.Build(context.Background())
}

func TestMetaAPIsRejectUnauthorized(t *testing.T) {
	s, mt := newMetaServer(t)
	h := s.UIHandler()
	_, _ = s.Meta.AddArchive(sessionmeta.Archive{Key: "tmux:gone", Kind: "tmux"})
	reqs := map[string]string{
		"/api/sessions/nickname":  `{"id":"$5","nickname":"x"}`,
		"/api/sessions/archive":   `{"id":"$5"}`,
		"/api/sessions/unarchive": `{"key":"tmux:gone"}`,
	}
	bad := []map[string]string{
		{"Cookie": "none"},
		{"X-Dev-Console": ""},
		{"Origin": "https://evil.example"},
		{"Origin": ""},
		{"Content-Type": "text/plain"},
	}
	for path, body := range reqs {
		for _, hdr := range bad {
			if w := postTo(h, path, body, hdr); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
				t.Errorf("%s %v → %d，應為 401/403", path, hdr, w.Code)
			}
		}
	}
	if k := mt.kills(); len(k) != 0 {
		t.Fatalf("未授權不得 kill：%v", k)
	}
	if !s.Meta.IsArchived("tmux:gone") {
		t.Fatal("未授權不得取消封存")
	}
	for _, c := range snapCards(t, s).Sessions {
		if c.Nickname != "" {
			t.Fatalf("未授權不得改暱稱：%+v", c)
		}
	}
}

func TestNicknameSetClearAndValidate(t *testing.T) {
	s, mt := newMetaServer(t)
	h := s.UIHandler()
	if w := postTo(h, "/api/sessions/nickname", `{"id":"$5","nickname":"  <b>客服</b>  "}`, nil); w.Code != http.StatusOK {
		t.Fatalf("設定暱稱 → %d %s", w.Code, w.Body.String())
	}
	var got string
	for _, c := range snapCards(t, s).Sessions {
		if c.ID == "$5" {
			got = c.Nickname
		}
	}
	if got != "<b>客服</b>" {
		t.Fatalf("暱稱應原樣保存（前端以 textContent 顯示），got %q", got)
	}
	// claude session id 的 key 也寫了：--resume 回來帶得回。
	if s.Meta.Resolve("claude:"+tmuxSID, "") != "<b>客服</b>" || s.Meta.Resolve("tmux:aiuse-claude-work-x", "") != "" {
		t.Fatal("已知 session id 的 Claude tmux 卡片，暱稱只存在 claude:<sid>")
	}
	// iTerm2 標題：有暱稱的 session 被設 set-titles；普通 shell 沒暱稱完全不動。
	var titled, plainTouched bool
	for _, c := range mt.calls {
		if c[0] == "set-option" && c[2] == "$5" && c[3] == "set-titles" {
			titled = true
		}
		if c[0] == "set-option" && c[2] == "$6" {
			plainTouched = true
		}
	}
	if !titled || plainTouched {
		t.Fatalf("標題同步不對：titled=%v plainTouched=%v", titled, plainTouched)
	}
	for _, body := range []string{
		`{"id":"$5","nickname":"` + strings.Repeat("字", sessionmeta.MaxNicknameRunes+1) + `"}`,
		`{"id":"$5","nickname":"x;"}`,
		`{"id":"aiuse-claude-work-x","nickname":"x"}`,
		`{"id":"$5 ; kill-server","nickname":"x"}`,
		`{"id":"$5","nickname":"x","extra":1}`,
	} {
		if w := postTo(h, "/api/sessions/nickname", body, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s → %d，應為 400", body, w.Code)
		}
	}
	if w := postTo(h, "/api/sessions/nickname", `{"id":"$77","nickname":"x"}`, nil); w.Code != http.StatusNotFound {
		t.Errorf("不存在的 session → %d，應為 404", w.Code)
	}
	if w := postTo(h, "/api/sessions/nickname", `{"id":"$5","nickname":""}`, nil); w.Code != http.StatusOK {
		t.Fatalf("清空 → %d", w.Code)
	}
	for _, c := range snapCards(t, s).Sessions {
		if c.ID == "$5" && c.Nickname != "" {
			t.Fatalf("清空後不應有暱稱：%q", c.Nickname)
		}
	}
}

func TestArchiveKillsOnlyVerifiedTmuxSession(t *testing.T) {
	s, mt := newMetaServer(t)
	h := s.UIHandler()
	_ = postTo(h, "/api/sessions/nickname", `{"id":"$5","nickname":"客服 bug"}`, nil)
	// 格式不合法、或格式合法但不在當下列表：都不得 kill。
	for body, code := range map[string]int{`{"id":"$77"}`: 404, `{"id":"=plain"}`: 400, `{"id":"proc:9999"}`: 404, `{"id":"$5\n"}`: 400} {
		if w := postTo(h, "/api/sessions/archive", body, nil); w.Code != code {
			t.Errorf("%s → %d，應為 %d", body, w.Code, code)
		}
	}
	if k := mt.kills(); len(k) != 0 {
		t.Fatalf("未驗證的 id 不得 kill：%v", k)
	}
	w := postTo(h, "/api/sessions/archive", `{"id":"$5"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("封存 → %d %s", w.Code, w.Body.String())
	}
	var rec sessionmeta.Archive
	_ = json.Unmarshal(w.Body.Bytes(), &rec)
	if !strings.HasPrefix(rec.Key, "tmux:aiuse-claude-work-x@") || rec.Nickname != "客服 bug" || rec.Kind != "tmux" || rec.Profile != "work" || rec.Tool != "claude" ||
		rec.ResumeCmd != "aiuse claude work --resume "+tmuxSID || rec.Dir != "/home/x/proj" || rec.ArchivedAt.IsZero() {
		t.Fatalf("封存紀錄不完整：%+v", rec)
	}
	if k := mt.kills(); len(k) != 1 || k[0] != "$5" {
		t.Fatalf("應只 kill $5：%v", k)
	}
	snap := snapCards(t, s)
	for _, c := range snap.Sessions {
		if c.ID == "$5" {
			t.Fatal("封存後卡片應消失")
		}
	}
	if len(snap.Archived) != 1 || snap.Archived[0].Nickname != "客服 bug" {
		t.Fatalf("已封存區應有這筆且保留暱稱：%+v", snap.Archived)
	}
	// 普通 shell：沒有接手指令（前端據此寫「無法找回」）。
	w = postTo(h, "/api/sessions/archive", `{"id":"$6"}`, nil)
	rec = sessionmeta.Archive{}
	_ = json.Unmarshal(w.Body.Bytes(), &rec)
	if w.Code != 200 || rec.ResumeCmd != "" {
		t.Fatalf("普通 shell 封存應成功且無接手指令：%d %+v", w.Code, rec)
	}
}

func TestArchiveKillFailureRollsBack(t *testing.T) {
	s, mt := newMetaServer(t)
	mt.killErr = errors.New("boom")
	if w := postTo(s.UIHandler(), "/api/sessions/archive", `{"id":"$5"}`, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("kill 失敗應回 500，got %d", w.Code)
	}
	if len(s.Meta.Archived()) != 0 {
		t.Fatal("kill 失敗時應撤回封存紀錄")
	}
}

func TestArchiveProcCardNeverKills(t *testing.T) {
	s, mt := newMetaServer(t)
	h := s.UIHandler()
	_ = postTo(h, "/api/sessions/nickname", `{"id":"proc:4242","nickname":"iTerm 那個"}`, nil)
	w := postTo(h, "/api/sessions/archive", `{"id":"proc:4242"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("封存唯讀卡片 → %d %s", w.Code, w.Body.String())
	}
	if k := mt.kills(); len(k) != 0 {
		t.Fatalf("唯讀卡片封存不得呼叫 kill-session：%v", k)
	}
	var rec sessionmeta.Archive
	_ = json.Unmarshal(w.Body.Bytes(), &rec)
	if rec.Kind != "proc" || rec.Nickname != "iTerm 那個" || rec.ResumeCmd != "aiuse claude work --resume "+procSID || rec.Dir != "/home/x/p" {
		t.Fatalf("唯讀卡片封存紀錄不對：%+v", rec)
	}
	// 程序還活著（每輪仍掃得到）：維持隱藏。
	for _, c := range snapCards(t, s).Sessions {
		if c.ID == "proc:4242" {
			t.Fatal("封存後仍存活的唯讀卡片應維持隱藏")
		}
	}
	// 取消封存：只移除紀錄，不重開任何東西。
	before := len(mt.calls)
	if w := postTo(h, "/api/sessions/unarchive", `{"key":"`+rec.Key+`"}`, nil); w.Code != http.StatusNoContent {
		t.Fatalf("取消封存 → %d", w.Code)
	}
	for _, c := range mt.calls[before:] {
		if c[0] == "new-session" || c[0] == "kill-session" {
			t.Fatalf("取消封存不得建立／結束 session：%v", c)
		}
	}
	if w := postTo(h, "/api/sessions/unarchive", `{"key":"`+rec.Key+`"}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("重複取消封存應 404，got %d", w.Code)
	}
	found := false
	for _, c := range snapCards(t, s).Sessions {
		found = found || c.ID == "proc:4242"
	}
	if !found {
		t.Fatal("取消封存後唯讀卡片應重新出現")
	}
}

func TestSameNameNewTmuxSessionStaysVisible(t *testing.T) {
	s, mt := newMetaServer(t)
	h := s.UIHandler()
	if w := postTo(h, "/api/sessions/archive", `{"id":"$6"}`, nil); w.Code != 200 {
		t.Fatalf("封存普通 shell → %d", w.Code)
	}
	// 使用者又 `tmux new -s plain`：同名新 session 必須照常顯示，舊紀錄仍在已封存區。
	mt.add("$8", "plain", "\t\t", "zsh", "/home/x", "/dev/ttys092", "%8")
	snap := snapCards(t, s)
	visible := false
	for _, c := range snap.Sessions {
		visible = visible || c.ID == "$8"
	}
	if !visible || len(snap.Archived) != 1 {
		t.Fatalf("同名新 session 應顯示、舊紀錄應保留：visible=%v archived=%+v", visible, snap.Archived)
	}
	// 再封存一次同名 session，kill 失敗：只撤回這一筆，舊紀錄不受影響。
	mt.killErr = errors.New("boom")
	if w := postTo(h, "/api/sessions/archive", `{"id":"$8"}`, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("kill 失敗應 500，got %d", w.Code)
	}
	if l := s.Meta.Archived(); len(l) != 1 || l[0].Label != "plain" {
		t.Fatalf("撤回時不得刪掉同名的舊紀錄：%+v", l)
	}
	mt.killErr = nil
	if w := postTo(h, "/api/sessions/archive", `{"id":"$8"}`, nil); w.Code != 200 || len(s.Meta.Archived()) != 2 {
		t.Fatalf("同名 session 第二次封存應另成一筆：%d %+v", w.Code, s.Meta.Archived())
	}
}

func TestResumedClaudeRemovesArchiveRecord(t *testing.T) {
	s, mt := newMetaServer(t)
	h := s.UIHandler()
	_ = postTo(h, "/api/sessions/nickname", `{"id":"$5","nickname":"客服 bug"}`, nil)
	if w := postTo(h, "/api/sessions/archive", `{"id":"$5"}`, nil); w.Code != 200 {
		t.Fatalf("封存 → %d", w.Code)
	}
	if n := len(snapCards(t, s).Archived); n != 1 {
		t.Fatalf("應有 1 筆封存紀錄，got %d", n)
	}
	// 照接手指令 --resume：新的 tmux session、同一個 Claude session id（hook 回報）。
	mt.add("$9", "aiuse-claude-work-y", "claude-v1\twork\tclaude", "claude", "/home/x/proj", "/dev/ttys093", "%9")
	s.Hooks.Apply(hookEvent("%9", tmuxSID))
	snap := snapCards(t, s)
	var card *snapshot.Card
	for i := range snap.Sessions {
		if snap.Sessions[i].ID == "$9" {
			card = &snap.Sessions[i]
		}
	}
	if card == nil || card.Nickname != "客服 bug" {
		t.Fatalf("重開的卡片應出現並帶回暱稱：%+v", card)
	}
	if len(snap.Archived) != 0 {
		t.Fatalf("重開後封存紀錄應自動移除：%+v", snap.Archived)
	}
}

func TestArchivedProcStillAliveIsNotAResume(t *testing.T) {
	s, _ := newMetaServer(t)
	h := s.UIHandler()
	if w := postTo(h, "/api/sessions/archive", `{"id":"proc:4242"}`, nil); w.Code != 200 {
		t.Fatalf("封存唯讀卡片 → %d", w.Code)
	}
	// 封存時那個程序本身還活著：維持隱藏、紀錄不移除；重複封存回 409，不覆寫。
	snap := snapCards(t, s)
	if len(snap.Archived) != 1 {
		t.Fatalf("程序本身還活著不算重開：%+v", snap.Archived)
	}
	if w := postTo(h, "/api/sessions/archive", `{"id":"proc:4242"}`, nil); w.Code != http.StatusConflict {
		t.Fatalf("重複封存應 409，got %d", w.Code)
	}
}

func TestArchiveWriteFailureDoesNotKill(t *testing.T) {
	s, mt := newMetaServer(t)
	s.Meta.SetWriter(func(string, []byte) error { return errors.New("磁碟滿") })
	if w := postTo(s.UIHandler(), "/api/sessions/archive", `{"id":"$5"}`, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("紀錄寫不進去應 500，got %d", w.Code)
	}
	if k := mt.kills(); len(k) != 0 {
		t.Fatalf("紀錄寫入失敗不得 kill：%v", k)
	}
	if w := postTo(s.UIHandler(), "/api/sessions/nickname", `{"id":"$5","nickname":"x"}`, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("暱稱寫不進去應 500，got %d", w.Code)
	}
	for _, c := range snapCards(t, s).Sessions {
		if c.Nickname != "" {
			t.Fatalf("寫檔失敗不得留下記憶體中的暱稱：%+v", c)
		}
	}
}
