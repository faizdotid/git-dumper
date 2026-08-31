package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Config holds the validated command-line options.
type Config struct {
	URL                   string
	Directory             string
	Jobs                  int
	Retry                 int
	Timeout               int
	Headers               map[string]string
	Branches              []string
	Proxy                 string // normalized URL, e.g. "socks5://host:port"
	ClientCertP12         string
	ClientCertP12Password string
	Method                string
	AnyStatus             bool
	SkipVerify            bool
}

// stringSlice collects repeatable flags such as -H and -b.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

var (
	headRegex    = regexp.MustCompile(`^(ref:.*|[0-9a-f]{40}$)`)
	branchRegex  = regexp.MustCompile(`^[A-Za-z0-9\-\._]+$`)
	sha1Regex    = regexp.MustCompile(`(^|\s)([a-f0-9]{40})($|\s)`)
	infoPacksSha = regexp.MustCompile(`pack-([a-f0-9]{40})\.pack`)
)

// normalizeURL strips trailing slashes, "HEAD" and ".git" suffixes.
func normalizeURL(url string) string {
	url = strings.TrimRight(url, "/")
	if strings.HasSuffix(url, "HEAD") {
		url = url[:len(url)-4]
	}
	url = strings.TrimRight(url, "/")
	if strings.HasSuffix(url, ".git") {
		url = url[:len(url)-4]
	}
	url = strings.TrimRight(url, "/")
	return url
}

// gitCheckout runs `git checkout .` in directory, propagating the proxy
// through ALL_PROXY like the Python version does.
func gitCheckout(directory, proxy string, ignoreErrors bool) error {
	cmd := exec.Command("git", "checkout", ".")
	cmd.Dir = directory
	if proxy != "" {
		cmd.Env = append(os.Environ(), "ALL_PROXY="+proxy)
	}
	if ignoreErrors {
		devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err == nil {
			defer devnull.Close()
			cmd.Stderr = devnull
		}
		_ = cmd.Run()
		return nil
	}
	return cmd.Run()
}

