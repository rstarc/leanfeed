// Package filestore implements store.Store with plain files: an OPML file
// for subscriptions, and one directory per feed and per entry. All files are
// loaded into an in-memory index at startup; every change is written to disk
// first, then applied to the index.
package filestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"sync"
	"time"

	"leanfeed/internal/ids"
	"leanfeed/internal/opml"
	"leanfeed/internal/store"
)

// Store is the file-backed store. One RWMutex guards the index and all
// file writes: reads take the read lock, changes take the write lock.
type Store struct {
	dir string
	log *slog.Logger

	mu      sync.RWMutex
	feeds   map[string]*store.Feed    // by feed ID, from OPML + feed.json
	entries map[string]*store.Entry   // by entry ID, from meta.json
	byFeed  map[string][]*store.Entry // per feed, sorted newest first
}

var _ store.Store = (*Store)(nil)

var (
	// Feed IDs from files are used as directory names, so they must be
	// plain slugs. Entry directories are 16 hex characters.
	validFeedID  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	validEntryID = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// Open loads the data directory, creating it if needed. It removes leftover
// temp files and skips orphaned feed directories and unreadable entries,
// logging each.
func Open(dir string, log *slog.Logger) (*Store, error) {
	start := time.Now()
	s := &Store{
		dir:     dir,
		log:     log,
		feeds:   map[string]*store.Feed{},
		entries: map[string]*store.Entry{},
		byFeed:  map[string][]*store.Entry{},
	}
	if err := os.MkdirAll(filepath.Join(dir, "feeds"), 0o755); err != nil {
		return nil, err
	}
	if _, err := readDirClean(dir); err != nil {
		return nil, err
	}
	if err := s.loadFeeds(); err != nil {
		return nil, err
	}
	if err := s.loadEntries(); err != nil {
		return nil, err
	}
	log.Info("index built", "feeds", len(s.feeds), "entries", len(s.entries), "elapsed", time.Since(start))
	return s, nil
}

// Close waits for in-flight writes. The store must not be used afterwards.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return nil
}

func (s *Store) opmlPath() string                { return filepath.Join(s.dir, "subscriptions.opml") }
func (s *Store) feedDir(id string) string        { return filepath.Join(s.dir, "feeds", id) }
func (s *Store) feedJSONPath(id string) string   { return filepath.Join(s.feedDir(id), "feed.json") }
func (s *Store) entriesDir(feedID string) string { return filepath.Join(s.feedDir(feedID), "entries") }
func (s *Store) entryDir(e *store.Entry) string  { return filepath.Join(s.entriesDir(e.FeedID), e.ID) }

// loadFeeds reads subscriptions.opml and each feed.json. The OPML is the
// source of truth for the feed list, titles, URLs and folders. Feeds added
// to the OPML by hand get an ID and a feed.json.
func (s *Store) loadFeeds() error {
	subs, err := readOPMLFile(s.opmlPath())
	if err != nil {
		return err
	}
	opmlChanged := false
	for _, sub := range subs {
		if err := store.CheckFeedURL(sub.XMLURL); err != nil {
			s.log.Warn("skipping subscription", "url", sub.XMLURL, "err", err)
			continue
		}
		id := sub.ID
		if !validFeedID.MatchString(id) {
			id = ids.FeedID(sub.Title, sub.XMLURL)
			opmlChanged = true
		}
		if _, dup := s.feeds[id]; dup {
			s.log.Warn("skipping duplicate subscription", "id", id, "url", sub.XMLURL)
			opmlChanged = true
			continue
		}
		f, err := s.loadFeedJSON(id)
		if errors.Is(err, os.ErrNotExist) {
			f = store.Feed{ID: id}
		} else if err != nil {
			return fmt.Errorf("feed %s: %w", id, err)
		}
		f.ID, f.URL, f.Title, f.SiteURL, f.Folder = id, sub.XMLURL, sub.Title, sub.HTMLURL, sub.Folder
		if errors.Is(err, os.ErrNotExist) {
			if err := s.writeFeed(f); err != nil {
				return err
			}
		}
		s.feeds[id] = &f
	}
	if opmlChanged {
		return s.writeOPML(s.feeds)
	}
	return nil
}

