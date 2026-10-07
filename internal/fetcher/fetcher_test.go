package fetcher

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"leanfeed/internal/store"
	"leanfeed/internal/store/filestore"
)

var (
	ctx      = context.Background()
	quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))
	now      = time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "feeds", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// serveFixture returns a handler that serves a fixture file.
func serveFixture(t testing.TB, name string) http.HandlerFunc {
	data := fixture(t, name)
	return func(w http.ResponseWriter, r *http.Request) { w.Write(data) }
}

func newStore(t testing.TB) store.Store {
	t.Helper()
	s, err := filestore.Open(t.TempDir(), quietLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newFetcher returns a fetcher with a 30 minute interval and a fixed clock.
func newFetcher(t testing.TB, s store.Store) *Fetcher {
	f := New(s, Config{Interval: 30 * time.Minute, Workers: 4, UserAgent: "leanfeed/test"}, quietLog)
	f.now = func() time.Time { return now }
	return f
}

func addFeed(t testing.TB, s store.Store, url string) store.Feed {
	t.Helper()
	feed, err := s.AddFeed(ctx, store.NewFeed{URL: url, Title: "Test feed"})
	if err != nil {
		t.Fatal(err)
	}
	return feed
}

func getFeed(t testing.TB, s store.Store, id string) store.Feed {
	t.Helper()
	f, err := s.GetFeed(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func entries(t testing.TB, s store.Store, feedID string) []store.Entry {
	t.Helper()
	page, err := s.ListEntries(ctx, store.Query{View: store.ViewAll, FeedID: feedID})
	if err != nil {
		t.Fatal(err)
	}
	return page.Entries
}

func content(t testing.TB, s store.Store, id string) string {
	t.Helper()
	c, err := s.EntryContent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFetchStoresEntries(t *testing.T) {
	srv := httptest.NewServer(serveFixture(t, "rss2.xml"))
	defer srv.Close()
	s := newStore(t)
	feed := addFeed(t, s, srv.URL+"/feed.xml")

	if err := newFetcher(t, s).Fetch(ctx, feed.ID); err != nil {
		t.Fatal(err)
	}

	got := getFeed(t, s, feed.ID)
	if got.LastStatus != store.StatusOK || got.LastError != "" || !got.LastFetchedAt.Equal(now) ||
		!got.NextFetchAt.Equal(now.Add(30*time.Minute)) || got.ConsecutiveFailures != 0 ||
		got.SiteURL != "https://rss.example/" || got.Title != "Test feed" {
		t.Errorf("feed after fetch = %+v", got)
	}

	es := entries(t, s, feed.ID)
	if len(es) != 3 {
		t.Fatalf("got %d entries, want 3", len(es))
	}
	third, second := es[0], es[1]
	if third.Title != "Third post" || third.Author != "Jane Doe" || third.GUID != "rss-example-3" ||
		third.URL != "https://rss.example/posts/3" || !third.PublishedAt.Equal(time.Date(2026, 10, 6, 14, 0, 0, 0, time.UTC)) {
		t.Errorf("third entry = %+v", third)
	}
	if !second.PublishedAt.Equal(time.Date(2026, 10, 5, 7, 30, 0, 0, time.UTC)) {
		t.Errorf("second entry published = %v, want 07:30 UTC", second.PublishedAt)
	}

	c := content(t, s, third.ID)
	for _, want := range []string{"<em>third</em>", `href="https://rss.example/about"`, `src="https://rss.example/posts/img/3.png"`} {
		if !strings.Contains(c, want) {
			t.Errorf("content %q missing %q", c, want)
		}
	}
	if strings.Contains(c, "script") || strings.Contains(c, "Summary of the third") {
		t.Errorf("content %q: want full content, sanitized", c)
	}
	if c := content(t, s, second.ID); c != "<p>Only a summary for the second post.</p>" {
		t.Errorf("summary-only content = %q", c)
	}
}

func TestFetchSendsUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Write(fixture(t, "rss2.xml"))
	}))
	defer srv.Close()
	s := newStore(t)
	feed := addFeed(t, s, srv.URL)
	newFetcher(t, s).Fetch(ctx, feed.ID)
	if ua != "leanfeed/test" {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestFetchConditionalGet(t *testing.T) {
	var gotETag, gotModified string
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotETag, gotModified = r.Header.Get("If-None-Match"), r.Header.Get("If-Modified-Since")
		if gotETag == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Tue, 06 Oct 2026 08:00:00 GMT")
		w.Write(fixture(t, "rss2.xml"))
	}))
	defer srv.Close()
	s := newStore(t)
	feed := addFeed(t, s, srv.URL)
	f := newFetcher(t, s)

	if err := f.Fetch(ctx, feed.ID); err != nil {
		t.Fatal(err)
	}
	if gotETag != "" || gotModified != "" {
		t.Errorf("first request sent validators %q, %q", gotETag, gotModified)
	}
	es := entries(t, s, feed.ID)
	if len(es) != 3 {
		t.Fatalf("first fetch stored %d entries, want 3", len(es))
	}
	if err := s.SetRead(ctx, es[0].ID, true); err != nil {
		t.Fatal(err)
	}

	later := now.Add(time.Hour)
	f.now = func() time.Time { return later }
	if err := f.Fetch(ctx, feed.ID); err != nil {
		t.Fatal(err)
	}
	if gotETag != `"v1"` || gotModified != "Tue, 06 Oct 2026 08:00:00 GMT" {
		t.Errorf("second request validators = %q, %q", gotETag, gotModified)
	}
	got := getFeed(t, s, feed.ID)
	if got.LastStatus != store.StatusOK || !got.LastFetchedAt.Equal(later) || !got.NextFetchAt.Equal(later.Add(30*time.Minute)) ||
		got.ETag != `"v1"` || got.LastModified != "Tue, 06 Oct 2026 08:00:00 GMT" {
		t.Errorf("feed after 304 = %+v", got)
	}
	if es := entries(t, s, feed.ID); len(es) != 3 || !es[0].Read {
		t.Errorf("entries after 304 changed: %+v", es)
	}
}

