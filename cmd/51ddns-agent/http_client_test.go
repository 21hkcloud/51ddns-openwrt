package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Keep the TCP socket open while discarding traffic, like a stale connection
// after an egress change. A subsequent dial remains healthy.
type blackholeConn struct {
	net.Conn
	drop        atomic.Bool
	closed      atomic.Bool
	blockWrites atomic.Bool
	done        chan struct{}
	once        sync.Once
}

func (c *blackholeConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if err != nil || !c.drop.Load() {
			return n, err
		}
	}
}

func (c *blackholeConn) Write(p []byte) (int, error) {
	if c.blockWrites.Load() {
		<-c.done
		return 0, net.ErrClosed
	}
	if c.drop.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func (c *blackholeConn) Close() error {
	c.closed.Store(true)
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

type dialRecorder struct {
	mu    sync.Mutex
	conns []*blackholeConn
}

func (d *dialRecorder) dial(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	wrapped := &blackholeConn{Conn: conn, done: make(chan struct{})}
	d.mu.Lock()
	d.conns = append(d.conns, wrapped)
	d.mu.Unlock()
	return wrapped, nil
}

func (d *dialRecorder) snapshot() []*blackholeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*blackholeConn(nil), d.conns...)
}

func testControlClient(t *testing.T, h2 bool, handler http.Handler) (*agent, *dialRecorder) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = h2
	server.StartTLS()
	t.Cleanup(server.Close)
	client := newControlHTTPClient()
	transport := client.Transport.(*reconnectingTransport).base
	transport.TLSClientConfig = &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	transport.Proxy = nil
	dials := &dialRecorder{}
	transport.DialContext = dials.dial
	t.Cleanup(client.CloseIdleConnections)
	return &agent{
		apiURL: server.URL, deviceToken: "test-token",
		deviceID:     "00000000-0000-4000-8000-000000000001",
		deviceIDFile: filepath.Join(t.TempDir(), "device.id"),
		statusPath:   filepath.Join(t.TempDir(), "status.json"),
		httpClient:   client,
	}, dials
}

func serveControlResponse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/agent/activate":
		_, _ = fmt.Fprint(w, `{"device_id":"00000000-0000-4000-8000-000000000001"}`)
	case "/v1/agent/ip-report":
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	default:
		_, _ = fmt.Fprint(w, `{"frpc_toml":"test configuration"}`)
	}
}

func controlOperation(name string, a *agent) error {
	switch name {
	case "activate":
		return a.activate(context.Background())
	case "report":
		return a.reportIP(context.Background())
	default:
		_, err := a.fetch(context.Background())
		return err
	}
}

func TestControlHTTP2ReplacesBlackholedConnection(t *testing.T) {
	for _, operation := range []string{"activate", "report", "fetch"} {
		t.Run(operation, func(t *testing.T) {
			var requests atomic.Int32
			a, dials := testControlClient(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 {
					t.Errorf("expected HTTP/2, got %s", r.Proto)
				}
				if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-51DDNS-Device-ID") != aTestDeviceID {
					t.Error("missing device authorization")
				}
				requests.Add(1)
				serveControlResponse(w, r)
			}))
			if err := controlOperation(operation, a); err != nil {
				t.Fatal(err)
			}
			first := dials.snapshot()[0]
			first.drop.Store(true)
			a.httpClient.Timeout = 200 * time.Millisecond
			started := time.Now()
			err := controlOperation(operation, a)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("blackholed request error = %v", err)
			}
			if requests.Load() != 1 {
				t.Fatal("the failed request must not be replayed automatically")
			}
			a.httpClient.Timeout = time.Second
			if err := controlOperation(operation, a); err != nil {
				t.Fatalf("next request did not recover on a new connection: %v", err)
			}
			if !first.closed.Load() || len(dials.snapshot()) != 2 || requests.Load() != 2 {
				t.Fatal("failed socket was not replaced exactly once")
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("recovery exceeded the request timeout plus a fresh request")
			}
			t.Logf("request timeout 200ms, recovered in %s", time.Since(started).Round(time.Millisecond))
		})
	}
}

const aTestDeviceID = "00000000-0000-4000-8000-000000000001"

func TestControlHTTP2RecoversFromBodyTimeout(t *testing.T) {
	for _, operation := range []string{"activate", "report", "fetch"} {
		t.Run(operation, func(t *testing.T) {
			var stall atomic.Bool
			a, dials := testControlClient(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stall.Load() {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, "{")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				serveControlResponse(w, r)
			}))
			if err := controlOperation(operation, a); err != nil {
				t.Fatal(err)
			}
			first := dials.snapshot()[0]
			stall.Store(true)
			a.httpClient.Timeout = 200 * time.Millisecond
			if err := controlOperation(operation, a); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("body timeout must not be reported as success: %v", err)
			}
			if !first.closed.Load() {
				t.Fatal("body timeout left the failed connection open")
			}
			stall.Store(false)
			a.httpClient.Timeout = time.Second
			if err := controlOperation(operation, a); err != nil {
				t.Fatal(err)
			}
			if len(dials.snapshot()) != 2 {
				t.Fatal("body timeout did not trigger a new connection")
			}
		})
	}
}

