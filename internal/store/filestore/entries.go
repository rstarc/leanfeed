package filestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"leanfeed/internal/ids"
	"leanfeed/internal/store"
)

// UpsertEntries stores new entries and updates entries whose content
// changed, keeping their read and starred state. Entries missing from the
// feed are kept.
func (s *Store) UpsertEntries(ctx context.Context, feedID string, in []store.IncomingEntry) (store.UpsertStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stats store.UpsertStats
	if _, ok := s.feeds[feedID]; !ok {
		return stats, store.ErrNotFound
	}
	now := time.Now().UTC().Truncate(time.Second)
	added := false
	for _, ie := range in {
		id := ids.EntryID(feedID, ie.GUID, ie.URL, ie.Title, ie.PublishedAt)
		sum := sha256.Sum256([]byte(ie.RawContent))
		e := store.Entry{
			ID:            id,
			FeedID:        feedID,
			GUID:          ie.GUID,
			URL:           ie.URL,
			Title:         ie.Title,
			Author:        ie.Author,
			PublishedAt:   ie.PublishedAt.UTC(),
			UpdatedAt:     ie.UpdatedAt.UTC(),
			FetchedAt:     now,
			ContentSHA256: hex.EncodeToString(sum[:]),
		}

		old, exists := s.entries[id]
		switch {
		case !exists:
			e.PublishedAt = publishedOrFallback(e)
			if err := s.createEntry(e, ie); err != nil {
				return stats, err
			}
			s.entries[id] = &e
			s.byFeed[feedID] = append(s.byFeed[feedID], &e)
			added = true
			stats.Added++
		case old.ContentSHA256 == e.ContentSHA256:
			stats.Unchanged++
		default:
			e.FetchedAt, e.Read, e.Starred = old.FetchedAt, old.Read, old.Starred
			e.PublishedAt = publishedOrFallback(e)
			if err := s.updateEntry(e, ie); err != nil {
				return stats, err
			}
			*old = e
			added = true // the published date may have changed
			stats.Updated++
		}
	}
	if added {
		sortNewestFirst(s.byFeed[feedID])
	}
	return stats, nil
}

// publishedOrFallback returns the published date, else the updated date,
// else the fetch time.
func publishedOrFallback(e store.Entry) time.Time {
	switch {
	case !e.PublishedAt.IsZero():
		return e.PublishedAt
	case !e.UpdatedAt.IsZero():
		return e.UpdatedAt
	default:
		return e.FetchedAt
	}
}

// createEntry writes all three files into entries/.tmp-<id>/, then renames
// the directory into place, so an entry either fully exists or does not.
func (s *Store) createEntry(e store.Entry, ie store.IncomingEntry) error {
	final := s.entryDir(&e)
	tmp := filepath.Join(s.entriesDir(e.FeedID), tmpPrefix+e.ID)
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	meta, err := marshalJSON(e)
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
	}{
		{"content.raw.html", []byte(ie.RawContent)},
		{"content.html", []byte(ie.Content)},
		{"meta.json", meta},
	}
	for _, f := range files {
		if err := writeFileSync(filepath.Join(tmp, f.name), f.data); err != nil {
			_ = os.RemoveAll(tmp) // startup removes it if this fails
			return err
		}
	}
	// A directory not in the index has an unreadable meta.json; replace it.
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// updateEntry rewrites the content files, then meta.json. A crash in
// between leaves the old checksum in meta.json, so the next fetch rewrites
// the entry again.
func (s *Store) updateEntry(e store.Entry, ie store.IncomingEntry) error {
	dir := s.entryDir(&e)
	if err := writeFileAtomic(filepath.Join(dir, "content.raw.html"), []byte(ie.RawContent)); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "content.html"), []byte(ie.Content)); err != nil {
		return err
	}
	return s.writeMeta(e)
}

func (s *Store) writeMeta(e store.Entry) error {
	return writeJSONAtomic(filepath.Join(s.entryDir(&e), "meta.json"), e)
}

// matching returns the entries that q selects, newest first, ignoring
// Limit and Offset. The caller holds the lock.
func (s *Store) matching(q store.Query) []*store.Entry {
	var out []*store.Entry
	for feedID, list := range s.byFeed {
		if q.FeedID != "" && feedID != q.FeedID {
			continue
		}
		if q.Folder != "" && s.feeds[feedID].Folder != q.Folder {
			continue
		}
		for _, e := range list {
			if (q.View == store.ViewUnread && e.Read) || (q.View == store.ViewStarred && !e.Starred) {
				continue
			}
			out = append(out, e)
		}
	}
	sortNewestFirst(out)
	return out
}

func (s *Store) ListEntries(ctx context.Context, q store.Query) (store.EntryPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.matching(q)
	page := store.EntryPage{Entries: []store.Entry{}, Total: len(all)}
	start := min(max(q.Offset, 0), len(all))
	end := len(all)
	if q.Limit > 0 {
		end = min(start+q.Limit, end)
	}
	for _, e := range all[start:end] {
		page.Entries = append(page.Entries, *e)
	}
	return page, nil
}

func (s *Store) GetEntry(ctx context.Context, id string) (store.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[id]
	if !ok {
		return store.Entry{}, store.ErrNotFound
	}
	return *e, nil
}

// EntryContent reads the sanitized content from disk.
func (s *Store) EntryContent(ctx context.Context, id string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[id]
	if !ok {
		return "", store.ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.entryDir(e), "content.html"))
	return string(data), err
}

func (s *Store) SetRead(ctx context.Context, id string, read bool) error {
	return s.setState(id, func(e *store.Entry) { e.Read = read })
}

func (s *Store) SetStarred(ctx context.Context, id string, starred bool) error {
	return s.setState(id, func(e *store.Entry) { e.Starred = starred })
}

// setState applies change to a copy of the entry, writes it, then updates
// the index.
func (s *Store) setState(id string, change func(*store.Entry)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.entries[id]
	if !ok {
		return store.ErrNotFound
	}
	e := *old
	change(&e)
	if e == *old {
		return nil
	}
	if err := s.writeMeta(e); err != nil {
		return err
	}
	*old = e
	return nil
}

// MarkAllRead marks every unread entry that q selects as read, rewriting
// one meta.json per entry. It returns the number of entries changed.
func (s *Store) MarkAllRead(ctx context.Context, q store.Query) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, old := range s.matching(q) {
		if old.Read {
			continue
		}
		e := *old
		e.Read = true
		if err := s.writeMeta(e); err != nil {
			return n, err
		}
		*old = e
		n++
	}
	return n, nil
}

// UnreadCounts returns the number of unread entries per feed ID.
func (s *Store) UnreadCounts(ctx context.Context) (map[string]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]int{}
	for feedID, list := range s.byFeed {
		for _, e := range list {
			if !e.Read {
				counts[feedID]++
			}
		}
	}
	return counts, nil
}
