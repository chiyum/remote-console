package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sendKeyCalls 回傳 metaTmux 收到的 send-keys argv。
func (f *metaTmux) sendKeyCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c[0] == "send-keys" {
			out = append(out, strings.Join(c, " "))
		}
	}
	return out
}

func TestKeysAPISendsWhitelistedKeyByID(t *testing.T) {
	s, mt := newMetaServer(t)
	h := s.UIHandler()
	for _, c := range [][2]string{{"down", "Down"}, {"btab", "BTab"}, {"ctrl-c", "C-c"}, {"3", "3"}} {
		w := postTo(h, "/api/sessions/keys", `{"session":"plain","key":"`+c[0]+`"}`, nil)
		if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
			t.Fatalf("%s → %d %q，應為 204 且無內容", c[0], w.Code, w.Body.String())
		}
	}
	want := []string{"send-keys -t $6 Down", "send-keys -t $6 BTab", "send-keys -t $6 C-c", "send-keys -t $6 3"}
	if got := mt.sendKeyCalls(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("應以 session id 依序送鍵：%v", got)
	}
}

func TestKeysAPIRejects(t *testing.T) {
	s, mt := newMetaServer(t)
	h := s.UIHandler()
	cases := []struct {
		body string
		code int
	}{
		{`{"session":"plain","key":"a"}`, 400},
		{`{"session":"plain","key":"rm -rf /"}`, 400},
		{`{"session":"plain","key":"C-d"}`, 400},
		{`{"session":"plain","key":"down down"}`, 400},
		{`{"session":"plain","key":"Escape"}`, 400},
		{`{"session":"plain","key":""}`, 400},
		{`{"session":"plain","key":"down","extra":1}`, 400},
		{`{"session":"plain;ls","key":"down"}`, 400},
		{`{"session":"plain:0","key":"down"}`, 400},
		{`{"session":"=plain","key":"down"}`, 400},
		{`{"session":"$6","key":"down"}`, 400},
		{`{"session":"","key":"down"}`, 400},
		{`{"session":"dc-qa-不存在-xyz","key":"down"}`, 400},
		{`{"session":"dc-qa-nope-xyz","key":"down"}`, 404},
		// 前綴：tmux 的 -t 名稱 會前綴比對到 aiuse-claude-work-x；API 必須精確同名。
		{`{"session":"aiuse","key":"down"}`, 404},
		{`{"session":"pla","key":"down"}`, 404},
	}
	for _, c := range cases {
		w := postTo(h, "/api/sessions/keys", c.body, nil)
		if w.Code != c.code {
			t.Errorf("%s → %d %s，應為 %d", c.body, w.Code, w.Body.String(), c.code)
		}
		if strings.Contains(w.Body.String(), "tmux ") || strings.Contains(w.Body.String(), "zsh") {
			t.Errorf("回應不得帶 tmux 內容：%s", w.Body.String())
		}
	}
	for _, hdr := range []map[string]string{
		{"Cookie": "none"},
		{"X-Dev-Console": ""},
		{"Origin": "http://evil.example"},
		{"Origin": ""},
		{"Content-Type": "text/plain"},
	} {
		if w := postTo(h, "/api/sessions/keys", `{"session":"plain","key":"down"}`, hdr); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Errorf("%v → %d，應為 401/403", hdr, w.Code)
		}
	}
	if got := mt.sendKeyCalls(); len(got) != 0 {
		t.Fatalf("被拒的請求不得送鍵：%v", got)
	}
}

func TestInjectKeybar(t *testing.T) {
	out, ok := injectKeybar([]byte("<html><head><title>x</title></head><body></body></html>"))
	if !ok || string(out) != "<html><head><title>x</title>"+keybarSnippet+"</head><body></body></html>" {
		t.Fatalf("注入結果不對：%v %s", ok, out)
	}
	in := []byte("<html><body>no head</body></html>")
	if out, ok := injectKeybar(in); ok || !bytes.Equal(out, in) {
		t.Fatalf("找不到 </head> 應原樣回傳")
	}
}

