// Package fetcher downloads feeds on a schedule, parses and sanitizes them,
// and hands new entries to the store.
package fetcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mmcdole/gofeed"

	"leanfeed/internal/store"
)

const (
	defaultInterval    = 30 * time.Minute
	defaultWorkers     = 4
	defaultTimeout     = 30 * time.Second
	defaultMaxBodySize = 10 << 20
	maxRedirects       = 5
	maxBackoff         = 24 * time.Hour
	queueSize          = 4096
)

// Config holds the fetcher's settings. Zero values get the defaults.
type Config struct {
	Interval    time.Duration // time between successful fetches of a feed
	Workers     int           // concurrent fetches
	UserAgent   string
	Timeout     time.Duration // per request, including the body
	MaxBodySize int64         // bytes
}

// Fetcher fetches feeds and records the results in the store.
type Fetcher struct {
	store     store.Store
	cfg       Config
	log       *slog.Logger
	now       func() time.Time
	transport http.RoundTripper
	tick      time.Duration // how often the scheduler looks for due feeds

	queue   chan string // feed IDs waiting for a worker
	mu      sync.Mutex
	pending map[string]bool // feed IDs in the queue or being fetched
}

func New(s store.Store, cfg Config, log *slog.Logger) *Fetcher {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxBodySize <= 0 {
		cfg.MaxBodySize = defaultMaxBodySize
	}
	return &Fetcher{
		store:     s,
		cfg:       cfg,
		log:       log,
		now:       time.Now,
		transport: http.DefaultTransport,
		tick:      time.Minute,
		queue:     make(chan string, queueSize),
		pending:   map[string]bool{},
	}
}

// response is the outcome of a successful HTTP request.
type response struct {
	notModified        bool
	body               []byte
	etag, lastModified string
	permanentURL       string   // final URL if every redirect was permanent
	url                *url.URL // final URL after all redirects
}

// statusError is a non-2xx, non-304 HTTP response.
type statusError struct {
	status     string
	code       int
	retryAfter time.Duration // from Retry-After on 429 and 503; zero if absent
}

func (e *statusError) Error() string { return "HTTP " + e.status }

// Fetch fetches one feed now, stores new and changed entries, and records
// the outcome. A fetch cancelled through ctx is not recorded.
func (f *Fetcher) Fetch(ctx context.Context, id string) error {
	feed, err := f.store.GetFeed(ctx, id)
	if err != nil {
		return err
	}
	res, err := f.get(ctx, feed.URL, feed.ETag, feed.LastModified)
	var parsed *gofeed.Feed
	if err == nil && !res.notModified {
		parsed, err = parse(res.body)
	}
	if err == nil && parsed != nil {
		_, err = f.store.UpsertEntries(ctx, id, incomingEntries(parsed, feed.URL))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if rerr := f.store.RecordFetch(ctx, id, f.result(feed, res, parsed, err)); rerr != nil {
		return rerr
	}
	if err != nil {
		f.log.Info("fetch failed", "feed", id, "err", err)
	}
	return err
}

// result builds the FetchResult for a fetch that ended with err.
func (f *Fetcher) result(feed store.Feed, res response, parsed *gofeed.Feed, err error) store.FetchResult {
	now := f.now().UTC()
	r := store.FetchResult{FetchedAt: now, ETag: feed.ETag, LastModified: feed.LastModified}
	if err != nil {
		r.Status = store.StatusError
		r.Error = err.Error()
		r.ConsecutiveFailures = feed.ConsecutiveFailures + 1
		wait := f.backoff(r.ConsecutiveFailures)
		var se *statusError
		if errors.As(err, &se) && se.retryAfter > wait {
			wait = se.retryAfter
		}
		r.NextFetchAt = now.Add(wait)
		return r
	}
	r.Status = store.StatusOK
	r.NextFetchAt = now.Add(f.cfg.Interval)
	r.URL = res.permanentURL
	if !res.notModified {
		r.ETag, r.LastModified = res.etag, res.lastModified
	}
	if parsed != nil {
		r.SiteURL = siteURL(parsed, feed.URL)
	}
	return r
}

// backoff doubles the interval per consecutive failure, up to maxBackoff.
func (f *Fetcher) backoff(failures int) time.Duration {
	wait := f.cfg.Interval
	for range failures {
		wait *= 2
		if wait >= maxBackoff {
			return maxBackoff
		}
	}
	return wait
}

// get performs a conditional GET with the configured timeout, size limit
// and redirect limit.
func (f *Fetcher) get(ctx context.Context, feedURL, etag, lastModified string) (response, error) {
	var res response
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return res, err
	}
	req.Header.Set("User-Agent", f.cfg.UserAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	redirected, permanent := false, true
	client := &http.Client{
		Transport: f.transport,
		Timeout:   f.cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			redirected = true
			if code := req.Response.StatusCode; code != http.StatusMovedPermanently && code != http.StatusPermanentRedirect {
				permanent = false
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return res, err
	}
	defer func() { _ = resp.Body.Close() }()

	res.url = resp.Request.URL
	if redirected && permanent {
		res.permanentURL = resp.Request.URL.String()
	}
	if resp.StatusCode == http.StatusNotModified {
		res.notModified = true
		return res, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		se := &statusError{status: resp.Status, code: resp.StatusCode}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			se.retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), f.now())
		}
		return res, se
	}
	res.body, err = io.ReadAll(io.LimitReader(resp.Body, f.cfg.MaxBodySize+1))
	if err != nil {
		return res, err
	}
	if int64(len(res.body)) > f.cfg.MaxBodySize {
		return res, fmt.Errorf("feed is larger than %d bytes", f.cfg.MaxBodySize)
	}
	res.etag = resp.Header.Get("ETag")
	res.lastModified = resp.Header.Get("Last-Modified")
	return res, nil
}

