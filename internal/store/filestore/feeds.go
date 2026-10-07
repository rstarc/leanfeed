package filestore

import (
	"context"
	"io"
	"maps"
	"os"
	"sort"
	"strings"
	"time"

	"leanfeed/internal/ids"
	"leanfeed/internal/opml"
	"leanfeed/internal/store"
)

// ListFeeds returns all feeds sorted by title, ignoring case.
func (s *Store) ListFeeds(ctx context.Context) ([]store.Feed, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sortedFeeds(func(store.Feed) bool { return true }), nil
}

func (s *Store) sortedFeeds(keep func(store.Feed) bool) []store.Feed {
	out := []store.Feed{}
	for _, f := range s.feeds {
		if keep(*f) {
			out = append(out, *f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Store) GetFeed(ctx context.Context, id string) (store.Feed, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.feeds[id]
	if !ok {
		return store.Feed{}, store.ErrNotFound
	}
	return *f, nil
}

// hasURL reports whether a feed other than exceptID uses url.
func (s *Store) hasURL(url, exceptID string) bool {
	for _, f := range s.feeds {
		if f.URL == url && f.ID != exceptID {
			return true
		}
	}
	return false
}

// AddFeed subscribes to a feed. It creates the feed's directory and
// feed.json first, then adds it to the OPML; a crash in between leaves an
// orphan directory that startup ignores.
func (s *Store) AddFeed(ctx context.Context, nf store.NewFeed) (store.Feed, error) {
	if err := store.CheckFeedURL(nf.URL); err != nil {
		return store.Feed{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := store.Feed{ID: ids.FeedID(nf.Title, nf.URL), URL: nf.URL, Title: nf.Title, SiteURL: nf.SiteURL, Folder: nf.Folder}
	if _, exists := s.feeds[f.ID]; exists || s.hasURL(f.URL, "") {
		return store.Feed{}, store.ErrExists
	}
	if err := s.writeFeed(f); err != nil {
		return store.Feed{}, err
	}
	next := maps.Clone(s.feeds)
	next[f.ID] = &f
	if err := s.writeOPML(next); err != nil {
		return store.Feed{}, err
	}
	s.feeds = next
	return f, nil
}

func (s *Store) UpdateFeed(ctx context.Context, id string, u store.FeedUpdate) (store.Feed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.feeds[id]
	if !ok {
		return store.Feed{}, store.ErrNotFound
	}
	f := *old
	if u.Title != nil {
		f.Title = *u.Title
	}
	if u.Folder != nil {
		f.Folder = *u.Folder
	}
	if u.URL != nil {
		if err := store.CheckFeedURL(*u.URL); err != nil {
			return store.Feed{}, err
		}
		if s.hasURL(*u.URL, id) {
			return store.Feed{}, store.ErrExists
		}
		f.URL = *u.URL
	}
	if err := s.saveFeed(f); err != nil {
		return store.Feed{}, err
	}
	return f, nil
}

// saveFeed writes feed.json and the OPML, then replaces the feed in the index.
func (s *Store) saveFeed(f store.Feed) error {
	if err := s.writeFeed(f); err != nil {
		return err
	}
	next := maps.Clone(s.feeds)
	next[f.ID] = &f
	if err := s.writeOPML(next); err != nil {
		return err
	}
	s.feeds = next
	return nil
}

// RemoveFeed removes the feed from the OPML first, then deletes its
// directory. A crash in between leaves an orphan directory.
func (s *Store) RemoveFeed(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.feeds[id]; !ok {
		return store.ErrNotFound
	}
	next := maps.Clone(s.feeds)
	delete(next, id)
	if err := s.writeOPML(next); err != nil {
		return err
	}
	s.feeds = next
	for _, e := range s.byFeed[id] {
		delete(s.entries, e.ID)
	}
	delete(s.byFeed, id)
	return os.RemoveAll(s.feedDir(id))
}

// DueFeeds returns the feeds whose next fetch time is not after now.
func (s *Store) DueFeeds(ctx context.Context, now time.Time) ([]store.Feed, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sortedFeeds(func(f store.Feed) bool { return !f.NextFetchAt.After(now) }), nil
}

func (s *Store) RecordFetch(ctx context.Context, id string, r store.FetchResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.feeds[id]
	if !ok {
		return store.ErrNotFound
	}
	f := *old
	f.LastFetchedAt = r.FetchedAt
	f.NextFetchAt = r.NextFetchAt
	f.LastStatus = r.Status
	f.LastError = r.Error
	f.ConsecutiveFailures = r.ConsecutiveFailures
	f.ETag, f.LastModified = r.ETag, r.LastModified
	if r.URL != "" {
		f.URL = r.URL
	}
	if r.SiteURL != "" {
		f.SiteURL = r.SiteURL
	}
	if f.URL == old.URL && f.SiteURL == old.SiteURL {
		// The OPML is unchanged; only feed.json needs writing.
		if err := s.writeFeed(f); err != nil {
			return err
		}
		*old = f
		return nil
	}
	return s.saveFeed(f)
}

// ImportOPML subscribes to every feed in the document that has a valid URL
// and is not already subscribed. IDs in the document are ignored.
func (s *Store) ImportOPML(ctx context.Context, r io.Reader) (store.ImportStats, error) {
	subs, err := opml.Parse(r)
	if err != nil {
		return store.ImportStats{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var stats store.ImportStats
	next := maps.Clone(s.feeds)
	seenURL := map[string]bool{}
	for _, f := range s.feeds {
		seenURL[f.URL] = true
	}
	for _, sub := range subs {
		title := sub.Title
		if title == "" {
			title = sub.XMLURL
		}
		f := store.Feed{ID: ids.FeedID(title, sub.XMLURL), URL: sub.XMLURL, Title: title, SiteURL: sub.HTMLURL, Folder: sub.Folder}
		if store.CheckFeedURL(f.URL) != nil || seenURL[f.URL] || next[f.ID] != nil {
			stats.Skipped++
			continue
		}
		if err := s.writeFeed(f); err != nil {
			return store.ImportStats{}, err
		}
		seenURL[f.URL] = true
		next[f.ID] = &f
		stats.Added++
	}
	if err := s.writeOPML(next); err != nil {
		return store.ImportStats{}, err
	}
	s.feeds = next
	return stats, nil
}

func (s *Store) ExportOPML(ctx context.Context, w io.Writer) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return opml.Write(w, subscriptions(s.feeds))
}
