package fetcher

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"leanfeed/internal/store"
)

// Subscribe fetches and parses feedURL before subscribing, so a URL that is
// not a feed is never added. The feed's title comes from the feed itself,
// falling back to the URL's host. After a permanent redirect the final URL
// is stored.
func (f *Fetcher) Subscribe(ctx context.Context, feedURL, folder string) (store.Feed, error) {
	p, err := f.probe(ctx, feedURL)
	if err != nil {
		return store.Feed{}, err
	}
	title := strings.TrimSpace(p.parsed.Title)
	if title == "" {
		u, _ := url.Parse(p.url)
		title = u.Host
	}
	feed, err := f.store.AddFeed(ctx, store.NewFeed{
		URL: p.url, Title: title, SiteURL: siteURL(p.parsed, p.url), Folder: folder,
	})
	if err != nil {
		return store.Feed{}, err
	}
	return f.storeProbe(ctx, feed, p)
}

// ChangeURL points a feed at a new URL. Like Subscribe, it fetches the URL
// first and leaves the feed unchanged unless the URL is a feed. The feed
// keeps its ID, title, folder and entries.
func (f *Fetcher) ChangeURL(ctx context.Context, id, feedURL string) (store.Feed, error) {
	p, err := f.probe(ctx, feedURL)
	if err != nil {
		return store.Feed{}, err
	}
	feed, err := f.store.UpdateFeed(ctx, id, store.FeedUpdate{URL: &p.url})
	if err != nil {
		return store.Feed{}, err
	}
	return f.storeProbe(ctx, feed, p)
}

// probed is a URL that was fetched and parsed as a feed.
type probed struct {
	url    string // the URL to store: the redirect target after a permanent redirect
	res    response
	parsed *gofeed.Feed
}

// probe fetches and parses feedURL without changing the store.
func (f *Fetcher) probe(ctx context.Context, feedURL string) (probed, error) {
	if err := store.CheckFeedURL(feedURL); err != nil {
		return probed{}, err
	}
	res, err := f.get(ctx, feedURL, "", "")
	if err != nil {
		return probed{}, fmt.Errorf("fetching %s: %w", feedURL, err)
	}
	parsed, err := parse(res.body)
	if err != nil {
		return probed{}, err
	}
	if res.permanentURL != "" {
		feedURL = res.permanentURL
		res.permanentURL = "" // stored as the feed URL, not as a redirect
	}
	return probed{url: feedURL, res: res, parsed: parsed}, nil
}

// storeProbe stores the entries of a probed feed and records the fetch.
func (f *Fetcher) storeProbe(ctx context.Context, feed store.Feed, p probed) (store.Feed, error) {
	if _, err := f.store.UpsertEntries(ctx, feed.ID, incomingEntries(p.parsed, p.url)); err != nil {
		return store.Feed{}, err
	}
	if err := f.store.RecordFetch(ctx, feed.ID, f.result(feed, p.res, p.parsed, nil)); err != nil {
		return store.Feed{}, err
	}
	return f.store.GetFeed(ctx, feed.ID)
}

// Run starts the workers and checks for due feeds at once and then every
// tick. It returns when ctx is cancelled and all workers have stopped;
// cancelling ctx also cancels in-flight fetches.
func (f *Fetcher) Run(ctx context.Context) {
	done := make(chan struct{})
	for range f.cfg.Workers {
		go func() {
			defer func() { done <- struct{}{} }()
			f.work(ctx)
		}()
	}

	f.enqueueDue(ctx)
	ticker := time.NewTicker(f.tick)
	defer ticker.Stop()
	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case <-ticker.C:
			f.enqueueDue(ctx)
		}
	}
	for range f.cfg.Workers {
		<-done
	}
}

func (f *Fetcher) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-f.queue:
			err := f.Fetch(ctx, id)
			if errors.Is(err, store.ErrNotFound) {
				f.log.Debug("feed removed before fetch", "feed", id)
			}
			f.mu.Lock()
			delete(f.pending, id)
			f.mu.Unlock()
		}
	}
}

func (f *Fetcher) enqueueDue(ctx context.Context) {
	due, err := f.store.DueFeeds(ctx, f.now())
	if err != nil {
		f.log.Error("listing due feeds", "err", err)
		return
	}
	for _, feed := range due {
		f.enqueue(feed.ID)
	}
}

// RefreshAll queues every feed for fetching, whether due or not.
func (f *Fetcher) RefreshAll(ctx context.Context) error {
	feeds, err := f.store.ListFeeds(ctx)
	if err != nil {
		return err
	}
	for _, feed := range feeds {
		f.enqueue(feed.ID)
	}
	return nil
}

// enqueue queues a feed unless it is already queued or being fetched. If
// the queue is full the feed is skipped; it stays due and is retried on a
// later tick.
func (f *Fetcher) enqueue(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending[id] {
		return
	}
	select {
	case f.queue <- id:
		f.pending[id] = true
	default:
		f.log.Warn("fetch queue full; skipping feed until the next check", "feed", id)
	}
}