// TestTermIndexInjectedThroughProxy 以假 ttyd（unix socket）驗證：index 頁注入按鍵列（明文與 gzip 兩種瀏覽器），
// 其他資源原樣轉發。
func TestTermIndexInjectedThroughProxy(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "dck")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "t.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	const page = "<!DOCTYPE html><html><head><title>ttyd</title></head><body>T</body></html>"
	ttyd := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/term/token" {
			w.Header().Set("Content-Type", "application/json;charset=utf-8")
			_, _ = io.WriteString(w, `{"token": ""}`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		// 模擬 ttyd：瀏覽器接受 gzip 就回 gzip。
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			_, _ = io.WriteString(zw, page)
			_ = zw.Close()
			return
		}
		_, _ = io.WriteString(w, page)
	})}
	go func() { _ = ttyd.Serve(ln) }()
	t.Cleanup(func() { _ = ttyd.Close() })

	s, _ := newTestServer(t)
	s.Cfg.TtydSocket = sock
	h := s.UIHandler()
	get := func(path, ae string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:1", "dev-console.localhost:7690"
		r.AddCookie(&http.Cookie{Name: CookieName, Value: testCookie})
		if ae != "" {
			r.Header.Set("Accept-Encoding", ae)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	want := "<title>ttyd</title>" + keybarSnippet + "</head>"

	w := get("/term/?arg=plain", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), want) || w.Header().Get("Content-Encoding") != "" {
		t.Fatalf("明文 index 應注入：%d %q %q", w.Code, w.Header().Get("Content-Encoding"), w.Body.String())
	}
	w = get("/term/?arg=plain", "gzip, deflate, br")
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("瀏覽器接受 gzip 應回 gzip：%v", w.Header())
	}
	gzLen := w.Body.Len()
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !strings.Contains(string(body), want) || w.Header().Get("Content-Length") != strconv.Itoa(gzLen) {
		t.Fatalf("gzip index 應注入且長度正確：%q %v", body, w.Header())
	}
	w = get("/term/token", "")
	if w.Body.String() != `{"token": ""}` {
		t.Fatalf("token 應原樣轉發：%q", w.Body.String())
	}
}

// tmuxCalls 回傳 metaTmux 收到的、會改變 pane 的指令：單獨的 send-keys／copy-mode，
// 以及原子捲動 if-shell 裡條件成立時要執行的指令串（去掉結尾回報標記）。
func (f *metaTmux) tmuxCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		switch c[0] {
		case "send-keys", "copy-mode":
			out = append(out, strings.Join(c, " "))
		case "if-shell":
			out = append(out, "if "+c[3]+": "+strings.TrimSuffix(c[5], " ; display-message -p dc-scroll-ok"))
		}
	}
	return out
}

// sgrHex 把滾輪序列寫成 send-keys -H 的參數，與 tmux 套件的組法一致。
func sgrHex(seq string) string {
	parts := make([]string, len(seq))
	for i := 0; i < len(seq); i++ {
		parts[i] = fmt.Sprintf("%02x", seq[i])
	}
	return strings.Join(parts, " ")
}

func TestScrollAPIByPaneState(t *testing.T) {
	cases := []struct {
		name, state, body, want string
		code                    int
	}{
		{"Claude 翻頁上", "", `{"session":"plain","action":"page-up"}`, "if $6: send-keys -t %6 PPage", 204},
		{"Claude 翻頁下帶 count 1", "", `{"session":"plain","action":"page-down","count":1}`, "if $6: send-keys -t %6 NPage", 204},
		{"Claude 逐列上 3", "", `{"session":"plain","action":"line-up","count":3}`, "if $6: send-keys -t %6 -H " + sgrHex(strings.Repeat("\x1b[<64;51;21M", 3)), 204},
		{"Codex 逐列下", "1\t0\t0\t0\t\t1\t100\t40\t%6\tcodex\n", `{"session":"plain","action":"line-down","count":2}`, "if $6: send-keys -t %6 -H " + sgrHex(strings.Repeat("\x1b[<65;51;21M", 2)), 204},
		{"全螢幕無滑鼠回報逐列＝409 不送", "1\t0\t0\t0\t\t0\t80\t24\t%6\tvim\n", `{"session":"plain","action":"line-up","count":1}`, "", 409},
		{"shell 往上進 copy-mode", "0\t0\t0\t0\t\t500\t100\t40\t%6\tzsh\n", `{"session":"plain","action":"page-up"}`, "if $6: copy-mode -e -t %6 ; send-keys -t %6 -X page-up", 204},
		{"shell 在底部往下不動作", "0\t0\t0\t0\t\t500\t100\t40\t%6\tzsh\n", `{"session":"plain","action":"line-down","count":5}`, "", 204},
		{"其他 tmux 模式＝409", "0\t0\t0\t1\ttree-mode\t500\t100\t40\t%6\tzsh\n", `{"session":"plain","action":"page-up"}`, "", 409},
	}
	for _, c := range cases {
		s, mt := newMetaServer(t)
		mt.scroll = c.state
		w := postTo(s.UIHandler(), "/api/sessions/scroll", c.body, nil)
		if w.Code != c.code {
			t.Errorf("%s：%d %s，應為 %d", c.name, w.Code, w.Body.String(), c.code)
		}
		if got := strings.Join(mt.tmuxCalls(), ","); got != c.want {
			t.Errorf("%s：送出 %q，應為 %q", c.name, got, c.want)
		}
	}
}