// parseRetryAfter reads a Retry-After value in seconds or as an HTTP date.
// It returns zero for missing or invalid values.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now)
	}
	return 0
}

// parse parses RSS, Atom or JSON Feed. A document that fails to parse
// because of invalid UTF-8 is retried with the invalid bytes replaced.
func parse(body []byte) (*gofeed.Feed, error) {
	parsed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil && !utf8.Valid(body) {
		parsed, err = gofeed.NewParser().Parse(bytes.NewReader(bytes.ToValidUTF8(body, []byte("�"))))
	}
	if err != nil {
		return nil, fmt.Errorf("not a valid feed: %w", err)
	}
	return parsed, nil
}

// siteURL returns the feed's home page, resolved against the feed URL.
func siteURL(parsed *gofeed.Feed, feedURL string) string {
	return resolve(parsed.Link, feedURL)
}

// resolve returns ref resolved against base, or ref unchanged if either
// does not parse.
func resolve(ref, base string) string {
	if ref == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// incomingEntries converts parsed items. Content falls back to the summary;
// relative URLs resolve against the entry link, then the site URL.
func incomingEntries(parsed *gofeed.Feed, feedURL string) []store.IncomingEntry {
	site := siteURL(parsed, feedURL)
	base := site
	if base == "" {
		base = feedURL
	}
	feedAuthor := firstAuthor(parsed.Authors)
	var out []store.IncomingEntry
	for _, item := range parsed.Items {
		link := resolve(item.Link, base)
		raw := item.Content
		if raw == "" {
			raw = item.Description
		}
		contentBase := link
		if contentBase == "" {
			contentBase = base
		}
		e := store.IncomingEntry{
			GUID:       item.GUID,
			URL:        link,
			Title:      strings.TrimSpace(item.Title),
			Author:     firstAuthor(item.Authors),
			RawContent: raw,
			Content:    Sanitize(raw, contentBase),
		}
		if e.Author == "" {
			e.Author = feedAuthor
		}
		if item.PublishedParsed != nil {
			e.PublishedAt = *item.PublishedParsed
		}
		if item.UpdatedParsed != nil {
			e.UpdatedAt = *item.UpdatedParsed
		}
		out = append(out, e)
	}
	return out
}

func firstAuthor(authors []*gofeed.Person) string {
	for _, a := range authors {
		if a != nil && a.Name != "" {
			return a.Name
		}
	}
	return ""
}
