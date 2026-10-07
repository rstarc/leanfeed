# leanfeed: Product Requirements & Technical Design

Status: final design for MVP implementation (2026-10-07)

## Overview

leanfeed is a simple, minimal and fast self-hosted RSS/Atom reader: one Go binary, plain files on disk, and an htmx web UI. It is built for one person who wants to own their subscriptions and reading history as ordinary files, the way Obsidian treats notes.

Guiding principles, in priority order when they conflict:

1. **Simple, readable code over performance.** Prefer the obvious implementation; optimize only where a stated requirement demands it.
2. **Plain, portable files.** All state is JSON, HTML or OPML that a person can read, grep, back up and version-control without leanfeed.
3. **Fast where it matters.** Startup and page loads feel instant at personal scale.
4. **Secure by default.** Untrusted feed content is always sanitized; the app binds to localhost unless told otherwise.
5. **Small scope.** Anything not needed to read feeds daily is deferred.

## Part 1: Product requirements

### Goals

- Follow a few dozen to a few hundred feeds and read new entries in a clean web UI.
- Keep all data as plain files that remain useful without leanfeed.
- Run as a single binary with no database, no external services and minimal setup.

### Non-goals (MVP)

- Multiple users, accounts or permissions.
- Mobile apps or sync APIs (Fever, Google Reader).
- Search, filters, full-text extraction, retention or cleanup, notifications.
- Feed discovery from a site's home page (the user pastes a feed URL).

### Target user

One technical user who self-hosts on a home server, NAS, Raspberry Pi or laptop, and reaches leanfeed over localhost or a private network (for example Tailscale).

### MVP functional requirements

| ID | Requirement |
| --- | --- |
| F1 | Add a feed by URL, optionally into a folder. The first fetch runs immediately. |
| F2 | Remove a feed after confirmation; its directory is deleted. |
| F3 | Rename a feed, move it between folders (one level of folders) and change its URL. A new URL is fetched first and is saved only if it is a feed; the feed keeps its ID and entries. |
| F4 | Import subscriptions from OPML; export current subscriptions as OPML. |
| F5 | Fetch all feeds on a schedule (default every 30 minutes), plus a manual "refresh all" action. |
| F6 | Show a sidebar of folders and feeds with unread counts. |
| F7 | List entries for: all unread, all, starred, one folder, one feed; newest first, paginated. |
| F8 | Open an entry: title, feed, date, link to the original, sanitized content. |
| F9 | Mark an entry read or unread; opening an entry marks it read. |
| F10 | Star and unstar an entry. |
| F11 | Mark all entries in the current view as read. |
| F12 | Show each feed's last fetch status and error, so broken feeds are visible. |

### Non-functional requirements

| ID | Requirement | How it is verified |
| --- | --- | --- |
| N1 | Code favors simplicity and readability over performance. Standard library first; few dependencies. | Code review; dependency list stays short |
| N2 | All persistent state is plain JSON, HTML or OPML in one data directory. No binary or proprietary formats. | Inspect the data directory |
| N3 | Building the index for 10,000 entries takes under 1 s on an SSD with a warm OS cache. | Benchmark with 10,000 generated entries |
| N4 | Pages render in under 100 ms server-side for 10,000 entries. | Benchmark of list handlers |
| N5 | A crash or power loss never leaves a corrupt file; at worst the last change is lost. | Atomic writes; startup cleanup test |
| N6 | Feed HTML is sanitized before it is shown; no script from a feed ever runs. | Sanitizer tests with hostile fixtures |
| N7 | Ships as one static binary with templates and assets embedded. | Build produces a single file |
| N8 | Handles 300 feeds without tuning. | Fetch test against fixture server |
| N9 | Polite fetching: conditional GET, timeouts, size limits, backoff on errors. | Fetcher tests |

## Part 2: Technical design

### Architecture

leanfeed is one Go process with three parts around a single store: a fetcher that writes new entries, a web server that reads and updates them, and the store, which owns the files and the in-memory index. Only the store touches the data directory.