// 讀旗標後畫面變了（tmux 端條件不成立）：回 409，回應不帶 tmux 內容。
func TestScrollAPIStateChangedIs409(t *testing.T) {
	s, mt := newMetaServer(t)
	mt.ifResult = "dc-scroll-skip\n"
	w := postTo(s.UIHandler(), "/api/sessions/scroll", `{"session":"plain","action":"line-up","count":2}`, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "畫面剛改變") {
		t.Fatalf("應為 409 畫面剛改變：%d %s", w.Code, w.Body.String())
	}
}

func TestScrollAPIRejects(t *testing.T) {
	s, mt := newMetaServer(t)
	h := s.UIHandler()
	cases := []struct {
		body string
		code int
	}{
		{`{"session":"plain","action":"hello"}`, 400},
		{`{"session":"plain","action":"PageUpX"}`, 400},
		{`{"session":"plain","action":"\"; ls"}`, 400},
		{`{"session":"plain","action":"page-up;"}`, 400},
		{`{"session":"plain","action":"PPage"}`, 400},
		{`{"session":"plain","action":""}`, 400},
		{`{"session":"plain","key":"page-up"}`, 400},
		{`{"session":"plain","action":"page-up","count":2}`, 400},
		{`{"session":"plain","action":"line-up"}`, 400},
		{`{"session":"plain","action":"line-up","count":0}`, 400},
		{`{"session":"plain","action":"line-up","count":-1}`, 400},
		{`{"session":"plain","action":"line-down","count":31}`, 400},
		{`{"session":"plain","action":"line-down","count":1.5}`, 400},
		{`{"session":"plain","action":"line-down","count":"3"}`, 400},
		{`{"session":"plain","action":"line-down","count":99999999999999999999}`, 400},
		{`{"session":"plain","action":"page-up","text":"hello"}`, 400},
		{`{"session":"plain;ls","action":"page-up"}`, 400},
		{`{"session":"=plain","action":"page-up"}`, 400},
		{`{"session":"$6","action":"page-up"}`, 400},
		{`{"session":"pla","action":"page-up"}`, 404},
		{`{"session":"aiuse","action":"page-up"}`, 404},
	}
	for _, c := range cases {
		w := postTo(h, "/api/sessions/scroll", c.body, nil)
		if w.Code != c.code {
			t.Errorf("%s → %d %s，應為 %d", c.body, w.Code, w.Body.String(), c.code)
		}
		if strings.Contains(w.Body.String(), "tmux ") || strings.Contains(w.Body.String(), "zsh") {
			t.Errorf("回應不得帶 tmux 內容：%s", w.Body.String())
		}
	}
	for _, hdr := range []map[string]string{
		{"Cookie": "none"},
		{"X-Dev-Console": ""},
		{"Origin": "http://evil.example"},
		{"Origin": ""},
		{"Content-Type": "text/plain"},
	} {
		if w := postTo(h, "/api/sessions/scroll", `{"session":"plain","action":"page-up"}`, hdr); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Errorf("%v → %d，應為 401/403", hdr, w.Code)
		}
	}
	if got := mt.tmuxCalls(); len(got) != 0 {
		t.Fatalf("被拒的請求不得送鍵或進 copy-mode：%v", got)
	}
	// 捲動動作不可借道既有的按鍵 API。
	if w := postTo(h, "/api/sessions/keys", `{"session":"plain","key":"page-up"}`, nil); w.Code != 400 {
		t.Fatalf("keys API 不應接受捲動動作：%d", w.Code)
	}
}
