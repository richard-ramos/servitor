package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

type CredentialProxy struct {
	cfg      Config
	redactor *Redactor
	auth     AuthProvider
	server   *http.Server
	upstream *url.URL
	proxy    *httputil.ReverseProxy
}

func NewCredentialProxy(cfg Config, redactor *Redactor, auth AuthProvider) (*CredentialProxy, error) {
	up, err := url.Parse(cfg.ProxyUpstreamBaseURL())
	if err != nil {
		return nil, err
	}
	p := &CredentialProxy{
		cfg:      cfg,
		redactor: redactor,
		auth:     auth,
		upstream: up,
	}
	p.proxy = p.newReverseProxy()
	return p, nil
}

func (p *CredentialProxy) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handle)
	p.server = &http.Server{
		Addr:              p.cfg.ProxyListenAddr(),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", p.server.Addr)
	if err != nil {
		return err
	}
	go func() {
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Printf("credential proxy error: %s\n", p.redactor.Redact(err.Error()))
		}
	}()
	fmt.Printf("credential proxy listening on %s auth=%s\n", p.server.Addr, p.auth.Name())
	return nil
}

func (p *CredentialProxy) Shutdown(ctx context.Context) error {
	if p.server == nil {
		return nil
	}
	return p.server.Shutdown(ctx)
}

func (p *CredentialProxy) handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	status := http.StatusBadGateway
	defer func() {
		fmt.Printf("proxy %s %s status=%d duration=%s\n", r.Method, r.URL.Path, status, time.Since(start).Round(time.Millisecond))
	}()
	if !p.allowedProxyPath(r.URL.Path) {
		status = http.StatusForbidden
		http.Error(w, "forbidden upstream path", status)
		return
	}
	if !p.authorizedClient(r) {
		status = http.StatusUnauthorized
		http.Error(w, "unauthorized proxy client", status)
		return
	}
	authHeader, err := p.auth.Authorization(r.Context())
	if err != nil {
		status = http.StatusUnauthorized
		http.Error(w, p.redactor.Redact(err.Error()), status)
		return
	}
	r.Header.Set("X-Servitor-Upstream-Authorization", authHeader)
	p.proxy.ServeHTTP(&statusRecorder{ResponseWriter: w, status: &status}, r)
}

func (p *CredentialProxy) newReverseProxy() *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(p.upstream)
	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		inboundPath := req.URL.Path
		originalDirector(req)
		req.URL.Scheme = p.upstream.Scheme
		req.URL.Host = p.upstream.Host
		req.URL.Path = singleJoiningSlash(p.upstream.Path, p.trimProxyPrefix(inboundPath))
		if p.upstream.Path == "" || p.upstream.Path == "/" {
			req.URL.Path = inboundPath
		}
		req.Host = p.upstream.Host
		authHeader := req.Header.Get("X-Servitor-Upstream-Authorization")
		req.Header.Del("X-Servitor-Upstream-Authorization")
		stripAuthHeaders(req.Header)
		req.Header.Set("Authorization", authHeader)
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, p.redactor.Redact(err.Error()), http.StatusBadGateway)
	}
	return rp
}

func (p *CredentialProxy) allowedProxyPath(path string) bool {
	if p.cfg.CodexAuthMode == AuthModeChatGPT {
		return path == "/backend-api/codex" || strings.HasPrefix(path, "/backend-api/codex/")
	}
	return path == "/v1" || strings.HasPrefix(path, "/v1/")
}

func (p *CredentialProxy) trimProxyPrefix(path string) string {
	if p.cfg.CodexAuthMode == AuthModeChatGPT {
		return strings.TrimPrefix(path, "/backend-api/codex")
	}
	return strings.TrimPrefix(path, "/v1")
}

func (p *CredentialProxy) authorizedClient(r *http.Request) bool {
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	want := "Bearer " + p.cfg.OpenAIProxyClientToken
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

type statusRecorder struct {
	http.ResponseWriter
	status *int
}

func (r *statusRecorder) WriteHeader(status int) {
	*r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if *r.status == http.StatusBadGateway {
		*r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(p)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying response writer does not support hijacking")
	}
	return h.Hijack()
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func stripAuthHeaders(h http.Header) {
	for _, key := range []string{"Authorization", "Api-Key", "X-Api-Key", "OpenAI-Api-Key"} {
		h.Del(key)
	}
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}