```
+-------------------------+          +-------------------------+
| Browser                 |          | Feed sites              |
| htmx, server-rendered   |          | RSS, Atom, JSON Feed    |
+-------------------------+          +-------------------------+
            ^ |                                  ^
  pages +   | v                                  | conditional GET
  partials  |                                    |
+-------------------------+          +-------------------------+
| Web server              |          | Fetcher                 |
| net/http + html/template|          | scheduler + 4 workers   |
| routes, CSRF check      |          | gofeed + bluemonday     |
+-------------------------+          +-------------------------+
            |  list, read,                       |  upsert entries
            v  mark read, star                   v
+----------------------------------------------------------------+
| Store (store.Store interface; filestore implementation)        |
| in-memory index behind one RWMutex                             |
| the only component that touches files                          |
+----------------------------------------------------------------+
                               ^ |  atomic writes;
                               | v  meta.json loaded at startup
+----------------------------------------------------------------+
| Data directory                                                 |
| subscriptions.opml, feed.json, meta.json, content HTML         |
+----------------------------------------------------------------+
```

The browser and feed sites never touch the data directory; every read and write passes through the store.

- **Language:** Go 1.22 or newer (for the standard `net/http` routing patterns).
- **Dependencies:** `github.com/mmcdole/gofeed` (RSS, Atom, JSON Feed parsing), `github.com/microcosm-cc/bluemonday` (HTML sanitizing), htmx (one vendored, embedded JS file). Everything else is the standard library.
- **No:** database, ORM, web framework, CSS framework build step, JavaScript bundler.

### Project layout

```
leanfeed/
  cmd/leanfeed/main.go      flags, wiring, version, signal handling
  internal/ids/             feed and entry ID generation (shared by all stores)
  internal/store/           Store interface, domain types, errors
  internal/store/storetest/ contract test suite for any Store
  internal/store/filestore/ file implementation: files, atomic writes, index
  internal/fetcher/         scheduler, HTTP fetch, parse, sanitize
  internal/opml/            OPML read and write
  internal/web/             handlers, templates/, static/ (htmx, css)
  testdata/feeds/           real and broken feed fixtures
```

Each package has one job and a small exported API. `web` and `fetcher` depend only on the `store.Store` interface; only `cmd/leanfeed` knows which implementation is used.

### On-disk data model

All state lives in one data directory. Subscriptions and folders are in `subscriptions.opml`; each feed has a directory holding its fetch state and one directory per entry.

```
data/
  subscriptions.opml                 feed list and folders (source of truth)
  feeds/
    <feed-id>/
      feed.json                      fetch state for this feed
      entries/
        <entry-id>/
          meta.json                  metadata + read/starred state
          content.raw.html           content exactly as received
          content.html               sanitized content shown in the UI
```

#### IDs

- **Feed ID:** a readable slug of the feed title plus the first 8 hex characters of SHA-256 of the feed URL, for example `example-blog-3f9a2c1e`. The slug uses lowercase ASCII letters, digits and hyphens, at most 40 characters, and falls back to `feed` when the title is empty or unusable. The ID is fixed at subscription time and stored in `feed.json` and the OPML; renaming the feed or a URL redirect never changes it, so the directory name stays stable.
- **Entry ID:** first 16 hex characters of SHA-256 of `feedID + "\n" + key`, where key is the entry GUID, or the link if the GUID is empty, or title + published date if both are empty.
- The entry directory name is the entry ID, so "have we seen this entry?" is a directory existence check.

#### subscriptions.opml

Standard OPML 2.0. Folders are top-level `<outline>` elements with child feed outlines; unfoldered feeds sit at the top level. Each feed outline carries `xmlUrl`, `htmlUrl`, `text`, and a custom `leanfeedId` attribute so it maps to its directory. Other readers ignore the extra attribute, so the file imports anywhere.

#### feed.json

```json
{
  "id": "example-blog-3f9a2c1e",
  "url": "https://example.com/feed.xml",
  "title": "Example Blog",
  "site_url": "https://example.com/",
  "etag": "\"abc123\"",
  "last_modified": "Tue, 06 Oct 2026 08:00:00 GMT",
  "last_fetched_at": "2026-10-07T09:30:00Z",
  "next_fetch_at": "2026-10-07T10:00:00Z",
  "last_status": "ok",
  "last_error": "",
  "consecutive_failures": 0
}
```

#### meta.json

Kept small: no content and no long summaries, so loading 10,000 of them stays fast (N3).

```json
{
  "id": "a1b2c3d4e5f60718",
  "feed_id": "example-blog-3f9a2c1e",
  "guid": "https://example.com/posts/42",
  "url": "https://example.com/posts/42",
  "title": "Post title",
  "author": "Jane Doe",
  "published_at": "2026-10-06T14:00:00Z",
  "updated_at": "2026-10-06T14:00:00Z",
  "fetched_at": "2026-10-07T09:30:00Z",
  "content_sha256": "9e1f...",
  "read": false,
  "starred": false
}
```

