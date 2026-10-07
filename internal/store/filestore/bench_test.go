package filestore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"leanfeed/internal/store"
)

// writeCorpus subscribes to feeds and writes perFeed entries for each
// directly as files, which is much faster than synced writes through the store.
func writeCorpus(tb testing.TB, dir string, feeds, perFeed int) {
	tb.Helper()
	s, err := Open(dir, quietLog)
	if err != nil {
		tb.Fatal(err)
	}
	for f := range feeds {
		feed, err := s.AddFeed(ctx, store.NewFeed{URL: fmt.Sprintf("https://f%d.example/feed", f), Title: fmt.Sprintf("Feed %d", f), Folder: fmt.Sprintf("Folder %d", f%5)})
		if err != nil {
			tb.Fatal(err)
		}
		for i := range perFeed {
			e := store.Entry{
				ID:          fmt.Sprintf("%016x", f*perFeed+i),
				FeedID:      feed.ID,
				GUID:        fmt.Sprintf("https://f%d.example/posts/%d", f, i),
				URL:         fmt.Sprintf("https://f%d.example/posts/%d", f, i),
				Title:       fmt.Sprintf("Post %d of feed %d", i, f),
				Author:      "Jane Doe",
				PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour),
				Read:        i%3 == 0,
				Starred:     i%50 == 0,
			}
			edir := filepath.Join(dir, "feeds", feed.ID, "entries", e.ID)
			if err := os.MkdirAll(edir, 0o755); err != nil {
				tb.Fatal(err)
			}
			meta, _ := json.MarshalIndent(e, "", "  ")
			for name, data := range map[string][]byte{
				"meta.json":        meta,
				"content.raw.html": []byte("<p>raw</p>"),
				"content.html":     []byte("<p>clean</p>"),
			} {
				if err := os.WriteFile(filepath.Join(edir, name), data, 0o644); err != nil {
					tb.Fatal(err)
				}
			}
		}
	}
	s.Close()
}

// TestIndexBuild10k guards N3: building the index for 10,000 entries takes
// under 1 s with a warm OS cache.
func TestIndexBuild10k(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 30,000 files")
	}
	dir := t.TempDir()
	writeCorpus(t, dir, 50, 200)
	s := open(t, dir) // warm the OS cache
	s.Close()

	start := time.Now()
	s = open(t, dir)
	elapsed := time.Since(start)
	defer s.Close()

	page, _ := s.ListEntries(ctx, store.Query{View: store.ViewAll, Limit: 1})
	if page.Total != 10_000 {
		t.Fatalf("loaded %d entries, want 10000", page.Total)
	}
	t.Logf("index build for 10,000 entries: %v", elapsed)
	if elapsed > time.Second {
		t.Errorf("index build took %v, want under 1 s (N3)", elapsed)
	}
}

func BenchmarkOpen10k(b *testing.B) {
	dir := b.TempDir()
	writeCorpus(b, dir, 50, 200)
	for b.Loop() {
		s, err := Open(dir, quietLog)
		if err != nil {
			b.Fatal(err)
		}
		s.Close()
	}
}

func BenchmarkListEntries10k(b *testing.B) {
	dir := b.TempDir()
	writeCorpus(b, dir, 50, 200)
	s, err := Open(dir, quietLog)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	for b.Loop() {
		s.ListEntries(ctx, store.Query{View: store.ViewUnread, Limit: 50})
	}
}
