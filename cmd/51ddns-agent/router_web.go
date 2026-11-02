package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const routerCookie = "__Host-51ddns-router"
const routerConsole = "https://console.51ddns.com"

type routerWebConfig struct {
	Enabled     bool     `json:"enabled"`
	Target      string   `json:"target"`
	HTTP        []string `json:"http"`
	HTTPS       []string `json:"https"`
	Certificate string   `json:"certificate"`
}
type routerWebStatus struct {
	Hostname string `json:"hostname,omitempty"`
	State    string `json:"state"`
}
type routerWeb struct {
	mu          sync.RWMutex
	config      routerWebConfig
	api         string
	token       string
	device      string
	host        string
	state       string
	proxy       *httputil.ReverseProxy
	client      *http.Client
	probeClient *http.Client
	server      *http.Server
	gate        chan struct{}
}

func (a *agent) startRouterWeb(ctx context.Context) error {
	if a.routerConfigPath == "" {
		return nil
	}
	f, err := os.Open(a.routerConfigPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var config routerWebConfig
	if err = json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&config); err != nil {
		return err
	}
	w := &routerWeb{config: config, api: a.apiURL, token: a.deviceToken, client: a.httpClient, gate: make(chan struct{}, 16), state: "disabled"}
	a.routerWeb = w
	if !config.Enabled {
		return nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:17851")
	if err != nil {
		w.state = "port_in_use"
		return nil
	}
	w.state = "detecting"
	w.server = &http.Server{Handler: w, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() { <-ctx.Done(); _ = w.server.Close() }()
	go func() { _ = w.server.Serve(listener) }()
	return nil
}

func localRouterTarget(raw string, addresses []net.Addr) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid local router URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return nil, errors.New("router address must be a local interface IP")
	}
	local := ip.IsLoopback()
	for _, a := range addresses {
		s, _, _ := net.ParseCIDR(a.String())
		if s != nil && s.Equal(ip) {
			local = true
		}
	}
	if !local {
		return nil, errors.New("router address is not a local interface")
	}
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 || n == 17851 {
			return nil, errors.New("invalid local router port")
		}
	}
	u.Path = ""
	return u, nil
}
func listenerURL(scheme, address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// Self-signed uhttpd certificates are accepted only when they exactly match the
// configured local public certificate. No global insecure TLS or redirect follow.
func routerTransport(cert string) *http.Transport {
	pins := map[[32]byte]bool{}
	rest := []byte(cert)
	for {
		block, tail := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = tail
		if block.Type == "CERTIFICATE" {
			if c, e := x509.ParseCertificate(block.Bytes); e == nil {
				pins[sha256.Sum256(c.Raw)] = true
			}
		}
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(pins) > 0 {
		tlsConfig.InsecureSkipVerify = true // VerifyConnection enforces exact local certificate pin.
		tlsConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("missing router certificate")
			}
			c := cs.PeerCertificates[0]
			now := time.Now()
			if !pins[sha256.Sum256(c.Raw)] || now.Before(c.NotBefore) || now.After(c.NotAfter) {
				return errors.New("router certificate does not match local configuration")
			}
			return nil
		}
	}
	return &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, TLSClientConfig: tlsConfig, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 64 << 10}
}
func (w *routerWeb) discover(ctx context.Context) (*httputil.ReverseProxy, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	candidates := []string{}
	if w.config.Target != "" {
		candidates = append(candidates, w.config.Target)
	} else {
		for _, s := range w.config.HTTPS {
			candidates = append(candidates, listenerURL("https", s))
		}
		for _, s := range w.config.HTTP {
			candidates = append(candidates, listenerURL("http", s))
		}
	}
	if len(candidates) > 8 {
		candidates = candidates[:8]
	}
	for _, raw := range candidates {
		u, e := localRouterTarget(raw, addresses)
		if e != nil {
			continue
		}
		tr := routerTransport(w.config.Certificate)
		client := &http.Client{Transport: tr, Timeout: 4 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > 3 || next.URL.Scheme != u.Scheme || next.URL.Host != u.Host || !strings.HasPrefix(next.URL.Path, "/cgi-bin/luci/") {
				return http.ErrUseLastResponse
			}
			return nil
		}}
		request, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String()+"/cgi-bin/luci/", nil)
		if e != nil {
			continue
		}
		response, e := client.Do(request)
		if e != nil {
			tr.CloseIdleConnections()
			continue
		}
		body, e := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		// A generic service on port 80 is not evidence of the router's LuCI endpoint.
		text := strings.ToLower(string(body))
		identified := strings.Contains(text, "luci") || strings.Contains(text, "sysauth")
		if e != nil || !identified || (response.StatusCode != 200 && response.StatusCode != 403) {
			tr.CloseIdleConnections()
			continue
		}
		proxy := httputil.NewSingleHostReverseProxy(u)
		original := proxy.Director
		proxy.Director = func(r *http.Request) {
			original(r)
			r.Host = u.Host
			r.Header.Del("Forwarded")
			r.Header.Del("X-Forwarded-Host")
			r.Header.Del("X-Forwarded-Proto")
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-51DDNS-Public-Proto")
			if r.Header.Get("Origin") != "" {
				r.Header.Set("Origin", u.Scheme+"://"+u.Host)
			}
			if ref, e := url.Parse(r.Header.Get("Referer")); e == nil && ref.Host != "" {
				ref.Scheme = u.Scheme
				ref.Host = u.Host
				r.Header.Set("Referer", ref.String())
			}
		}
		proxy.Transport = tr
		proxy.ModifyResponse = func(r *http.Response) error {
			if location := r.Header.Get("Location"); location != "" {
				if dest, e := url.Parse(location); e != nil {
					return errors.New("invalid router redirect")
				} else if dest.IsAbs() || dest.Host != "" {
					if (dest.Scheme == "" || dest.Scheme == "http" || dest.Scheme == "https") && dest.Host == u.Host && dest.User == nil {
						w.mu.RLock()
						dest.Scheme = "https"
						dest.Host = w.host
						w.mu.RUnlock()
						r.Header.Set("Location", dest.String())
					} else {
						return errors.New("router redirect left local management origin")
					}
				}
			}
			// Never allow upstream content to replace the access-session cookie.
			cookies := r.Cookies()
			r.Header.Del("Set-Cookie")
			for _, c := range cookies {
				if c.Name != routerCookie {
					c.Domain = ""
					c.Secure = true
					r.Header.Add("Set-Cookie", c.String())
				}
			}
			r.Header.Set("Cache-Control", "no-store")
			r.Header.Set("Referrer-Policy", "strict-origin")
			return nil
		}
		proxy.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, _ error) {
			http.Error(rw, "路由器管理页面暂时不可达", http.StatusBadGateway)
		}
		return proxy, nil
	}
	return nil, errors.New("router web detection failed")
}

