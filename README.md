# leanfeed

leanfeed is a small, self-hosted RSS, Atom and JSON Feed reader for one person. It is one Go binary with a web interface. It keeps all data as plain files: OPML for subscriptions, JSON for metadata, HTML for content. You can read, search, back up and version-control these files without leanfeed.

## Getting started

You need Go 1.26 or newer and `make`.

```sh
make            # run all tests, then build bin/leanfeed
bin/leanfeed serve
```

Open <http://127.0.0.1:8080>. Choose **Manage feeds** to add a feed or to import an OPML file from another reader. You can enter the feed URL or the address of the website; for a website, leanfeed subscribes to the first feed the page announces. On the same page you can rename a feed, move it to a folder or change its URL. leanfeed fetches a new URL first and saves it only if it is a feed. The feed keeps its entries.

Drag the line between two columns to change their width; double-click it to reset it. The « button hides the menu, and » shows it again. Your browser remembers these settings. The ⤢ button in the top right corner of an article shows it across the whole window; press it again or Esc to return. Click a feed name to see that feed's entries.

Other `make` targets:

| Target | What it does |
| --- | --- |
| `make build` | Builds `bin/leanfeed`. The version is taken from `git describe`. |
| `make test` | Runs `go vet`, then all tests with the race detector, including the slow checks with 10,000 entries and 300 feeds. |
| `make test-short` | Runs the tests without the slow checks. |
| `make lint` | Runs golangci-lint, which also checks the formatting (gofumpt and goimports). |
| `make fmt` | Applies the formatting that `make lint` checks. |
| `make vuln` | Checks the dependencies for known vulnerabilities with govulncheck. |
| `make clean` | Removes `bin/`, including the lint and vulnerability tools that `make lint` and `make vuln` install there. |

## Configuration

### Commands

| Command | What it does |
| --- | --- |
| `leanfeed serve` | Runs the web server and fetches feeds on a schedule. |
| `leanfeed import FILE` | Imports subscriptions from an OPML file. |
| `leanfeed export` | Writes subscriptions as OPML to standard output. |
| `leanfeed healthcheck` | Asks the server at `--addr` for `/healthz`. Exits with `0` if it answers `200` within 5 seconds, and with `1` otherwise. |
| `leanfeed --version` | Prints the version. |
| `leanfeed --help` | Lists the commands and flags. `leanfeed COMMAND --help` shows help for one command. |

Run `import` and `export` only while the server is stopped. They read and write the data directory directly.

### Parameters

Each parameter can be set with a flag or an environment variable. A flag wins over an environment variable, and an environment variable wins over the default. An empty environment variable counts as not set. Flags can come before or after the command, for example `leanfeed --data /srv/leanfeed serve` or `leanfeed serve --data /srv/leanfeed`.

| Flag | Environment variable | Default | Meaning |
| --- | --- | --- | --- |
| `--data DIR` | `LEANFEED_DATA` | `./data` | Directory that holds all subscriptions, entries and fetch state. leanfeed creates it if it does not exist. |
| `--addr HOST:PORT` | `LEANFEED_ADDR` | `127.0.0.1:8080` | Address the web server listens on. The default accepts connections from this computer only. |
| `--interval DURATION` | `LEANFEED_INTERVAL` | `30m` | Time between two fetches of the same feed, written like `45m` or `2h`. The minimum is `1m`. |
| `--workers N` | `LEANFEED_WORKERS` | `4` | Number of feeds fetched at the same time. The minimum is `1`. |

`serve` uses all four parameters. `import` and `export` use only `--data`. `healthcheck` uses only `--addr`; a host of `0.0.0.0`, `::` or none means this computer.

After a failed fetch, leanfeed doubles the wait for each further failure, up to 24 hours. It honours `Retry-After` on HTTP 429 and 503 responses.

Exit codes: `0` means success, `1` means the command failed, and `2` means the command line was wrong.

## Data directory

```
data/
  subscriptions.opml            feeds and folders
  feeds/<feed-id>/
    feed.json                   fetch state: ETag, last status, last error
    entries/<entry-id>/
      meta.json                 title, dates, read and starred state
      content.raw.html          content as received
      content.html              sanitized content shown in the browser
```

A feed ID is a slug of the feed title plus a hash of its URL, for example `example-blog-3f9a2c1e`. It never changes, even when you rename the feed or the feed moves to a new URL.

Every change is written atomically: leanfeed writes a temporary file, syncs it to disk and renames it into place. A crash or power loss therefore never leaves a half-written file. At startup leanfeed removes leftover temporary files, whose names start with `.tmp-`.

