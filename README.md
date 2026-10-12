# git-dumper (Go)

A tool to dump a git repository from a website.

This is a **Go rewrite** of [arthaud/git-dumper](https://github.com/arthaud/git-dumper)
(originally written in Python by Maxime Arthaud, MIT License), maintained at
[github.com/faizdotid/git-dumper](https://github.com/faizdotid/git-dumper).

Compared to the Python version:

- Single static binary, no Python environment or dependencies needed
- Much faster (goroutines instead of multiprocessing; ~17x in local benchmarks)
- TLS certificate verification is **on by default**; use `-k`/`--insecure` to skip it
  (the Python version always skips verification)
- `.git/config` is sanitized more thoroughly (see [Security](#security)) and
  `git checkout` runs with a hardened environment
- Honors `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` when `--proxy` is not given,
  like `requests` does
- Keeps cookies across requests (session behavior like `requests.Session`)
- `--max-size` caps the size of downloaded responses (0 = unlimited)

## Install

### Using `go install` (requires Go 1.25+)

```bash
go install github.com/faizdotid/git-dumper@latest
```

The binary lands in `$GOPATH/bin` (usually `~/go/bin`); make sure it is on your `PATH`.

### Build from source

```bash
git clone https://github.com/faizdotid/git-dumper.git
cd git-dumper
go build -o git-dumper .
```

### Prebuilt binary

Download from the [releases page](https://github.com/faizdotid/git-dumper/releases),
then:

```bash
chmod +x git-dumper
mv git-dumper /usr/local/bin/   # or anywhere on your PATH
```

## Usage

```bash
git-dumper [options] URL DIR
```

Example:

```bash
git-dumper https://example.com/.git ./dumped-repo
```

The target `git` binary is required for the final `git checkout .` step.

### Options

```
-j, --jobs N                 number of simultaneous requests (default 10)
-r, --retry N                number of request attempts before giving up (default 3)
-t, --timeout N              maximum time in seconds before giving up (default 3)
-u, --user-agent STRING      user-agent to use for requests
-H, --header NAME=VALUE      additional http header (repeatable)
-b, --branch NAME            additional branch names to check for (repeatable)
-X, --method METHOD          HTTP method to use for all requests (default GET)
    --proxy PROXY            proxy: socks5:host:port, socks4:host:port,
                             http://host:port, or host:port (default socks5);
                             falls back to HTTP_PROXY/HTTPS_PROXY/NO_PROXY
    --any-status             accept any HTTP status code as long as the body is
                             non-empty and not HTML
    --max-size BYTES         maximum response size to download in bytes
                             (0 = unlimited, default 0)
-k, --insecure               skip TLS certificate verification (like curl -k)
-h, --help                   show this help message and exit
    --client-cert-p12 FILE   client certificate in PKCS#12
    --client-cert-p12-password PASSWORD
                             password for the client certificate
```

Flags and positional arguments can be given in any order.

## How it works

1. Probes `URL/.git/HEAD` to confirm an exposed git repository.
2. If directory listing is enabled on `.git/`, downloads everything recursively.
3. Otherwise, fetches common files (`.git/index`, hooks, `objects/info/packs`, ...),
   discovers refs by scanning fetched files, downloads packs listed in
   `objects/info/packs`, then crawls the object graph (commits → trees → blobs)
   to fetch every referenced loose object.
4. Sanitizes `.git/config` and runs `git checkout .` to restore the working tree.

## Security

Dumping a repository from a website means fetching and then running `git` on
**attacker-controlled data**. Always run `git-dumper` in a sandbox, container or
throwaway VM, and never point it at trusted working directories.

Mitigations built in:

- TLS certificate verification is enabled by default (`-k` disables it).
- Before `git checkout .`, unsafe `.git/config` keys are commented out:
  `core.fsmonitor`, `core.sshcommand`, `core.askpass`, `core.editor`,
  `core.pager`, `core.hooksPath`, `include.path` / `includeIf.*.path` and
  `filter.*.smudge` / `clean` / `process` / `required`. Both section form
  (`[core]` + `editor = ...`) and dotted form (`core.editor = ...`) are handled.
- `git checkout` runs with `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`,
  `GIT_ATTR_NOSYSTEM=1` and `GIT_TERMINAL_PROMPT=0`, so the machine's global git
  configuration cannot execute code or prompt for credentials.
- Download paths are validated against directory traversal (`..`, absolute
  paths, backslashes).
- `--max-size` limits the size of downloaded responses if the target is
  untrusted about disk usage.

Note that this hardening is best-effort: git features beyond configuration
(e.g. repository content itself) are not sandboxed.

## License

MIT — same as the original [arthaud/git-dumper](https://github.com/arthaud/git-dumper).
