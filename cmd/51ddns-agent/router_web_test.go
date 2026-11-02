package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterWebPublicReadiness(t *testing.T) {
	var invalid atomic.Bool
	w := &routerWeb{host: testRouterHost, state: "assigned", proxy: &httputil.ReverseProxy{}, gate: make(chan struct{}, 16)}
	s := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("probe leaked credentials")
		}
		if invalid.Load() {
			rw.WriteHeader(204)
			return
		}
		w.ServeHTTP(rw, r)
	}))
	defer s.Close()
	tr := s.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = "example.com"
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, s.Listener.Addr().String())
	}
	w.probeClient = &http.Client{Transport: tr, Timeout: time.Second}
	defer tr.CloseIdleConnections()
	w.checkPublic(context.Background())
	if w.status().State != "ready" {
		t.Fatal("valid HTTPS round-trip not ready")
	}
	w.mu.Lock()
	w.state = "assigned"
	w.mu.Unlock()
	invalid.Store(true)
	w.checkPublic(context.Background())
	if w.status().State != "assigned" {
		t.Fatal("missing nonce accepted")
	}
	w.probeClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: tr.DialContext}, Timeout: time.Second}
	w.checkPublic(context.Background())
	if w.status().State != "assigned" {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestRouterWebLocalLoginRedirect(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/luci/" {
			http.Redirect(w, r, "/cgi-bin/luci/admin/", 302)
			return
		}
		io.WriteString(w, "LuCI login")
	}))
	defer s.Close()
	w := &routerWeb{config: routerWebConfig{Target: s.URL}, host: testRouterHost}
	p, err := w.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.Transport.(*http.Transport).CloseIdleConnections()
}

const testRouterHost = "router-12345678901234567890123456789012.cdn.cloud.toddns.com"

func TestRouterWebLocalTargetValidation(t *testing.T) {
	local := []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.5.1"), Mask: net.CIDRMask(24, 32)}}
	for _, raw := range []string{"http://127.0.0.1:8080", "https://192.168.5.1:8443", "http://[::1]:80"} {
		if _, err := localRouterTarget(raw, local); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"http://192.168.5.2", "http://example.com", "http://169.254.169.254", "http://0.0.0.0", "http://127.0.0.1:17851", "http://user:pass@127.0.0.1", "http://127.0.0.1/admin", "http://127.0.0.1?x=1", "file:///etc/passwd", "http://127.0.0.1:99999"} {
		if _, err := localRouterTarget(raw, local); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
func TestRouterWebDiscoveryHTTPSPinAndNoRedirectFollow(t *testing.T) {
	router := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>LuCI login</html>") }))
	defer router.Close()
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: router.Certificate().Raw}))
	w := &routerWeb{config: routerWebConfig{Target: router.URL, Certificate: cert}, host: testRouterHost}
	if _, err := w.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	wrong := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer wrong.Close()
	// httptest servers share their default certificate; use malformed pin instead.
	w.config.Certificate = ""
	if _, err := w.discover(context.Background()); err == nil {
		t.Fatal("self-signed certificate accepted without pin")
	}
	var hits atomic.Int32
	forbidden := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer forbidden.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { http.Redirect(rw, r, forbidden.URL, 302) }))
	defer redirect.Close()
	w.config = routerWebConfig{Target: redirect.URL}
	if _, err := w.discover(context.Background()); err == nil || hits.Load() != 0 {
		t.Fatal("followed redirect or identified a redirect as LuCI")
	}
	generic := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { io.WriteString(rw, "some other service") }))
	defer generic.Close()
	w.config.Target = generic.URL
	if _, err := w.discover(context.Background()); err == nil {
		t.Fatal("arbitrary web service accepted")
	}
}

