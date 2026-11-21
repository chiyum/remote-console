package server

import (
	"net"
	"net/http"
	"strings"
)

// CookieName 是配對後的 session cookie（HttpOnly、SameSite=Strict）。
const CookieName = "dc_session"

// Identity 是通過身分檢查後的來源描述。
type Identity struct {
	// Login 是 tailscale serve 帶的帳號（經 serve 時）或 "paired-device"（本機直連時）。
	Login string `json:"login"`
	// Via 是 "serve"（Host 為 tailnet 名稱）或 "local"（Host 為本機專用名稱 dev-console.localhost）。
	Via string `json:"via"`
}

// SessionChecker 驗證 cookie token（由 authstore 實作，測試可注入）。
type SessionChecker interface {
	Valid(token string) bool
}

// Authorizer 決定一個 UI 請求能否進來。
//
// 前提（2026-10-23 實測）：App 版 Tailscale serve 只能反代 TCP 127.0.0.1，而本機任何程序（含 Docker 容器經
// host.docker.internal）都能直連同一個 port 並偽造 Host／X-Forwarded-For／Tailscale-User-Login。
// 所以「放行」只認 cookie（只有讀得到 0600 配對碼檔的人換得到）；header 只用來「拒絕」：
//   - Host 必須是 tailnet 名稱或本機專用名稱 dev-console.localhost:port（擋 DNS rebinding）。
//     127.0.0.1／localhost 不再接受：cookie 不分 port，綁在 127.0.0.1 會被瀏覽器送給同機其他服務。
//   - 帶了 Tailscale-User-Login 卻不在白名單 → 拒絕（serve 會覆寫此 header，非擁有者的 tailnet 裝置進不來）。
type Authorizer struct {
	PublicHost    string
	LocalHost     string
	ListenAddr    string
	AllowedLogins []string
	Sessions      SessionChecker
}

// HostOK 檢查 Host，回傳來源類型。
func (a Authorizer) HostOK(r *http.Request) (via string, ok bool) {
	_, port, _ := net.SplitHostPort(a.ListenAddr)
	switch {
	case a.PublicHost != "" && strings.EqualFold(stripPort(r.Host), a.PublicHost):
		return "serve", true
	case a.LocalHost != "" && strings.EqualFold(r.Host, a.LocalHost+":"+port):
		return "local", true
	}
	return "", false
}

// Check 回傳身分與是否放行；拒絕原因只寫 log。
func (a Authorizer) Check(r *http.Request) (Identity, bool, string) {
	if !isLoopback(r.RemoteAddr) {
		return Identity{}, false, "非 loopback 來源"
	}
	via, ok := a.HostOK(r)
	if !ok {
		return Identity{}, false, "Host 不符：" + r.Host
	}
	login := strings.TrimSpace(r.Header.Get("Tailscale-User-Login"))
	if login != "" && !a.loginAllowed(login) {
		return Identity{}, false, "帳號不在白名單：" + login
	}
	c, err := r.Cookie(CookieName)
	if err != nil || a.Sessions == nil || !a.Sessions.Valid(c.Value) {
		return Identity{}, false, "未配對（缺有效 cookie）"
	}
	if login == "" {
		login = "paired-device"
	}
	return Identity{Login: login, Via: via}, true, ""
}

func (a Authorizer) loginAllowed(login string) bool {
	for _, l := range a.AllowedLogins {
		if strings.EqualFold(l, login) {
			return true
		}
	}
	return false
}

// AllowedOrigins 是允許發起寫入／WebSocket 的 Origin。
func (a Authorizer) AllowedOrigins() []string {
	_, port, _ := net.SplitHostPort(a.ListenAddr)
	var o []string
	if a.LocalHost != "" {
		o = append(o, "http://"+a.LocalHost+":"+port)
	}
	if a.PublicHost != "" {
		o = append(o, "https://"+a.PublicHost)
	}
	return o
}

// OriginOK：寫入與 WebSocket 一律必須帶相符的 Origin（不再對本機直連豁免）。
// 瀏覽器一定會送；沒送的請求視為非瀏覽器程序，直接拒絕。
func (a Authorizer) OriginOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	for _, o := range a.AllowedOrigins() {
		if origin == o {
			return true
		}
	}
	return false
}

func isLoopback(remote string) bool {
	h, _, err := net.SplitHostPort(remote)
	if err != nil {
		h = remote
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func stripPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