// fetchGit dumps a git repository into the output directory.
func fetchGit(cfg *Config) int {
	if cfg.Jobs < 1 || cfg.Retry < 1 || cfg.Timeout < 1 {
		eprintf("error: invalid jobs/retry/timeout\n")
		return 1
	}

	client, err := newHTTPClient(cfg)
	if err != nil {
		eprintf("error: %s\n", err)
		return 1
	}
	f := &fetcher{cfg: cfg, client: client}

	if entries, _ := os.ReadDir(cfg.Directory); len(entries) > 0 {
		printf("Warning: Destination '%s' is not empty\n", cfg.Directory)
	}

	// find base url
	cfg.URL = normalizeURL(cfg.URL)

	// check for /.git/HEAD
	printf("[-] Testing %s/.git/HEAD ", cfg.URL)
	resp, err := f.request(".git/HEAD")
	if err != nil {
		printf("\nerror: Unable to connect to %s. Error: %s\n", cfg.URL, err)
		return 1
	}
	printf("[%d]\n", resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		eprintf("error: Unable to read %s/.git/HEAD: %s\n", cfg.URL, err)
		return 1
	}

	if valid, msg := verifyResponse(resp, cfg.AnyStatus); !valid {
		f.reject(resp, ".git/HEAD", msg)
		return 1
	} else if !headRegex.MatchString(strings.TrimSpace(string(body))) {
		eprintf("error: %s/.git/HEAD is not a git HEAD file\n", cfg.URL)
		return 1
	}

	// check for directory listing
	printf("[-] Testing %s/.git/ ", cfg.URL)
	resp, err = f.request(".git/")
	if err != nil {
		printf("\nerror: Unable to connect to %s. Error: %s\n", cfg.URL, err)
		return 1
	}
	printf("[%d]\n", resp.StatusCode)

	if (resp.StatusCode == 200 || cfg.AnyStatus) && isHTML(resp) {
		indexed := getIndexedFiles(resp.Body)
		resp.Body.Close()
		for _, file := range indexed {
			if file != "HEAD" {
				continue
			}

			printf("[-] Fetching .git recursively\n")
			processTasks(
				[]string{".git/", ".gitignore"},
				nil,
				cfg.Jobs,
				f.recursiveDownloadTask,
			)

			printf("[-] Sanitizing .git/config\n")
			sanitizeFile(filepath.Join(cfg.Directory, ".git", "config"))

			printf("[-] Running git checkout .\n")
			if err := gitCheckout(cfg.Directory, cfg.Proxy, false); err != nil {
				eprintf("error: git checkout failed: %s\n", err)
				return 1
			}
			return 0
		}
	}
	if resp.Body != nil {
		resp.Body.Close()
	}

	// no directory listing
	printf("[-] Fetching common files\n")
	tasks := []string{
		".gitignore",
		".git/COMMIT_EDITMSG",
		".git/description",
		".git/hooks/applypatch-msg.sample",
		".git/hooks/commit-msg.sample",
		".git/hooks/post-commit.sample",
		".git/hooks/post-receive.sample",
		".git/hooks/post-update.sample",
		".git/hooks/pre-applypatch.sample",
		".git/hooks/pre-commit.sample",
		".git/hooks/pre-push.sample",
		".git/hooks/pre-rebase.sample",
		".git/hooks/pre-receive.sample",
		".git/hooks/prepare-commit-msg.sample",
		".git/hooks/update.sample",
		".git/index",
		".git/info/exclude",
		".git/objects/info/packs",
	}
	processTasks(tasks, nil, cfg.Jobs, f.downloadTask)

	// find refs
	printf("[-] Finding refs/\n")
	tasks = []string{
		".git/FETCH_HEAD",
		".git/HEAD",
		".git/ORIG_HEAD",
		".git/config",
		".git/info/refs",
		".git/logs/HEAD",
		".git/logs/refs/heads/main",
		".git/logs/refs/heads/master",
		".git/logs/refs/heads/staging",
		".git/logs/refs/heads/production",
		".git/logs/refs/heads/development",
		".git/logs/refs/remotes/origin/HEAD",
		".git/logs/refs/remotes/origin/main",
		".git/logs/refs/remotes/origin/master",
		".git/logs/refs/remotes/origin/staging",
		".git/logs/refs/remotes/origin/production",
		".git/logs/refs/remotes/origin/development",
		".git/logs/refs/stash",
		".git/packed-refs",
		".git/refs/heads/main",
		".git/refs/heads/master",
		".git/refs/heads/staging",
		".git/refs/heads/production",
		".git/refs/heads/development",
		".git/refs/remotes/origin/HEAD",
		".git/refs/remotes/origin/main",
		".git/refs/remotes/origin/master",
		".git/refs/remotes/origin/staging",
		".git/refs/remotes/origin/production",
		".git/refs/remotes/origin/development",
		".git/refs/stash",
		".git/refs/wip/wtree/refs/heads/main",
		".git/refs/wip/wtree/refs/heads/master",
		".git/refs/wip/wtree/refs/heads/staging",
		".git/refs/wip/wtree/refs/heads/production",
		".git/refs/wip/wtree/refs/heads/development",
		".git/refs/wip/index/refs/heads/main",
		".git/refs/wip/index/refs/heads/master",
		".git/refs/wip/index/refs/heads/staging",
		".git/refs/wip/index/refs/heads/production",
		".git/refs/wip/index/refs/heads/development",
	}

	// include user-specified branches
	for _, branch := range cfg.Branches {
		if !branchRegex.MatchString(branch) {
			printf("Warning: ignoring invalid branch name '%s'\n", branch)
			continue
		}
		tasks = append(tasks,
			".git/logs/refs/heads/"+branch,
			".git/refs/heads/"+branch,
			".git/logs/refs/remotes/origin/"+branch,
			".git/refs/remotes/origin/"+branch,
			".git/refs/wip/wtree/refs/heads/"+branch,
			".git/refs/wip/index/refs/heads/"+branch,
		)
	}

	processTasks(tasks, nil, cfg.Jobs, f.findRefsTask)

	// find packs
	printf("[-] Finding packs\n")
	tasks = nil

	infoPacksPath := filepath.Join(cfg.Directory, ".git", "objects", "info", "packs")
	if content, err := os.ReadFile(infoPacksPath); err == nil {
		for _, m := range infoPacksSha.FindAllStringSubmatch(string(content), -1) {
			tasks = append(tasks,
				".git/objects/pack/pack-"+m[1]+".idx",
				".git/objects/pack/pack-"+m[1]+".pack",
			)
		}
	}

	processTasks(tasks, nil, cfg.Jobs, f.downloadTask)

	// find objects
	printf("[-] Finding objects\n")
	objs := make(map[string]bool)
	packedObjs := make(map[string]bool)

	// .git/packed-refs, .git/info/refs, .git/refs/*, .git/logs/*
	files := []string{
		filepath.Join(cfg.Directory, ".git", "packed-refs"),
		filepath.Join(cfg.Directory, ".git", "info", "refs"),
		filepath.Join(cfg.Directory, ".git", "FETCH_HEAD"),
		filepath.Join(cfg.Directory, ".git", "ORIG_HEAD"),
	}
	for _, subdir := range []string{"refs", "logs"} {
		root := filepath.Join(cfg.Directory, ".git", subdir)
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() {
				files = append(files, path)
			}
			return nil
		})
	}

	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, m := range sha1Regex.FindAllStringSubmatch(string(content), -1) {
			objs[m[2]] = true
		}
	}

	// use .git/index to find objects
	indexPath := filepath.Join(cfg.Directory, ".git", "index")
	if fileExists(indexPath) {
		// A corrupt/garbage index (e.g. an error page saved via --any-status)
		// must not abort the whole dump; skip it and rely on other sources.
		if entries, err := indexObjects(indexPath); err != nil {
			eprintf("[-] Skipping unparseable .git/index: %s\n", err)
		} else {
			for _, obj := range entries {
				objs[obj] = true
			}
		}
	}

	// use packs to find more objects to fetch, and objects that are packed
	packDir := filepath.Join(cfg.Directory, ".git", "objects", "pack")
	if entries, err := os.ReadDir(packDir); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "pack-") || !strings.HasSuffix(name, ".pack") {
				continue
			}
			packPath := filepath.Join(packDir, name)
			idxPath := filepath.Join(packDir, strings.TrimSuffix(name, ".pack")+".idx")
			// A corrupt pack/idx must not abort the dump; skip it and continue.
			packed, referenced, err := packObjects(packPath, idxPath)
			if err != nil {
				eprintf("[-] Skipping unparseable pack %s: %s\n", name, err)
				continue
			}
			for _, obj := range packed {
				packedObjs[obj] = true
			}
			for _, obj := range referenced {
				objs[obj] = true
			}
		}
	}

	// fetch all objects
	printf("[-] Fetching objects\n")
	tasks = nil
	for obj := range objs {
		tasks = append(tasks, obj)
	}
	done := make([]string, 0, len(packedObjs))
	for obj := range packedObjs {
		done = append(done, obj)
	}
	processTasks(tasks, done, cfg.Jobs, f.findObjectsTask)

	// git checkout
	printf("[-] Running git checkout .\n")
	sanitizeFile(filepath.Join(cfg.Directory, ".git", "config"))
	gitCheckout(cfg.Directory, cfg.Proxy, true) // ignore errors

	return 0
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: git-dumper [options] URL DIR\n\nDump a git repository from a website.\n\noptions:\n")
	flag.PrintDefaults()
}

