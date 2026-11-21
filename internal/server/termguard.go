package server

import (
	"context"
	"net/http"

	"github.com/chiyum/remote-console/internal/ttydwatch"
)

// TtydChecker 回報 ttyd 目前健康狀態（正式實作是 *ttydwatch.Watcher）。
type TtydChecker interface {
	Check(ctx context.Context) ttydwatch.Status
}

// handleTermHealth 給終端機頁（termguard.js）與總覽頁查 ttyd 狀態。每次都即時檢查（不讀快取），
// 終端機頁才能在 ttyd 停掉後幾秒內就顯示「終端機服務異常」，而不是等監看週期。
func (s *Server) handleTermHealth(w http.ResponseWriter, r *http.Request, _ Identity) {
	if s.Ttyd == nil {
		writeJSON(w, http.StatusOK, ttydwatch.Status{Reason: "未啟用 ttyd 監看"})
		return
	}
	writeJSON(w, http.StatusOK, s.Ttyd.Check(r.Context()))
}

// termDownPage 是 ttyd 連不上時 /term/ 頁面的替代內容：直接開終端機網址也看得到明確提示，
// 並由 termguard.js 退避查健康狀態，恢復後自動重新載入（不必手動重整）。
const termDownPage = `<!doctype html>
<html lang="zh-Hant" data-dc-term-down="1"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>dev-console</title>` + termGuardSnippet + `<link rel="stylesheet" href="/keybar.css"></head>
<body style="background:#0d1117;color:#e6edf3;font:16px -apple-system,BlinkMacSystemFont,sans-serif">
<noscript><p style="padding:2em">終端機服務異常：ttyd 目前連不上，請稍後重新整理。</p></noscript>
</body></html>
`

// termGuardSnippet 必須是同步 script、放在 ttyd 自己的 script 之前：它要先包住 WebSocket 才看得到 ttyd 的連線。
const termGuardSnippet = `<script src="/termguard.js"></script>`

// serveTermDown 回 503 替代頁。
func serveTermDown(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(termDownPage))
}
