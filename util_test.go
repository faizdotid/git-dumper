package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSafePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"a/b", true},
		{".git/config", true},
		{"a/./b", true},
		{"refs/heads/main", true},
		{"", false},
		{"/etc/passwd", false},
		{"//host/path", false},
		{"../x", false},
		{"a/../../b", false},
		{"a/..", false},
		{"..", false},
		{`a\..\b`, false},
		{`..\x`, false},
	}
	for _, tt := range tests {
		if got := isSafePath(tt.path); got != tt.want {
			t.Errorf("isSafePath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestIsSHA1(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"0123456789abcdef0123456789abcdef01234567", true},
		{zeroSHA1, true},
		{"0123456789ABCDEF0123456789abcdef01234567", false},
		{"0123456789abcdef0123456789abcdef0123456", false},
		{"0123456789abcdef0123456789abcdef012345678", false},
		{"g123456789abcdef0123456789abcdef01234567", false},
		{"", false},
		{"HEAD", false},
	}
	for _, tt := range tests {
		if got := isSHA1(tt.s); got != tt.want {
			t.Errorf("isSHA1(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestSanitizeConfigContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "section form",
			input: "[core]\n\teditor = vim\n",
			want:  "[core]\n# \teditor = vim\n",
		},
		{
			name:  "blank line before unsafe key",
			input: "[core]\n\n\tfsmonitor = /tmp/evil\n",
			want:  "[core]\n\n# \tfsmonitor = /tmp/evil\n",
		},
		{
			name:  "dotted key at top level",
			input: "core.fsmonitor = true\n",
			want:  "# core.fsmonitor = true\n",
		},
		{
			name:  "dotted key core.editor",
			input: "core.editor = /tmp/evil\n",
			want:  "# core.editor = /tmp/evil\n",
		},
		{
			name:  "case insensitive",
			input: "[CORE]\n\tEditor = vim\n",
			want:  "[CORE]\n# \tEditor = vim\n",
		},
		{
			name:  "safe key untouched",
			input: "[user]\n\tname = editor\n",
			want:  "[user]\n\tname = editor\n",
		},
		{
			name:  "similar key untouched",
			input: "[core]\n\teditorconfig = x\n",
			want:  "[core]\n\teditorconfig = x\n",
		},
		{
			name:  "include section",
			input: "[include]\n\tpath = /tmp/evil.conf\n",
			want:  "[include]\n# \tpath = /tmp/evil.conf\n",
		},
		{
			name:  "includeIf section",
			input: "[includeIf \"gitdir:/tmp\"]\n\tpath = /tmp/evil.conf\n",
			want:  "[includeIf \"gitdir:/tmp\"]\n# \tpath = /tmp/evil.conf\n",
		},
		{
			name:  "dotted include path",
			input: "include.path = /tmp/evil.conf\n",
			want:  "# include.path = /tmp/evil.conf\n",
		},
		{
			name:  "filter section",
			input: "[filter \"lfs\"]\n\tsmudge = cmd\n\trequired = true\n",
			want:  "[filter \"lfs\"]\n# \tsmudge = cmd\n# \trequired = true\n",
		},
		{
			name:  "dotted filter key",
			input: "filter.lfs.process = cmd\n",
			want:  "# filter.lfs.process = cmd\n",
		},
		{
			name:  "safe filter key",
			input: "[filter \"x\"]\n\tname = y\n",
			want:  "[filter \"x\"]\n\tname = y\n",
		},
		{
			name:  "hookspath",
			input: "[core]\n\thooksPath = /tmp/hooks\n",
			want:  "[core]\n# \thooksPath = /tmp/hooks\n",
		},
		{
			name:  "line continuation",
			input: "[core]\n\teditor = vim \\\n\t\t--noplugin\n",
			want:  "[core]\n# \teditor = vim \\\n# \t\t--noplugin\n",
		},
		{
			name:  "crlf preserved",
			input: "[core]\r\n\teditor = vim\r\n",
			want:  "[core]\r\n# \teditor = vim\r\n",
		},
		{
			name:  "commented lines untouched",
			input: "[core]\n# editor = vim\n",
			want:  "[core]\n# editor = vim\n",
		},
		{
			name:  "keyword in value untouched",
			input: "[remote \"origin\"]\n\turl = https://example.com/pager\n",
			want:  "[remote \"origin\"]\n\turl = https://example.com/pager\n",
		},
		{
			name:  "no trailing newline",
			input: "[core]\n\teditor = x",
			want:  "[core]\n# \teditor = x",
		},
		{
			name:  "unsafe key after safe section",
			input: "[core]\n\tbare = false\n[filter \"lfs\"]\n\tclean = cmd\n",
			want:  "[core]\n\tbare = false\n[filter \"lfs\"]\n# \tclean = cmd\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(sanitizeConfigContent([]byte(tt.input)))
			if got != tt.want {
				t.Errorf("sanitizeConfigContent(%q):\n got: %q\nwant: %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeConfigContentIdempotent(t *testing.T) {
	content := []byte("[core]\n\teditor = vim\n[user]\n\tname = x\n")
	once := sanitizeConfigContent(content)
	twice := sanitizeConfigContent(once)
	if string(once) != string(twice) {
		t.Errorf("sanitization is not idempotent:\n first: %q\nsecond: %q", once, twice)
	}
}

func TestSanitizeFilePreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("[core]\n\teditor = vim\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := sanitizeFile(path); err != nil {
		t.Fatalf("sanitizeFile: %s", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("permissions changed to %o, want 600", got)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[core]\n# \teditor = vim\n"; string(content) != want {
		t.Errorf("content = %q, want %q", content, want)
	}
}

func TestSanitizeFileKeepsCleanFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	content := []byte("[core]\n\trepositoryformatversion = 0\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sanitizeFile(path); err != nil {
		t.Fatalf("sanitizeFile: %s", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("clean file was modified: %q", got)
	}
}

func TestSanitizeFileMissing(t *testing.T) {
	if err := sanitizeFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("sanitizeFile on missing file: expected error")
	}
}

func TestParseSectionHeader(t *testing.T) {
	tests := []struct {
		line      string
		wantName  string
		wantMatch bool
	}{
		{"[core]", "core", true},
		{"  [core]  ", "core", true},
		{"[core \"sub\"]", "core", true},
		{"[filter \"lfs\"]", "filter", true},
		{"[CORE]", "core", true},
		{"core = x", "", false},
		{"[unterminated", "", false},
		{"[]", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		name, ok := parseSectionHeader(tt.line)
		if ok != tt.wantMatch || name != tt.wantName {
			t.Errorf("parseSectionHeader(%q) = (%q, %v), want (%q, %v)", tt.line, name, ok, tt.wantName, tt.wantMatch)
		}
	}
}

func TestParseConfigKey(t *testing.T) {
	tests := []struct {
		line      string
		wantKey   string
		wantMatch bool
	}{
		{"editor = vim", "editor", true},
		{"  Editor = vim  ", "editor", true},
		{"core.editor=vim", "core.editor", true},
		{"# editor = vim", "", false},
		{"; editor = vim", "", false},
		{"no equals sign", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		key, ok := parseConfigKey(tt.line)
		if ok != tt.wantMatch || key != tt.wantKey {
			t.Errorf("parseConfigKey(%q) = (%q, %v), want (%q, %v)", tt.line, key, ok, tt.wantKey, tt.wantMatch)
		}
	}
}

func TestIsUnsafeConfigKey(t *testing.T) {
	tests := []struct {
		section string
		key     string
		want    bool
	}{
		{"core", "fsmonitor", true},
		{"core", "editor", true},
		{"core", "bare", false},
		{"filter", "smudge", true},
		{"filter", "name", false},
		{"include", "path", true},
		{"includeif", "path", true},
		{"remote", "url", false},
		{"core", "core.editor", true},
		{"filter", "filter.lfs.smudge", true},
		{"", "core.fsmonitor", true},
		{"", "core.pager", true},
		{"", "filter.lfs.clean", true},
		{"", "include.path", true},
		{"", "includeif.gitdir:/tmp.path", true},
		{"", "user.name", false},
		{"", "core", false},
	}
	for _, tt := range tests {
		if got := isUnsafeConfigKey(tt.section, tt.key); got != tt.want {
			t.Errorf("isUnsafeConfigKey(%q, %q) = %v, want %v", tt.section, tt.key, got, tt.want)
		}
	}
}