You may edit `subscriptions.opml` by hand while the server is stopped. A feed without a `leanfeedId` attribute gets an ID at the next start.

leanfeed has no backup feature. Back up the data directory with your usual tools, such as filesystem snapshots or restic.

## Deployment

### systemd

```ini
[Unit]
Description=leanfeed
After=network-online.target
Wants=network-online.target

[Service]
# Copy bin/leanfeed from `make build` to /usr/local/bin.
ExecStart=/usr/local/bin/leanfeed serve
Environment=LEANFEED_DATA=/var/lib/leanfeed
DynamicUser=yes
StateDirectory=leanfeed
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

### Podman

```sh
podman build --format docker --build-arg VERSION=$(git describe --tags --always) -t leanfeed .
podman run -d --name leanfeed -p 127.0.0.1:8080:8080 -v leanfeed-data:/data leanfeed
```

The `--format docker` option keeps the image's health check. Podman's default image format (OCI) has no health checks and drops it. `podman ps` shows whether the container is healthy, and `podman healthcheck run leanfeed` runs the check at once.

The same commands work with Docker; leave out `--format docker`.

Inside the container leanfeed listens on all interfaces. The `-p 127.0.0.1:8080:8080` option publishes the port on localhost only. The image runs as a non-root user (UID 65532). If you mount a host directory instead of a named volume, that user must be able to write to it. With rootless Podman, add `:U` to the mount, for example `-v ./data:/data:U`, and Podman changes the directory's owner to match.

### Health check

`GET /healthz` answers `200` with the text `ok` when leanfeed can read its data, and `503` otherwise. Point an uptime monitor or a container orchestrator at it. Where no HTTP client is available, run `leanfeed healthcheck`. The container image uses it as its `HEALTHCHECK`.

## Security

- leanfeed listens on `127.0.0.1` unless you set `--addr`. It has no login. Control access through the network, for example with a VPN such as Tailscale.
- Feed content is sanitized before it is stored: scripts, styles, iframes, forms and event handlers are removed. A Content Security Policy allows scripts only from leanfeed itself.
- Requests that change data must carry htmx's `HX-Request` header and, if the browser sends an `Origin` header, come from the same host. Other websites cannot add that header, so they cannot make your browser change your leanfeed data.
- Images in feed content load directly from their origin, so those sites can see that you read the entry. leanfeed sends no referrer.

## Development

```sh
make test               # go vet, then all tests with the race detector, including the slow checks
make test-short         # skips the slow checks and the browser tests
make test-ui            # only the UI flow tests and the browser tests
make lint               # golangci-lint, including formatting
make vuln               # govulncheck
go test -run XXX -bench . ./internal/store/filestore ./internal/web
```

The fetcher and browser tests start local HTTP servers with `httptest`, so they need permission to listen on a local port.

GitHub Actions runs the same checks on every push to `main` and on pull requests: `go vet`, the build, all tests with the race detector and the browser tests, golangci-lint and govulncheck (`.github/workflows/ci.yml`).

### UI tests

The UI has two kinds of tests in `internal/web`:

- **Flow tests** (`flows_test.go`) click through the UI like a user, for example "open the feeds page, add a feed, click the feed in the sidebar". They run without a browser: a small stand-in for htmx in `uiharness_test.go` sends the requests htmx would send and applies the responses to an in-memory page. They are fast and run everywhere, but they do not run JavaScript or CSS.
- **Browser tests** (`browser_test.go`) drive headless Chrome through the main journeys with real htmx, CSS and Content Security Policy. Each test fails if the page reports a CSP violation, a JavaScript error or an htmx error. They need Chrome: `nix develop` provides it and sets `LEANFEED_CHROME`. Without Chrome, or with `-short`, they are skipped.

```sh
nix develop -c make test-ui
```

The flake allows the unfree `google-chrome` package on macOS, because nixpkgs has no Chromium build for macOS. On Linux it uses Chromium. Nix flakes only see files that git tracks, so `flake.nix` must be added to git before `nix develop` works.

Code layout:

| Package | Job |
| --- | --- |
| `cmd/leanfeed` | Flags, commands, wiring and graceful shutdown |
| `internal/ids` | Feed and entry IDs |
| `internal/opml` | OPML reading and writing |
| `internal/store` | The `Store` interface and domain types |
| `internal/store/storetest` | Contract tests that every `Store` must pass |
| `internal/store/filestore` | The file-based `Store` |
| `internal/fetcher` | Scheduling, HTTP, parsing and sanitizing |
| `internal/web` | Handlers, templates and static files |

The web server and the fetcher use only the `store.Store` interface. A database-backed store can replace `filestore` if it passes `storetest.Run`.