func (w *routerWeb) call(ctx context.Context, path string, input, output any) int {
	body, err := json.Marshal(input)
	if err != nil {
		return 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.api+path, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	w.mu.RLock()
	device := w.device
	w.mu.RUnlock()
	req.Header.Set("Authorization", "Bearer "+w.token)
	req.Header.Set("X-51DDNS-Device-ID", device)
	req.Header.Set("Content-Type", "application/json")
	response, err := w.client.Do(req)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	if response.StatusCode == 200 && output != nil {
		if json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(output) != nil {
			return 0
		}
	}
	return response.StatusCode
}

func (a *agent) syncRouterWeb(ctx context.Context) {
	w := a.routerWeb
	if w == nil {
		return
	}
	w.mu.Lock()
	w.device = a.deviceID
	w.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	enabled := w.config.Enabled && w.server != nil
	w.mu.RLock()
	previous := w.proxy
	w.mu.RUnlock()
	var proxy *httputil.ReverseProxy
	defer func() {
		if previous != nil {
			if tr, ok := previous.Transport.(*http.Transport); ok {
				tr.CloseIdleConnections()
			}
		}
		w.mu.RLock()
		retained := w.proxy == proxy
		w.mu.RUnlock()
		if proxy != nil && !retained {
			if tr, ok := proxy.Transport.(*http.Transport); ok {
				tr.CloseIdleConnections()
			}
		}
	}()
	if enabled {
		var err error
		proxy, err = w.discover(ctx)
		if err != nil {
			w.mu.Lock()
			w.state = "target_unavailable"
			w.mu.Unlock()
			enabled = false
		}
	}
	var result struct {
		Hostname string `json:"hostname"`
		Enabled  bool   `json:"enabled"`
	}
	status := w.call(ctx, "/v1/agent/router-web", map[string]bool{"enabled": enabled}, &result)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.host = ""
	w.proxy = nil
	if status != 200 {
		w.state = map[int]string{402: "plan_required", 409: "domain_quota_exceeded", 404: "server_not_ready", 403: "disabled"}[status]
		if w.state == "" {
			w.state = "verification_unavailable"
		}
		return
	}
	if !enabled {
		if w.state != "target_unavailable" && w.state != "port_in_use" {
			w.state = "disabled"
		}
		return
	}
	if !validRouterHost(result.Hostname) {
		w.state = "verification_unavailable"
		return
	}
	w.host = result.Hostname
	if !result.Enabled {
		w.state = "disabled"
		return
	}
	w.proxy = proxy
	w.state = "assigned"
}
func validRouterHost(host string) bool {
	// The production suffix is fixed in this first candidate; no API-provided URL.
	const suffix = ".cdn.cloud.toddns.com"
	label := strings.TrimSuffix(host, suffix)
	if !strings.HasSuffix(host, suffix) || !strings.HasPrefix(label, "router-") || len(label) != 39 {
		return false
	}
	b, e := hex.DecodeString(strings.TrimPrefix(label, "router-"))
	return e == nil && len(b) == 16
}
func (w *routerWeb) status() *routerWebStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return &routerWebStatus{Hostname: w.host, State: w.state}
}

