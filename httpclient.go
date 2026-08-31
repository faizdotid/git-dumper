package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"h12.io/socks"
	"software.sslmate.com/src/go-pkcs12"
)

// newHTTPClient builds an http.Client mirroring the Python requests.Session:
// no redirect following, custom headers/method, optional proxy
// (http/socks4/socks5) and optional PKCS#12 client certificate. TLS
// certificate verification is skipped when -k/--insecure is given.
func newHTTPClient(cfg *Config) (*http.Client, error) {
	timeout := time.Duration(cfg.Timeout) * time.Second

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.SkipVerify},
		DialContext: (&net.Dialer{
			Timeout: timeout,
		}).DialContext,
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
			dialSocks := socks.Dial(fmt.Sprintf("socks4://%s", proxyURL.Host))
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialSocks(network, addr)
			}
		default:
			return nil, fmt.Errorf("invalid proxy, got `%s`", cfg.Proxy)
		}
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

	return &http.Client{
		Transport: transport,
		// allow_redirects=False
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// request performs a single HTTP request, retrying on transport errors
// up to cfg.Retry times (mirrors requests' max_retries).
func (f *fetcher) request(path string) (*http.Response, error) {
	reqURL := fmt.Sprintf("%s/%s", f.cfg.URL, path)

	var lastErr error
	for attempt := 0; attempt < f.cfg.Retry; attempt++ {
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
