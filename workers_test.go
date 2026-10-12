package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testHTTPResponse(status int, headers map[string]string, body string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestIsHTML(t *testing.T) {
	tests := []struct {
		contentType string
		want        bool
	}{
		{"text/html", true},
		{"text/html; charset=utf-8", true},
		{"application/octet-stream", false},
		{"text/plain", false},
		{"", false},
	}
	for _, tt := range tests {
		resp := testHTTPResponse(200, map[string]string{"Content-Type": tt.contentType}, "")
		if got := isHTML(resp); got != tt.want {
			t.Errorf("isHTML(%q) = %v, want %v", tt.contentType, got, tt.want)
		}
	}
}

func TestVerifyResponse(t *testing.T) {
	tests := []struct {
		name      string
		resp      *http.Response
		anyStatus bool
		wantValid bool
	}{
		{"ok", testHTTPResponse(200, map[string]string{"Content-Type": "application/octet-stream"}, "x"), false, true},
		{"unexpected status", testHTTPResponse(404, map[string]string{"Content-Type": "text/plain"}, "x"), false, false},
		{"any status accepts 404", testHTTPResponse(404, map[string]string{"Content-Type": "text/plain"}, "x"), true, true},
		{"zero length", testHTTPResponse(200, map[string]string{"Content-Length": "0"}, ""), false, false},
		{"html", testHTTPResponse(200, map[string]string{"Content-Type": "text/html"}, "<html></html>"), false, false},
		{"html rejected even with any status", testHTTPResponse(500, map[string]string{"Content-Type": "text/html"}, "<html></html>"), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, reason := verifyResponse(tt.resp, tt.anyStatus)
			if valid != tt.wantValid {
				t.Fatalf("verifyResponse() valid = %v, want %v", valid, tt.wantValid)
			}
			if valid && reason != nil {
				t.Error("valid response must not carry a reject reason")
			}
			if !valid && reason == nil {
				t.Error("rejected response must carry a reject reason")
			}
		})
	}
}

func TestRejectReasonArgs(t *testing.T) {
	_, reason := verifyResponse(testHTTPResponse(404, nil, "x"), false)
	if reason == nil || len(reason.args) != 1 || reason.args[0] != 404 {
		t.Errorf("status reason args = %#v, want [404]", reason)
	}

	_, reason = verifyResponse(testHTTPResponse(200, map[string]string{"Content-Type": "text/html"}, "<html>"), false)
	if reason == nil || len(reason.args) != 1 || reason.args[0] != "text/html" {
		t.Errorf("html reason args = %#v, want [text/html]", reason)
	}

	_, reason = verifyResponse(testHTTPResponse(200, map[string]string{"Content-Length": "0"}, ""), false)
	if reason == nil || len(reason.args) != 0 {
		t.Errorf("zero-length reason args = %#v, want none", reason)
	}
}

func TestGetIndexedFiles(t *testing.T) {
	page := `<html><body>
		<a href="HEAD">HEAD</a>
		<a href="objects/">objects/</a>
		<a href="../">../</a>
		<a href="/abs/path">abs</a>
		<a href="//evil.example/x">protocol relative</a>
		<a href="http://evil.example/x">absolute</a>
		<a href="a/b.txt">file</a>
		<a href="">empty</a>
		<a href="%2e%2e/escape">encoded traversal</a>
	</body></html>`

	got := getIndexedFiles(strings.NewReader(page))
	want := []string{"HEAD", "objects/", "a/b.txt"}
	if len(got) != len(want) {
		t.Fatalf("getIndexedFiles() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("getIndexedFiles()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriteResponseMaxSize(t *testing.T) {
	dir := t.TempDir()
	f := &fetcher{cfg: &Config{Directory: dir, MaxSize: 5}}

	resp := testHTTPResponse(200, nil, "0123456789")
	if err := f.writeResponse(resp, "big.bin"); err == nil {
		t.Fatal("expected an error for an oversized response")
	} else if !strings.Contains(err.Error(), "--max-size") {
		t.Errorf("error = %q, want mention of --max-size", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err == nil {
		t.Error("partial file was not removed")
	}

	resp = testHTTPResponse(200, nil, "abc")
	if err := f.writeResponse(resp, "small.bin"); err != nil {
		t.Fatalf("writeResponse: %s", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "small.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc" {
		t.Errorf("content = %q, want %q", got, "abc")
	}
}

func TestWriteResponseUnlimited(t *testing.T) {
	dir := t.TempDir()
	f := &fetcher{cfg: &Config{Directory: dir}}

	resp := testHTTPResponse(200, nil, "0123456789")
	if err := f.writeResponse(resp, "file.bin"); err != nil {
		t.Fatalf("writeResponse: %s", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "file.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "0123456789" {
		t.Errorf("content = %q", got)
	}
}

func TestFindObjectsTaskInvalidSHA(t *testing.T) {
	f := &fetcher{
		cfg:    &Config{URL: "http://127.0.0.1:1", Directory: t.TempDir()},
		client: http.DefaultClient,
	}
	for _, obj := range []string{"", "zz", "abc", "g123456789abcdef0123456789abcdef01234567", "0123456789abcdef0123456789abcdef0123456"} {
		if got := f.findObjectsTask(obj); got != nil {
			t.Errorf("findObjectsTask(%q) = %v, want nil", obj, got)
		}
	}
}

func TestRecursiveDownloadTaskRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", ".git/objects/ab/")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer srv.Close()

	cfg := &Config{
		URL: srv.URL, Directory: t.TempDir(), Jobs: 1, Retry: 1, Timeout: 5,
		Method: "GET", Headers: map[string]string{"User-Agent": "test"},
	}
	client, err := newHTTPClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f := &fetcher{cfg: cfg, client: client}

	got := f.recursiveDownloadTask(".git/objects/ab")
	if len(got) != 1 || got[0] != ".git/objects/ab/" {
		t.Errorf("recursiveDownloadTask() = %v, want [.git/objects/ab/]", got)
	}
}

func TestRecursiveDownloadTaskDirectoryIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><a href="abc123">abc123</a><a href="../">../</a></body></html>`)
	}))
	defer srv.Close()

	cfg := &Config{
		URL: srv.URL, Directory: t.TempDir(), Jobs: 1, Retry: 1, Timeout: 5,
		Method: "GET", Headers: map[string]string{"User-Agent": "test"},
	}
	client, err := newHTTPClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f := &fetcher{cfg: cfg, client: client}

	got := f.recursiveDownloadTask(".git/objects/ab/")
	if len(got) != 1 || got[0] != ".git/objects/ab/abc123" {
		t.Errorf("recursiveDownloadTask() = %v, want [.git/objects/ab/abc123]", got)
	}
}
