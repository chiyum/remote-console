package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeSessions 只認一個固定 token。
type fakeSessions struct{ good string }

func (f fakeSessions) Valid(t string) bool { return t != "" && t == f.good }

const goodToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAuthorizerRequiresCookie(t *testing.T) {
	a := Authorizer{PublicHost: "mac.ts.net", LocalHost: "dev-console.localhost", ListenAddr: "127.0.0.1:7690", AllowedLogins: []string{"owner@x.com"}, Sessions: fakeSessions{goodToken}}
	cases := []struct {
		name   string
		remote string
		host   string
		login  string
		xff    string
		cookie string
		ok     bool
	}{
		// 這幾條是 Docker 容器經 host.docker.internal 能偽造的組合：沒有 cookie 一律拒絕。
		{"偽造本機直連 Host", "127.0.0.1:5000", "127.0.0.1:7690", "", "", "", false},
		{"偽造擁有者 login", "127.0.0.1:5000", "mac.ts.net", "owner@x.com", "100.2.2.2", "", false},
		{"偽造本機 tailnet IP", "127.0.0.1:5000", "mac.ts.net", "", "100.64.0.10", "", false},
		{"錯誤 cookie", "127.0.0.1:5000", "127.0.0.1:7690", "", "", "bad", false},
		// 有效 cookie。
		{"本機已配對", "127.0.0.1:5000", "dev-console.localhost:7690", "", "", goodToken, true},
		{"127.0.0.1 Host 即使有 cookie 也拒（cookie 不得綁 127.0.0.1）", "127.0.0.1:5000", "127.0.0.1:7690", "", "", goodToken, false},
		{"本機名稱錯 port", "127.0.0.1:5000", "dev-console.localhost:8080", "", "", goodToken, false},
		{"擁有者已配對", "127.0.0.1:5000", "mac.ts.net", "owner@x.com", "100.2.2.2", goodToken, true},
		{"tag 裝置已配對", "127.0.0.1:5000", "mac.ts.net", "", "100.64.0.10", goodToken, true},
		// header 仍可用來拒絕。
		{"其他帳號即使有 cookie", "127.0.0.1:5000", "mac.ts.net", "other@x.com", "100.2.2.2", goodToken, false},
		{"DNS rebinding", "127.0.0.1:5000", "evil.com:7690", "", "", goodToken, false},
		{"非 loopback", "192.168.1.5:5000", "dev-console.localhost:7690", "", "", goodToken, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr, r.Host = c.remote, c.host
		if c.login != "" {
			r.Header.Set("Tailscale-User-Login", c.login)
		}
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.cookie != "" {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: c.cookie})
		}
		if _, ok, why := a.Check(r); ok != c.ok {
			t.Errorf("%s: ok=%v want %v (%s)", c.name, ok, c.ok, why)
		}
	}
}

func TestOriginOKAlwaysRequired(t *testing.T) {
	a := Authorizer{PublicHost: "mac.ts.net", LocalHost: "dev-console.localhost", ListenAddr: "127.0.0.1:7690"}
	r := httptest.NewRequest("POST", "/", nil)
	if a.OriginOK(r) {
		t.Fatal("沒帶 Origin 應拒絕（本機直連也不豁免）")
	}
	r.Header.Set("Origin", "https://evil.com")
	if a.OriginOK(r) {
		t.Fatal("錯誤 Origin 應拒絕")
	}
	r.Header.Set("Origin", "http://127.0.0.1:7690")
	if a.OriginOK(r) {
		t.Fatal("127.0.0.1 Origin 不再接受")
	}
	for _, o := range []string{"https://mac.ts.net", "http://dev-console.localhost:7690"} {
		r.Header.Set("Origin", o)
		if !a.OriginOK(r) {
			t.Fatalf("%s 應允許", o)
		}
	}
}

// DC_ALLOWED_LOGINS 沒設定（空名單）：任何帶 Tailscale 登入身分的請求都拒絕，即使 cookie 有效——不預設放行任何帳號。
func TestEmptyAllowedLoginsRejectsAnyTailscaleLogin(t *testing.T) {
	a := Authorizer{PublicHost: "mac.ts.net", LocalHost: "dev-console.localhost", ListenAddr: "127.0.0.1:7690", Sessions: fakeSessions{goodToken}}
	for _, login := range []string{"owner@x.com", "anyone@example.com"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr, r.Host = "127.0.0.1:5000", "mac.ts.net"
		r.Header.Set("Tailscale-User-Login", login)
		r.Header.Set("X-Forwarded-For", "100.2.2.2")
		r.AddCookie(&http.Cookie{Name: CookieName, Value: goodToken})
		if _, ok, _ := a.Check(r); ok {
			t.Errorf("空名單時 %s 不應被放行", login)
		}
	}
}
