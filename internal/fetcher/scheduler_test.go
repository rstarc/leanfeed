package fetcher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"leanfeed/internal/store"
)

func TestSubscribe(t *testing.T) {
	srv := httptest.NewServer(serveFixture(t, "rss2.xml"))
	defer srv.Close()
	s := newStore(t)

	feed, err := newFetcher(t, s).Subscribe(ctx, srv.URL+"/feed.xml", "Tech")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(feed.ID, "example-rss-") || feed.Title != "Example RSS" || feed.Folder != "Tech" ||
		feed.URL != srv.URL+"/feed.xml" || feed.SiteURL != "https://rss.example/" ||
		feed.LastStatus != store.StatusOK || !feed.NextFetchAt.Equal(now.Add(30*time.Minute)) {
		t.Errorf("subscribed feed = %+v", feed)
	}
	if stored := getFeed(t, s, feed.ID); stored != feed {
		t.Errorf("stored feed = %+v, want %+v", stored, feed)
	}
	if es := entries(t, s, feed.ID); len(es) != 3 {
		t.Errorf("stored %d entries, want 3", len(es))
	}
}

func TestSubscribeRejects(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/page", serveFixture(t, "not-a-feed.html"))
	mux.Handle("/gone", http.NotFoundHandler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tests := []struct{ name, url, errText string }{
		{"HTML page", srv.URL + "/page", "not a valid feed"},
		{"HTTP error", srv.URL + "/gone", "404"},
		{"file URL", "file:///etc/passwd", "only http and https"},
		{"javascript URL", "javascript:alert(1)", "only http and https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			_, err := newFetcher(t, s).Subscribe(ctx, tt.url, "")
			if err == nil || !strings.Contains(err.Error(), tt.errText) {
				t.Errorf("Subscribe error = %v, want it to mention %q", err, tt.errText)
			}
			if feeds, _ := s.ListFeeds(ctx); len(feeds) != 0 {
				t.Errorf("rejected Subscribe stored feeds: %+v", feeds)
			}
		})
	}
}