func TestControlHTTPKeepsHealthyConnections(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("http2=%v", h2), func(t *testing.T) {
			a, dials := testControlClient(t, h2, http.HandlerFunc(serveControlResponse))
			for i := 0; i < 3; i++ {
				if err := controlOperation("fetch", a); err != nil {
					t.Fatal(err)
				}
			}
			if len(dials.snapshot()) != 1 || dials.snapshot()[0].closed.Load() {
				t.Fatal("healthy requests must reuse their connection")
			}
			a.httpClient.CloseIdleConnections()
			if err := controlOperation("fetch", a); err != nil {
				t.Fatal(err)
			}
			if len(dials.snapshot()) != 2 {
				t.Fatal("CloseIdleConnections was not forwarded to the transport")
			}
		})
	}
}

func TestControlHTTPDoesNotRetryOrResetHTTPFailures(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			var reject atomic.Bool
			a, dials := testControlClient(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if reject.Load() {
					http.Error(w, "test rejection", status)
					return
				}
				serveControlResponse(w, r)
			}))
			if err := a.reportIP(context.Background()); err != nil {
				t.Fatal(err)
			}
			reject.Store(true)
			if err := a.reportIP(context.Background()); err == nil {
				t.Fatal("HTTP rejection must be returned to the caller")
			}
			if requests.Load() != 2 || len(dials.snapshot()) != 1 || dials.snapshot()[0].closed.Load() {
				t.Fatal("HTTP rejection replayed POST or discarded a healthy connection")
			}
		})
	}
}

func TestControlHTTPCancellationKeepsHealthyConnection(t *testing.T) {
	arrived := make(chan struct{})
	a, dials := testControlClient(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cancel" {
			close(arrived)
			<-r.Context().Done()
			return
		}
		serveControlResponse(w, r)
	}))
	if err := controlOperation("fetch", a); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.apiURL+"/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		response, err := a.httpClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not arrive")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation blocked")
	}
	if err := controlOperation("fetch", a); err != nil {
		t.Fatal(err)
	}
	if len(dials.snapshot()) != 1 || dials.snapshot()[0].closed.Load() {
		t.Fatal("user cancellation discarded a healthy HTTP/2 connection")
	}
}

func TestControlHTTPDeadlineReleasesBlockedWriter(t *testing.T) {
	a, dials := testControlClient(t, true, http.HandlerFunc(serveControlResponse))
	if err := controlOperation("fetch", a); err != nil {
		t.Fatal(err)
	}
	first := dials.snapshot()[0]
	first.blockWrites.Store(true)
	defer first.Close() // Keep a broken implementation from hanging test cleanup.
	a.httpClient.Timeout = 200 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- controlOperation("report", a) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked write was reported as success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline did not release the blocked HTTP/2 writer")
	}
	if !first.closed.Load() {
		t.Fatal("deadline left the blocked socket open")
	}
	a.httpClient.Timeout = time.Second
	if err := controlOperation("fetch", a); err != nil {
		t.Fatal(err)
	}
	if len(dials.snapshot()) != 2 {
		t.Fatal("blocked writer did not trigger a new connection")
	}
}

func TestControlHTTPLateBodyFailureDoesNotRetireNewPool(t *testing.T) {
	a, dials := testControlClient(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			_, _ = fmt.Fprint(w, "{")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		serveControlResponse(w, r)
	}))
	response, err := a.httpClient.Get(a.apiURL + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := dials.snapshot()[0]
	first.drop.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := a.fetch(ctx); err == nil {
		t.Fatal("blackholed request succeeded")
	}
	if err := controlOperation("fetch", a); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("retired pool's unfinished body did not fail")
	}
	if err := controlOperation("fetch", a); err != nil {
		t.Fatal(err)
	}
	conns := dials.snapshot()
	if len(conns) != 2 || conns[1].closed.Load() {
		t.Fatal("late failure from retired pool discarded its healthy replacement")
	}
}

func TestControlPoolClosesDialFinishingAfterRetirement(t *testing.T) {
	clientConn, peerConn := net.Pipe()
	defer peerConn.Close()
	started, release := make(chan struct{}), make(chan struct{})
	pool := newControlConnectionPool(&http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		close(started)
		<-release
		return clientConn, nil
	}})
	done := make(chan error, 1)
	go func() {
		conn, err := pool.transport.DialContext(context.Background(), "tcp", "unused")
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()
	<-started
	pool.close()
	close(release)
	if err := <-done; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("late dial error = %v", err)
	}
	_ = peerConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peerConn.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("retired pool leaked a late connection: %v", err)
	}
}
