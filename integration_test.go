package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// initFixtureRepo creates a small git repository with go-git, so the tests do
// not need the git binary to build fixtures.
func initFixtureRepo(t *testing.T, dir string) plumbing.Hash {
	t.Helper()

	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "sub/b.txt"} {
		if _, err := wt.Add(name); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// noListingHandler serves files under root but returns 404 for directories,
// like a web server with directory listing disabled.
func noListingHandler(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" || strings.HasSuffix(r.URL.Path, "/") || strings.Contains(path, "..") {
			http.NotFound(w, r)
			return
		}
		full := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, full)
	})
}

func TestEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available for the final checkout")
	}

	src := t.TempDir()
	head := initFixtureRepo(t, src)

	modes := []struct {
		name    string
		handler http.Handler
	}{
		{"with directory listing", http.FileServer(http.Dir(src))},
		{"without directory listing", noListingHandler(src)},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			srv := httptest.NewServer(mode.handler)
			defer srv.Close()

			dump := filepath.Join(t.TempDir(), "dump")
			cfg := &Config{
				URL:       srv.URL,
				Directory: dump,
				Jobs:      4,
				Retry:     2,
				Timeout:   10,
				Method:    http.MethodGet,
				Headers:   map[string]string{"User-Agent": "git-dumper-test"},
			}
			if rc := fetchGit(cfg); rc != 0 {
				t.Fatalf("fetchGit returned %d", rc)
			}

			repo, err := git.PlainOpen(dump)
			if err != nil {
				t.Fatalf("dumped directory is not a git repository: %s", err)
			}
			ref, err := repo.Head()
			if err != nil {
				t.Fatal(err)
			}
			if ref.Hash() != head {
				t.Errorf("HEAD = %s, want %s", ref.Hash(), head)
			}

			for name, want := range map[string]string{"a.txt": "hello\n", "sub/b.txt": "world\n"} {
				content, err := os.ReadFile(filepath.Join(dump, filepath.FromSlash(name)))
				if err != nil {
					t.Errorf("checkout did not restore %s: %s", name, err)
					continue
				}
				if string(content) != want {
					t.Errorf("%s = %q, want %q", name, content, want)
				}
			}
		})
	}
}

// sanity check that the test fixture server actually serves HEAD before
// running the full end-to-end test
func TestFixtureServer(t *testing.T) {
	src := t.TempDir()
	initFixtureRepo(t, src)

	handlers := map[string]http.Handler{
		"listing":    http.FileServer(http.Dir(src)),
		"no listing": noListingHandler(src),
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()

			resp, err := http.Get(fmt.Sprintf("%s/.git/HEAD", srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "ref: ") {
				t.Errorf("GET /.git/HEAD = %d %q", resp.StatusCode, body)
			}
		})
	}
}