func TestSubscribeTwiceIsErrExists(t *testing.T) {
	srv := httptest.NewServer(serveFixture(t, "rss2.xml"))
	defer srv.Close()
	s := newStore(t)
	f := newFetcher(t, s)
	if _, err := f.Subscribe(ctx, srv.URL, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Subscribe(ctx, srv.URL, ""); !errors.Is(err, store.ErrExists) {
		t.Errorf("second Subscribe error = %v, want ErrExists", err)
	}
}

func TestSubscribeStoresPermanentRedirectTarget(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/old", http.RedirectHandler("/new", http.StatusMovedPermanently))
	mux.Handle("/new", serveFixture(t, "rss2.xml"))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := newStore(t)
	feed, err := newFetcher(t, s).Subscribe(ctx, srv.URL+"/old", "")
	if err != nil {
		t.Fatal(err)
	}
	if feed.URL != srv.URL+"/new" {
		t.Errorf("URL = %q, want the redirect target", feed.URL)
	}
}

func TestSubscribeUntitledFeedUsesHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<rss version="2.0"><channel><title>  </title><item><title>x</title><guid>x</guid></item></channel></rss>`)
	}))
	defer srv.Close()
	s := newStore(t)
	feed, err := newFetcher(t, s).Subscribe(ctx, srv.URL+"/rss", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimPrefix(srv.URL, "http://"); feed.Title != want {
		t.Errorf("title = %q, want host %q", feed.Title, want)
	}
}

func TestChangeURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/gone", http.NotFoundHandler())
	mux.Handle("/feed.xml", serveFixture(t, "rss2.xml"))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := newStore(t)
	f := newFetcher(t, s)
	old := addFeed(t, s, srv.URL+"/gone")
	if err := f.Fetch(ctx, old.ID); err == nil {
		t.Fatal("fetching the broken URL succeeded")
	}

	feed, err := f.ChangeURL(ctx, old.ID, srv.URL+"/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	if feed.ID != old.ID || feed.Title != old.Title || feed.URL != srv.URL+"/feed.xml" ||
		feed.LastStatus != store.StatusOK || feed.ConsecutiveFailures != 0 || feed.SiteURL != "https://rss.example/" {
		t.Errorf("feed after ChangeURL = %+v", feed)
	}
	if stored := getFeed(t, s, old.ID); stored != feed {
		t.Errorf("stored feed = %+v, want %+v", stored, feed)
	}
	if es := entries(t, s, old.ID); len(es) != 3 {
		t.Errorf("stored %d entries, want 3", len(es))
	}
}

func TestChangeURLRejects(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/page", serveFixture(t, "not-a-feed.html"))
	mux.Handle("/other.xml", serveFixture(t, "atom.xml"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tests := []struct {
		name, url string
		wantErr   func(error) bool
	}{
		{"HTML page", srv.URL + "/page", func(err error) bool { return err != nil && strings.Contains(err.Error(), "not a valid feed") }},
		{"file URL", "file:///etc/passwd", func(err error) bool { return err != nil && strings.Contains(err.Error(), "only http and https") }},
		{"another feed's URL", srv.URL + "/other.xml", func(err error) bool { return errors.Is(err, store.ErrExists) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			feed := addFeed(t, s, "https://old.example/feed.xml")
			if _, err := s.AddFeed(ctx, store.NewFeed{URL: srv.URL + "/other.xml", Title: "Other"}); err != nil {
				t.Fatal(err)
			}
			_, err := newFetcher(t, s).ChangeURL(ctx, feed.ID, tt.url)
			if !tt.wantErr(err) {
				t.Errorf("ChangeURL error = %v", err)
			}
			if got := getFeed(t, s, feed.ID); got != feed {
				t.Errorf("rejected ChangeURL changed the feed to %+v", got)
			}
		})
	}
}

// eventually polls cond every 10 ms until it holds or 10 s pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	eventuallyWithin(t, 10*time.Second, what, cond)
}

func eventuallyWithin(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runFetcher starts f.Run with a 10 ms tick and returns a function that
// stops it and waits for Run to return.
func runFetcher(t *testing.T, f *Fetcher) (stop func()) {
	t.Helper()
	f.tick = 10 * time.Millisecond
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { f.Run(rctx); close(done) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestRunFetchesOnlyDueFeeds(t *testing.T) {
	cs := &countingServer{hits: map[string]int{}}
	srv := httptest.NewServer(cs.handler(t))
	defer srv.Close()
	s := newStore(t)
	addFeed(t, s, srv.URL+"/due")
	later := addFeed(t, s, srv.URL+"/later")
	if err := s.RecordFetch(ctx, later.ID, store.FetchResult{Status: store.StatusOK, NextFetchAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	runFetcher(t, newFetcher(t, s))
	eventually(t, "the due feed to be fetched", func() bool { return cs.count("/due") == 1 })
	time.Sleep(100 * time.Millisecond) // about 10 more ticks
	if cs.count("/due") != 1 || cs.count("/later") != 0 {
		t.Errorf("fetch counts: due %d, later %d; want 1 and 0", cs.count("/due"), cs.count("/later"))
	}
}

func TestRefreshAllFetchesFeedsThatAreNotDue(t *testing.T) {
	cs := &countingServer{hits: map[string]int{}}
	srv := httptest.NewServer(cs.handler(t))
	defer srv.Close()
	s := newStore(t)
	for _, p := range []string{"/a", "/b"} {
		feed := addFeed(t, s, srv.URL+p)
		s.RecordFetch(ctx, feed.ID, store.FetchResult{Status: store.StatusOK, NextFetchAt: now.Add(time.Hour)})
	}
	f := newFetcher(t, s)
	runFetcher(t, f)

	if err := f.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both feeds to be fetched", func() bool { return cs.count("/a") == 1 && cs.count("/b") == 1 })
}

func TestRunCancelsInFlightFetches(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
	}))
	defer srv.Close()
	defer close(release)
	s := newStore(t)
	feed := addFeed(t, s, srv.URL)

	stop := runFetcher(t, newFetcher(t, s))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not start fetching the due feed")
	}
	stop()
	if got := getFeed(t, s, feed.ID); got.LastStatus != "" {
		t.Errorf("cancelled fetch was recorded: %+v", got)
	}
}

// TestRun300Feeds guards N8: 300 feeds are fetched without tuning.
func TestRun300Feeds(t *testing.T) {
	if testing.Short() {
		t.Skip("fetches 300 feeds")
	}
	cs := &countingServer{hits: map[string]int{}}
	srv := httptest.NewServer(cs.handler(t))
	defer srv.Close()
	s := newStore(t)
	for i := range 300 {
		addFeed(t, s, fmt.Sprintf("%s/feed/%d", srv.URL, i))
	}
	start := time.Now()
	runFetcher(t, newFetcher(t, s))
	// Every entry is three synced files. On macOS each sync is a full flush
	// (F_FULLFSYNC, about 4 ms), so this takes about 15 s there.
	eventuallyWithin(t, time.Minute, "all 300 feeds to be fetched", func() bool {
		due, _ := s.DueFeeds(ctx, now)
		return len(due) == 0
	})
	t.Logf("fetched 300 feeds (900 entries) in %v", time.Since(start))
	feeds, _ := s.ListFeeds(ctx)
	for _, f := range feeds {
		if f.LastStatus != store.StatusOK {
			t.Errorf("feed %s status %q: %s", f.ID, f.LastStatus, f.LastError)
		}
	}
	if page, _ := s.ListEntries(ctx, store.Query{View: store.ViewAll}); page.Total != 900 {
		t.Errorf("stored %d entries, want 900", page.Total)
	}
}
