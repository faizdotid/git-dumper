package main

import (
	"io"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// fetcher carries the shared state used by all workers. Unlike the Python
// version (one requests.Session per process), a single http.Client is safe
// for concurrent use and is shared by all goroutines.
type fetcher struct {
	cfg    *Config
	client *http.Client
}

func isHTML(resp *http.Response) bool {
	return strings.Contains(resp.Header.Get("Content-Type"), "text/html")
}

// verifyResponse mirrors the Python verify_response(). It returns false and
// an error message format when the response should be rejected.
func verifyResponse(resp *http.Response, anyStatus bool) (bool, string) {
	if !anyStatus && resp.StatusCode != 200 {
		return false, "[-] %s/%s: unexpected HTTP status %d\n"
	}
	if resp.Header.Get("Content-Length") == "0" {
		return false, "[-] %s/%s: zero-length body (nothing to save)\n"
	}
	if isHTML(resp) {
		return false, "[-] %s/%s: responded with HTML (%s), not a git file\n"
	}
	return true, ""
}

func (f *fetcher) reject(resp *http.Response, path, msg string) {
	// drain the (usually small) error body so the connection can be reused;
	// undrained bodies force a new TCP connection for the next request
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	args := []interface{}{f.cfg.URL, path}
	if strings.Contains(msg, "%d") {
		args = append(args, resp.StatusCode)
	}
	if strings.Contains(msg, "(%s)") {
		args = append(args, resp.Header.Get("Content-Type"))
	}
	eprintf(msg, args...)
}

// writeResponse streams the response body to directory/path.
func (f *fetcher) writeResponse(resp *http.Response, path string) error {
	abspath, err := filepath.Abs(filepath.Join(f.cfg.Directory, path))
	if err != nil {
		return err
	}
	if err := createIntermediateDirs(abspath); err != nil {
		return err
	}

	out, err := os.Create(abspath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// getIndexedFiles returns all the files in a directory index webpage.
func getIndexedFiles(body io.Reader) []string {
	doc, err := html.Parse(body)
	if err != nil {
		return nil
	}

	var files []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, attr := range n.Attr {
				if attr.Key != "href" {
					continue
				}
				u, err := urlpkg.Parse(attr.Val)
				if err != nil {
					continue
				}
				if u.Path != "" && isSafePath(u.Path) &&
					u.Scheme == "" && u.Host == "" {
					files = append(files, u.Path)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return files
}

// downloadTask downloads a single file (DownloadWorker).
func (f *fetcher) downloadTask(path string) []string {
	if fileExists(filepath.Join(f.cfg.Directory, path)) {
		printf("[=] Already downloaded %s/%s\n", f.cfg.URL, path)
		return nil
	}

	resp, err := f.request(path)
	if err != nil {
		eprintf("[-] %s/%s: request failed: %s\n", f.cfg.URL, path, err)
		return nil
	}
	defer resp.Body.Close()

	printf("[*] Fetching %s/%s [%d]\n", f.cfg.URL, path, resp.StatusCode)

	if valid, msg := verifyResponse(resp, f.cfg.AnyStatus); !valid {
		f.reject(resp, path, msg)
		return nil
	}

	if err := f.writeResponse(resp, path); err != nil {
		eprintf("[-] %s/%s: failed to write to disk: %s\n", f.cfg.URL, path, err)
	}
	return nil
}

// recursiveDownloadTask downloads a directory recursively (RecursiveDownloadWorker).
func (f *fetcher) recursiveDownloadTask(path string) []string {
	if fileExists(filepath.Join(f.cfg.Directory, path)) {
		printf("[=] Already downloaded %s/%s\n", f.cfg.URL, path)
		return nil
	}

	resp, err := f.request(path)
	if err != nil {
		eprintf("[-] %s/%s: request failed: %s\n", f.cfg.URL, path, err)
		return nil
	}
	defer resp.Body.Close()

	printf("[*] Fetching %s/%s [%d]\n", f.cfg.URL, path, resp.StatusCode)

	// a redirect to path + "/" means path is a directory
	if (resp.StatusCode == 301 || resp.StatusCode == 302) &&
		strings.HasSuffix(resp.Header.Get("Location"), path+"/") {
		return []string{path + "/"}
	}

	if strings.HasSuffix(path, "/") { // directory index
		if !isHTML(resp) {
			eprintf("[-] %s/%s: expected HTML directory index, got %s\n", f.cfg.URL, path, resp.Header.Get("Content-Type"))
			return nil
		}

		var tasks []string
		for _, filename := range getIndexedFiles(resp.Body) {
			tasks = append(tasks, path+filename)
		}
		return tasks
	}

	// file
	if valid, msg := verifyResponse(resp, f.cfg.AnyStatus); !valid {
		f.reject(resp, path, msg)
		return nil
	}

	if err := f.writeResponse(resp, path); err != nil {
		eprintf("[-] %s/%s: failed to write to disk: %s\n", f.cfg.URL, path, err)
	}
	return nil
}

var refRegex = regexp.MustCompile(`refs(/[a-zA-Z0-9\-\.\_\*]+)+`)

// findRefsTask finds refs/ mentioned in fetched files (FindRefsWorker).
func (f *fetcher) findRefsTask(path string) []string {
	resp, err := f.request(path)
	if err != nil {
		eprintf("[-] %s/%s: request failed: %s\n", f.cfg.URL, path, err)
		return nil
	}
	defer resp.Body.Close()

	printf("[*] Fetching %s/%s [%d]\n", f.cfg.URL, path, resp.StatusCode)

	if valid, msg := verifyResponse(resp, f.cfg.AnyStatus); !valid {
		f.reject(resp, path, msg)
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		eprintf("[-] %s/%s: failed to read response body: %s\n", f.cfg.URL, path, err)
		return nil
	}

	abspath, err := filepath.Abs(filepath.Join(f.cfg.Directory, path))
	if err == nil {
		if err := createIntermediateDirs(abspath); err == nil {
			os.WriteFile(abspath, body, 0o644)
		}
	}

	// find refs
	var tasks []string
	for _, ref := range refRegex.FindAllString(string(body), -1) {
		if !strings.HasSuffix(ref, "*") && isSafePath(ref) {
			tasks = append(tasks, ".git/"+ref, ".git/logs/"+ref)
		}
	}
	return tasks
}

// findObjectsTask downloads an object file and returns the SHA1s it
// references (FindObjectsWorker).
func (f *fetcher) findObjectsTask(obj string) []string {
	path := ".git/objects/" + obj[:2] + "/" + obj[2:]
	abspath, _ := filepath.Abs(filepath.Join(f.cfg.Directory, path))

	if fileExists(abspath) {
		printf("[=] Already downloaded %s/%s\n", f.cfg.URL, path)
	} else {
		resp, err := f.request(path)
		if err != nil {
			eprintf("[-] %s/%s: request failed: %s\n", f.cfg.URL, path, err)
			return nil
		}
		defer resp.Body.Close()

		printf("[*] Fetching %s/%s [%d]\n", f.cfg.URL, path, resp.StatusCode)

		if valid, msg := verifyResponse(resp, f.cfg.AnyStatus); !valid {
			f.reject(resp, path, msg)
			return nil
		}

		if err := f.writeResponse(resp, path); err != nil {
			eprintf("[-] %s/%s: failed to write to disk: %s\n", f.cfg.URL, path, err)
			return nil
		}
	}

	// parse object file to find other objects
	refs, err := getReferencedSHA1(abspath)
	if err != nil {
		eprintf("[-] %s: cannot parse object file: %s\n", abspath, err)
		return nil
	}
	return refs
}
