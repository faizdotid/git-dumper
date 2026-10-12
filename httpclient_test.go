package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// newTestFetcher builds a fetcher with sane test defaults; zero-valued fields
// are filled in.
func newTestFetcher(t *testing.T, srv *httptest.Server, cfg *Config) *fetcher {
	t.Helper()
	if srv != nil && cfg.URL == "" {
		cfg.URL = srv.URL
	}
	if cfg.Directory == "" {
		cfg.Directory = t.TempDir()
	}
	if cfg.Method == "" {
		cfg.Method = http.MethodGet
	}
	if cfg.Jobs == 0 {
		cfg.Jobs = 1
	}
	if cfg.Retry == 0 {
		cfg.Retry = 1
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{"User-Agent": "test"}
	}

	client, err := newHTTPClient(cfg)
	if err != nil {
		t.Fatalf("newHTTPClient: %s", err)
	}
	return &fetcher{cfg: cfg, client: client}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	f := newTestFetcher(t, srv, &Config{})
	resp, err := f.request("x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want %d (redirects must not be followed)", resp.StatusCode, http.StatusFound)
	}
}

func TestClientKeepsCookies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "secret"})
			io.WriteString(w, "set")
			return
		}
		cookie, err := r.Cookie("sid")
		if err != nil {
			http.Error(w, "missing cookie", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, cookie.Value)
	}))
	defer srv.Close()

	f := newTestFetcher(t, srv, &Config{})

	resp, err := f.request("set")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	resp, err = f.request("check")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "secret" {
		t.Errorf("second request body = %q, want the stored cookie %q", body, "secret")
	}
}

func TestProxyFromEnvironmentWhenUnset(t *testing.T) {
	f := newTestFetcher(t, nil, &Config{})
	tr, ok := f.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", f.client.Transport)
	}
	if tr.Proxy == nil {
		t.Error("expected environment proxy fallback when --proxy is not given")
	}
}

func TestExplicitProxy(t *testing.T) {
	f := newTestFetcher(t, nil, &Config{Proxy: "http://127.0.0.1:9"})
	tr := f.client.Transport.(*http.Transport)
	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := tr.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:9" {
		t.Errorf("proxy = %v, want http://127.0.0.1:9", proxyURL)
	}
}

func TestSocks4ProxyCreatesClient(t *testing.T) {
	if _, err := newHTTPClient(&Config{Proxy: "socks4://127.0.0.1:9", Timeout: 1}); err != nil {
		t.Errorf("newHTTPClient with socks4 proxy: %s", err)
	}
}

func TestInvalidProxyScheme(t *testing.T) {
	if _, err := newHTTPClient(&Config{Proxy: "ftp://127.0.0.1:9", Timeout: 1}); err == nil {
		t.Error("expected an error for an unsupported proxy scheme")
	}
}

func TestRequestBodyReadTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(2 * time.Second) // longer than the 1s timeout
		io.WriteString(w, "body")
	}))
	defer srv.Close()

	f := newTestFetcher(t, srv, &Config{Timeout: 1})
	resp, err := f.request("slow")
	if err != nil {
		t.Fatalf("request failed before reading the body: %s", err)
	}
	defer resp.Body.Close()

	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("expected a read timeout error while reading the stalled body")
	}
}

func TestRequestRetriesTransportErrors(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %s", err)
				return
			}
			conn.Close()
			return
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	f := newTestFetcher(t, srv, &Config{Retry: 3})

	start := time.Now()
	resp, err := f.request("x")
	if err != nil {
		t.Fatalf("request failed after retries: %s", err)
	}
	defer resp.Body.Close()

	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Errorf("elapsed = %s, expected exponential backoff delays", elapsed)
	}
}

func TestRequestDoesNotRetryNonIdempotentMethods(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %s", err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	f := newTestFetcher(t, srv, &Config{Method: http.MethodPost, Retry: 5})
	if _, err := f.request("x"); err == nil {
		t.Fatal("expected the POST request to fail")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 (POST must not be retried)", got)
	}
}

func TestRetryDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{4, 1600 * time.Millisecond},
		{5, 2000 * time.Millisecond},
		{50, 2000 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := retryDelay(tt.attempt); got != tt.want {
			t.Errorf("retryDelay(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestIdempotentMethods(t *testing.T) {
	idempotent := []string{"GET", "HEAD", "OPTIONS", "PUT", "DELETE", "TRACE"}
	nonIdempotent := []string{"POST", "PATCH", "CONNECT"}
	for _, m := range idempotent {
		if !idempotentMethods[m] {
			t.Errorf("%s should be retryable", m)
		}
	}
	for _, m := range nonIdempotent {
		if idempotentMethods[m] {
			t.Errorf("%s should not be retryable", m)
		}
	}
}

func TestExplicitProxyURLRoundTrip(t *testing.T) {
	u, err := url.Parse("socks5://localhost:1080")
	if err != nil {
		t.Fatal(err)
	}
	f := newTestFetcher(t, nil, &Config{Proxy: u.String()})
	tr := f.client.Transport.(*http.Transport)
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	got, err := tr.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "socks5://localhost:1080" {
		t.Errorf("proxy = %v", got)
	}
}
