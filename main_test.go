package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"http://example.com/.git/", "http://example.com"},
		{"http://example.com/.git", "http://example.com"},
		{"http://example.com/.git/HEAD", "http://example.com"},
		{"http://example.com/HEAD", "http://example.com"},
		{"http://example.com/", "http://example.com"},
		{"http://example.com", "http://example.com"},
		{"http://example.com/repo/.git/", "http://example.com/repo"},
	}
	for _, tt := range tests {
		if got := normalizeURL(tt.in); got != tt.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHeadRegex(t *testing.T) {
	valid := []string{
		"ref: refs/heads/main",
		"ref: refs/heads/feature/x",
		"0123456789abcdef0123456789abcdef01234567",
	}
	for _, s := range valid {
		if !headRegex.MatchString(strings.TrimSpace(s)) {
			t.Errorf("headRegex should match %q", s)
		}
	}

	invalid := []string{
		"",
		"not a head",
		"0123456789abcdef0123456789abcdef0123456",   // 39 chars
		"0123456789abcdef0123456789abcdef012345678", // 41 chars
		"ref: refs/heads/main\njunk",                // extra line
		"ref: refs/heads/main extra\njunk",
		"HEAD",
	}
	for _, s := range invalid {
		if headRegex.MatchString(strings.TrimSpace(s)) {
			t.Errorf("headRegex should not match %q", s)
		}
	}
}

func TestObjectSHAs(t *testing.T) {
	dir := t.TempDir()
	shaA := "1111111111111111111111111111111111111111"
	shaB := "2222222222222222222222222222222222222222"

	files := map[string]string{
		"zero": " " + zeroSHA1 + " \n",
		"a":    shaA + "\n",
		"b":    " " + shaB + " \n",
	}
	var paths []string
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	paths = append(paths, filepath.Join(dir, "missing"))

	objs := objectSHAs(paths)
	if len(objs) != 2 {
		t.Fatalf("objectSHAs returned %d objects, want 2: %v", len(objs), objs)
	}
	if !objs[shaA] || !objs[shaB] {
		t.Errorf("objectSHAs missing expected SHAs: %v", objs)
	}
	if objs[zeroSHA1] {
		t.Error("objectSHAs must skip the null object id")
	}
}

func TestGitCheckoutEnv(t *testing.T) {
	env := gitCheckoutEnv("socks5://localhost:9050")
	values := make(map[string]string)
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok {
			values[name] = value
		}
	}

	want := map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_ATTR_NOSYSTEM":   "1",
		"GIT_TERMINAL_PROMPT": "0",
		"ALL_PROXY":           "socks5://localhost:9050",
	}
	for name, value := range want {
		if values[name] != value {
			t.Errorf("gitCheckoutEnv()[%s] = %q, want %q", name, values[name], value)
		}
	}
}

func TestEnsureGitDirs(t *testing.T) {
	dir := t.TempDir()
	if err := ensureGitDirs(dir); err != nil {
		t.Fatalf("ensureGitDirs: %s", err)
	}
	for _, path := range []string{
		filepath.Join(dir, ".git", "refs"),
		filepath.Join(dir, ".git", "objects"),
	} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Errorf("directory %s was not created: %v", path, err)
		}
	}
	// idempotent
	if err := ensureGitDirs(dir); err != nil {
		t.Errorf("ensureGitDirs is not idempotent: %s", err)
	}
}

