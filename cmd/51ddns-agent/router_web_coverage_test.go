package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func routerProbeServer(t *testing.T, hostname string, handler http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: []string{hostname},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func probeRoots(servers ...*httptest.Server) *x509.CertPool {
	roots := x509.NewCertPool()
	for _, server := range servers {
		roots.AddCert(server.Certificate())
	}
	return roots
}

func probeFixture(servers map[string]*httptest.Server, roots *x509.CertPool, addresses ...string) *routerWeb {
	w := &routerWeb{host: testRouterHost, state: "assigned", proxy: &httputil.ReverseProxy{}, probeRoots: roots}
	w.probeLookup = func(context.Context, string) ([]net.IPAddr, error) {
		out := []net.IPAddr{}
		for _, address := range addresses {
			out = append(out, net.IPAddr{IP: net.ParseIP(address)})
		}
		return out, nil
	}
	w.probeDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		server := servers[host]
		if server == nil {
			return nil, errors.New("QA address unavailable")
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	return w
}

func TestRouterReadinessCoversEveryResolvedAddress(t *testing.T) {
	var calls atomic.Int32
	good := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != testRouterHost || r.TLS.ServerName != testRouterHost {
			t.Error("Host or SNI changed to IP")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials leaked")
		}
		rw.Header().Set("X-51DDNS-Router-Probe", r.Header.Get("X-51DDNS-Probe"))
		rw.WriteHeader(204)
	})
	one, two := routerProbeServer(t, testRouterHost, good), routerProbeServer(t, testRouterHost, good)
	servers := map[string]*httptest.Server{"203.0.113.158": one, "203.0.113.159": two, "2001:db8::159": two}
	for _, addresses := range [][]string{{"203.0.113.158"}, {"203.0.113.158", "203.0.113.159"},
		{"203.0.113.158", "2001:db8::159"}, {"203.0.113.158", "203.0.113.158", "203.0.113.159"}} {
		w := probeFixture(servers, probeRoots(one, two), addresses...)
		calls.Store(0)
		w.checkPublic(context.Background())
		if w.status().State != "ready" {
			t.Fatal("all live addresses rejected", addresses, w.status())
		}
		expected := len(addresses)
		if expected == 3 {
			expected = 2
		}
		if int(calls.Load()) != expected {
			t.Fatal("address was skipped or duplicated", calls.Load(), expected)
		}
	}
}

func TestRouterReadinessOneBadAddressNeverLooksReady(t *testing.T) {
	good := routerProbeServer(t, testRouterHost, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-51DDNS-Router-Probe", r.Header.Get("X-51DDNS-Probe"))
		rw.WriteHeader(204)
	}))
	bad := routerProbeServer(t, testRouterHost, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(502) }))
	w := probeFixture(map[string]*httptest.Server{"203.0.113.158": good, "203.0.113.159": bad}, probeRoots(good, bad), "203.0.113.158", "203.0.113.159")
	w.state = "ready"
	w.checkPublic(context.Background())
	if w.status().State != "public_unavailable" {
		t.Fatal("single-node success falsely ready", w.status())
	}
	w.probeLookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.158")}}, nil
	}
	w.checkPublic(context.Background())
	if w.status().State != "ready" {
		t.Fatal("recovery not detected")
	}
}

func TestRouterReadinessRejectsWrongSANEvenWithTrustedCA(t *testing.T) {
	wrong := routerProbeServer(t, "wrong.example.test", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-51DDNS-Router-Probe", r.Header.Get("X-51DDNS-Probe"))
		rw.WriteHeader(204)
	}))
	w := probeFixture(map[string]*httptest.Server{"203.0.113.158": wrong}, probeRoots(wrong), "203.0.113.158")
	w.checkPublic(context.Background())
	if w.status().State == "ready" {
		t.Fatal("wrong SAN accepted")
	}
}

func TestRouterReadinessDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	server := routerProbeServer(t, testRouterHost, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/_51ddns/health" {
			http.Redirect(rw, r, "https://"+testRouterHost+"/other", http.StatusTemporaryRedirect)
			return
		}
		rw.Header().Set("X-51DDNS-Router-Probe", r.Header.Get("X-51DDNS-Probe"))
		rw.WriteHeader(204)
	}))
	w := probeFixture(map[string]*httptest.Server{"203.0.113.158": server}, probeRoots(server), "203.0.113.158")
	w.checkPublic(context.Background())
	if w.status().State == "ready" || calls.Load() != 1 {
		t.Fatal("redirect followed", w.status(), calls.Load())
	}
}

func TestRouterReadinessUnreachableAAAARevokesReadiness(t *testing.T) {
	server := routerProbeServer(t, testRouterHost, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-51DDNS-Router-Probe", r.Header.Get("X-51DDNS-Probe"))
		rw.WriteHeader(204)
	}))
	w := probeFixture(map[string]*httptest.Server{"203.0.113.158": server}, probeRoots(server), "203.0.113.158", "2001:db8::159")
	w.state = "ready"
	w.checkPublic(context.Background())
	if w.status().State != "public_unavailable" {
		t.Fatal("unreachable AAAA accepted", w.status())
	}
}

