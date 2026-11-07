package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDirectRouterWebNativeLuCI(t *testing.T) {
	target := os.Getenv("DDNS_QA_NATIVE_LUCI")
	if target == "" {
		t.Skip("requires an isolated OpenWrt 24.10 guest with stock LuCI and synthetic root credentials")
	}
	w := &routerWeb{config: routerWebConfig{Target: target, DirectHostname: testRouterHost},
		host: testRouterHost, credentialProof: directRouterCredentialProof, gate: make(chan struct{}, 16)}
	var err error
	w.proxy, err = w.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://" + testRouterHost
	call := func(method, path, body string, lan bool, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		r.URL.Scheme, r.URL.Host = "", ""
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if strings.HasSuffix(path, "/admin/ubus") {
			r.Header.Set("Content-Type", "application/json")
		}
		if method == "POST" {
			r.Header.Set("Origin", origin)
		}
		if lan {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("Sec-Fetch-Mode", "navigate")
			r.Header.Set("Sec-Fetch-Dest", "document")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		w.ServeHTTP(out, r)
		return out
	}
	const luci = "/cgi-bin/luci/"
	if os.Getenv("DDNS_QA_NATIVE_UNREADY") == "1" {
		if routerCredentialReady(w.credentialProof, time.Now()) {
			t.Fatal("native password guard is still ready")
		}
		if out := call("GET", luci, "", false, nil); out.Code != 303 || !strings.HasPrefix(out.Header().Get("Location"), routerConsole+"/router-access?") {
			t.Fatalf("native guard fallback status=%d", out.Code)
		}
		t.Log("QA_DIRECT_NATIVE_PASSWORD_REMOVAL_RESTORED_OWNER_GATE")
		return
	}
	if out := call("GET", "/", "", true, nil); out.Code != 302 || out.Header().Get("Location") != luci {
		t.Fatalf("landing status=%d", out.Code)
	}
	login := call("GET", luci, "", true, nil)
	if login.Code != 403 || login.Header().Get("X-LuCI-Login-Required") != "yes" || !strings.Contains(login.Body.String(), "luci_password") {
		t.Fatalf("stock native login status=%d", login.Code)
	}
	unauthRPC := call("POST", luci+"admin/ubus", `{"jsonrpc":"2.0","method":"call","id":1,"params":["00000000000000000000000000000000","session","login",{"username":"root","password":"unused"}]}`, false, nil)
	if unauthRPC.Code != 403 || strings.Contains(unauthRPC.Body.String(), "ubus_rpc_session") {
		t.Fatalf("direct RPC login status=%d", unauthRPC.Code)
	}
	wrong := call("POST", luci, "luci_username=root&luci_password=incorrect", false, nil)
	if wrong.Code != 403 || len(wrong.Result().Cookies()) != 0 {
		t.Fatalf("wrong password status=%d", wrong.Code)
	}
	form := url.Values{"luci_username": {"root"}, "luci_password": {os.Getenv("DDNS_QA_NATIVE_PASSWORD")}}
	ok := call("POST", luci, form.Encode(), false, nil)
	cookies := ok.Result().Cookies()
	if ok.Code != 302 || len(cookies) != 1 || !strings.HasPrefix(cookies[0].Name, "sysauth_") ||
		!cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("stock password login status=%d cookies=%d", ok.Code, len(cookies))
	}
	if out := call("GET", luci+"admin/status/overview", "", false, cookies[0]); out.Code != 200 ||
		strings.Contains(out.Body.String(), "name=\"luci_password\"") {
		t.Fatalf("stock administration status=%d", out.Code)
	}
	if out := call("POST", "/ubus/", "{}", false, cookies[0]); out.Code != 404 {
		t.Fatalf("raw RPC status=%d", out.Code)
	}
	// The native page uses its authenticated RPC fallback when raw /ubus is unavailable.
	rpc := call("POST", luci+"admin/ubus", `{"jsonrpc":"2.0","method":"call","id":1,"params":["`+cookies[0].Value+`","system","board",{}]}`, false, cookies[0])
	if rpc.Code != 200 || !strings.Contains(rpc.Body.String(), "OpenWrt") {
		t.Fatalf("stock RPC fallback status=%d", rpc.Code)
	}
	t.Log("QA_DIRECT_NATIVE_FORM_AND_RPC")
}
