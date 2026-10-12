package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Log prefixes keep the output scannable:
//
//	[*] progress / info        (stdout)
//	[=] skipped, already done  (stdout)
//	[!] warning, recoverable   (stderr)
//	[-] failure / rejection    (stderr)
var logMu sync.Mutex

func printf(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintf(os.Stdout, format, args...)
}

func eprintf(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintf(os.Stderr, format, args...)
}

// isSafePath prevents directory traversal attacks: reject empty and absolute
// paths, paths with a Windows separator (which could bypass the ".." check on
// other platforms) and any path escaping its base via "..".
func isSafePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return false
	}
	for _, elem := range strings.Split(path, "/") {
		if elem == ".." {
			return false
		}
	}
	return true
}

// isSHA1 reports whether s is a 40-character lowercase hex SHA1.
func isSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// createIntermediateDirs creates intermediate directories, if necessary.
func createIntermediateDirs(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

// unsafeCoreKeys are [core] configuration keys that can execute external
// commands or influence git when it runs in an untrusted repository. They are
// commented out before `git checkout .` is executed.
var unsafeCoreKeys = map[string]bool{
	"fsmonitor":  true,
	"sshcommand": true,
	"askpass":    true,
	"editor":     true,
	"pager":      true,
	"hookspath":  true,
}

// unsafeFilterKeys are keys of a [filter "<name>"] section that execute
// external commands during checkout.
var unsafeFilterKeys = map[string]bool{
	"smudge":   true,
	"clean":    true,
	"process":  true,
	"required": true,
}

// parseSectionHeader returns the lowercased section name of a
// `[section]` or `[section "subsection"]` line.
func parseSectionHeader(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") {
		return "", false
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return "", false
	}
	name := strings.TrimSpace(line[1:end])
	if i := strings.IndexAny(name, " \t\""); i >= 0 {
		name = name[:i]
	}
	if name == "" {
		return "", false
	}
	return strings.ToLower(name), true
}

// parseConfigKey returns the lowercased key of a `key = value` line.
func parseConfigKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
		return "", false
	}
	i := strings.Index(trimmed, "=")
	if i < 0 {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(trimmed[:i])), true
}

// isUnsafeConfigKey reports whether the given config key must be commented
// out. It handles both section form (`[core]` + `editor = ...`) and dotted
// form (`core.editor = ...`); git itself only accepts the dotted form on the
// command line, but matching it anywhere is cheap defense in depth.
func isUnsafeConfigKey(section, key string) bool {
	// section form
	switch section {
	case "core":
		if unsafeCoreKeys[key] {
			return true
		}
	case "filter":
		if unsafeFilterKeys[key] {
			return true
		}
	case "include", "includeif":
		if key == "path" {
			return true
		}
	}

	// dotted form, e.g. `core.fsmonitor = ...`
	if rest, ok := strings.CutPrefix(key, "core."); ok {
		return unsafeCoreKeys[rest]
	}
	if rest, ok := strings.CutPrefix(key, "include."); ok {
		return rest == "path"
	}
	if strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path") {
		return true
	}
	if rest, ok := strings.CutPrefix(key, "filter."); ok {
		if i := strings.LastIndexByte(rest, '.'); i >= 0 {
			return unsafeFilterKeys[rest[i+1:]]
		}
	}
	return false
}

// sanitizeConfigContent comments out unsafe configuration lines. It works
// line by line, tracking the current section, so blank lines, indentation or
// dotted keys cannot smuggle an unsafe key past the sanitizer.
func sanitizeConfigContent(content []byte) []byte {
	lines := bytes.SplitAfter(content, []byte("\n"))
	var out bytes.Buffer
	out.Grow(len(content))

	section := ""
	continuation := false // previous unsafe line ended with a line-continuation '\'
	for _, raw := range lines {
		text := strings.TrimSuffix(string(raw), "\n")
		analysis := strings.TrimSuffix(text, "\r")

		unsafe := continuation
		if !unsafe {
			if name, ok := parseSectionHeader(analysis); ok {
				section = name
			} else if key, ok := parseConfigKey(analysis); ok {
				unsafe = isUnsafeConfigKey(section, key)
			}
		}

		if unsafe {
			out.WriteString("# ")
			out.WriteString(text)
			continuation = strings.HasSuffix(strings.TrimSpace(analysis), `\`)
		} else {
			out.WriteString(text)
			continuation = false
		}
		if strings.HasSuffix(string(raw), "\n") {
			out.WriteString("\n")
		}
	}
	return out.Bytes()
}

// sanitizeFile comments out, in place, possibly unsafe lines of a git config
// file, preserving its permission bits.
func sanitizeFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	modified := sanitizeConfigContent(content)
	if bytes.Equal(content, modified) {
		return nil
	}

	if err := os.WriteFile(path, modified, info.Mode().Perm()); err != nil {
		return err
	}
	eprintf("[!] '%s' was altered: commented out unsafe config lines\n", path)
	return nil
}