func readOPMLFile(path string) ([]opml.Subscription, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	subs, err := opml.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return subs, nil
}

func (s *Store) loadFeedJSON(id string) (store.Feed, error) {
	var f store.Feed
	data, err := os.ReadFile(s.feedJSONPath(id))
	if err != nil {
		return f, err
	}
	err = json.Unmarshal(data, &f)
	return f, err
}

// loadEntries decodes every meta.json of every subscribed feed with a
// worker pool. Content files are never read at startup.
func (s *Store) loadEntries() error {
	dirs, err := readDirClean(filepath.Join(s.dir, "feeds"))
	if err != nil {
		return err
	}
	type job struct{ feedID, entryID string }
	var jobs []job
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		feedID := d.Name()
		if _, ok := s.feeds[feedID]; !ok {
			s.log.Warn("ignoring orphan feed directory", "dir", feedID)
			continue
		}
		if _, err := readDirClean(s.feedDir(feedID)); err != nil {
			return err
		}
		entryDirs, err := readDirClean(s.entriesDir(feedID))
		if err != nil {
			return err
		}
		for _, e := range entryDirs {
			if e.IsDir() && validEntryID.MatchString(e.Name()) {
				jobs = append(jobs, job{feedID, e.Name()})
			}
		}
	}

	results := make([]*store.Entry, len(jobs))
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				j := jobs[i]
				e, err := s.loadMeta(j.feedID, j.entryID)
				if err != nil {
					s.log.Warn("skipping unreadable entry", "feed", j.feedID, "entry", j.entryID, "err", err)
					continue
				}
				results[i] = e
			}
		}()
	}
	for i := range jobs {
		next <- i
	}
	close(next)
	wg.Wait()

	for _, e := range results {
		if e != nil {
			s.entries[e.ID] = e
			s.byFeed[e.FeedID] = append(s.byFeed[e.FeedID], e)
		}
	}
	for _, list := range s.byFeed {
		sortNewestFirst(list)
	}
	return nil
}

// loadMeta removes temp files left by an interrupted rewrite of the entry,
// then decodes its meta.json.
func (s *Store) loadMeta(feedID, entryID string) (*store.Entry, error) {
	dir := filepath.Join(s.entriesDir(feedID), entryID)
	if _, err := readDirClean(dir); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	var e store.Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	// The directory names are authoritative.
	e.ID, e.FeedID = entryID, feedID
	return &e, nil
}

func sortNewestFirst(list []*store.Entry) {
	sort.Slice(list, func(i, j int) bool {
		if !list[i].PublishedAt.Equal(list[j].PublishedAt) {
			return list[i].PublishedAt.After(list[j].PublishedAt)
		}
		return list[i].ID < list[j].ID
	})
}

// writeOPML writes feeds as subscriptions.opml.
func (s *Store) writeOPML(feeds map[string]*store.Feed) error {
	var buf bytes.Buffer
	if err := opml.Write(&buf, subscriptions(feeds)); err != nil {
		return err
	}
	return writeFileAtomic(s.opmlPath(), buf.Bytes())
}

func subscriptions(feeds map[string]*store.Feed) []opml.Subscription {
	subs := make([]opml.Subscription, 0, len(feeds))
	for _, f := range feeds {
		subs = append(subs, opml.Subscription{ID: f.ID, Title: f.Title, XMLURL: f.URL, HTMLURL: f.SiteURL, Folder: f.Folder})
	}
	// Map order is random; sort so the file is stable between writes.
	sort.Slice(subs, func(i, j int) bool { return subs[i].ID < subs[j].ID })
	return subs
}

// writeFeed writes feed.json, creating the feed's directories if needed.
func (s *Store) writeFeed(f store.Feed) error {
	if err := os.MkdirAll(s.entriesDir(f.ID), 0o755); err != nil {
		return err
	}
	return writeJSONAtomic(s.feedJSONPath(f.ID), f)
}