Timestamps are RFC 3339 in UTC. A missing published date falls back to the updated date, then to `fetched_at`.

#### Content files

`content.raw.html` stores the entry's content exactly as received (or the summary if there is no content), for re-processing if sanitizer rules change. `content.html` stores the sanitized HTML fragment the UI displays. Both are fragments, not full HTML documents.

### Storage interface (backing service)

All persistence goes through one Go interface, `store.Store`, so the file store can later be replaced by a database (SQLite, PostgreSQL) without touching the web server or fetcher. The MVP ships one implementation, `filestore`.

Design rules for the interface:

- **Domain-level, not file-level.** No paths, directories or file handles appear in the interface; it speaks in feeds, entries and queries.
- **Values out, not pointers.** Methods return copies, so callers can never mutate the in-memory index without the store's lock, and a database implementation returns the same shapes.
- **Queries as data.** Listing uses a `Query` struct that maps to both an in-memory filter and a SQL `WHERE` clause; no callbacks or iterators over internals.
- **Shared IDs.** Feed and entry IDs come from `internal/ids`, not from an implementation, so data can be migrated between stores with IDs intact.
- **Context everywhere.** Every method takes `context.Context` for cancellation and timeouts, which a database needs and the file store tolerates.
- **Sentinel errors.** `store.ErrNotFound` and `store.ErrExists`, checked with `errors.Is`.

```go
package store

type Store interface {
    // Feeds and subscriptions
    ListFeeds(ctx context.Context) ([]Feed, error)
    GetFeed(ctx context.Context, id string) (Feed, error)
    AddFeed(ctx context.Context, f NewFeed) (Feed, error)
    UpdateFeed(ctx context.Context, id string, u FeedUpdate) (Feed, error) // title, folder, URL
    RemoveFeed(ctx context.Context, id string) error                       // deletes feed and all its entries
    DueFeeds(ctx context.Context, now time.Time) ([]Feed, error)
    RecordFetch(ctx context.Context, id string, r FetchResult) error       // ETag, Last-Modified, status, error, next fetch
    ImportOPML(ctx context.Context, r io.Reader) (ImportStats, error)
    ExportOPML(ctx context.Context, w io.Writer) error

    // Entries
    UpsertEntries(ctx context.Context, feedID string, in []IncomingEntry) (UpsertStats, error)
    ListEntries(ctx context.Context, q Query) (EntryPage, error)
    GetEntry(ctx context.Context, id string) (Entry, error)
    EntryContent(ctx context.Context, id string) (string, error) // sanitized HTML
    SetRead(ctx context.Context, id string, read bool) error
    SetStarred(ctx context.Context, id string, starred bool) error
    MarkAllRead(ctx context.Context, q Query) (int, error)
    UnreadCounts(ctx context.Context) (map[string]int, error) // by feed ID

    Close() error // waits for in-flight writes
}

type View int

const (
    ViewUnread View = iota
    ViewAll
    ViewStarred
)

type Query struct {
    View   View
    FeedID string // optional
    Folder string // optional
    Limit  int
    Offset int
}

type IncomingEntry struct {
    GUID, URL, Title, Author string
    PublishedAt, UpdatedAt   time.Time
    RawContent               string // as received
    Content                  string // sanitized by the fetcher
}
```

`Feed`, `NewFeed`, `FeedUpdate`, `FetchResult`, `Entry`, `EntryPage`, `UpsertStats` and `ImportStats` are plain structs mirroring `feed.json` and `meta.json`. Sanitizing stays in the fetcher, so every store receives already-safe content.

A shared contract test suite, `storetest.Run(t, newStore)`, exercises every method and edge case (missing IDs, preserved read state on content change, folder moves, pagination). Every implementation must pass it; that suite is what makes a future database store a drop-in replacement. What does not carry over is the plain-file portability itself, which is a property of `filestore`.

### File store and in-memory index

Files are the persistence layer; the in-memory index is the query layer. Every change is written to disk first, then applied to memory, so the index never holds state the files lack.

#### Index

```go
type Index struct {
    Feeds   map[string]*Feed   // by feed ID, from OPML + feed.json
    Entries map[string]*Entry  // by entry ID, from meta.json
    ByFeed  map[string][]*Entry // per feed, sorted newest first
}
```

List views (unread, starred, folder, all) are computed by filtering and sorting these slices on each request. At 10,000 entries that is well under a millisecond, so there are no secondary indexes or caches (N1).

