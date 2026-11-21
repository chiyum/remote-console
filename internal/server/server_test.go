package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/chiyum/remote-console/internal/authstore"
	"github.com/chiyum/remote-console/internal/config"
	"github.com/chiyum/remote-console/internal/hookstore"
	"github.com/chiyum/remote-console/internal/hub"
	"github.com/chiyum/remote-console/internal/snapshot"
)

// fakeTmux 記錄所有呼叫；new-session 回傳固定 id，has-session 一律成功。
type fakeTmux struct {
	mu    sync.Mutex
	calls [][]string
}

func (f *fakeTmux) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	switch args[0] {
	case "new-session":
		return "$9\n", nil
	}
	return "", nil
}

func (f *fakeTmux) count(cmd string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c[0] == cmd {
			n++
		}
	}
	return n
}

// testCookie 是 newTestServer 以真實配對流程換到的 session token。
var testCookie string

func newTestServer(t *testing.T) (*Server, *fakeTmux) {
	home := t.TempDir()
	for _, p := range []string{".ai-profiles/claude/work", ".ai-profiles/codex/lab", "proj"} {
		_ = os.MkdirAll(filepath.Join(home, p), 0o755)
	}
	cfg := config.Config{Home: home, ProfilesRoot: filepath.Join(home, ".ai-profiles"), ClaudeHome: filepath.Join(home, ".claude"),
		AiusePath: fakeExecutable(t, t.TempDir(), "aiuse"), ListenAddr: "127.0.0.1:7690", HookAddr: "127.0.0.1:7691", PublicHost: "mac.ts.net", LocalHost: "dev-console.localhost", SessionPath: "/usr/bin", Lang: "C"}
	ft := &fakeTmux{}
	h := hub.New()
	hooks := hookstore.New("")
	store, err := authstore.Open(filepath.Join(home, "data"))
	if err != nil {
		t.Fatal(err)
	}
	code, _ := authstore.IssuePairCode(filepath.Join(home, "data"), time.Minute)
	if testCookie, err = store.Redeem(code, "test", 0); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Store: store,
		Cfg:   cfg, Auth: Authorizer{PublicHost: cfg.PublicHost, LocalHost: cfg.LocalHost, ListenAddr: cfg.ListenAddr, AllowedLogins: []string{"o@x"}, Sessions: store},
		Hub: h, Hooks: hooks, Collector: snapshot.New(cfg, ft, hooks, h.Publish),
		Launcher: &Launcher{Cfg: cfg, Tmux: ft}, Tmux: ft, Static: fstest.MapFS{"index.html": {Data: []byte("hi")}},
	}
	return s, ft
}