func TestFetchErrorBackoff(t *testing.T) {
	status := http.StatusInternalServerError
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "boom", status)
			return
		}
		w.Header().Set("ETag", `"ok"`)
		w.Write(fixture(t, "rss2.xml"))
	}))
	defer srv.Close()
	s := newStore(t)
	feed := addFeed(t, s, srv.URL)
	f := newFetcher(t, s)

	tests := []struct {
		failures int
		wait     time.Duration
	}{
		{1, time.Hour},
		{2, 2 * time.Hour},
		{3, 4 * time.Hour},
		{4, 8 * time.Hour},
		{5, 16 * time.Hour},
		{6, 24 * time.Hour}, // capped
		{7, 24 * time.Hour},
	}
	for _, tt := range tests {
		if err := f.Fetch(ctx, feed.ID); err == nil {
			t.Fatal("Fetch of HTTP 500 returned no error")
		}
		got := getFeed(t, s, feed.ID)
		if got.LastStatus != store.StatusError || got.ConsecutiveFailures != tt.failures || !got.NextFetchAt.Equal(now.Add(tt.wait)) {
			t.Errorf("after %d failures: status %q, failures %d, next in %v; want next in %v",
				tt.failures, got.LastStatus, got.ConsecutiveFailures, got.NextFetchAt.Sub(now), tt.wait)
		}
		if !strings.Contains(got.LastError, "500") {
			t.Errorf("LastError = %q, want it to mention 500", got.LastError)
		}
	}

	status = http.StatusOK
	if err := f.Fetch(ctx, feed.ID); err != nil {
		t.Fatal(err)
	}
	got := getFeed(t, s, feed.ID)
	if got.LastStatus != store.StatusOK || got.LastError != "" || got.ConsecutiveFailures != 0 || !got.NextFetchAt.Equal(now.Add(30*time.Minute)) {
		t.Errorf("after recovery = %+v", got)
	}

	// A failure keeps the validators from the last success.
	status = http.StatusBadGateway
	f.Fetch(ctx, feed.ID)
	if got := getFeed(t, s, feed.ID); got.ETag != `"ok"` {
		t.Errorf("ETag after failure = %q, want kept", got.ETag)
	}
}

func TestFetchRetryAfter(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		retryAfter string
		wait       time.Duration
	}{
		{"429 longer than backoff", http.StatusTooManyRequests, "7200", 2 * time.Hour},
		{"503 shorter than backoff", http.StatusServiceUnavailable, "60", time.Hour},
		{"HTTP date", http.StatusServiceUnavailable, "Wed, 07 Oct 2026 14:30:00 GMT", 5 * time.Hour},
		{"ignored on 500", http.StatusInternalServerError, "7200", time.Hour},
		{"garbage", http.StatusTooManyRequests, "soon", time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", tt.retryAfter)
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			s := newStore(t)
			feed := addFeed(t, s, srv.URL)
			newFetcher(t, s).Fetch(ctx, feed.ID)
			if got := getFeed(t, s, feed.ID); !got.NextFetchAt.Equal(now.Add(tt.wait)) {
				t.Errorf("next fetch in %v, want %v", got.NextFetchAt.Sub(now), tt.wait)
			}
		})
	}
}

func TestFetchTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	s := newStore(t)
	feed := addFeed(t, s, srv.URL)
	f := newFetcher(t, s)
	f.cfg.Timeout = 50 * time.Millisecond

	if err := f.Fetch(ctx, feed.ID); err == nil {
		t.Fatal("Fetch of a hanging server returned no error")
	}
	if got := getFeed(t, s, feed.ID); got.LastStatus != store.StatusError || got.ConsecutiveFailures != 1 {
		t.Errorf("feed after timeout = %+v", got)
	}
}

func TestFetchBodyLimit(t *testing.T) {
	srv := httptest.NewServer(serveFixture(t, "rss2.xml"))
	defer srv.Close()
	s := newStore(t)
	feed := addFeed(t, s, srv.URL)
	f := newFetcher(t, s)
	f.cfg.MaxBodySize = 1024 // the fixture is larger

	if err := f.Fetch(ctx, feed.ID); err == nil {
		t.Fatal("Fetch of an oversized body returned no error")
	}
	got := getFeed(t, s, feed.ID)
	if got.LastStatus != store.StatusError || !strings.Contains(got.LastError, "larger than") {
		t.Errorf("feed after oversized body = %+v", got)
	}
	if es := entries(t, s, feed.ID); len(es) != 0 {
		t.Errorf("stored %d entries from a truncated body", len(es))
	}
}

func TestFetchRedirects(t *testing.T) {
	tests := []struct {
		name      string
		chain     []int // status of each redirect hop before the feed
		updateURL bool
	}{
		{"301 updates URL", []int{301}, true},
		{"308 updates URL", []int{308}, true},
		{"302 keeps URL", []int{302}, false},
		{"307 keeps URL", []int{307}, false},
		{"301 then 302 keeps URL", []int{301, 302}, false},
		{"two 301s update URL", []int{301, 301}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			for i, code := range tt.chain {
				next := "/hop" + string(rune('1'+i))
				if i == len(tt.chain)-1 {
					next = "/final"
				}
				mux.Handle("/hop"+string(rune('0'+i)), http.RedirectHandler(next, code))
			}
			mux.Handle("/final", serveFixture(t, "rss2.xml"))
			srv := httptest.NewServer(mux)
			defer srv.Close()
			s := newStore(t)
			feed := addFeed(t, s, srv.URL+"/hop0")

			if err := newFetcher(t, s).Fetch(ctx, feed.ID); err != nil {
				t.Fatal(err)
			}
			want := srv.URL + "/hop0"
			if tt.updateURL {
				want = srv.URL + "/final"
			}
			got := getFeed(t, s, feed.ID)
			if got.URL != want || got.ID != feed.ID {
				t.Errorf("feed = %s %s, want URL %s with the same ID", got.ID, got.URL, want)
			}
		})
	}
}

func TestFetchTooManyRedirects(t *testing.T) {
	var srv *httptest.Server
	hops := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()
	s := newStore(t)
	feed := addFeed(t, s, srv.URL)
	if err := newFetcher(t, s).Fetch(ctx, feed.ID); err == nil {
		t.Fatal("redirect loop returned no error")
	}
	if hops != 6 {
		t.Errorf("server saw %d requests, want 6 (first request plus 5 redirects)", hops)
	}
	if got := getFeed(t, s, feed.ID); got.LastStatus != store.StatusError {
		t.Errorf("status = %q", got.LastStatus)
	}
}

func TestFetchNotAFeed(t *testing.T) {
	srv := httptest.NewServer(serveFixture(t, "not-a-feed.html"))
	defer srv.Close()
	s := newStore(t)
	feed := addFeed(t, s, srv.URL)
	if err := newFetcher(t, s).Fetch(ctx, feed.ID); err == nil {
		t.Fatal("Fetch of an HTML page returned no error")
	}
	if got := getFeed(t, s, feed.ID); got.LastStatus != store.StatusError || got.LastError == "" {
		t.Errorf("feed = %+v", got)
	}
}

