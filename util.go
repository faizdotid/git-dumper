package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func printf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stdout, format, args...)
	os.Stdout.Sync()
}

func eprintf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format, args...)
}

// isSafePath prevents directory traversal attacks: reject absolute paths
// and any path escaping its base via "..".
func isSafePath(path string) bool {
	if strings.HasPrefix(path, "/") {
		return false
	}
	for _, elem := range strings.Split(path, "/") {
		if elem == ".." {
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

var unsafeConfigLine = regexp.MustCompile(`(?im)^\s*(?:fsmonitor|sshcommand|askpass|editor|pager)`)

// sanitizeFile comments out, in place, possibly unsafe lines based on regex.
func sanitizeFile(path string) {
	if !fileExists(path) {
		return
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return
	}

	modified := unsafeConfigLine.ReplaceAll(content, []byte("# $0"))
	if string(content) != string(modified) {
		printf("Warning: '%s' file was altered\n", path)
		os.WriteFile(path, modified, 0o644)
	}
}