func main() {
	flag.Usage = usage

	var cfg Config
	var headers, branches stringSlice
	var proxyArg, userAgent string

	flag.StringVar(&proxyArg, "proxy", "", "use the specified proxy")
	flag.StringVar(&cfg.Method, "X", "GET", "HTTP method to use for all requests (e.g. GET, POST)")
	flag.StringVar(&cfg.Method, "method", "GET", "HTTP method to use for all requests (e.g. GET, POST)")
	flag.BoolVar(&cfg.AnyStatus, "any-status", false, "accept any HTTP status code as long as the body is non-empty and not HTML")
	flag.BoolVar(&cfg.SkipVerify, "k", false, "skip TLS certificate verification (like curl -k)")
	flag.BoolVar(&cfg.SkipVerify, "insecure", false, "skip TLS certificate verification (like curl -k)")
	flag.StringVar(&cfg.ClientCertP12, "client-cert-p12", "", "client certificate in PKCS#12")
	flag.StringVar(&cfg.ClientCertP12Password, "client-cert-p12-password", "", "password for the client certificate")
	flag.IntVar(&cfg.Jobs, "j", 10, "number of simultaneous requests")
	flag.IntVar(&cfg.Jobs, "jobs", 10, "number of simultaneous requests")
	flag.IntVar(&cfg.Retry, "r", 3, "number of request attempts before giving up")
	flag.IntVar(&cfg.Retry, "retry", 3, "number of request attempts before giving up")
	flag.IntVar(&cfg.Timeout, "t", 3, "maximum time in seconds before giving up")
	flag.IntVar(&cfg.Timeout, "timeout", 3, "maximum time in seconds before giving up")
	flag.StringVar(&userAgent, "u", "Mozilla/5.0 (Windows NT 10.0; rv:78.0) Gecko/20100101 Firefox/78.0", "user-agent to use for requests")
	flag.StringVar(&userAgent, "user-agent", "Mozilla/5.0 (Windows NT 10.0; rv:78.0) Gecko/20100101 Firefox/78.0", "user-agent to use for requests")
	flag.Var(&headers, "H", "additional http headers, e.g `NAME=VALUE`")
	flag.Var(&headers, "header", "additional http headers, e.g `NAME=VALUE`")
	flag.Var(&branches, "b", "additional branch names to check for, e.g. `-b dev -b prod`")
	flag.Var(&branches, "branch", "additional branch names to check for, e.g. `-b dev -b prod`")

	// argparse accepts flags and positional arguments in any order, while
	// flag.Parse() stops at the first positional argument; re-parse in a loop
	// to match that behavior.
	var positional []string
	args := os.Args[1:]
	for len(args) > 0 {
		flag.CommandLine.Parse(args)
		args = flag.Args()
		if len(args) > 0 {
			positional = append(positional, args[0])
			args = args[1:]
		}
	}

	fail := func(format string, args ...interface{}) {
		eprintf(format+"\n", args...)
		os.Exit(2)
	}

	if len(positional) != 2 {
		usage()
		os.Exit(2)
	}
	cfg.URL = positional[0]
	cfg.Directory = positional[1]

	// method
	cfg.Method = strings.ToUpper(cfg.Method)

	// jobs
	if cfg.Jobs < 1 {
		fail("invalid number of jobs, got `%d`", cfg.Jobs)
	}

	// retry
	if cfg.Retry < 1 {
		fail("invalid number of retries, got `%d`", cfg.Retry)
	}

	// timeout
	if cfg.Timeout < 1 {
		fail("invalid timeout, got `%d`", cfg.Timeout)
	}

	// header
	cfg.Headers = map[string]string{"User-Agent": userAgent}
	for _, header := range headers {
		tokens := strings.SplitN(header, "=", 2)
		if len(tokens) != 2 {
			fail("http header must have the form NAME=VALUE, got `%s`", header)
		}
		cfg.Headers[strings.TrimSpace(tokens[0])] = strings.TrimSpace(tokens[1])
	}
	cfg.Branches = branches

	// proxy
	if proxyArg != "" {
		proxyPatterns := []struct {
			pattern string
			scheme  string
		}{
			{`^socks5:(.*):(\d+)$`, "socks5"},
			{`^socks4:(.*):(\d+)$`, "socks4"},
			{`^http://(.*):(\d+)$`, "http"},
			{`^(.*):(\d+)$`, "socks5"},
		}
		valid := false
		for _, p := range proxyPatterns {
			if m := regexp.MustCompile(p.pattern).FindStringSubmatch(proxyArg); m != nil {
				cfg.Proxy = fmt.Sprintf("%s://%s:%s", p.scheme, m[1], m[2])
				valid = true
				break
			}
		}
		if !valid {
			fail("invalid proxy, got `%s`", proxyArg)
		}
	}

	// output directory
	if err := os.MkdirAll(cfg.Directory, 0o755); err != nil {
		fail("`%s` is not a directory", cfg.Directory)
	}
	if info, err := os.Stat(cfg.Directory); err != nil || !info.IsDir() {
		fail("`%s` is not a directory", cfg.Directory)
	}

	// client certificate
	if cfg.ClientCertP12 != "" {
		if !fileExists(cfg.ClientCertP12) {
			fail("client certificate `%s` does not exist or is not a file", cfg.ClientCertP12)
		}
		if cfg.ClientCertP12Password == "" {
			fail("client certificate password is required")
		}
	}

	// fetch everything
	os.Exit(fetchGit(&cfg))
}
