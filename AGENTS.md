# leanfeed

A small, self-hosted RSS, Atom and JSON Feed reader for one person: one Go binary,
plain files on disk, and an htmx web UI. `design.md` holds the requirements and the
technical design; `README.md` is the user documentation.

When goals conflict, they rank in this order (from `design.md`):

1. Simple, readable code over performance.
2. Plain, portable files: all state is JSON, HTML or OPML.
3. Fast where it matters: startup and page loads at personal scale.
4. Secure by default: feed content is always sanitized; the server binds to localhost.
5. Small scope: anything not needed to read feeds daily is deferred.

## Commands

- `make` — runs all tests, then builds `bin/leanfeed`
- `make build` — builds the static binary `bin/leanfeed`, stamping in the version from `git describe`
- `make test` — runs `go vet`, then all tests with the race detector, including the slow checks with 10,000 entries and 300 feeds; must pass before a change is done
- `make test-short` — runs the tests without the slow checks and the browser tests; use it while iterating
- `make test-ui` — runs only the UI flow tests and the headless-browser tests
- `make lint` — runs golangci-lint, which also reports formatting (gofumpt, goimports); must pass before a change is done
- `make fmt` — applies the formatting `make lint` checks for
- `make vuln` — checks the dependencies for known vulnerabilities with govulncheck
- `make clean` — removes `bin/`, including the installed lint and vulnerability tools

Use the Makefile targets; do not call `go build` or `go test` directly. Run a single
test while working on it, but finish with `make test` and `make lint`. This applies to
local development only: CI calls the Go toolchain directly, see
`.github/workflows/ci.yml`. The tool versions there and in the Makefile must match.

Tools come from the Nix flake. Run `nix develop -c make test-ui` (or `make test`) so the
browser tests find Chrome through `LEANFEED_CHROME`; without it they are skipped, which
is not a pass. Many tests start `httptest` servers, so they need permission to listen
on a local port.

Containers are built with Podman, not Docker. Build with
`podman build --format docker`: Podman's default OCI format drops the `HEALTHCHECK`
instruction.

## Working agreement

- Use red/green TDD. Write the failing test first, run it, and watch it fail for the
  expected reason (a missing feature, not a typo or a compile error). Then write the
  minimum code that makes it pass, then refactor while the tests stay green. A test
  that passes at once tests nothing new.
- Keep changes small and within the request. If you notice adjacent work, mention it
  instead of doing it.
- Write idiomatic Go. Follow Effective Go and the Go Code Review Comments, and match
  the style of the surrounding code.
- Prefer readable code over concise code. A few more lines of plain, obvious code beat
  a shorter, clever version.
- Use descriptive names for variables, functions and types. Single letters are fine
  only for method receivers and very short loops.
- Use strong types. Give domain values their own named types and constants (see
  `store.View`), use structs rather than `map[string]any`, and keep `any` out of
  APIs. Use the sentinel errors `store.ErrNotFound` and `store.ErrExists` and check
  them with `errors.Is`.
- Use established patterns and best practices. Reach for the standard library first,
  then the patterns this codebase already uses (see Conventions), before inventing
  new ones.
- Return errors and wrap them with `%w`; do not panic. Check every error. Where
  ignoring one is right, say so with `_ =` and, if the reason is not obvious, a comment.
- Comments explain why, not what.
- Do not add third-party dependencies without asking. The current ones are gofeed,
  bluemonday, `golang.org/x/net`, cobra, htmx and the Lora and Fira Code fonts, plus
  chromedp for the browser tests.
- Update `README.md` and `design.md` in the same commit when behavior, commands,
  flags, routes or the data format change. Write documentation in plain language:
  short sentences, one idea per sentence.
- Make one commit per feature or fix. The subject is imperative and capitalized, for
  example "Add a /healthz endpoint"; the body says why.

## Layout

| Package | Job |
| --- | --- |
| `cmd/leanfeed` | Commands (cobra), flags and environment variables, wiring, graceful shutdown |
| `internal/ids` | Feed and entry IDs, shared by every store |
| `internal/opml` | OPML reading and writing |
| `internal/store` | The `Store` interface, domain types and errors |
| `internal/store/storetest` | Contract tests that every `Store` must pass |
| `internal/store/filestore` | The file-based `Store`: data directory, atomic writes, in-memory index |
| `internal/fetcher` | Scheduler, HTTP, parsing, sanitizing and feed discovery |
| `internal/web` | Handlers, `templates/` and `static/`, all embedded in the binary |
| `testdata/feeds` | Real and broken feed fixtures |

## Conventions

- Only the store touches the data directory. `web` and `fetcher` depend on the
  `store.Store` interface; only `cmd/leanfeed` knows it is a `filestore`.
- A new or changed `Store` method gets a case in `storetest.Run`, so a future database
  store stays a drop-in replacement.
- Every file write is atomic: write a `.tmp-` file, sync it, rename it into place
  (`writeFileAtomic`, `writeJSONAtomic`). A new entry is written as a `.tmp-` directory
  and renamed as a whole.
- Persistent state stays plain JSON, HTML or OPML. No binary or proprietary formats.
- Feed content is sanitized in the fetcher before it reaches the store. Only content
  from `store.EntryContent` may be rendered as `template.HTML`.
- Every page works as a full page load. htmx requests (`HX-Request`) get partials, and
  a change of state returns out-of-band swaps for everything that shows it: the row,
  the entry actions and the sidebar counts.
- Requests that change data are `POST` or `DELETE` sent by htmx. The CSRF check relies
  on the `HX-Request` header, so do not add plain HTML forms that change data.
- The Content Security Policy allows scripts, styles and fonts only from leanfeed
  itself: no inline `<script>`, `style` attributes or event handler attributes.
  Browser behavior goes in `static/ui.js`.
- A theme is a block of custom properties under `[data-theme="…"]` in `style.css`,
  with a dark version under `prefers-color-scheme`, plus an entry in `themes` in
  `internal/web/appearance.go`. Rules use the properties, never fixed colors or fonts.
- Every parameter is a flag with a matching `LEANFEED_*` environment variable, listed
  in `envVars` in `cmd/leanfeed/main.go` and in the README tables.
- Logging uses `log/slog`.
- Tests use the standard library only. Fakes such as `fakeFetcher` are hand-written
  in the test file that needs them; there is no mocking library. Network tests use
  `httptest` servers and the fixtures in `testdata/feeds`.
- UI changes need flow tests (`flows_test.go`, run through `uiharness_test.go`) for
  the htmx behavior, and browser tests (`browser_test.go`) for anything that depends
  on JavaScript, CSS or the Content Security Policy.