#### Startup

1. Read `subscriptions.opml` and each `feed.json`.
2. Remove leftover temp directories and files (names starting with `.tmp-`).
3. Walk `feeds/*/entries/*/meta.json` and decode them with a worker pool of `runtime.NumCPU()` goroutines. HTML files are never read at startup.
4. Sort each feed's entries by published date. Log the entry count and elapsed time.

Expected cost: about 100 to 400 ms for 10,000 entries on an SSD with a warm cache. A benchmark guards N3.

#### Concurrency

The store holds one `sync.RWMutex`. Reads (page renders) take the read lock; writes (mark read, add entries, change feeds) take the write lock for the file write plus the memory update. One lock is slower than fine-grained locking, but at this scale writes take milliseconds and the code stays easy to reason about.

#### Atomic writes

- **Updating a file** (`meta.json`, `feed.json`, OPML): write `.tmp-<name>` in the same directory, `fsync`, then `rename` over the original.
- **Creating an entry:** write all three files into `entries/.tmp-<entry-id>/`, then `rename` the directory to `entries/<entry-id>/`. An entry either fully exists or does not.
- **Deleting a feed:** remove it from the OPML first (atomic write), then delete its directory. A crash in between leaves an orphan directory, which startup ignores and logs.

#### Operations

`filestore` implements `store.Store` (see Storage interface). `MarkAllRead` rewrites one `meta.json` per changed entry; a few hundred small writes is acceptable.

### Fetcher

A single scheduler goroutine wakes every minute, picks feeds whose `next_fetch_at` has passed, and hands them to a pool of 4 workers. Each worker fetches, parses, sanitizes and calls `store.UpsertEntries`.

#### Scheduling and backoff

- Default interval: 30 minutes (`--interval`). After a success, `next_fetch_at = now + interval`.
- After a failure, the interval doubles per consecutive failure, capped at 24 hours. The error is stored in `feed.json` and shown in the UI (F12).
- `429` or `503` with `Retry-After` uses that delay if it is longer.
- "Refresh all" and adding a feed enqueue fetches immediately, ignoring `next_fetch_at`.

#### HTTP

- Only `http` and `https` URLs are accepted.
- Conditional GET with `If-None-Match` and `If-Modified-Since`; a `304` only updates timestamps.
- 30 s timeout, 10 MB body limit, at most 5 redirects, `User-Agent: leanfeed/<version>`.
- A permanent redirect (`301`, `308`) updates the feed URL in OPML and `feed.json`; the feed ID stays the same.

#### Parsing and sanitizing

