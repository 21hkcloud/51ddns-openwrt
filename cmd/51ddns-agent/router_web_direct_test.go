package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectRouterWebPreservesNativeLoginAndRequestBoundaries(t *testing.T) {
	const luci = "/cgi-bin/luci/"
	const nativeCookie = "sysauth_https"
	var upstreamCalls atomic.Int32
	proof := filepath.Join(t.TempDir(), "ready")
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if _, err := r.Cookie(routerCookie); err == nil || r.Header.Get("Authorization") != "" || r.Header.Get("Auth-User") != "" ||
			r.Header.Get("Auth-Pass") != "" || r.Header.Get("Http-Auth-User") != "" || r.Header.Get("Http-Auth-Pass") != "" {
			t.Error("non-LuCI credentials reached the router")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == luci:
			if r.Header.Get("Origin") != "http://"+r.Host {
				t.Error("native Origin was not translated")
			}
			password := r.FormValue("luci_password")
			if r.FormValue("luci_username") == "root" && (password == "router-fixture" || password == "unsafe-fixture") {
				if password == "unsafe-fixture" {
					if err := os.Remove(proof); err != nil {
						t.Error(err)
					}
				} else {
					time.AfterFunc(80*time.Millisecond, func() {
						now := time.Now()
						if err := os.Chtimes(proof, now, now); err != nil {
							t.Error(err)
						}
					})
				}
				http.SetCookie(w, &http.Cookie{Name: nativeCookie, Value: "native-fixture", HttpOnly: true, Path: luci})
				http.Redirect(w, r, "http://"+r.Host+luci, http.StatusFound)
				return
			}
		case strings.HasPrefix(r.URL.Path, "/luci-static/"):
			_, _ = io.WriteString(w, "asset")
			return
		}
		if c, err := r.Cookie(nativeCookie); err == nil && c.Value == "native-fixture" {
			_, _ = io.WriteString(w, "native administration")
			return
		}
		w.Header().Set("X-LuCI-Login-Required", "yes")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "LuCI native login")
	}))
	defer router.Close()
	var apiCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()
	if err := os.WriteFile(proof, nil, 0644); err != nil {
		t.Fatal(err)
	}
	w := &routerWeb{config: routerWebConfig{Target: router.URL, DirectHostname: testRouterHost},
		host: testRouterHost, api: api.URL, client: api.Client(), credentialProof: proof, gate: make(chan struct{}, 16)}
	var err error
	w.proxy, err = w.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	upstreamCalls.Store(0)
	call := func(method, target, site, origin, body string, navigation, withCookie, https bool) *httptest.ResponseRecorder {
		scheme := "https"
		if !https {
			scheme = "http"
		}
		r := httptest.NewRequest(method, scheme+"://"+w.host+target, strings.NewReader(body))
		r.URL.Scheme, r.URL.Host = "", ""
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if strings.Contains(target, "/admin/ubus") {
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
		r.Header.Set("Sec-Fetch-Site", site)
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Basic dummy")
		r.Header.Set("Auth-User", "root")
		r.Header.Set("Auth-Pass", "dummy")
		r.Header.Set("Http-Auth-User", "root")
		r.Header.Set("Http-Auth-Pass", "dummy")
		r.AddCookie(&http.Cookie{Name: routerCookie, Value: "cloud-fixture"})
		if withCookie {
			r.AddCookie(&http.Cookie{Name: nativeCookie, Value: "native-fixture"})
		}
		if navigation {
			r.Header.Set("Sec-Fetch-Mode", "navigate")
			r.Header.Set("Sec-Fetch-Dest", "document")
		}
		r.Header.Set("X-51DDNS-Probe", strings.Repeat("a", 32))
		rr := httptest.NewRecorder()
		w.ServeHTTP(rr, r)
		return rr
	}
	origin := "https://" + testRouterHost
	root := call("GET", "/", "cross-site", "", "", true, true, true)
	if root.Code != 302 || root.Header().Get("Location") != luci {
		t.Fatalf("root landing: %d %s", root.Code, root.Header().Get("Location"))
	}
	login := call("GET", luci, "cross-site", "", "", true, true, true)
	if login.Code != 403 || login.Header().Get("X-LuCI-Login-Required") != "yes" || !strings.Contains(login.Body.String(), "LuCI native login") {
		t.Fatalf("native login after LAN click: %d", login.Code)
	}
	wrong := call("POST", luci, "same-origin", origin, "luci_username=root&luci_password=wrong", false, false, true)
	if wrong.Code != 403 || len(wrong.Result().Cookies()) != 0 {
		t.Fatalf("wrong password: %d", wrong.Code)
	}
	correct := call("POST", luci, "same-origin", origin, "luci_username=root&luci_password=router-fixture", false, false, true)
	cookies := correct.Result().Cookies()
	if correct.Code != 302 || correct.Header().Get("Location") != origin+luci || len(cookies) != 1 ||
		cookies[0].Name != nativeCookie || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Domain != "" {
		t.Fatalf("native password login: %d", correct.Code)
	}
	const rpc = luci + "admin/ubus"
	const allowedRPC = `{"jsonrpc":"2.0","method":"call","id":1,"params":["fixture","system","board",{}]}`
	if got := call("POST", rpc, "same-origin", origin, allowedRPC, false, true, true); got.Code != 200 {
		t.Fatalf("native RPC fallback: %d", got.Code)
	}
	if got := call("GET", "/luci-static/resources/luci.js", "same-origin", "", "", false, false, true); got.Code != 200 {
		t.Fatalf("native login assets: %d", got.Code)
	}
	unsafe := call("POST", luci, "same-origin", origin, "luci_username=root&luci_password=unsafe-fixture", false, false, true)
	if unsafe.Code != 502 || len(unsafe.Result().Cookies()) != 0 || unsafe.Header().Get("Location") != "" {
		t.Fatalf("unconfirmed native session escaped: %d", unsafe.Code)
	}
	if err = os.WriteFile(proof, nil, 0644); err != nil {
		t.Fatal(err)
	}
	allowedCalls := upstreamCalls.Load()
	for _, attack := range []struct{ path, body string }{
		{rpc, `{"method":"call","params":["00000000000000000000000000000000","session","login",{}]}`},
		{rpc + "/session.login", `[` + allowedRPC + `,{"method":"call","params":["0","\u0073ession","\u006cogin",{}]}]`},
		{rpc, `{"method":"call","Method":"list","params":["0","session","login",{}]}`},
		{rpc, `{"method":"list","method":"call","params":["0","session","login",{}]}`},
		{rpc, `{"method":"call","params":["0","session\u0000other","login\u0000other",{}]}`},
		{rpc, `{"method":"list","method\u0000x":"call","params":["0","session","login",{}]}`},
		{rpc, `{"method":"list"}{"method":"call","params":["0","session","login",{}]}`},
	} {
		if got := call("POST", attack.path, "same-origin", origin, attack.body, false, true, true); got.Code != 403 {
			t.Errorf("RPC login attack returned %d", got.Code)
		}
	}
	for _, row := range []struct {
		name, method, path, site, origin string
		navigation, https                bool
		want                             int
	}{
		{"raw ubus", "POST", "/ubus/", "same-origin", origin, false, true, 404},
		{"encoded traversal", "POST", luci + "%2e%2e/%2e%2e/ubus", "same-origin", origin, false, true, 404},
		{"double-encoded traversal", "POST", luci + "%252e%252e", "same-origin", origin, false, true, 404},
		{"foreign mutation", "POST", luci, "cross-site", "https://evil.example", false, true, 403},
		{"missing origin mutation", "POST", luci, "", "", false, true, 403},
		{"cross-site embed", "GET", luci, "cross-site", "", false, true, 403},
		{"cross-origin sibling", "GET", luci + "admin/status", "same-site", "", true, true, 403},
		{"credential in URL", "GET", luci + "?luci%5Fpassword=test", "", "", false, true, 400},
		{"old grant callback", "POST", "/_51ddns/callback", "cross-site", routerConsole, false, true, 403},
		{"HTTP redirect", "GET", luci, "", "", false, false, 307},
		{"health nonce", "GET", "/_51ddns/health", "", "", false, true, 204},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := call(row.method, row.path, row.site, row.origin, "", row.navigation, true, row.https)
			if got.Code != row.want {
				t.Errorf("got %d, want %d", got.Code, row.want)
			}
		})
	}
	if upstreamCalls.Load() != allowedCalls || apiCalls.Load() != 0 {
		t.Fatalf("unexpected upstream calls: LuCI %d, account API %d", upstreamCalls.Load()-allowedCalls, apiCalls.Load())
	}
	expired := time.Now().Add(-time.Minute)
	if err = os.Chtimes(proof, expired, expired); err != nil {
		t.Fatal(err)
	}
	if got := call("GET", luci, "same-origin", "", "", false, false, true); got.Code != 303 ||
		!strings.HasPrefix(got.Header().Get("Location"), routerConsole+"/router-access?host="+url.QueryEscape(testRouterHost)) {
		t.Fatalf("expired password check did not restore the owner gate: %d", got.Code)
	}
	now := time.Now()
	if err = os.Chtimes(proof, now, now); err != nil {
		t.Fatal(err)
	}
	w.config.DirectHostname = "router-00000000000000000000000000000000.cdn.cloud.toddns.com"
	if got := call("GET", luci, "same-origin", "", "", false, false, true); got.Code != 303 || upstreamCalls.Load() != allowedCalls {
		t.Fatalf("another host skipped the owner gate: %d", got.Code)
	}
}
