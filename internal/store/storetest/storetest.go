// Package storetest is the contract suite every store.Store implementation
// must pass. Call Run from the implementation's tests.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"leanfeed/internal/opml"
	"leanfeed/internal/store"
)

// NewStore returns an empty store. Run calls it once per subtest.
type NewStore func(t *testing.T) store.Store

// Run runs the whole contract suite against stores made by newStore.
func Run(t *testing.T, newStore NewStore) {
	tests := []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"AddFeed", testAddFeed},
		{"AddFeedRejectsDuplicateURL", testAddFeedRejectsDuplicateURL},
		{"AddFeedRejectsBadURL", testAddFeedRejectsBadURL},
		{"ListFeedsSortedByTitle", testListFeedsSortedByTitle},
		{"FeedsAreCopies", testFeedsAreCopies},
		{"MissingFeed", testMissingFeed},
		{"UpdateFeed", testUpdateFeed},
		{"UpdateFeedRejectsDuplicateURL", testUpdateFeedRejectsDuplicateURL},
		{"RemoveFeedDeletesEntries", testRemoveFeedDeletesEntries},
		{"RecordFetchAndDueFeeds", testRecordFetchAndDueFeeds},
		{"RecordFetchChangesURL", testRecordFetchChangesURL},
		{"UpsertNewEntry", testUpsertNewEntry},
		{"UpsertUnchangedEntry", testUpsertUnchangedEntry},
		{"UpsertChangedEntryKeepsState", testUpsertChangedEntryKeepsState},
		{"UpsertDuplicateInBatch", testUpsertDuplicateInBatch},
		{"UpsertUnknownFeed", testUpsertUnknownFeed},
		{"PublishedDateFallback", testPublishedDateFallback},
		{"MissingEntry", testMissingEntry},
		{"ListEntriesViews", testListEntriesViews},
		{"ListEntriesFilters", testListEntriesFilters},
		{"ListEntriesNewestFirst", testListEntriesNewestFirst},
		{"ListEntriesPagination", testListEntriesPagination},
		{"MarkAllRead", testMarkAllRead},
		{"UnreadCounts", testUnreadCounts},
		{"ImportOPML", testImportOPML},
		{"ImportOPMLRejectsMalformed", testImportOPMLRejectsMalformed},
		{"ExportOPML", testExportOPML},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})
			tt.fn(t, s)
		})
	}
}

var ctx = context.Background()

const (
	blogURL = "https://example.com/feed.xml"
	blogID  = "example-blog-7a775db7" // ids.FeedID("Example Blog", blogURL)
)

func addFeed(t *testing.T, s store.Store, url, title, folder string) store.Feed {
	t.Helper()
	f, err := s.AddFeed(ctx, store.NewFeed{URL: url, Title: title, Folder: folder})
	if err != nil {
		t.Fatalf("AddFeed(%q): %v", url, err)
	}
	return f
}

func upsert(t *testing.T, s store.Store, feedID string, in ...store.IncomingEntry) store.UpsertStats {
	t.Helper()
	stats, err := s.UpsertEntries(ctx, feedID, in)
	if err != nil {
		t.Fatalf("UpsertEntries: %v", err)
	}
	return stats
}

func list(t *testing.T, s store.Store, q store.Query) store.EntryPage {
	t.Helper()
	page, err := s.ListEntries(ctx, q)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	return page
}

func titles(entries []store.Entry) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Title)
	}
	return out
}

func day(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }

func entry(guid, title string, published time.Time) store.IncomingEntry {
	return store.IncomingEntry{
		GUID: guid, URL: "https://example.com/" + guid, Title: title,
		PublishedAt: published, UpdatedAt: published,
		RawContent: "<p>" + title + "</p><script>x</script>", Content: "<p>" + title + "</p>",
	}
}

func testAddFeed(t *testing.T, s store.Store) {
	f, err := s.AddFeed(ctx, store.NewFeed{URL: blogURL, Title: "Example Blog", SiteURL: "https://example.com/", Folder: "Tech"})
	if err != nil {
		t.Fatal(err)
	}
	want := store.Feed{ID: blogID, URL: blogURL, Title: "Example Blog", SiteURL: "https://example.com/", Folder: "Tech"}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("AddFeed = %+v, want %+v", f, want)
	}
	got, err := s.GetFeed(ctx, blogID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("GetFeed = %+v, %v; want %+v", got, err, want)
	}
	all, err := s.ListFeeds(ctx)
	if err != nil || !reflect.DeepEqual(all, []store.Feed{want}) {
		t.Errorf("ListFeeds = %+v, %v; want [%+v]", all, err, want)
	}
}