func TestRouterWebProtectedProxyFullHTTPFlow(t *testing.T) {
	var hits atomic.Int32
	var revoked atomic.Bool
	session := strings.Repeat("s", 43)
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" && strings.Contains(r.Header.Get("Cookie"), routerCookie) {
			t.Error("access cookie leaked to router")
		}
		if strings.Contains(r.Header.Get("Authorization"), "synthetic-agent") {
			t.Error("agent token leaked")
		}
		hits.Add(1)
		if r.Method == "POST" && r.Header.Get("Origin") != "http://"+r.Host {
			t.Error("router origin not rewritten")
		}
		w.Header().Add("Set-Cookie", "sysauth=router-session; Path=/")
		w.Header().Add("Set-Cookie", routerCookie+"=evil; Path=/")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://"+r.Host+"/cgi-bin/luci/", 302)
			return
		}
		io.WriteString(w, "<html>LuCI login</html>")
	}))
	defer router.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-agent" || r.Header.Get("X-51DDNS-Device-ID") != "synthetic-device" {
			t.Error("agent authentication missing")
			w.WriteHeader(401)
			return
		}
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["host"] != testRouterHost {
			t.Error("wrong host")
		}
		if revoked.Load() {
			w.WriteHeader(503)
			return
		}
		if in["code"] == strings.Repeat("c", 43) {
			json.NewEncoder(w).Encode(map[string]any{"session": session})
			return
		}
		if in["session"] == session {
			json.NewEncoder(w).Encode(map[string]any{"allowed": true, "expires": time.Now().Add(time.Minute).Unix()})
			return
		}
		w.WriteHeader(401)
	}))
	defer api.Close()
	w := &routerWeb{config: routerWebConfig{Target: router.URL}, api: api.URL, token: "synthetic-agent", device: "synthetic-device", host: testRouterHost, client: api.Client(), gate: make(chan struct{}, 16)}
	var err error
	w.proxy, err = w.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hits.Store(0)
	call := func(method, path, body, cookie, origin, site string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://"+testRouterHost+path, strings.NewReader(body))
		r.URL.Scheme, r.URL.Host = "", "" // Match origin-form requests received by net/http.
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", site)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: routerCookie, Value: cookie})
			r.AddCookie(&http.Cookie{Name: "sysauth", Value: "router-session"})
		}
		rw := httptest.NewRecorder()
		w.ServeHTTP(rw, r)
		return rw
	}
	if r := call("GET", "/", "", "", "", "cross-site"); r.Code != 303 || !strings.HasPrefix(r.Header().Get("Location"), routerConsole+"/router-access?") {
		t.Fatal("login redirect", r.Code)
	}
	if hits.Load() != 0 {
		t.Fatal("unauthenticated upstream request")
	}
	if r := call("POST", "/_51ddns/callback", "code="+strings.Repeat("c", 43), "", "https://evil.example", ""); r.Code != 403 {
		t.Fatal("callback origin")
	}
	r := call("POST", "/_51ddns/callback", url.Values{"code": {strings.Repeat("c", 43)}}.Encode(), "", routerConsole, "")
	if r.Header().Get("Referrer-Policy") != "strict-origin" {
		t.Fatal("callback must preserve browser Origin")
	}
	cookies := r.Result().Cookies()
	if r.Code != 200 || len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("secure session cookie", r.Code, cookies)
	}
	r = call("GET", "/cgi-bin/luci/", "", session, "", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "LuCI") {
		t.Fatal("router page", r.Code)
	}
	if r.Header().Get("Referrer-Policy") != "strict-origin" {
		t.Fatal("router login page must preserve browser Origin")
	}
	if len(r.Result().Cookies()) != 1 || r.Result().Cookies()[0].Name != "sysauth" {
		t.Fatal("upstream replaced guard cookie")
	}
	if r = call("POST", "/cgi-bin/luci/", "password=synthetic-router", session, "https://"+testRouterHost, ""); r.Code != 200 {
		t.Fatal("router login POST", r.Code)
	}
	if r = call("POST", "/cgi-bin/luci/", "", session, "null", "same-origin"); r.Code != 403 {
		t.Fatal("opaque Origin must remain rejected")
	}
	before := hits.Load()
	if r = call("POST", "/cgi-bin/luci/", "", session, "https://evil.example", ""); r.Code != 403 || hits.Load() != before {
		t.Fatal("CSRF reached upstream")
	}
	r = call("GET", "/redirect", "", session, "", "")
	if r.Code != 302 || r.Header().Get("Location") != "https://"+testRouterHost+"/cgi-bin/luci/" {
		t.Fatal("LAN redirect leaked", r.Header())
	}
	revoked.Store(true)
	before = hits.Load()
	r = call("GET", "/cgi-bin/luci/", "", session, "", "")
	if r.Code != 503 || hits.Load() != before {
		t.Fatal("failed-open during API outage")
	}
	w.mu.Lock()
	w.proxy = nil
	w.mu.Unlock()
	if r = call("GET", "/", "", session, "", ""); r.Code != 503 {
		t.Fatal("disabled guard accepted")
	}
}
