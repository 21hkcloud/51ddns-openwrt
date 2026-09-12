package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

func newControlHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &reconnectingTransport{
			base: http.DefaultTransport.(*http.Transport).Clone(),
		},
	}
}

type reconnectingTransport struct {
	base *http.Transport
	mu   sync.Mutex
	pool *controlConnectionPool
}

func (t *reconnectingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	if t.pool == nil {
		t.pool = newControlConnectionPool(t.base)
	}
	pool := t.pool
	t.mu.Unlock()
	invalidate := func(err error) {
		// User cancellation does not imply a broken connection. A deadline or
		// network failure does: HTTP/2 otherwise cancels only the stream and
		// can keep reusing a blackholed TCP socket until the kernel times out.
		if errors.Is(err, context.Canceled) {
			return
		}
		t.mu.Lock()
		if t.pool != pool {
			t.mu.Unlock()
			return // A late failure from a retired pool must not close its replacement.
		}
		t.pool = nil
		t.mu.Unlock()
		pool.close()
	}
	// A write can itself block while net/http cancels an HTTP/2 stream. Release
	// the sockets at the deadline rather than waiting for RoundTrip to unwind.
	stopDeadline := context.AfterFunc(request.Context(), func() {
		if errors.Is(request.Context().Err(), context.DeadlineExceeded) {
			invalidate(context.DeadlineExceeded)
		}
	})
	response, err := pool.transport.RoundTrip(request)
	if err != nil {
		stopDeadline()
		invalidate(err)
		return nil, err
	}
	// Client.Timeout also covers response reads, after RoundTrip has returned.
	response.Body = &reconnectingBody{ReadCloser: response.Body, invalidate: invalidate, stopDeadline: stopDeadline}
	return response, nil
}

func (t *reconnectingTransport) CloseIdleConnections() {
	t.mu.Lock()
	pool := t.pool
	t.mu.Unlock()
	if pool != nil {
		pool.transport.CloseIdleConnections()
	}
}

// Track sockets when dialing them, not through httptrace: traced connections
// are owned by net/http. Retire the private pool and close our underlying
// sockets so even an HTTP/2 writer blocked in the kernel is released promptly.
type controlConnectionPool struct {
	transport *http.Transport
	mu        sync.Mutex
	retired   bool
	conns     map[*controlConn]struct{}
}

func newControlConnectionPool(base *http.Transport) *controlConnectionPool {
	pool := &controlConnectionPool{transport: base.Clone(), conns: make(map[*controlConn]struct{})}
	dial := base.DialContext
	pool.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		tracked := &controlConn{Conn: conn, pool: pool}
		pool.mu.Lock()
		if pool.retired {
			pool.mu.Unlock()
			_ = conn.Close()
			return nil, net.ErrClosed
		}
		pool.conns[tracked] = struct{}{}
		pool.mu.Unlock()
		return tracked, nil
	}
	return pool
}

func (p *controlConnectionPool) close() {
	p.mu.Lock()
	p.retired = true
	connections := make([]*controlConn, 0, len(p.conns))
	for conn := range p.conns {
		connections = append(connections, conn)
	}
	p.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	p.transport.CloseIdleConnections()
}

type controlConn struct {
	net.Conn
	pool *controlConnectionPool
}

func (c *controlConn) Close() error {
	c.pool.mu.Lock()
	delete(c.pool.conns, c)
	c.pool.mu.Unlock()
	return c.Conn.Close()
}

type reconnectingBody struct {
	io.ReadCloser
	invalidate   func(error)
	stopDeadline func() bool
}

func (b *reconnectingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		b.invalidate(err)
	}
	return n, err
}

func (b *reconnectingBody) Close() error {
	defer b.stopDeadline()
	return b.ReadCloser.Close()
}
