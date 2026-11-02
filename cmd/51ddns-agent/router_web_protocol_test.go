package main

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"
)

func TestRouterWebRequiresPublicTLS(t *testing.T) {
	for _, protocol := range []string{"", "http", "https"} {
		w := &routerWeb{host: testRouterHost, proxy: &httputil.ReverseProxy{}, gate: make(chan struct{}, 16)}
		r := httptest.NewRequest("GET", "/_51ddns/health", nil)
		r.Host = testRouterHost
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-51DDNS-Public-Proto", protocol)
		r.Header.Set("X-51DDNS-Probe", "12345678901234567890123456789012")
		out := httptest.NewRecorder()
		w.ServeHTTP(out, r)
		want := http.StatusTemporaryRedirect
		if protocol == "https" {
			want = http.StatusNoContent
		}
		if out.Code != want {
			t.Errorf("protocol %q: got %d want %d", protocol, out.Code, want)
		}
	}
}
