package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterWebRevokesActiveStreams(t *testing.T) {
	for _, failure := range []string{"revoked", "api_unavailable", "disabled"} {
		for _, upgrade := range []bool{false, true} {
			t.Run(fmt.Sprint(failure, "/upgrade=", upgrade), func(t *testing.T) {
				var revoked atomic.Bool
				closed := make(chan struct{}, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/cgi-bin/luci/" {
						io.WriteString(w, "LuCI login")
						return
					}
					if upgrade {
						c, b, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						defer c.Close()
						fmt.Fprint(b, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
						b.Flush()
						c.SetReadDeadline(time.Now().Add(12 * time.Second))
						one := make([]byte, 1)
						c.Read(one)
					} else {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: begin\n\n")
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					}
					closed <- struct{}{}
				}))
				defer upstream.Close()
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if revoked.Load() {
						code := 403
						if failure == "api_unavailable" {
							code = 503
						}
						w.WriteHeader(code)
						return
					}
					fmt.Fprintf(w, `{"allowed":true,"expires":%d}`, time.Now().Add(time.Minute).Unix())
				}))
				defer api.Close()
				guard := &routerWeb{config: routerWebConfig{Target: upstream.URL}, host: testRouterHost, api: api.URL, client: api.Client(), gate: make(chan struct{}, 16)}
				var err error
				guard.proxy, err = guard.discover(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(guard)
				defer server.Close()
				c, err := net.Dial("tcp", server.Listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(12 * time.Second))
				headers := ""
				if upgrade {
					headers = "Connection: Upgrade\r\nUpgrade: websocket\r\n"
				}
				fmt.Fprintf(c, "GET /stream HTTP/1.1\r\nHost: %s\r\nX-51DDNS-Public-Proto: https\r\nOrigin: https://%s\r\nCookie: %s=%s\r\n%s\r\n", testRouterHost, testRouterHost, routerCookie, strings.Repeat("s", 43), headers)
				reader := bufio.NewReader(c)
				resp, err := http.ReadResponse(reader, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				expected := 200
				if upgrade {
					expected = 101
				}
				if resp.StatusCode != expected {
					t.Fatal(resp.StatusCode)
				}
				if failure == "disabled" {
					guard.mu.Lock()
					guard.proxy = nil
					guard.mu.Unlock()
				} else {
					revoked.Store(true)
				}
				select {
				case <-closed:
				case <-time.After(9 * time.Second):
					t.Fatal("revoked stream stayed connected")
				}
			})
		}
	}
}
