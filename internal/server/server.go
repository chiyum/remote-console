// Package server 提供兩個 HTTP listener：
//   - UI（ListenAddr）：總覽頁、唯讀 API、SSE、新開 session、/term/ 反代 ttyd；由 tailscale serve 對 tailnet 公開。
//   - Hook（HookAddr）：只收 Claude hooks 回報；serve 不反代這個 port，tailnet 打不到。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/chiyum/remote-console/internal/authstore"
	"github.com/chiyum/remote-console/internal/config"
	"github.com/chiyum/remote-console/internal/hookstore"
	"github.com/chiyum/remote-console/internal/hub"
	"github.com/chiyum/remote-console/internal/logthrottle"
	"github.com/chiyum/remote-console/internal/sessionmeta"
	"github.com/chiyum/remote-console/internal/snapshot"
	"github.com/chiyum/remote-console/internal/tmux"
	"github.com/chiyum/remote-console/internal/ttydwatch"
	"github.com/chiyum/remote-console/internal/validate"
)

// Server 聚合所有依賴（由 main 注入）。
type Server struct {
	Cfg       config.Config
	Auth      Authorizer
	Hub       *hub.Hub
	Collector *snapshot.Collector
	Hooks     *hookstore.Store
	Launcher  *Launcher
	Tmux      tmux.Runner
	Static    fs.FS
	// Auth store：配對碼兌換與 hook token 驗證（檔案秘密，見 authstore 套件說明）。
	Store *authstore.Store
	// Meta 是暱稱與封存紀錄（0600 檔，見 sessionmeta 套件說明）。
	Meta *sessionmeta.Store
	// TtydGate 追蹤經反代的 ttyd WebSocket 數，ttyd 自動重啟期間擋新連線（ADR 0006）；nil＝不追蹤。
	TtydGate *ttydwatch.Gate
	// Ttyd 提供 ttyd 健康狀態；nil＝健康 API 回「未啟用」。
	Ttyd TtydChecker
}

// securityHeaders 對所有 UI 回應加上防嵌入、CSP 與最小權限 header。
// 總覽頁只有外部 JS/CSS，可用嚴格 CSP；ttyd 的頁面是內嵌 script/style 的單檔，/term/ 只能放寬 inline。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.Cfg.ListenAddr)
	// 'self' 對 ws/wss 的涵蓋在舊版 Safari 不一致，明列同源 WebSocket。
	connect := "'self' ws://" + s.Cfg.LocalHost + ":" + port
	if s.Cfg.PublicHost != "" {
		connect += " wss://" + s.Cfg.PublicHost
	}
	base := "default-src 'none'; img-src 'self' data:; font-src 'self' data:; connect-src " + connect +
		"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	strict := base + "; script-src 'self'; style-src 'self'"
	term := base + "; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if strings.HasPrefix(r.URL.Path, "/term/") {
			h.Set("Content-Security-Policy", term)
		} else {
			h.Set("Content-Security-Policy", strict)
		}
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// requireAuth 包住需要身分的路由。
func (s *Server) requireAuth(next func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok, why := s.Auth.Check(r)
		if !ok {
			log.Printf("[auth] 拒絕 %s %s：%s", r.Method, r.URL.Path, why)
			http.Error(w, "forbidden: 此裝置尚未配對（在 Mac 執行 dev-console pair）", http.StatusForbidden)
			return
		}
		next(w, r, id)
	}
}

// UIHandler 組 UI 路由。
func (s *Server) UIHandler() http.Handler {
	mux := http.NewServeMux()
	// 靜態頁本身不含任何資料，公開即可（本機掛 tag 時 curl 健檢也能 200）；資料全在需身分的 API。
	// 用不帶 method 的 "/"：若寫 "GET /" 會與不帶 method 的 "/term/" 互相衝突（Go 1.22 mux 註冊時 panic）。
	static := http.FileServerFS(s.Static)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		static.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.HandleFunc("POST /auth/pair", s.handlePair)
	// 首頁 <link rel="icon"> 指向 favicon.svg；仍有瀏覽器會盲抓 /favicon.ico，回 204 免得 console 噴 404。
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/whoami", s.requireAuth(func(w http.ResponseWriter, r *http.Request, id Identity) {
		writeJSON(w, http.StatusOK, id)
	}))
	mux.HandleFunc("GET /api/snapshot", s.requireAuth(func(w http.ResponseWriter, r *http.Request, _ Identity) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.Collector.Latest(r.Context()))
	}))
	mux.HandleFunc("GET /api/events", s.requireAuth(s.handleEvents))
	mux.HandleFunc("GET /api/profiles", s.requireAuth(func(w http.ResponseWriter, r *http.Request, _ Identity) {
		writeJSON(w, http.StatusOK, profilesResponse(s.Cfg))
	}))
	mux.HandleFunc("POST /api/sessions", s.requireAuth(s.handleLaunch))
	mux.HandleFunc("POST /api/sessions/nickname", s.requireAuth(s.handleNickname))
	mux.HandleFunc("POST /api/sessions/archive", s.requireAuth(s.handleArchive))
	mux.HandleFunc("POST /api/sessions/unarchive", s.requireAuth(s.handleUnarchive))
	mux.HandleFunc("POST /api/sessions/keys", s.requireAuth(s.handleKeys))
	mux.HandleFunc("POST /api/sessions/scroll", s.requireAuth(s.handleScroll))
	mux.HandleFunc("GET /api/term/health", s.requireAuth(s.handleTermHealth))
	mux.Handle("/term/", s.requireAuth(s.handleTerm()))
	return s.securityHeaders(mux)
}