func testAddFeedRejectsDuplicateURL(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	_, err := s.AddFeed(ctx, store.NewFeed{URL: blogURL, Title: "Another Name"})
	if !errors.Is(err, store.ErrExists) {
		t.Errorf("second AddFeed error = %v, want ErrExists", err)
	}
}

func testAddFeedRejectsBadURL(t *testing.T, s store.Store) {
	if _, err := s.AddFeed(ctx, store.NewFeed{URL: "javascript:alert(1)", Title: "x"}); err == nil {
		t.Error("AddFeed with javascript: URL succeeded")
	}
	if feeds, _ := s.ListFeeds(ctx); len(feeds) != 0 {
		t.Errorf("ListFeeds after rejected add = %+v, want none", feeds)
	}
}

func testListFeedsSortedByTitle(t *testing.T, s store.Store) {
	addFeed(t, s, "https://c.example/feed", "charlie", "")
	addFeed(t, s, "https://a.example/feed", "Alpha", "Z folder")
	addFeed(t, s, "https://b.example/feed", "Bravo", "")
	feeds, err := s.ListFeeds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range feeds {
		got = append(got, f.Title)
	}
	if want := []string{"Alpha", "Bravo", "charlie"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ListFeeds titles = %v, want %v", got, want)
	}
}

func testFeedsAreCopies(t *testing.T, s store.Store) {
	f := addFeed(t, s, blogURL, "Example Blog", "")
	f.Title = "changed"
	feeds, _ := s.ListFeeds(ctx)
	if len(feeds) != 1 {
		t.Fatalf("ListFeeds = %+v, want one feed", feeds)
	}
	feeds[0].Title = "changed"
	got, _ := s.GetFeed(ctx, blogID)
	if got.Title != "Example Blog" {
		t.Errorf("store feed title = %q after mutating returned copies", got.Title)
	}
}

func testMissingFeed(t *testing.T, s store.Store) {
	title := "x"
	checks := map[string]error{}
	_, checks["GetFeed"] = s.GetFeed(ctx, "nope")
	_, checks["UpdateFeed"] = s.UpdateFeed(ctx, "nope", store.FeedUpdate{Title: &title})
	checks["RemoveFeed"] = s.RemoveFeed(ctx, "nope")
	checks["RecordFetch"] = s.RecordFetch(ctx, "nope", store.FetchResult{Status: store.StatusOK})
	for name, err := range checks {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s(missing) error = %v, want ErrNotFound", name, err)
		}
	}
}