func TestFetchCancelledIsNotAFailure(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer srv.Close()
	defer close(release)
	s := newStore(t)
	feed := addFeed(t, s, srv.URL)

	cctx, cancel := context.WithCancel(ctx)
	go func() { <-started; cancel() }()
	if err := newFetcher(t, s).Fetch(cctx, feed.ID); err == nil {
		t.Fatal("cancelled Fetch returned no error")
	}
	if got := getFeed(t, s, feed.ID); got.LastStatus != "" || got.ConsecutiveFailures != 0 {
		t.Errorf("cancelled fetch was recorded: %+v", got)
	}
}

func TestFetchFixtureCorpus(t *testing.T) {
	tests := []struct {
		fixture string
		titles  []string // newest first, except entries with equal dates
		check   func(t *testing.T, s store.Store, es []store.Entry)
	}{
		{"atom.xml", []string{"Atom entry two", "Atom entry one"}, func(t *testing.T, s store.Store, es []store.Entry) {
			if es[0].Author != "John Roe" || !es[0].UpdatedAt.Equal(time.Date(2026, 10, 6, 19, 0, 0, 0, time.UTC)) {
				t.Errorf("atom entry = %+v", es[0])
			}
			if c := content(t, s, es[0].ID); c != "<p>Atom <b>HTML</b> content.</p>" {
				t.Errorf("atom content = %q", c)
			}
			if !es[1].PublishedAt.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
				t.Errorf("entry without published date: PublishedAt = %v, want updated date", es[1].PublishedAt)
			}
		}},
		{"feed.json", []string{"JSON item two", "JSON item one"}, func(t *testing.T, s store.Store, es []store.Entry) {
			if es[0].GUID != "json-2" || es[0].Author != "Kim Poe" {
				t.Errorf("json entry = %+v", es[0])
			}
			if c := content(t, s, es[0].ID); c != "<p>JSON <i>HTML</i> content.</p>" {
				t.Errorf("json content = %q", c)
			}
		}},
		{"no-guid.xml", []string{"Has link", "Title and date only"}, nil},
		{"no-dates.xml", []string{"Bad date", "Undated"}, func(t *testing.T, s store.Store, es []store.Entry) {
			for _, e := range es {
				if e.PublishedAt.IsZero() {
					t.Errorf("%q has no published date; want the fetch time", e.Title)
				}
			}
		}},
		{"duplicates.xml", []string{"Other", "Same"}, nil},
		{"relative-urls.xml", []string{"Relative link"}, func(t *testing.T, s store.Store, es []store.Entry) {
			if es[0].URL != "https://relative.example/blog/posts/7" {
				t.Errorf("entry URL = %q, want resolved against the site URL", es[0].URL)
			}
			c := content(t, s, es[0].ID)
			for _, want := range []string{`href="https://relative.example/blog/other"`, `src="https://relative.example/blog/posts/pic.png"`} {
				if !strings.Contains(c, want) {
					t.Errorf("content %q missing %q", c, want)
				}
			}
		}},
		{"hostile.xml", []string{"Hostile content"}, func(t *testing.T, s store.Store, es []store.Entry) {
			c := strings.ToLower(content(t, s, es[0].ID))
			for _, bad := range []string{"steal", "<script", "<iframe", "javascript:", "<style", "<form", "<input", "onclick", "onerror"} {
				if strings.Contains(c, bad) {
					t.Errorf("content %q contains %q", c, bad)
				}
			}
		}},
		{"latin1.xml", []string{"Crème brûlée"}, nil},
		{"bad-encoding.xml", []string{"Caf� au lait"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			srv := httptest.NewServer(serveFixture(t, tt.fixture))
			defer srv.Close()
			s := newStore(t)
			feed := addFeed(t, s, srv.URL)
			if err := newFetcher(t, s).Fetch(ctx, feed.ID); err != nil {
				t.Fatal(err)
			}
			es := entries(t, s, feed.ID)
			var got []string
			for _, e := range es {
				got = append(got, e.Title)
			}
			if !sameSet(got, tt.titles) {
				t.Fatalf("titles = %q, want %q", got, tt.titles)
			}
			if tt.check != nil {
				tt.check(t, s, es)
			}
		})
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// countingServer serves rss2.xml and counts requests per path.
type countingServer struct {
	mu   sync.Mutex
	hits map[string]int
}

func (c *countingServer) handler(t testing.TB) http.HandlerFunc {
	data := fixture(t, "rss2.xml")
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.hits[r.URL.Path]++
		c.mu.Unlock()
		w.Write(data)
	}
}

func (c *countingServer) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}
