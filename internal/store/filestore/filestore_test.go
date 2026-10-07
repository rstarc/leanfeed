package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"leanfeed/internal/opml"

	"leanfeed/internal/store"
	"leanfeed/internal/store/storetest"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir, quietLog)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		return open(t, t.TempDir())
	})
}

var ctx = context.Background()

const (
	blogURL = "https://example.com/feed.xml"
	blogID  = "example-blog-7a775db7"
	entryID = "59eaeb92f4591b08" // ids.EntryID(blogID, "guid-1", ...)
)

func post(guid, title string) store.IncomingEntry {
	return store.IncomingEntry{
		GUID: guid, URL: "https://example.com/" + guid, Title: title,
		PublishedAt: time.Date(2026, 10, 6, 14, 0, 0, 0, time.UTC),
		RawContent:  "<p>" + title + `</p><img src=x onerror="alert(1)">`,
		Content:     "<p>" + title + "</p>",
	}
}

// addBlog subscribes to the example blog in folder Tech and stores entry guid-1.
func addBlog(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.AddFeed(ctx, store.NewFeed{URL: blogURL, Title: "Example Blog", SiteURL: "https://example.com/", Folder: "Tech"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertEntries(ctx, blogID, []store.IncomingEntry{post("guid-1", "Post")}); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// tempNames returns every path under dir whose name starts with ".tmp-".
func tempNames(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if strings.HasPrefix(d.Name(), ".tmp-") {
			found = append(found, path)
		}
		return nil
	})
	return found
}

func TestDataLayout(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	addBlog(t, s)
	if err := s.SetStarred(ctx, entryID, true); err != nil {
		t.Fatal(err)
	}

	subs, err := opml.Parse(strings.NewReader(readFile(t, filepath.Join(dir, "subscriptions.opml"))))
	if err != nil {
		t.Fatal(err)
	}
	wantSubs := []opml.Subscription{{ID: blogID, Title: "Example Blog", XMLURL: blogURL, HTMLURL: "https://example.com/", Folder: "Tech"}}
	if !reflect.DeepEqual(subs, wantSubs) {
		t.Errorf("subscriptions.opml = %+v, want %+v", subs, wantSubs)
	}

	feed := readJSON(t, filepath.Join(dir, "feeds", blogID, "feed.json"))
	if feed["id"] != blogID || feed["url"] != blogURL || feed["title"] != "Example Blog" || feed["site_url"] != "https://example.com/" {
		t.Errorf("feed.json = %v", feed)
	}

	entryDir := filepath.Join(dir, "feeds", blogID, "entries", entryID)
	meta := readJSON(t, filepath.Join(entryDir, "meta.json"))
	if meta["id"] != entryID || meta["feed_id"] != blogID || meta["guid"] != "guid-1" ||
		meta["title"] != "Post" || meta["published_at"] != "2026-10-06T14:00:00Z" ||
		meta["read"] != false || meta["starred"] != true {
		t.Errorf("meta.json = %v", meta)
	}
	if got := readFile(t, filepath.Join(entryDir, "content.raw.html")); got != `<p>Post</p><img src=x onerror="alert(1)">` {
		t.Errorf("content.raw.html = %q", got)
	}
	if got := readFile(t, filepath.Join(entryDir, "content.html")); got != "<p>Post</p>" {
		t.Errorf("content.html = %q", got)
	}
	if tmp := tempNames(t, dir); len(tmp) != 0 {
		t.Errorf("temp files left behind: %v", tmp)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	addBlog(t, s)
	if _, err := s.UpsertEntries(ctx, blogID, []store.IncomingEntry{post("guid-2", "Second")}); err != nil {
		t.Fatal(err)
	}
	title := "Renamed"
	if _, err := s.UpdateFeed(ctx, blogID, store.FeedUpdate{Title: &title}); err != nil {
		t.Fatal(err)
	}
	fetched := time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)
	if err := s.RecordFetch(ctx, blogID, store.FetchResult{
		FetchedAt: fetched, NextFetchAt: fetched.Add(30 * time.Minute), Status: store.StatusOK, ETag: `"e"`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRead(ctx, entryID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStarred(ctx, entryID, true); err != nil {
		t.Fatal(err)
	}
	wantFeed, _ := s.GetFeed(ctx, blogID)
	wantEntries, _ := s.ListEntries(ctx, store.Query{View: store.ViewAll})
	s.Close()

	s = open(t, dir)
	gotFeed, err := s.GetFeed(ctx, blogID)
	if err != nil || !reflect.DeepEqual(gotFeed, wantFeed) {
		t.Errorf("feed after reopen = %+v, %v\nwant %+v", gotFeed, err, wantFeed)
	}
	gotEntries, _ := s.ListEntries(ctx, store.Query{View: store.ViewAll})
	if !reflect.DeepEqual(gotEntries, wantEntries) {
		t.Errorf("entries after reopen =\n%+v\nwant\n%+v", gotEntries, wantEntries)
	}
	if content, _ := s.EntryContent(ctx, entryID); content != "<p>Post</p>" {
		t.Errorf("content after reopen = %q", content)
	}
}

func TestRemoveFeedDeletesDirectory(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	addBlog(t, s)
	if err := s.RemoveFeed(ctx, blogID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "feeds", blogID)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("feed directory still exists: %v", err)
	}
	if strings.Contains(readFile(t, filepath.Join(dir, "subscriptions.opml")), blogURL) {
		t.Error("subscriptions.opml still lists the removed feed")
	}
}

func TestOpenCreatesMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "data")
	s := open(t, dir)
	addBlog(t, s)
	if _, err := os.Stat(filepath.Join(dir, "subscriptions.opml")); err != nil {
		t.Error(err)
	}
}

func TestStartupRemovesTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	addBlog(t, s)
	s.Close()

	feedDir := filepath.Join(dir, "feeds", blogID)
	halfEntry := filepath.Join(feedDir, "entries", ".tmp-0123456789abcdef")
	leftovers := []string{
		filepath.Join(dir, ".tmp-subscriptions.opml"),
		filepath.Join(feedDir, ".tmp-feed.json"),
		filepath.Join(feedDir, "entries", entryID, ".tmp-meta.json"),
		filepath.Join(halfEntry, "meta.json"),
	}
	for _, p := range leftovers {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(`{"id":"0123456789abcdef","title":"half"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s = open(t, dir)
	if tmp := tempNames(t, dir); len(tmp) != 0 {
		t.Errorf("temp files after startup: %v", tmp)
	}
	page, _ := s.ListEntries(ctx, store.Query{View: store.ViewAll})
	if page.Total != 1 {
		t.Errorf("entries = %d, want 1 (half-written entry must not load)", page.Total)
	}
}

func TestStartupIgnoresOrphanDirectories(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	addBlog(t, s)
	s.Close()

	orphan := filepath.Join(dir, "feeds", "orphan-12345678", "entries", "0123456789abcdef")
	os.MkdirAll(orphan, 0o755)
	os.WriteFile(filepath.Join(orphan, "meta.json"), []byte(`{"id":"0123456789abcdef","title":"orphan"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "feeds", ".DS_Store"), []byte("junk"), 0o644)

	s = open(t, dir)
	feeds, _ := s.ListFeeds(ctx)
	if len(feeds) != 1 {
		t.Errorf("feeds = %+v, want only the subscribed one", feeds)
	}
	if page, _ := s.ListEntries(ctx, store.Query{View: store.ViewAll}); page.Total != 1 {
		t.Errorf("entries = %d, want 1", page.Total)
	}
}

func TestStartupSkipsCorruptMeta(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	addBlog(t, s)
	if _, err := s.UpsertEntries(ctx, blogID, []store.IncomingEntry{post("guid-2", "Second")}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.WriteFile(filepath.Join(dir, "feeds", blogID, "entries", entryID, "meta.json"), []byte("{not json"), 0o644)

	s = open(t, dir)
	page, _ := s.ListEntries(ctx, store.Query{View: store.ViewAll})
	if len(page.Entries) != 1 || page.Entries[0].Title != "Second" {
		t.Errorf("entries = %+v, want only the readable one", page.Entries)
	}
}

func TestStartupAcceptsHandEditedOPML(t *testing.T) {
	dir := t.TempDir()
	doc := `<?xml version="1.0"?>
<opml version="2.0"><body>
  <outline text="Folder">
    <outline type="rss" text="Hand Edited" xmlUrl="https://hand.example/feed"/>
    <outline type="rss" text="Sneaky" xmlUrl="https://sneaky.example/feed" leanfeedId="../../etc"/>
  </outline>
</body></opml>`
	os.WriteFile(filepath.Join(dir, "subscriptions.opml"), []byte(doc), 0o644)

	s := open(t, dir)
	f, err := s.GetFeed(ctx, "hand-edited-a5a3cec9")
	if err != nil || f.Folder != "Folder" || f.URL != "https://hand.example/feed" {
		t.Errorf("hand-added feed = %+v, %v", f, err)
	}
	feeds, _ := s.ListFeeds(ctx)
	for _, f := range feeds {
		if strings.Contains(f.ID, "/") || strings.Contains(f.ID, ".") {
			t.Errorf("unsafe feed ID %q accepted", f.ID)
		}
	}
	if len(feeds) != 2 {
		t.Errorf("feeds = %+v, want 2", feeds)
	}
	if _, err := os.Stat(filepath.Join(dir, "feeds", "hand-edited-a5a3cec9", "feed.json")); err != nil {
		t.Errorf("feed.json not created: %v", err)
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "subscriptions.opml")), `leanfeedId="hand-edited-a5a3cec9"`) {
		t.Error("subscriptions.opml was not updated with the assigned ID")
	}
}
