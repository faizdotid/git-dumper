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
                             http://host:port, or host:port (default socks5)
    --any-status             accept any HTTP status code as long as the body is
                             non-empty and not HTML
-k, --insecure               skip TLS certificate verification (like curl -k)
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
4. Sanitizes `.git/config` (comments out `fsmonitor`/`sshcommand`/`askpass`/
   `editor`/`pager` lines) and runs `git checkout .` to restore the working tree.

## License

MIT — same as the original [arthaud/git-dumper](https://github.com/arthaud/git-dumper).
