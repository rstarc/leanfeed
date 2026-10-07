// Package corpus writes large data directories in the file store's format
// for tests and benchmarks. It writes files directly, which is much faster
// than synced writes through the store.
package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"leanfeed/internal/ids"
	"leanfeed/internal/opml"
	"leanfeed/internal/store"
)

// Write creates feeds feeds in five folders, each with perFeed entries.
// Every third entry is read and every fiftieth is starred.
func Write(tb testing.TB, dir string, feeds, perFeed int) {
	tb.Helper()
	var subs []opml.Subscription
	for f := range feeds {
		feed := store.Feed{URL: fmt.Sprintf("https://f%d.example/feed", f), Title: fmt.Sprintf("Feed %d", f)}
		feed.ID = ids.FeedID(feed.Title, feed.URL)
		subs = append(subs, opml.Subscription{ID: feed.ID, Title: feed.Title, XMLURL: feed.URL, Folder: fmt.Sprintf("Folder %d", f%5)})
		writeJSON(tb, filepath.Join(dir, "feeds", feed.ID, "feed.json"), feed)
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
			writeJSON(tb, filepath.Join(edir, "meta.json"), e)
			writeFile(tb, filepath.Join(edir, "content.raw.html"), []byte("<p>raw</p>"))
			writeFile(tb, filepath.Join(edir, "content.html"), []byte("<p>clean</p>"))
		}
	}
	f, err := os.Create(filepath.Join(dir, "subscriptions.opml"))
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	if err := opml.Write(f, subs); err != nil {
		tb.Fatal(err)
	}
}

func writeJSON(tb testing.TB, path string, v any) {
	tb.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		tb.Fatal(err)
	}
	writeFile(tb, path, data)
}

func writeFile(tb testing.TB, path string, data []byte) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		tb.Fatal(err)
	}
}
