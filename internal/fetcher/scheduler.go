package fetcher

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"leanfeed/internal/store"
)

// Subscribe fetches and parses feedURL before subscribing, so a URL that is
// not a feed is never added. The feed's title comes from the feed itself,
// falling back to the URL's host. After a permanent redirect the final URL
// is stored.
func (f *Fetcher) Subscribe(ctx context.Context, feedURL, folder string) (store.Feed, error) {
	if err := store.CheckFeedURL(feedURL); err != nil {
		return store.Feed{}, err
	}
	res, err := f.get(ctx, feedURL, "", "")
	if err != nil {
		return store.Feed{}, fmt.Errorf("fetching %s: %w", feedURL, err)
	}
	parsed, err := parse(res.body)
	if err != nil {
		return store.Feed{}, err
	}
	if res.permanentURL != "" {
		feedURL = res.permanentURL
	}
	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		u, _ := url.Parse(feedURL)
		title = u.Host
	}
	feed, err := f.store.AddFeed(ctx, store.NewFeed{
		URL: feedURL, Title: title, SiteURL: siteURL(parsed, feedURL), Folder: folder,
	})
	if err != nil {
		return store.Feed{}, err
	}
	if _, err := f.store.UpsertEntries(ctx, feed.ID, incomingEntries(parsed, feedURL)); err != nil {
		return store.Feed{}, err
	}
	res.permanentURL = "" // already stored as the feed URL
	if err := f.store.RecordFetch(ctx, feed.ID, f.result(feed, res, parsed, nil)); err != nil {
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