// Readiness requires a round-trip through the exact public HTTPS hostname.
// This request never includes account credentials or browser access cookies.
func (w *routerWeb) checkPublic(ctx context.Context) {
	w.mu.RLock()
	host, state := w.host, w.state
	w.mu.RUnlock()
	if host == "" || state != "assigned" {
		return
	}
	client := w.probeClient
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return
	}
	nonce := hex.EncodeToString(random)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/_51ddns/health", nil)
	if err != nil {
		return
	}
	request.Header.Set("X-51DDNS-Probe", nonce)
	response, err := client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode == 204 && response.Header.Get("X-51DDNS-Router-Probe") == nonce {
		w.mu.Lock()
		if w.host == host && w.state == "assigned" {
			w.state = "ready"
		}
		w.mu.Unlock()
	}
}

func (w *routerWeb) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "strict-origin")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("X-Frame-Options", "DENY")
	w.mu.RLock()
	host, proxy := w.host, w.proxy
	w.mu.RUnlock()
	if host == "" || r.Host != host || proxy == nil || r.URL.IsAbs() || r.Method == http.MethodConnect {
		http.Error(rw, "管理入口尚未就绪", 503)
		return
	}
	// The public gateway overwrites this header from its actual TLS connection.
	// FRPS must remain loopback-only; its own X-Forwarded-Proto describes the
	// internal HTTP hop and cannot establish the public protocol.
	if r.TLS == nil && r.Header.Get("X-51DDNS-Public-Proto") != "https" {
		http.Redirect(rw, r, "https://"+host+r.URL.RequestURI(), http.StatusTemporaryRedirect)
		return
	}
	select {
	case w.gate <- struct{}{}:
		defer func() { <-w.gate }()
	default:
		http.Error(rw, "请稍后再试", 429)
		return
	}
	if r.URL.Path == "/_51ddns/health" && r.Method == http.MethodGet {
		nonce := r.Header.Get("X-51DDNS-Probe")
		if b, e := hex.DecodeString(nonce); e != nil || len(b) != 16 {
			http.Error(rw, "invalid probe", 400)
			return
		}
		rw.Header().Set("X-51DDNS-Router-Probe", nonce)
		rw.WriteHeader(204)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if r.URL.Path == "/_51ddns/callback" {
		if r.Method != http.MethodPost || r.Header.Get("Origin") != routerConsole {
			http.Error(rw, "访问来源无效", 403)
			return
		}
		r.Body = http.MaxBytesReader(rw, r.Body, 2048)
		if r.ParseForm() != nil {
			http.Error(rw, "验证请求无效", 400)
			return
		}
		var out struct {
			Session string `json:"session"`
		}
		if w.call(ctx, "/v1/agent/router-web/session", map[string]string{"host": host, "code": r.PostForm.Get("code")}, &out) != 200 || len(out.Session) != 43 {
			http.Error(rw, "验证已失效，请返回控制台重试", 403)
			return
		}
		http.SetCookie(rw, &http.Cookie{Name: routerCookie, Value: out.Session, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 900})
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		// A same-origin user navigation ensures Strict cookies work after cross-site POST.
		_, _ = io.WriteString(rw, `<!doctype html><meta charset="utf-8"><title>验证成功</title><p>账号验证成功。仍需使用路由器自己的账号登录。</p><a href="/cgi-bin/luci/">打开路由器管理页面</a>`)
		return
	}
	cookie, err := r.Cookie(routerCookie)
	if err != nil {
		http.Redirect(rw, r, routerConsole+"/router-access?host="+url.QueryEscape(host), http.StatusSeeOther)
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" || r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "https://"+host {
		http.Error(rw, "跨站访问已拒绝", 403)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != "https://"+host {
		http.Error(rw, "管理操作需要同源验证", 403)
		return
	}
	var checked struct {
		Allowed bool  `json:"allowed"`
		Expires int64 `json:"expires"`
	}
	status := w.call(ctx, "/v1/agent/router-web/session", map[string]string{"host": host, "session": cookie.Value}, &checked)
	if status != 200 || !checked.Allowed || checked.Expires <= time.Now().Unix() {
		if status == 401 {
			http.SetCookie(rw, &http.Cookie{Name: routerCookie, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
			http.Redirect(rw, r, routerConsole+"/router-access?host="+url.QueryEscape(host), 303)
		} else {
			http.Error(rw, "暂时无法验证访问权限，请稍后重试", 503)
		}
		return
	}
	request := r.Clone(r.Context())
	request.Header = request.Header.Clone()
	request.Header.Del("Cookie")
	for _, c := range r.Cookies() {
		if c.Name != routerCookie {
			request.AddCookie(c)
		}
	}
	// Access grant and account credentials never reach uhttpd. Router credentials do.
	sessionContext, stop := context.WithDeadline(request.Context(), time.Unix(checked.Expires, 0))
	defer stop()
	// A long response or upgraded connection must not retain revoked access for
	// the rest of its 15-minute lifetime. Short requests finish before the first tick.
	go w.watchSession(sessionContext, stop, host, cookie.Value)
	proxy.ServeHTTP(rw, request.WithContext(sessionContext))
}

func (w *routerWeb) watchSession(ctx context.Context, cancel context.CancelFunc, host, session string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.mu.RLock()
			available := w.host == host && w.proxy != nil
			w.mu.RUnlock()
			if !available {
				cancel()
				return
			}
			check, done := context.WithTimeout(ctx, 3*time.Second)
			var result struct {
				Allowed bool  `json:"allowed"`
				Expires int64 `json:"expires"`
			}
			status := w.call(check, "/v1/agent/router-web/session", map[string]string{"host": host, "session": session}, &result)
			done()
			if status != 200 || !result.Allowed || result.Expires <= time.Now().Unix() {
				cancel()
				return
			}
		}
	}
}
