# leanfeed

leanfeed is a small, self-hosted RSS, Atom and JSON Feed reader for one person. It is one Go binary with a web interface. It keeps all data as plain files: OPML for subscriptions, JSON for metadata, HTML for content. You can read, search, back up and version-control these files without leanfeed.

## Build and run

You need Go 1.22 or newer.

```sh
go build -ldflags "-X main.version=$(git describe --tags --always)" -o leanfeed ./cmd/leanfeed
./leanfeed serve
```

Then open <http://127.0.0.1:8080>. Go to **Manage feeds** to add a feed by URL or to import an OPML file.

## Commands

| Command | What it does |
| --- | --- |
| `leanfeed serve` | Runs the web server and fetches feeds on a schedule. |
| `leanfeed import FILE` | Imports subscriptions from an OPML file. |
| `leanfeed export` | Writes subscriptions as OPML to standard output. |
| `leanfeed --version` | Prints the version. |

Run `import` and `export` only while the server is stopped. They read and write the data directory directly.

## Configuration

Every setting is a flag with a matching environment variable. A flag wins over the environment variable.

| Flag | Environment variable | Default | Meaning |
| --- | --- | --- | --- |
| `--data` | `LEANFEED_DATA` | `./data` | Data directory |
| `--addr` | `LEANFEED_ADDR` | `127.0.0.1:8080` | Listen address |
| `--interval` | `LEANFEED_INTERVAL` | `30m` | Time between fetches of a feed (at least `1m`) |
| `--workers` | `LEANFEED_WORKERS` | `4` | Number of feeds fetched at the same time |

After a failed fetch, leanfeed doubles the wait for each further failure, up to 24 hours. It honours `Retry-After` on HTTP 429 and 503 responses.

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
ExecStart=/usr/local/bin/leanfeed serve
Environment=LEANFEED_DATA=/var/lib/leanfeed
DynamicUser=yes
StateDirectory=leanfeed
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

### Docker

```sh
docker build --build-arg VERSION=$(git describe --tags --always) -t leanfeed .
docker run -d --name leanfeed -p 127.0.0.1:8080:8080 -v leanfeed-data:/data leanfeed
```

Inside the container leanfeed listens on all interfaces. The `-p 127.0.0.1:8080:8080` option publishes the port on localhost only. The image runs as a non-root user (UID 65532). If you mount a host directory instead of a named volume, that user must be able to write to it.

## Security

- leanfeed listens on `127.0.0.1` unless you set `--addr`. It has no login. Control access through the network, for example with a VPN such as Tailscale.
- Feed content is sanitized before it is stored: scripts, styles, iframes, forms and event handlers are removed. A Content Security Policy allows scripts only from leanfeed itself.
- Requests that change data must carry htmx's `HX-Request` header and, if the browser sends an `Origin` header, come from the same host. Other websites cannot add that header, so they cannot make your browser change your leanfeed data.
- Images in feed content load directly from their origin, so those sites can see that you read the entry. leanfeed sends no referrer.

## Development

```sh
go test ./...           # all tests, including the 10,000-entry and 300-feed checks
go test -short ./...    # skips the slow checks
go test -race ./...
go test -run XXX -bench . ./internal/store/filestore ./internal/web
```

The fetcher tests start local HTTP servers with `httptest`, so they need permission to listen on a local port.

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