func post(h http.Handler, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:1", "dev-console.localhost:7690"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Dev-Console", "1")
	r.Header.Set("Origin", "http://dev-console.localhost:7690")
	r.AddCookie(&http.Cookie{Name: CookieName, Value: testCookie})
	for k, v := range hdr {
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

func TestLaunchRejectsInjection(t *testing.T) {
	s, ft := newTestServer(t)
	h := s.UIHandler()
	bad := []string{
		`{"tool":"claude","profile":"work;touch /tmp/dc-pwn"}`,
		`{"tool":"claude","profile":"../work"}`,
		`{"tool":"claude","profile":"notexist"}`,
		`{"tool":"bash","profile":"work"}`,
		`{"tool":"claude","profile":"work","dir":"/nonexistent"}`,
		`{"tool":"claude","profile":"work","dir":"~/$(touch /tmp/dc-pwn2)"}`,
		`{"tool":"claude","profile":"work","dir":"/tmp;touch /tmp/dc-pwn3"}`,
		`{"tool":"claude","profile":"work","extra":1}`,
	}
	for _, b := range bad {
		if w := post(h, b, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s → %d %s", b, w.Code, w.Body.String())
		}
	}
	if n := ft.count("new-session"); n != 0 {
		t.Fatalf("不合法輸入不應呼叫 tmux new-session，實際 %d 次", n)
	}
}

func TestLaunchCSRF(t *testing.T) {
	s, ft := newTestServer(t)
	h := s.UIHandler()
	if w := post(h, `{"tool":"claude","profile":"work"}`, map[string]string{"X-Dev-Console": ""}); w.Code != http.StatusForbidden {
		t.Fatalf("缺自訂 header 應 403，got %d", w.Code)
	}
	if w := post(h, `{"tool":"claude","profile":"work"}`, map[string]string{"Origin": "https://evil.com"}); w.Code != http.StatusForbidden {
		t.Fatalf("跨站 Origin 應 403，got %d", w.Code)
	}
	if w := post(h, `{"tool":"claude","profile":"work"}`, map[string]string{"Origin": ""}); w.Code != http.StatusForbidden {
		t.Fatalf("本機直連沒帶 Origin 也應 403，got %d", w.Code)
	}
	if ft.count("new-session") != 0 {
		t.Fatal("CSRF 失敗不應建立 session")
	}
}

func TestLaunchClaudeArgv(t *testing.T) {
	s, ft := newTestServer(t)
	w := post(s.UIHandler(), `{"tool":"claude","profile":"work"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	var ns []string
	for _, c := range ft.calls {
		if c[0] == "new-session" {
			ns = c
		}
	}
	joined := strings.Join(ns, " ")
	realHome, _ := filepath.EvalSymlinks(s.Cfg.Home)
	if !strings.Contains(joined, "-c "+realHome+" ") || !strings.HasSuffix(joined, "-- "+s.Cfg.AiusePath+" claude work") || !strings.Contains(joined, "-s aiuse-claude-work-") {
		t.Fatalf("new-session 參數不符：%v", ns)
	}
	if !strings.Contains(w.Body.String(), `"termUrl":"/term/?arg=aiuse-claude-work-`) {
		t.Fatalf("回應缺 termUrl：%s", w.Body.String())
	}
}

func TestAPIRequiresAuthButIndexIsPublic(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.UIHandler()
	r := httptest.NewRequest("GET", "/api/snapshot", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:1", "mac.ts.net"
	r.Header.Set("Tailscale-User-Login", "stranger@x")
	r.Header.Set("X-Forwarded-For", "100.5.5.5")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("陌生帳號應 403，got %d", w.Code)
	}
	r = httptest.NewRequest("GET", "/term/?arg=x", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:1", "mac.ts.net"
	r.Header.Set("Tailscale-User-Login", "stranger@x")
	r.Header.Set("X-Forwarded-For", "100.5.5.5")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("終端機對陌生帳號應 403，got %d", w.Code)
	}
	r = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("首頁應 200，got %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("首頁 CSP 不符：%q", csp)
	}
}

func TestHookEndpointLoopbackOnly(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.HookHandler()
	body := `{"hook_event_name":"Stop","session_id":"s"}`
	r := httptest.NewRequest("POST", "/api/hook?profile=work", strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:1", "127.0.0.1:7691"
	r.Header.Set("X-Tmux-Pane", "%3")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || s.Hooks.Get("%3") != nil {
		t.Fatalf("沒帶 hook token 應 403，got %d", w.Code)
	}
	tok, _ := authstore.EnsureHookToken(filepath.Join(s.Cfg.Home, "data"))
	r = httptest.NewRequest("POST", "/api/hook?profile=work", strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:1", "127.0.0.1:7691"
	r.Header.Set("X-Tmux-Pane", "%3")
	r.Header.Set("X-DC-Hook-Token", tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || s.Hooks.Get("%3") == nil || s.Hooks.Get("%3").State != hookstore.StateDone {
		t.Fatalf("hook 應被接受：%d", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/hook", strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:1", "127.0.0.1:7691"
	r.Header.Set("X-Forwarded-For", "100.1.1.1")
	r.Header.Set("X-DC-Hook-Token", tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("帶 XFF（經代理）應 403，got %d", w.Code)
	}

	// 不在 tmux 的 claude（沒有 X-Tmux-Pane）改以 session id 為 key：沒 token 一樣 403、不寫入。
	const sid = "aaaaaaaa-0000-4000-8000-000000000001"
	sidBody := `{"hook_event_name":"UserPromptSubmit","session_id":"` + sid + `"}`
	r = httptest.NewRequest("POST", "/api/hook?profile=work", strings.NewReader(sidBody))
	r.RemoteAddr, r.Host = "127.0.0.1:1", "127.0.0.1:7691"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || s.Hooks.Get(hookstore.SessionKey(sid)) != nil {
		t.Fatalf("非 tmux hook 沒帶 token 應 403 且不寫入，got %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/hook?profile=work", strings.NewReader(sidBody))
	r.RemoteAddr, r.Host = "127.0.0.1:1", "127.0.0.1:7691"
	r.Header.Set("X-DC-Hook-Token", tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if st := s.Hooks.Get(hookstore.SessionKey(sid)); w.Code != http.StatusNoContent || st == nil || st.State != hookstore.StateWorking {
		t.Fatalf("帶 token 的非 tmux hook 應以 session id 記錄：%d %+v", w.Code, st)
	}
}

// TestForgedLoopbackRejected 模擬 Docker 容器經 host.docker.internal 直連：偽造 Host 與各種 header、沒有 cookie。
func TestForgedLoopbackRejected(t *testing.T) {
	s, ft := newTestServer(t)
	h := s.UIHandler()
	host := "dev-console.localhost:7690"
	forge := func(method, path string, body string, extra map[string]string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr, r.Host = "127.0.0.1:1", host
		r.Header.Set("Origin", "http://"+host)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Dev-Console", "1")
		for k, v := range extra {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	wsHdr := map[string]string{"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ=="}
	checks := []struct {
		name, method, path, body string
		extra                    map[string]string
	}{
		{"POST /api/sessions", "POST", "/api/sessions", `{"tool":"claude","profile":"work"}`, nil},
		{"POST /api/sessions 偽造 login", "POST", "/api/sessions", `{"tool":"claude","profile":"work"}`, map[string]string{"Tailscale-User-Login": "o@x", "X-Forwarded-For": "100.64.0.10"}},
		{"/term/ws", "GET", "/term/ws?arg=dc-a3", "", wsHdr},
		{"/term/", "GET", "/term/?arg=dc-a3", "", nil},
		{"/api/whoami", "GET", "/api/whoami", "", nil},
		{"/api/snapshot", "GET", "/api/snapshot", "", nil},
		{"假 cookie", "GET", "/api/whoami", "", map[string]string{"Cookie": CookieName + "=" + strings.Repeat("a", 64)}},
	}
	for _, h := range []string{"dev-console.localhost:7690", "127.0.0.1:7690"} {
		host = h
		for _, c := range checks {
			if code := forge(c.method, c.path, c.body, c.extra); code != http.StatusForbidden {
				t.Errorf("[Host %s] %s 應 403，got %d", h, c.name, code)
			}
		}
	}
	if ft.count("new-session") != 0 {
		t.Fatal("偽造請求不應建立 session")
	}
}

func TestPairEndpointSetsCookie(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.UIHandler()
	dir := filepath.Join(s.Cfg.Home, "data")
	pair := func(method, host, origin, code string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/auth/pair", strings.NewReader("code="+code))
		r.RemoteAddr, r.Host = "127.0.0.1:1", host
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	code, _ := authstore.IssuePairCode(dir, time.Minute)
	// GET 不再接受；沒 Origin、跨站 Origin、127.0.0.1 Host 都拒，且不消耗配對碼。
	if w := pair("GET", "mac.ts.net", "https://mac.ts.net", code); w.Code == http.StatusNoContent {
		t.Fatal("GET 不應能配對")
	}
	for _, c := range []struct{ host, origin string }{
		{"mac.ts.net", ""}, {"mac.ts.net", "https://evil.com"}, {"127.0.0.1:7690", "http://127.0.0.1:7690"},
	} {
		if w := pair("POST", c.host, c.origin, code); w.Code != http.StatusForbidden {
			t.Fatalf("%+v 應 403，got %d", c, w.Code)
		}
	}
	w := pair("POST", "mac.ts.net", "https://mac.ts.net", code)
	ck := w.Result().Cookies()
	if w.Code != http.StatusNoContent || len(ck) != 1 || !ck[0].HttpOnly || !ck[0].Secure || ck[0].SameSite != http.SameSiteStrictMode || ck[0].MaxAge < 80*86400 {
		t.Fatalf("serve 配對應設 HttpOnly+Secure+Strict 長效 cookie：%d %+v", w.Code, ck)
	}
	if w := pair("POST", "mac.ts.net", "https://mac.ts.net", code); w.Code != http.StatusForbidden {
		t.Fatalf("重複使用應 403，got %d", w.Code)
	}
	code2, _ := authstore.IssuePairCode(dir, time.Minute)
	w = pair("POST", "dev-console.localhost:7690", "http://dev-console.localhost:7690", code2)
	ck = w.Result().Cookies()
	if w.Code != http.StatusNoContent || len(ck) != 1 || ck[0].MaxAge > 12*3600 || ck[0].Domain != "" {
		t.Fatalf("本機配對 cookie 應為 host-only、12 小時內：%+v", ck)
	}
}