1. Parse with `gofeed` (RSS 0.9x to 2.0, Atom, JSON Feed).
2. For each item, compute the entry ID (see IDs). Take content, falling back to the summary.
3. Resolve relative URLs in the content against the entry link (or the feed's site URL).
4. Sanitize with a strict `bluemonday` policy based on `UGCPolicy`: no scripts, styles, iframes, forms or event handlers. Links get `rel="noopener noreferrer"` and `target="_blank"`. Images stay but load directly from their origin.

#### New and changed entries

- **New entry** (directory does not exist): create it atomically, `read: false`.
- **Existing entry, same `content_sha256`:** do nothing.
- **Existing entry, changed content:** rewrite both content files and the metadata fields from the feed, keeping `read` and `starred` as they were.
- **Entry no longer in the feed:** keep it. Nothing is deleted in the MVP.

### Web UI

Server-rendered HTML with `html/template`, enhanced by htmx; every page also works as a full page load. The layout has three areas: a sidebar of folders and feeds with unread counts, an entry list, and the open entry. On narrow screens they stack as separate pages.

Styling is one small hand-written CSS file with system fonts and light and dark themes via `prefers-color-scheme`. Templates, CSS and htmx are embedded with `embed`.

#### Routes

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/` | Redirect to `/entries?view=unread` |
| GET | `/healthz` | `200 ok` if the store answers, else `503` |
| GET | `/entries` | Entry list; query `view` (unread, all, starred), `feed`, `folder`, `page` |
| GET | `/entries/{id}` | Entry view; marks it read |
| POST | `/entries/{id}/read`, `/entries/{id}/unread` | Set read state; returns updated row |
| POST | `/entries/{id}/star`, `/entries/{id}/unstar` | Set starred; returns updated row |
| POST | `/entries/mark-read` | Mark all in the current view read (same query params) |
| GET | `/feeds` | Manage feeds: list, status, last error |
| POST | `/feeds` | Add feed (`url`, optional `folder`) |
| POST | `/feeds/{id}` | Rename or move feed, or change its URL |
| DELETE | `/feeds/{id}` | Remove feed (after confirm) |
| POST | `/refresh` | Fetch all feeds now |
| GET | `/opml` | Download subscriptions as OPML |
| POST | `/opml` | Import OPML upload |

htmx requests (header `HX-Request`) get a partial (a row, the sidebar, the list); other requests get the full page. Unread counts in the sidebar refresh with an out-of-band swap after each state change.

#### Security

- **Bind address** defaults to `127.0.0.1:8080`. Exposing it needs an explicit `--addr`, ideally behind a VPN such as Tailscale.
- **No auth in the app** in the MVP; access control is the network.
- **Cross-site request forgery:** every non-GET request must carry the `HX-Request` header and, when present, an `Origin` matching the host. Browsers cannot add custom headers to cross-origin requests without CORS, which leanfeed never enables. Forms without htmx are not used for mutations.
- **Feed content** is shown only after sanitizing (N6), plus a `Content-Security-Policy` that allows scripts only from the app itself.
- **Other headers:** `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`.
- **Input:** feed URLs must be `http` or `https`; OPML uploads are capped at 5 MB.

### Configuration and deployment

Configuration is command-line flags with matching environment variables; there is no config file in the MVP.

| Flag | Env var | Default |
| --- | --- | --- |
| `--data` | `LEANFEED_DATA` | `./data` |
| `--addr` | `LEANFEED_ADDR` | `127.0.0.1:8080` |
| `--interval` | `LEANFEED_INTERVAL` | `30m` |
| `--workers` | `LEANFEED_WORKERS` | `4` |

Commands: `leanfeed serve` runs the server and fetcher; `leanfeed import <file.opml>` and `leanfeed export` work offline on the data directory. Deployment is the single binary under systemd, or a small Docker image with the data directory as a volume. Logs go to stdout via `log/slog`. Backup and restore are out of scope: use external tools such as filesystem snapshots or restic on the data directory. leanfeed only guarantees that files on disk are always consistent (N5).

**Versioning:** the git tag is the version, injected at build time with `go build -ldflags "-X main.version=$(git describe --tags --always)"`. `leanfeed --version` prints it and the fetcher's `User-Agent` includes it.

**Graceful shutdown:** on SIGINT or SIGTERM, stop the scheduler, cancel in-flight fetches through their context, call `http.Server.Shutdown` with a 10 s timeout, then `store.Close()`, which waits for pending writes.

**Known limitation:** `leanfeed import` and `leanfeed export` write the data directory directly and must only run while the server is stopped. Enforcing this (for example with a lock file) is deferred.

### Testing

- **Fixture corpus** in `testdata/feeds/`: real feeds (RSS 2.0, Atom, JSON Feed) plus broken ones: bad encoding, missing GUIDs, missing dates, relative URLs, duplicate items, hostile HTML.
- **Table-driven tests** per package; the fetcher runs against `httptest.Server` serving fixtures, including `304`, `429`, redirects, timeouts and oversized bodies.
- **Store tests** use `t.TempDir()`, including leftover `.tmp-` cleanup and preserved read state when content changes.
- **Benchmarks** generate 10,000 entries and assert index build under 1 s (N3) and list rendering under 100 ms (N4).
- **Sanitizer tests** assert that scripts, event handlers, `javascript:` URLs and iframes never survive.

The `storetest` contract suite runs against `filestore` in CI and against any future implementation.

### Milestones

1. **Store:** Store interface and contract tests, then filestore: data layout, IDs, atomic writes, index build, benchmark for N3.
2. **Fetcher:** scheduler, conditional GET, backoff, parse, sanitize, upsert.
3. **Web UI:** sidebar, entry list, entry view, read and star actions, mark all read.
4. **Feed management:** add, remove, rename, folders, OPML import and export, fetch status.
5. **Hardening:** security headers and CSRF check, fixture corpus complete, Dockerfile, README.

### Open questions

- [ ] Keyboard shortcuts (j/k, s, m) in the MVP or deferred?

Resolved: feed directories use a slug plus hash; removing a feed deletes its directory entirely after a confirmation prompt; backup and restore are external; entries are kept forever in the MVP.

### Deferred features

Search, filters and rules, full-text extraction, pruning of old entries (until then, entries are kept forever), auth, Fever or Google Reader API, feed discovery from site URLs, image proxy, multiple users.