func TestRouterReadinessLimitsAddressesConcurrencyAndTime(t *testing.T) {
	w := probeFixture(nil, nil)
	w.checkPublic(context.Background())
	if w.status().State == "ready" {
		t.Fatal("empty DNS accepted")
	}
	w.probeLookup = func(ctx context.Context, _ string) ([]net.IPAddr, error) { <-ctx.Done(); return nil, ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	before := time.Now()
	w.checkPublic(ctx)
	if time.Since(before) > time.Second {
		t.Fatal("DNS ignored cancellation")
	}
	w.probeLookup = func(context.Context, string) ([]net.IPAddr, error) {
		out := []net.IPAddr{}
		for i := 1; i <= 17; i++ {
			out = append(out, net.IPAddr{IP: net.IPv4(203, 0, 113, byte(i))})
		}
		return out, nil
	}
	var dialed atomic.Int32
	w.probeDial = func(context.Context, string, string) (net.Conn, error) {
		dialed.Add(1)
		return nil, errors.New("should not dial")
	}
	w.checkPublic(context.Background())
	if dialed.Load() != 0 {
		t.Fatal("oversized DNS dialed")
	}
	w.probeLookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.1")}, {IP: net.ParseIP("203.0.113.2")}, {IP: net.ParseIP("203.0.113.3")}}, nil
	}
	var active, maxActive atomic.Int32
	w.probeDial = func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel2()
	w.checkPublic(ctx2)
	if maxActive.Load() > 2 || active.Load() != 0 {
		t.Fatal("unbounded or leaked probes", maxActive.Load(), active.Load())
	}
}

func TestRouterCapacityUsesAvailableRelaysBeforeEnabledReport(t *testing.T) {
	for _, tc := range []struct {
		name       string
		nodes, cap int
		enabled    bool
	}{
		{"single-node-QA", 1, 1, true}, {"two-nodes-explicit-one", 2, 1, false},
		{"two-nodes-two", 2, 2, true}, {"three-nodes-cap-two", 3, 2, false}, {"unlimited", 2, 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			local := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.Write([]byte("LuCI sysauth")) }))
			defer local.Close()
			var reports []bool
			var reportsMu sync.Mutex
			var w *routerWeb
			api := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					var input struct {
						Enabled bool `json:"enabled"`
						Version int  `json:"protocol_version"`
					}
					if json.NewDecoder(r.Body).Decode(&input) != nil || input.Version != 2 {
						t.Error("report protocol missing")
					}
					if !input.Enabled && !tc.enabled && w.status().Hostname != "" {
						t.Error("usable host retained while waiting for capacity revocation")
					}
					reportsMu.Lock()
					reports = append(reports, input.Enabled)
					reportsMu.Unlock()
					json.NewEncoder(rw).Encode(map[string]any{"hostname": testRouterHost, "enabled": input.Enabled})
					return
				}
				items := []relayConfiguration{}
				for i := 0; i < tc.nodes; i++ {
					items = append(items, relayConfiguration{RelayNode: string(rune('a' + i)), FRPCTOML: "QA-config"})
				}
				json.NewEncoder(rw).Encode(configuration{RelayConfigs: items})
			}))
			defer api.Close()
			w = &routerWeb{config: routerWebConfig{Enabled: true, Target: local.URL}, server: &http.Server{}, api: api.URL, client: api.Client()}
			if !tc.enabled {
				w.host, w.proxy, w.state = testRouterHost, &httputil.ReverseProxy{}, "ready"
			}
			w.probeLookup = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("QA avoids public DNS") }
			a := &agent{apiURL: api.URL, httpClient: api.Client(), routerWeb: w, maxActiveRelays: tc.cap}
			if _, err := a.fetch(context.Background()); err != nil {
				t.Fatal(err)
			}
			reportsMu.Lock()
			defer reportsMu.Unlock()
			if len(reports) != 1 || reports[0] != tc.enabled {
				t.Fatal("wrong report", reports)
			}
			if !tc.enabled && (w.status().State != "relay_capacity_insufficient" || w.status().Hostname != "") {
				t.Fatal("capacity reason lost or usable host exposed", w.status())
			}
		})
	}
}

func TestRouterCapacityRevokesWhenAnotherRelayAppearsDuringReport(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.Write([]byte("LuCI sysauth")) }))
	defer local.Close()
	var mu sync.Mutex
	var gets int
	var enabled bool
	var reports []bool
	api := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			var input struct {
				Enabled bool `json:"enabled"`
				Version int  `json:"protocol_version"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Version != 2 {
				t.Error("missing version")
			}
			enabled = input.Enabled
			reports = append(reports, enabled)
			json.NewEncoder(rw).Encode(map[string]any{"hostname": testRouterHost, "enabled": enabled})
			return
		}
		gets++
		toml := "ordinary-only"
		if enabled {
			toml += " managed-proxy"
		}
		items := []relayConfiguration{{RelayNode: "node-hk1", FRPCTOML: toml}}
		if gets > 1 {
			items = append(items, relayConfiguration{RelayNode: "node-hk2", FRPCTOML: toml})
		}
		json.NewEncoder(rw).Encode(configuration{RelayConfigs: items})
	}))
	defer api.Close()
	w := &routerWeb{config: routerWebConfig{Enabled: true, Target: local.URL}, server: &http.Server{}, api: api.URL, client: api.Client()}
	a := &agent{apiURL: api.URL, httpClient: api.Client(), routerWeb: w, maxActiveRelays: 1}
	items, err := a.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 2 || !reports[0] || reports[1] || gets != 3 {
		t.Fatal("configuration race did not revoke", reports, gets)
	}
	if len(items) != 1 || items[0].FRPCTOML != "ordinary-only" || w.status().Hostname != "" || w.status().State != "relay_capacity_insufficient" {
		t.Fatal("stale managed configuration escaped", items, w.status())
	}
}