// handleEvents 是 SSE：連上先收最後一份快照，之後內容變動即推；每 20 秒送註解保活。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, _ Identity) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := s.Hub.Subscribe()
	defer cancel()
	// 先送一份現況，避免 Hub 尚無快照時前端空等。
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", s.Collector.Latest(r.Context()))
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", msg)
			fl.Flush()
		case <-ping.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// handleLaunch 建立新 session。防 CSRF：必須 JSON、必須帶自訂 header（跨站會觸發 preflight 而失敗）、Origin 必須相符。
func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request, id Identity) {
	if !s.csrfOK(w, r) {
		return
	}
	var req LaunchRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	res, err := s.Launcher.Launch(r.Context(), req)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, validate.ErrInvalid) {
			code = http.StatusBadRequest
		}
		log.Printf("[launch] %s 拒絕/失敗 %+v：%v", id.Login, req, err)
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("[launch] %s 建立 %s（%s %s @ %s）", id.Login, res.Name, req.Tool, req.Profile, res.Dir)
	s.Collector.Trigger()
	writeJSON(w, http.StatusCreated, res)
}

// handleTerm 反代到 ttyd 的 unix socket。arg（session 名）在這裡先驗一次：格式合法且 session 存在；
// ttyd 的 attach wrapper 會再驗一次（雙層），WebSocket 另檢查 Origin 防跨站劫持。
func (s *Server) handleTerm() func(http.ResponseWriter, *http.Request, Identity) {
	target, _ := url.Parse("http://ttyd")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", s.Cfg.TtydSocket)
		},
	}
	proxy.FlushInterval = -1
	// 不對外透露 ttyd 版本；終端機頁本身注入按鍵列（ADR 0005）。
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("Server")
		return rewriteKeybarResponse(resp)
	}
	// ttyd 掛掉時前端可能密集重試，同類失敗 30 秒只記一次（附略過次數）。
	failLog := logthrottle.New(30 * time.Second)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		failLog.Printf("ttyd-dial", "[term] ttyd 連線失敗：%v", err)
		if isTermIndex(r) {
			// 頁面本身也連不上 ttyd：回替代頁顯示「終端機服務異常」，恢復後自動重新載入。
			serveTermDown(w)
			return
		}
		http.Error(w, "終端機服務（ttyd）未啟動", http.StatusBadGateway)
	}
	return func(w http.ResponseWriter, r *http.Request, _ Identity) {
		isWS := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		if isWS && !s.Auth.OriginOK(r) {
			log.Printf("[term] 拒絕跨站 WebSocket，Origin=%q", r.Header.Get("Origin"))
			http.Error(w, "bad origin", http.StatusForbidden)
			return
		}
		// 頁面與 WebSocket 都必須帶 arg；靜態資源（/term/token 等）不帶 arg 也放行。
		path := strings.TrimPrefix(r.URL.Path, "/term")
		if path == "/" || path == "/ws" || r.URL.Query().Has("arg") {
			args := r.URL.Query()["arg"]
			if len(args) != 1 || !validate.SessionName(args[0]) || !tmux.HasSession(r.Context(), s.Tmux, args[0]) {
				http.Error(w, "session 不存在或名稱不合法", http.StatusBadRequest)
				return
			}
		}
		// 不把 tailscale 身分 header 帶進 ttyd（它不需要）。
		r.Header.Del("Tailscale-User-Login")
		r.Header.Del("Tailscale-User-Name")
		r.Header.Del("Tailscale-User-Profile-Pic")
		if isTermIndex(r) {
			r = prepareKeybarRequest(r)
		}
		// 計入活著的 WebSocket：反代升級後 ServeHTTP 會一直阻塞到連線結束，Release 時就是真的斷了。
		// ttyd 只聽 0700 目錄裡的 unix socket，所以這個計數就是 ttyd 的全部連線（自動重啟只在 0 時發生）。
		if isWS && s.TtydGate != nil {
			if !s.TtydGate.Acquire() {
				http.Error(w, "終端機服務重啟中", http.StatusServiceUnavailable)
				return
			}
			defer s.TtydGate.Release()
		}
		proxy.ServeHTTP(w, r)
	}
}

