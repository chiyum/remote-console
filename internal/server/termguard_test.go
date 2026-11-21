package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiyum/remote-console/internal/ttydwatch"
)

type fixedChecker struct{ st ttydwatch.Status }

func (f fixedChecker) Check(context.Context) ttydwatch.Status { return f.st }

func authedGet(h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr, r.Host = "127.0.0.1:1", "dev-console.localhost:7690"
	r.AddCookie(&http.Cookie{Name: CookieName, Value: testCookie})
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// 健康 API 需身分，回傳 checker 的狀態。
func TestTermHealthAPI(t *testing.T) {
	s, _ := newTestServer(t)
	s.Ttyd = fixedChecker{ttydwatch.Status{OK: false, Reason: "ttyd 沒有在執行", Limit: 8192}}
	h := s.UIHandler()

	r := httptest.NewRequest("GET", "/api/term/health", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:1", "dev-console.localhost:7690"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("未配對應 403，得 %d", w.Code)
	}

	w = authedGet(h, "/api/term/health", nil)
	var st ttydwatch.Status
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil || st.OK || st.Reason != "ttyd 沒有在執行" || st.Limit != 8192 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// ttyd socket 不存在時：頁面回含「終端機服務異常」的替代頁（503，含 termguard.js），WebSocket 回 502；
// 重啟中的 WebSocket 回 503 不碰 ttyd。
func TestTermDownFallbackAndGate(t *testing.T) {
	s, _ := newTestServer(t)
	dir, err := os.MkdirTemp("/tmp", "dcg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s.Cfg.TtydSocket = filepath.Join(dir, "missing.sock")
	s.TtydGate = &ttydwatch.Gate{}
	h := s.UIHandler()

	w := authedGet(h, "/term/?arg=dc-qa", nil)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "終端機服務異常") || !strings.Contains(w.Body.String(), termGuardSnippet) {
		t.Fatalf("替代頁不對：%d %q", w.Code, w.Body.String())
	}

	ws := map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Origin": "http://dev-console.localhost:7690"}
	w = authedGet(h, "/term/ws?arg=dc-qa", ws)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("ttyd 不在時 WebSocket 應 502，得 %d", w.Code)
	}
	if a, _ := s.TtydGate.State(); a != 0 {
		t.Fatalf("失敗的連線要釋放計數，active=%d", a)
	}

	if ok, _ := s.TtydGate.TryBeginRestart(); !ok {
		t.Fatal("應可進入重啟")
	}
	w = authedGet(h, "/term/ws?arg=dc-qa", ws)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("重啟中 WebSocket 應 503，得 %d", w.Code)
	}
}
