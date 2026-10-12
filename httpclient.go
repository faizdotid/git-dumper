package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

// idempotentMethods are the HTTP methods that are safe to retry
// automatically, mirroring urllib3's default allowed_methods.
var idempotentMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
	http.MethodPut:     true,
	http.MethodDelete:  true,
	http.MethodTrace:   true,
}

const (
	retryBackoff    = 200 * time.Millisecond
	retryBackoffMax = 2 * time.Second
)

// timeoutConn applies a per-read deadline, mirroring the read timeout of the
// Python requests library: the timeout applies between reads instead of
// capping the whole download, so large pack files on slow links still work.
type timeoutConn struct {
	net.Conn
	readTimeout time.Duration
}

func (c *timeoutConn) Read(b []byte) (int, error) {
	if c.readTimeout > 0 {
		if err := c.Conn.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Read(b)
}

// dialWithTimeout dials like dialer.DialContext but attaches a per-read
// deadline to the returned connection.
func dialWithTimeout(dialer *net.Dialer, readTimeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &timeoutConn{Conn: conn, readTimeout: readTimeout}, nil
	}
}

// newHTTPClient builds an http.Client mirroring the Python requests.Session:
// no redirect following, a cookie jar, custom headers/method, optional proxy
// (http/https/socks4/socks5, falling back to the environment when unset) and
// optional PKCS#12 client certificate. TLS certificate verification is skipped
// when -k/--insecure is given.
func newHTTPClient(cfg *Config) (*http.Client, error) {
	timeout := time.Duration(cfg.Timeout) * time.Second

	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: cfg.SkipVerify},
		DialContext:           dialWithTimeout(dialer, timeout),
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		// the default of 2 would constantly close and redial connections
		// when running many parallel workers against a single host
		MaxIdleConnsPerHost: cfg.Jobs,
	}

	// proxy
	if cfg.Proxy != "" {
		proxyURL, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy, got `%s`", cfg.Proxy)
		}
		switch proxyURL.Scheme {
		case "http", "https", "socks5":
			transport.Proxy = http.ProxyURL(proxyURL)
		case "socks4":
			dialSocks4 := socks4DialContext(dialer, proxyURL.Host)
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := dialSocks4(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &timeoutConn{Conn: conn, readTimeout: timeout}, nil
			}
		default:
			return nil, fmt.Errorf("invalid proxy, got `%s`", cfg.Proxy)
		}
	} else {
		// requests honors HTTP_PROXY/HTTPS_PROXY/NO_PROXY by default
		transport.Proxy = http.ProxyFromEnvironment
	}

	// client certificate (PKCS#12)
	if cfg.ClientCertP12 != "" {
		p12Data, err := os.ReadFile(cfg.ClientCertP12)
		if err != nil {
			return nil, err
		}
		key, cert, caCerts, err := pkcs12.DecodeChain(p12Data, cfg.ClientCertP12Password)
		if err != nil {
			return nil, fmt.Errorf("unable to load client certificate `%s`: %s", cfg.ClientCertP12, err)
		}
		tlsCert := tls.Certificate{
			Certificate: [][]byte{cert.Raw},
			PrivateKey:  key,
			Leaf:        cert,
		}
		for _, ca := range caCerts {
			tlsCert.Certificate = append(tlsCert.Certificate, ca.Raw)
		}
		transport.TLSClientConfig.Certificates = []tls.Certificate{tlsCert}
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	return &http.Client{
		Transport: transport,
		Jar:       jar,
		// allow_redirects=False
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// retryDelay returns the exponential backoff before retry attempt n (n >= 1).
func retryDelay(attempt int) time.Duration {
	delay := retryBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= retryBackoffMax {
			return retryBackoffMax
		}
	}
	return delay
}

// request performs a single HTTP request, retrying on transport errors up to
// cfg.Retry times with exponential backoff. Non-idempotent methods are not
// retried automatically.
func (f *fetcher) request(path string) (*http.Response, error) {
	reqURL := fmt.Sprintf("%s/%s", f.cfg.URL, path)

	attempts := f.cfg.Retry
	if !idempotentMethods[f.cfg.Method] {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(retryDelay(attempt))
		}

		req, err := http.NewRequest(f.cfg.Method, reqURL, nil)
		if err != nil {
			return nil, err
		}
		for name, value := range f.cfg.Headers {
			req.Header.Set(name, value)
		}

		resp, err := f.client.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