// HookHandler 是 hook 接收端：只接受 loopback 直連（HookAddr 本身只綁 127.0.0.1，serve 不反代它）。
func (s *Server) HookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/hook", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r.RemoteAddr) || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Tailscale-User-Login") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, port, _ := net.SplitHostPort(s.Cfg.HookAddr)
		if r.Host != "127.0.0.1:"+port && r.Host != "localhost:"+port {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// loopback 不代表可信（容器也連得到）；必須帶 0600 hook-token 檔的內容。
		if s.Store == nil || !s.Store.HookTokenOK(r.Header.Get("X-DC-Hook-Token")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var ev hookstore.Event
		// PreToolUse 的 tool_input 可能很大（Write 整個檔案），只讀前 1MB；解析失敗就略過。
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&ev); err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		ev.Profile = r.URL.Query().Get("profile")
		if !validate.ProfileName(ev.Profile) {
			ev.Profile = ""
		}
		if p := r.Header.Get("X-Tmux-Pane"); validate.PaneID(p) {
			ev.Pane = p
		}
		if s.Hooks.Apply(ev) {
			if ev.HookEventName == "Stop" {
				s.Collector.TriggerAfterStop(ev.TranscriptPath)
			} else {
				s.Collector.Trigger()
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// handlePair 以一次性配對碼換 session cookie（POST，必須帶相符 Origin）。配對碼只存在 0600 檔
// （dev-console pair 產生），容器或其他網頁拿不到；錯誤 5 次該碼作廢。
// 用 POST＋Origin 而非 GET：避免配對碼留在網址／瀏覽紀錄，也擋掉跨站把別人的碼塞進你瀏覽器（login CSRF）。
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	via, ok := s.Auth.HostOK(r)
	if !ok || !isLoopback(r.RemoteAddr) {
		http.Error(w, "forbidden: 本機請改用 http://"+s.Cfg.LocalHost+":7690/", http.StatusForbidden)
		return
	}
	if !s.Auth.OriginOK(r) {
		http.Error(w, "forbidden: Origin 不符", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if login := strings.TrimSpace(r.Header.Get("Tailscale-User-Login")); login != "" && !s.Auth.loginAllowed(login) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.Store == nil {
		http.Error(w, "auth store 未啟用", http.StatusInternalServerError)
		return
	}
	maxAge := authstore.SessionMaxAge
	if via == "local" {
		maxAge = authstore.LocalSessionMaxAge
	}
	tok, err := s.Store.Redeem(r.PostForm.Get("code"), r.UserAgent(), maxAge)
	if err != nil {
		log.Printf("[pair] 失敗（via %s）：%v", via, err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: via == "serve",
		MaxAge: int(maxAge.Seconds()),
	})
	log.Printf("[pair] 已配對新裝置（via %s，UA %q）", via, r.UserAgent())
	w.WriteHeader(http.StatusNoContent)
}

// ProfilesResponse 是 /api/profiles 的回應：Mode 決定前端要不要顯示 profile 選單。
//   - "aiuse"：設定的 aiuse 存在且可執行，列出 ~/.ai-profiles 底下的 profile。
//   - "direct"：沒有 aiuse，網頁直接開 claude／codex，不使用 profile（兩個清單一律空）。
type ProfilesResponse struct {
	Mode   string   `json:"mode"`
	Claude []string `json:"claude"`
	Codex  []string `json:"codex"`
}

func profilesResponse(cfg config.Config) ProfilesResponse {
	if !cfg.AiuseAvailable() {
		return ProfilesResponse{Mode: "direct", Claude: []string{}, Codex: []string{}}
	}
	orEmpty := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	return ProfilesResponse{
		Mode:   "aiuse",
		Claude: orEmpty(validate.ListProfiles(cfg.ProfilesRoot, "claude")),
		Codex:  orEmpty(validate.ListProfiles(cfg.ProfilesRoot, "codex")),
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