func TestParseArgs(t *testing.T) {
	outDir := func(t *testing.T) string {
		return filepath.Join(t.TempDir(), "out")
	}

	t.Run("flags before positionals", func(t *testing.T) {
		dir := outDir(t)
		cfg, err := parseArgs([]string{"-j", "2", "-H", "X-Test=1", "http://example.com/.git/", dir})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.URL != "http://example.com/.git/" || cfg.Directory != dir {
			t.Errorf("positionals parsed wrong: %q %q", cfg.URL, cfg.Directory)
		}
		if cfg.Jobs != 2 {
			t.Errorf("jobs = %d, want 2", cfg.Jobs)
		}
		if cfg.Headers["X-Test"] != "1" {
			t.Errorf("custom header not parsed: %v", cfg.Headers)
		}
		if cfg.Headers["User-Agent"] != defaultUserAgent {
			t.Errorf("default user agent missing: %v", cfg.Headers)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("output directory was not created: %v", err)
		}
	})

	t.Run("positionals before flags", func(t *testing.T) {
		dir := outDir(t)
		cfg, err := parseArgs([]string{"http://x", dir, "-j", "3"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Jobs != 3 {
			t.Errorf("jobs = %d, want 3", cfg.Jobs)
		}
	})

	t.Run("double dash", func(t *testing.T) {
		dir := outDir(t)
		cfg, err := parseArgs([]string{"--", "-not-a-flag", dir})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.URL != "-not-a-flag" {
			t.Errorf("url = %q, want %q", cfg.URL, "-not-a-flag")
		}
	})

	t.Run("help", func(t *testing.T) {
		for _, args := range [][]string{{"-h"}, {"--help"}, {"-help"}} {
			cfg, err := parseArgs(args)
			if err != nil {
				t.Fatalf("parseArgs(%v): %s", args, err)
			}
			if !cfg.Help {
				t.Errorf("parseArgs(%v): help not set", args)
			}
		}
	})

	t.Run("help after positionals", func(t *testing.T) {
		dir := outDir(t)
		cfg, err := parseArgs([]string{"http://x", dir, "--help"})
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Help {
			t.Error("help not set")
		}
	})

	t.Run("method is uppercased", func(t *testing.T) {
		dir := outDir(t)
		cfg, err := parseArgs([]string{"-X", "post", "http://x", dir})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Method != "POST" {
			t.Errorf("method = %q, want POST", cfg.Method)
		}
	})

	t.Run("max size", func(t *testing.T) {
		dir := outDir(t)
		cfg, err := parseArgs([]string{"--max-size", "1024", "http://x", dir})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MaxSize != 1024 {
			t.Errorf("max-size = %d, want 1024", cfg.MaxSize)
		}
	})

	t.Run("proxy schemes", func(t *testing.T) {
		tests := []struct {
			arg, want string
		}{
			{"localhost:1080", "socks5://localhost:1080"},
			{"socks5:localhost:1080", "socks5://localhost:1080"},
			{"socks4:1.2.3.4:9050", "socks4://1.2.3.4:9050"},
			{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		}
		for _, tt := range tests {
			dir := outDir(t)
			cfg, err := parseArgs([]string{"--proxy", tt.arg, "http://x", dir})
			if err != nil {
				t.Fatalf("parseArgs(proxy %q): %s", tt.arg, err)
			}
			if cfg.Proxy != tt.want {
				t.Errorf("proxy %q normalized to %q, want %q", tt.arg, cfg.Proxy, tt.want)
			}
		}
	})

	t.Run("invalid arguments", func(t *testing.T) {
		certFile := filepath.Join(t.TempDir(), "cert.p12")
		if err := os.WriteFile(certFile, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		tests := []struct {
			name    string
			args    []string
			wantErr string
		}{
			{"no arguments", nil, "expected URL and DIR"},
			{"one positional", []string{"http://x"}, "expected URL and DIR"},
			{"invalid jobs", []string{"-j", "0", "http://x", "out"}, "invalid number of jobs"},
			{"invalid retry", []string{"-r", "0", "http://x", "out"}, "invalid number of retries"},
			{"invalid timeout", []string{"-t", "0", "http://x", "out"}, "invalid timeout"},
			{"invalid max size", []string{"--max-size", "-1", "http://x", "out"}, "invalid max-size"},
			{"invalid header", []string{"-H", "NOEQUALS", "http://x", "out"}, "http header must have the form"},
			{"invalid proxy", []string{"--proxy", "not-a-proxy", "http://x", "out"}, "invalid proxy"},
			{"unknown flag", []string{"-z", "http://x", "out"}, "flag provided but not defined"},
			{"missing cert", []string{"--client-cert-p12", certFile + ".missing", "--client-cert-p12-password", "x", "http://x", "out"}, "does not exist"},
			{"missing cert password", []string{"--client-cert-p12", certFile, "http://x", "out"}, "password is required"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := parseArgs(tt.args)
				if err == nil {
					t.Fatalf("parseArgs(%v): expected error", tt.args)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("parseArgs(%v) error = %q, want containing %q", tt.args, err, tt.wantErr)
				}
			})
		}
	})
}
