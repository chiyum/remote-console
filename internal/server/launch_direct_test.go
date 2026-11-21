package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newDirectTestServer 是「直接模式」（沒有 aiuse）的測試伺服器：AiusePath 指向不存在的檔，
// SessionPath 只有一個暫存目錄，tools 列出要放進去的假執行檔（claude／codex）。
func newDirectTestServer(t *testing.T, tools ...string) (*Server, *fakeTmux, string) {
	t.Helper()
	s, ft := newTestServer(t)
	binDir := t.TempDir()
	for _, tool := range tools {
		fakeExecutable(t, binDir, tool)
	}
	s.Cfg.AiusePath = filepath.Join(t.TempDir(), "no-aiuse-here")
	s.Cfg.SessionPath = binDir
	s.Launcher.Cfg = s.Cfg
	return s, ft, binDir
}

func newSessionCall(t *testing.T, ft *fakeTmux) []string {
	t.Helper()
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var ns []string
	for _, c := range ft.calls {
		if c[0] == "new-session" {
			if ns != nil {
				t.Fatal("只應建立一個 session")
			}
			ns = c
		}
	}
	return ns
}

// 直接模式開 Claude／Codex：exec 陣列只有解析出的絕對路徑，不經 aiuse、不經 shell；名稱 dc-<tool>-*，不寫 @aiuse_* 標記。
func TestDirectModeLaunchExecsToolBinary(t *testing.T) {
	for _, tool := range []string{"claude", "codex"} {
		t.Run(tool, func(t *testing.T) {
			s, ft, binDir := newDirectTestServer(t, "claude", "codex")
			w := post(s.UIHandler(), `{"tool":"`+tool+`","profile":"","dir":"~/proj"}`, nil)
			if w.Code != http.StatusCreated {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			ns := newSessionCall(t, ft)
			joined := strings.Join(ns, " ")
			realHome, _ := filepath.EvalSymlinks(s.Cfg.Home)
			if !strings.HasSuffix(joined, "-- "+filepath.Join(binDir, tool)) || !strings.Contains(joined, "-s dc-"+tool+"-") ||
				!strings.Contains(joined, "-c "+filepath.Join(realHome, "proj")+" ") || strings.Contains(joined, "aiuse") {
				t.Fatalf("new-session 參數不符：%v", ns)
			}
			ft.mu.Lock()
			for _, c := range ft.calls {
				if c[0] == "set-option" && strings.HasPrefix(c[len(c)-2], "@aiuse_") {
					t.Errorf("直接模式不應寫 aiuse 標記：%v", c)
				}
			}
			ft.mu.Unlock()
			if !strings.Contains(w.Body.String(), `"termUrl":"/term/?arg=dc-`+tool+`-`) {
				t.Fatalf("回應缺 termUrl：%s", w.Body.String())
			}
		})
	}
}

// 直接模式找不到 codex：回明確錯誤，而且在建任何 tmux session 之前就停下（不留殘餘）。
func TestDirectModeMissingToolCreatesNoSession(t *testing.T) {
	s, ft, _ := newDirectTestServer(t, "claude")
	w := post(s.UIHandler(), `{"tool":"codex","profile":""}`, nil)
	if w.Code == http.StatusCreated || !strings.Contains(w.Body.String(), "找不到 codex") {
		t.Fatalf("應回找不到 codex：%d %s", w.Code, w.Body.String())
	}
	if n := ft.count("new-session"); n != 0 {
		t.Fatalf("找不到指令時不應建立 tmux session，實際 %d 次", n)
	}
}

// 直接模式的輸入驗證與 CSRF 不比 aiuse 模式寬：任何 profile 值都拒絕（不會走進 argv）、目錄規則相同、寫入要 cookie＋Origin＋自訂 header。
func TestDirectModeRejectsBadInput(t *testing.T) {
	s, ft, _ := newDirectTestServer(t, "claude", "codex")
	h := s.UIHandler()
	bad := []string{
		`{"tool":"claude","profile":"../x"}`,
		`{"tool":"claude","profile":"a;b"}`,
		`{"tool":"codex","profile":"work"}`,
		`{"tool":"bash","profile":""}`,
		`{"tool":"claude","profile":"","dir":"/nonexistent"}`,
		`{"tool":"claude","profile":"","dir":"/tmp"}`,
		`{"tool":"claude","profile":"","dir":"~/$(touch /tmp/dc-pwn2)"}`,
		`{"tool":"claude","profile":"","extra":1}`,
	}
	for _, b := range bad {
		if w := post(h, b, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s → %d %s", b, w.Code, w.Body.String())
		}
	}
	for _, hdr := range []map[string]string{{"X-Dev-Console": ""}, {"Origin": "https://evil.com"}, {"Origin": ""}} {
		if w := post(h, `{"tool":"claude","profile":""}`, hdr); w.Code != http.StatusForbidden {
			t.Errorf("%v 應 403，got %d", hdr, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"tool":"claude","profile":""}`))
	r.RemoteAddr, r.Host = "127.0.0.1:1", "dev-console.localhost:7690"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Dev-Console", "1")
	r.Header.Set("Origin", "http://dev-console.localhost:7690")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("沒有配對 cookie 應 403，got %d", w.Code)
	}
	if n := ft.count("new-session"); n != 0 {
		t.Fatalf("不合法請求不應呼叫 tmux new-session，實際 %d 次", n)
	}
}

// DC_CLAUDE_BIN／DC_CODEX_BIN 覆寫：只接受絕對路徑的可執行檔；設了但無效時不退回 PATH 搜尋（設定錯誤要讓人看見）。
func TestDirectModeBinOverride(t *testing.T) {
	s, ft, _ := newDirectTestServer(t, "codex")
	custom := fakeExecutable(t, t.TempDir(), "my-codex")
	s.Cfg.CodexBin = custom
	s.Launcher.Cfg = s.Cfg
	if w := post(s.UIHandler(), `{"tool":"codex","profile":""}`, nil); w.Code != http.StatusCreated {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if ns := newSessionCall(t, ft); ns[len(ns)-1] != custom {
		t.Fatalf("應執行覆寫的路徑：%v", ns)
	}
	s2, ft2, _ := newDirectTestServer(t, "codex")
	s2.Cfg.CodexBin = "relative/codex"
	s2.Launcher.Cfg = s2.Cfg
	if w := post(s2.UIHandler(), `{"tool":"codex","profile":""}`, nil); w.Code == http.StatusCreated {
		t.Fatalf("相對路徑覆寫應拒絕：%s", w.Body.String())
	}
	if ft2.count("new-session") != 0 {
		t.Fatal("覆寫無效時不應建立 session")
	}
}

func getProfiles(t *testing.T, s *Server) ProfilesResponse {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/profiles", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:1", "dev-console.localhost:7690"
	r.AddCookie(&http.Cookie{Name: CookieName, Value: testCookie})
	w := httptest.NewRecorder()
	s.UIHandler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	var p ProfilesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// /api/profiles 告訴前端目前模式：有 aiuse 列 profile；沒有 aiuse 回 direct 與空清單（前端據此隱藏 profile 選單）。
func TestProfilesAPIReportsMode(t *testing.T) {
	s, _ := newTestServer(t)
	if p := getProfiles(t, s); p.Mode != "aiuse" || len(p.Claude) != 1 || p.Claude[0] != "work" || len(p.Codex) != 1 || p.Codex[0] != "lab" {
		t.Fatalf("aiuse 模式回應不符：%+v", p)
	}
	d, _, _ := newDirectTestServer(t, "claude")
	if p := getProfiles(t, d); p.Mode != "direct" || p.Claude == nil || len(p.Claude) != 0 || len(p.Codex) != 0 {
		t.Fatalf("直接模式回應不符：%+v", p)
	}
}
