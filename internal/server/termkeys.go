package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/chiyum/remote-console/internal/tmux"
	"github.com/chiyum/remote-console/internal/validate"
)

// KeyRequest 是終端機頁按鍵列送一個鍵的輸入。
type KeyRequest struct {
	Session string `json:"session"`
	Key     string `json:"key"`
}

// handleKeys 把一個白名單鍵送進指定 tmux session（ADR 0005）。
// 驗證順序：CSRF（JSON＋X-Dev-Console＋Origin）→ 鍵名白名單 → 名稱格式 → 當下 list-sessions 精確同名 → 以 $N 送鍵。
// 名稱格式先擋掉 ";"、":"、"=" 等 tmux target 語法；再從列表取 id，避免 -t 名稱 的前綴比對誤中其他 session。
// 回應只有成功與否，不帶任何 tmux 輸出或畫面內容。
func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request, id Identity) {
	if !s.csrfOK(w, r) {
		return
	}
	var req KeyRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	if _, ok := tmux.WebKey(req.Key); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不支援的按鍵"})
		return
	}
	if !validate.SessionName(req.Session) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session 名稱不合法"})
		return
	}
	sid, ok := s.resolveSessionID(w, r, req.Session)
	if !ok {
		return
	}
	if err := tmux.SendWebKey(r.Context(), s.Tmux, sid, req.Key); err != nil {
		// tmux 的錯誤訊息只寫 log，不回給前端。
		log.Printf("[keys] %s 送 %s 到 %s(%s) 失敗：%v", id.Login, req.Key, req.Session, sid, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "送出按鍵失敗"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveSessionID 以當下 list-sessions 精確同名取 session id（$N）；找不到或查詢失敗時已寫好回應並回 false。
// 呼叫前必須先用 validate.SessionName 擋掉 tmux target 語法。
func (s *Server) resolveSessionID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	sessions, err := tmux.ListSessions(r.Context(), s.Tmux)
	if err != nil {
		log.Printf("[keys] list-sessions 失敗：%v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查詢 tmux 失敗"})
		return "", false
	}
	for _, sess := range sessions {
		if sess.Name == name {
			return sess.ID, true
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "找不到這個 session（可能已結束）"})
	return "", false
}

// ScrollRequest 是終端機頁捲動的輸入：action 只收 4 個白名單動作，count 是逐列的列數（翻頁省略或 1）。
type ScrollRequest struct {
	Session string `json:"session"`
	Action  string `json:"action"`
	Count   int    `json:"count,omitempty"`
}

// handleScroll 捲動指定 session 的作用中 pane（ADR 0009）。驗證順序與 handleKeys 相同；
// 實際送什麼由 tmux.PlanScroll 依 pane 當下旗標決定，使用者輸入只會是白名單動作與有上限的整數。
// 畫面不支援時回 409，不做任何送出。
func (s *Server) handleScroll(w http.ResponseWriter, r *http.Request, id Identity) {
	if !s.csrfOK(w, r) {
		return
	}
	var req ScrollRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	n, ok := tmux.ValidScroll(req.Action, req.Count)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不支援的捲動"})
		return
	}
	if !validate.SessionName(req.Session) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session 名稱不合法"})
		return
	}
	sid, ok := s.resolveSessionID(w, r, req.Session)
	if !ok {
		return
	}
	method, err := tmux.SendScroll(r.Context(), s.Tmux, sid, req.Action, n)
	if errors.Is(err, tmux.ErrScrollUnsupported) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "這個畫面不支援這種捲動"})
		return
	}
	if err != nil {
		log.Printf("[keys] %s 捲動 %s×%d 到 %s(%s) 失敗（%s）：%v", id.Login, req.Action, n, req.Session, sid, method, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "捲動失敗"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// keybarSnippet 注入到 ttyd index 的 </head> 前。檔案由總覽頁的靜態檔提供（/term/ 的 CSP 允許 'self'）。
// 開頭的 termguard.js 是同步 script（ADR 0006）：ttyd 的 script 在 <body>，晚於它執行，才包得到 WebSocket。
const keybarSnippet = termGuardSnippet + `<link rel="stylesheet" href="/keybar.css"><script src="/keybar.js" defer></script>`

// injectKeybar 在第一個 </head> 前插入按鍵列的 css／js；找不到就原樣回傳 false（終端機仍可用，只是沒有按鍵列）。
// 只依賴 </head>，不依賴 ttyd 的 bundle 內容，ttyd 升版不易壞。
func injectKeybar(html []byte) ([]byte, bool) {
	i := bytes.Index(html, []byte("</head>"))
	if i < 0 {
		return html, false
	}
	out := make([]byte, 0, len(html)+len(keybarSnippet))
	out = append(out, html[:i]...)
	out = append(out, keybarSnippet...)
	out = append(out, html[i:]...)
	return out, true
}

// keybarCtxKey 標記「這個反代請求是 ttyd index，回應要注入按鍵列」；值是瀏覽器是否接受 gzip。
type keybarCtxKey struct{}

// isTermIndex 判斷是否為終端機頁本身（不是 WebSocket、token 或其他資源）。
func isTermIndex(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/term/" && !strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// prepareKeybarRequest 對 index 請求拿掉 Accept-Encoding：http.Transport 會自己要 gzip 並透明解壓，
// 回應到 ModifyResponse 時就是明文 HTML，可直接改寫；瀏覽器原本接受 gzip 的話改寫後再壓回去。
func prepareKeybarRequest(r *http.Request) *http.Request {
	gz := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
	r = r.WithContext(context.WithValue(r.Context(), keybarCtxKey{}, gz))
	r.Header.Del("Accept-Encoding")
	return r
}

// maxIndexBytes 是 ttyd index 的讀取上限（1.7.7 明文約 730KB）；超過視為異常。
const maxIndexBytes = 16 << 20

// rewriteKeybarResponse 是反代 ModifyResponse 的一段：只處理標記過的 index 回應。
func rewriteKeybarResponse(resp *http.Response) error {
	gz, marked := resp.Request.Context().Value(keybarCtxKey{}).(bool)
	if !marked || resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return nil
	}
	if resp.Header.Get("Content-Encoding") != "" {
		// Transport 沒有幫忙解壓（不該發生）就不動，避免把壓縮資料當 HTML 改壞。
		log.Printf("[term] ttyd index 帶 Content-Encoding=%q，略過按鍵列注入", resp.Header.Get("Content-Encoding"))
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes+1))
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	if len(body) > maxIndexBytes {
		return errors.New("ttyd index 超過讀取上限")
	}
	out, ok := injectKeybar(body)
	if !ok {
		log.Printf("[term] ttyd index 找不到 </head>，按鍵列未注入（ttyd 改版？）")
	}
	if gz {
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestSpeed)
		_, _ = zw.Write(out)
		_ = zw.Close()
		out = b.Bytes()
		resp.Header.Set("Content-Encoding", "gzip")
		resp.Header.Add("Vary", "Accept-Encoding")
	}
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return nil
}