func testUpdateFeed(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "Tech")
	title := "Renamed"
	got, err := s.UpdateFeed(ctx, blogID, store.FeedUpdate{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != blogID || got.Title != "Renamed" || got.Folder != "Tech" || got.URL != blogURL {
		t.Errorf("after rename: %+v", got)
	}

	folder := ""
	newURL := "https://example.com/new.xml"
	got, err = s.UpdateFeed(ctx, blogID, store.FeedUpdate{Folder: &folder, URL: &newURL})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != blogID || got.Title != "Renamed" || got.Folder != "" || got.URL != newURL {
		t.Errorf("after move and URL change: %+v", got)
	}
	stored, _ := s.GetFeed(ctx, blogID)
	if !reflect.DeepEqual(stored, got) {
		t.Errorf("GetFeed = %+v, want %+v", stored, got)
	}

	bad := "ftp://example.com/x"
	if _, err := s.UpdateFeed(ctx, blogID, store.FeedUpdate{URL: &bad}); err == nil {
		t.Error("UpdateFeed accepted an ftp URL")
	}
}

func testUpdateFeedRejectsDuplicateURL(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	other := addFeed(t, s, "https://other.example/rss", "Other", "")
	url := blogURL
	if _, err := s.UpdateFeed(ctx, other.ID, store.FeedUpdate{URL: &url}); !errors.Is(err, store.ErrExists) {
		t.Errorf("UpdateFeed to existing URL error = %v, want ErrExists", err)
	}
}

func testRemoveFeedDeletesEntries(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	other := addFeed(t, s, "https://other.example/rss", "Other", "")
	upsert(t, s, blogID, entry("guid-1", "Blog post", day(1)))
	upsert(t, s, other.ID, entry("o-1", "Other post", day(2)))

	if err := s.RemoveFeed(ctx, blogID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFeed(ctx, blogID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetFeed after remove error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetEntry(ctx, "59eaeb92f4591b08"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetEntry of removed feed's entry error = %v, want ErrNotFound", err)
	}
	page := list(t, s, store.Query{View: store.ViewAll})
	if got := titles(page.Entries); !reflect.DeepEqual(got, []string{"Other post"}) {
		t.Errorf("entries after remove = %v, want [Other post]", got)
	}
	counts, _ := s.UnreadCounts(ctx)
	if counts[blogID] != 0 {
		t.Errorf("unread count of removed feed = %d", counts[blogID])
	}
}

func testRecordFetchAndDueFeeds(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	other := addFeed(t, s, "https://other.example/rss", "Other", "")
	now := day(7)

	due, _ := s.DueFeeds(ctx, now)
	if len(due) != 2 {
		t.Fatalf("new feeds due = %d, want 2", len(due))
	}

	r := store.FetchResult{
		FetchedAt: now, NextFetchAt: now.Add(30 * time.Minute), Status: store.StatusOK,
		ETag: `"abc"`, LastModified: "Tue, 06 Oct 2026 08:00:00 GMT", SiteURL: "https://example.com/",
	}
	if err := s.RecordFetch(ctx, blogID, r); err != nil {
		t.Fatal(err)
	}
	failed := store.FetchResult{
		FetchedAt: now, NextFetchAt: now.Add(time.Hour), Status: store.StatusError,
		Error: "HTTP 500", ConsecutiveFailures: 1,
	}
	if err := s.RecordFetch(ctx, other.ID, failed); err != nil {
		t.Fatal(err)
	}

	f, _ := s.GetFeed(ctx, blogID)
	want := store.Feed{
		ID: blogID, URL: blogURL, Title: "Example Blog", SiteURL: "https://example.com/",
		ETag: `"abc"`, LastModified: "Tue, 06 Oct 2026 08:00:00 GMT",
		LastFetchedAt: now, NextFetchAt: now.Add(30 * time.Minute), LastStatus: "ok",
	}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("feed after fetch =\n%+v\nwant\n%+v", f, want)
	}
	o, _ := s.GetFeed(ctx, other.ID)
	if o.LastStatus != "error" || o.LastError != "HTTP 500" || o.ConsecutiveFailures != 1 {
		t.Errorf("failed feed = %+v", o)
	}

	if due, _ := s.DueFeeds(ctx, now.Add(29*time.Minute)); len(due) != 0 {
		t.Errorf("due before next fetch = %d feeds, want 0", len(due))
	}
	due, _ = s.DueFeeds(ctx, now.Add(30*time.Minute))
	if len(due) != 1 || due[0].ID != blogID {
		t.Errorf("due at next fetch = %+v, want only %s", due, blogID)
	}
}

func testRecordFetchChangesURL(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	newURL := "https://example.com/moved.xml"
	if err := s.RecordFetch(ctx, blogID, store.FetchResult{Status: store.StatusOK, URL: newURL}); err != nil {
		t.Fatal(err)
	}
	f, _ := s.GetFeed(ctx, blogID)
	if f.ID != blogID || f.URL != newURL {
		t.Errorf("feed after redirect = %+v", f)
	}
}

func testUpsertNewEntry(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	in := entry("guid-1", "Post", day(6))
	in.Author = "Jane Doe"
	in.UpdatedAt = day(6).Add(time.Hour)
	stats := upsert(t, s, blogID, in)
	if stats != (store.UpsertStats{Added: 1}) {
		t.Errorf("stats = %+v, want 1 added", stats)
	}

	e, err := s.GetEntry(ctx, "59eaeb92f4591b08")
	if err != nil {
		t.Fatal(err)
	}
	if e.FeedID != blogID || e.GUID != "guid-1" || e.URL != "https://example.com/guid-1" ||
		e.Title != "Post" || e.Author != "Jane Doe" || !e.PublishedAt.Equal(day(6)) ||
		!e.UpdatedAt.Equal(day(6).Add(time.Hour)) || e.Read || e.Starred {
		t.Errorf("entry = %+v", e)
	}
	if e.FetchedAt.IsZero() {
		t.Error("FetchedAt is zero")
	}
	// sha256 of the raw content "<p>Post</p><script>x</script>"
	if e.ContentSHA256 != "e6101ecf844264b83d33b2934feba70d7b59b9add81c503a58a0a8dab4693b9b" {
		t.Errorf("ContentSHA256 = %q", e.ContentSHA256)
	}
	content, err := s.EntryContent(ctx, e.ID)
	if err != nil || content != "<p>Post</p>" {
		t.Errorf("EntryContent = %q, %v; want sanitized content", content, err)
	}
}

func testUpsertUnchangedEntry(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	upsert(t, s, blogID, entry("guid-1", "Post", day(6)))
	if err := s.SetRead(ctx, "59eaeb92f4591b08", true); err != nil {
		t.Fatal(err)
	}
	stats := upsert(t, s, blogID, entry("guid-1", "Post", day(6)))
	if stats != (store.UpsertStats{Unchanged: 1}) {
		t.Errorf("stats = %+v, want 1 unchanged", stats)
	}
	if e, _ := s.GetEntry(ctx, "59eaeb92f4591b08"); !e.Read {
		t.Error("read state lost on unchanged upsert")
	}
}

func testUpsertChangedEntryKeepsState(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	upsert(t, s, blogID, entry("guid-1", "Post", day(6)))
	id := "59eaeb92f4591b08"
	if err := s.SetRead(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStarred(ctx, id, true); err != nil {
		t.Fatal(err)
	}

	changed := entry("guid-1", "Post (edited)", day(6))
	stats := upsert(t, s, blogID, changed)
	if stats != (store.UpsertStats{Updated: 1}) {
		t.Errorf("stats = %+v, want 1 updated", stats)
	}
	e, _ := s.GetEntry(ctx, id)
	if e.Title != "Post (edited)" || !e.Read || !e.Starred {
		t.Errorf("entry after change = %+v, want new title with read and starred kept", e)
	}
	if content, _ := s.EntryContent(ctx, id); content != "<p>Post (edited)</p>" {
		t.Errorf("content after change = %q", content)
	}
}

func testUpsertDuplicateInBatch(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	stats := upsert(t, s, blogID, entry("guid-1", "Post", day(6)), entry("guid-1", "Post", day(6)))
	if stats.Added != 1 {
		t.Errorf("stats = %+v, want 1 added", stats)
	}
	if page := list(t, s, store.Query{View: store.ViewAll}); page.Total != 1 {
		t.Errorf("Total = %d, want 1", page.Total)
	}
}

func testUpsertUnknownFeed(t *testing.T, s store.Store) {
	_, err := s.UpsertEntries(ctx, "nope", []store.IncomingEntry{entry("g", "t", day(1))})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func testPublishedDateFallback(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	onlyUpdated := entry("u", "Only updated", time.Time{})
	onlyUpdated.UpdatedAt = day(3)
	noDates := entry("n", "No dates", time.Time{})
	before := time.Now().Add(-time.Second)
	upsert(t, s, blogID, onlyUpdated, noDates)
	after := time.Now().Add(time.Second)

	page := list(t, s, store.Query{View: store.ViewAll})
	if len(page.Entries) != 2 {
		t.Fatalf("entries = %+v, want 2", page.Entries)
	}
	for _, e := range page.Entries {
		switch e.Title {
		case "Only updated":
			if !e.PublishedAt.Equal(day(3)) {
				t.Errorf("PublishedAt = %v, want updated date %v", e.PublishedAt, day(3))
			}
		case "No dates":
			if !e.PublishedAt.Equal(e.FetchedAt) || e.PublishedAt.Before(before) || e.PublishedAt.After(after) {
				t.Errorf("PublishedAt = %v, FetchedAt = %v; want fetch time", e.PublishedAt, e.FetchedAt)
			}
		}
	}
}

func testMissingEntry(t *testing.T, s store.Store) {
	checks := map[string]error{}
	_, checks["GetEntry"] = s.GetEntry(ctx, "nope")
	_, checks["EntryContent"] = s.EntryContent(ctx, "nope")
	checks["SetRead"] = s.SetRead(ctx, "nope", true)
	checks["SetStarred"] = s.SetStarred(ctx, "nope", true)
	for name, err := range checks {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s(missing) error = %v, want ErrNotFound", name, err)
		}
	}
}

// seed adds two feeds in different folders and one top-level feed:
//
//	Tech/Example Blog: a1 (read), a2 (starred)
//	Tech/Other:        o1
//	News (no folder):  n1 (read, starred)
func seed(t *testing.T, s store.Store) (blog, other, news store.Feed) {
	t.Helper()
	blog = addFeed(t, s, blogURL, "Example Blog", "Tech")
	other = addFeed(t, s, "https://other.example/rss", "Other", "Tech")
	news = addFeed(t, s, "https://news.example/atom", "News", "")
	upsert(t, s, blog.ID, entry("a1", "a1", day(1)), entry("a2", "a2", day(2)))
	upsert(t, s, other.ID, entry("o1", "o1", day(3)))
	upsert(t, s, news.ID, entry("n1", "n1", day(4)))
	ids := map[string]string{}
	for _, e := range list(t, s, store.Query{View: store.ViewAll}).Entries {
		ids[e.Title] = e.ID
	}
	for _, err := range []error{
		s.SetRead(ctx, ids["a1"], true),
		s.SetStarred(ctx, ids["a2"], true),
		s.SetRead(ctx, ids["n1"], true),
		s.SetStarred(ctx, ids["n1"], true),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return blog, other, news
}

func testListEntriesViews(t *testing.T, s store.Store) {
	seed(t, s)
	tests := []struct {
		view store.View
		want []string
	}{
		{store.ViewUnread, []string{"o1", "a2"}},
		{store.ViewAll, []string{"n1", "o1", "a2", "a1"}},
		{store.ViewStarred, []string{"n1", "a2"}},
	}
	for _, tt := range tests {
		page := list(t, s, store.Query{View: tt.view})
		if got := titles(page.Entries); !reflect.DeepEqual(got, tt.want) || page.Total != len(tt.want) {
			t.Errorf("view %d = %v (total %d), want %v", tt.view, got, page.Total, tt.want)
		}
	}
}

func testListEntriesFilters(t *testing.T, s store.Store) {
	blog, _, _ := seed(t, s)
	tests := []struct {
		name string
		q    store.Query
		want []string
	}{
		{"feed", store.Query{View: store.ViewAll, FeedID: blog.ID}, []string{"a2", "a1"}},
		{"feed unread", store.Query{View: store.ViewUnread, FeedID: blog.ID}, []string{"a2"}},
		{"folder", store.Query{View: store.ViewAll, Folder: "Tech"}, []string{"o1", "a2", "a1"}},
		{"folder starred", store.Query{View: store.ViewStarred, Folder: "Tech"}, []string{"a2"}},
		{"unknown folder", store.Query{View: store.ViewAll, Folder: "Nope"}, []string{}},
	}
	for _, tt := range tests {
		page := list(t, s, tt.q)
		if got := titles(page.Entries); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func testListEntriesNewestFirst(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	other := addFeed(t, s, "https://other.example/rss", "Other", "")
	upsert(t, s, blogID, entry("b", "middle", day(2)), entry("a", "oldest", day(1)))
	upsert(t, s, other.ID, entry("c", "newest", day(3)))
	got := titles(list(t, s, store.Query{View: store.ViewAll}).Entries)
	if want := []string{"newest", "middle", "oldest"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func testListEntriesPagination(t *testing.T, s store.Store) {
	addFeed(t, s, blogURL, "Example Blog", "")
	var in []store.IncomingEntry
	for d := 1; d <= 5; d++ {
		name := string(rune('a' + d - 1))
		in = append(in, entry(name, name, day(d)))
	}
	upsert(t, s, blogID, in...)
	tests := []struct {
		limit, offset int
		want          []string
	}{
		{2, 0, []string{"e", "d"}},
		{2, 2, []string{"c", "b"}},
		{2, 4, []string{"a"}},
		{2, 6, []string{}},
		{0, 3, []string{"b", "a"}},
	}
	for _, tt := range tests {
		page := list(t, s, store.Query{View: store.ViewAll, Limit: tt.limit, Offset: tt.offset})
		if got := titles(page.Entries); !reflect.DeepEqual(got, tt.want) || page.Total != 5 {
			t.Errorf("limit %d offset %d = %v (total %d), want %v (total 5)", tt.limit, tt.offset, got, page.Total, tt.want)
		}
	}
}

func testMarkAllRead(t *testing.T, s store.Store) {
	blog, other, _ := seed(t, s)
	n, err := s.MarkAllRead(ctx, store.Query{View: store.ViewUnread, Folder: "Tech", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("MarkAllRead changed %d entries, want 2 (limit ignored)", n)
	}
	counts, _ := s.UnreadCounts(ctx)
	if counts[blog.ID] != 0 || counts[other.ID] != 0 {
		t.Errorf("unread counts after mark all = %v", counts)
	}
	if got := titles(list(t, s, store.Query{View: store.ViewStarred}).Entries); !reflect.DeepEqual(got, []string{"n1", "a2"}) {
		t.Errorf("starred after mark all = %v, want unchanged", got)
	}
	if n, _ := s.MarkAllRead(ctx, store.Query{View: store.ViewAll}); n != 0 {
		t.Errorf("second MarkAllRead changed %d, want 0", n)
	}
}

func testUnreadCounts(t *testing.T, s store.Store) {
	blog, other, news := seed(t, s)
	counts, err := s.UnreadCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts[blog.ID] != 1 || counts[other.ID] != 1 || counts[news.ID] != 0 {
		t.Errorf("UnreadCounts = %v, want blog 1, other 1, news 0", counts)
	}
}

const importDoc = `<?xml version="1.0"?>
<opml version="1.0"><head><title>From another reader</title></head><body>
  <outline text="Tech">
    <outline type="rss" text="Example Blog" xmlUrl="https://example.com/feed.xml" htmlUrl="https://example.com/"/>
    <outline type="rss" text="Evil" xmlUrl="javascript:alert(1)"/>
  </outline>
  <outline type="rss" text="Other" xmlUrl="https://other.example/rss" leanfeedId="../../escape"/>
  <outline type="rss" text="News" xmlUrl="https://news.example/atom"/>
</body></opml>`

func testImportOPML(t *testing.T, s store.Store) {
	addFeed(t, s, "https://news.example/atom", "Already here", "")
	stats, err := s.ImportOPML(ctx, strings.NewReader(importDoc))
	if err != nil {
		t.Fatal(err)
	}
	if stats != (store.ImportStats{Added: 2, Skipped: 2}) {
		t.Errorf("stats = %+v, want 2 added (blog, other), 2 skipped (evil, existing)", stats)
	}
	blog, err := s.GetFeed(ctx, blogID)
	if err != nil || blog.Folder != "Tech" || blog.SiteURL != "https://example.com/" {
		t.Errorf("imported blog = %+v, %v", blog, err)
	}
	// The leanfeedId from the file is ignored: IDs always come from the title and URL.
	other, err := s.GetFeed(ctx, "other-949920a5")
	if err != nil || other.Folder != "" {
		t.Errorf("imported other = %+v, %v", other, err)
	}
	if due, _ := s.DueFeeds(ctx, time.Now()); len(due) != 3 {
		t.Errorf("due after import = %d feeds, want 3", len(due))
	}
}

func testImportOPMLRejectsMalformed(t *testing.T, s store.Store) {
	if _, err := s.ImportOPML(ctx, strings.NewReader("<opml><body>")); err == nil {
		t.Error("ImportOPML of truncated XML succeeded")
	}
}

func testExportOPML(t *testing.T, s store.Store) {
	f, err := s.AddFeed(ctx, store.NewFeed{URL: blogURL, Title: "Example Blog", SiteURL: "https://example.com/", Folder: "Tech"})
	if err != nil {
		t.Fatal(err)
	}
	news := addFeed(t, s, "https://news.example/atom", "News", "")
	var buf bytes.Buffer
	if err := s.ExportOPML(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	got, err := opml.Parse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want := []opml.Subscription{
		{ID: f.ID, Title: "Example Blog", XMLURL: blogURL, HTMLURL: "https://example.com/", Folder: "Tech"},
		{ID: news.ID, Title: "News", XMLURL: "https://news.example/atom"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("exported =\n%+v\nwant\n%+v", got, want)
	}
}
