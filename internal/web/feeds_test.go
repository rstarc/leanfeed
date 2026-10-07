package web

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"leanfeed/internal/opml"
	"leanfeed/internal/store"
)

// doForm sends an htmx request with a form-encoded body.
func (f *fixture) doForm(method, target string, form url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

// upload sends an htmx multipart request with one file field.
func (f *fixture) upload(target, field string, content []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile(field, "subscriptions.opml")
	must(f.t, err)
	fw.Write(content)
	must(f.t, mw.Close())
	req := httptest.NewRequest("POST", target, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) feedTitles() []string {
	f.t.Helper()
	feeds, err := f.store.ListFeeds(ctx)
	must(f.t, err)
	var titles []string
	for _, feed := range feeds {
		titles = append(titles, feed.Title)
	}
	return titles
}

func TestFeedsPageShowsStatus(t *testing.T) {
	f := newFixture(t)
	fetched := time.Date(2026, 10, 7, 9, 30, 0, 0, time.Local)
	must(t, f.store.RecordFetch(ctx, f.blog.ID, store.FetchResult{
		FetchedAt: fetched, NextFetchAt: fetched.Add(30 * time.Minute), Status: store.StatusOK,
	}))
	must(t, f.store.RecordFetch(ctx, f.news.ID, store.FetchResult{
		FetchedAt: fetched, NextFetchAt: fetched.Add(2 * time.Hour), Status: store.StatusError,
		Error: "HTTP 500 Internal Server Error", ConsecutiveFailures: 2,
	}))

	rec := f.do("GET", "/feeds", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if byID(parseHTML(t, body), "sidebar") == nil {
		t.Error("full feeds page has no sidebar")
	}
	blog := elementText(t, body, "manage-"+f.blog.ID)
	for _, want := range []string{"OK", "2026-10-07 09:30"} {
		if !strings.Contains(blog, want) {
			t.Errorf("blog row %q missing %q", blog, want)
		}
	}
	news := elementText(t, body, "manage-"+f.news.ID)
	for _, want := range []string{"HTTP 500 Internal Server Error", "2 failures", "2026-10-07 11:30"} {
		if !strings.Contains(news, want) {
			t.Errorf("failing feed row %q missing %q", news, want)
		}
	}
	// Form values carry the editable title, folder and URL.
	for _, want := range []string{`value="Example Blog"`, `value="Tech"`, `value="https://example.com/feed.xml"`} {
		if !strings.Contains(body, want) {
			t.Errorf("feed form does not contain %s", want)
		}
	}
}

func TestSidebarMarksFailingFeeds(t *testing.T) {
	f := newFixture(t)
	must(t, f.store.RecordFetch(ctx, f.news.ID, store.FetchResult{Status: store.StatusError, Error: "HTTP 404 Not Found", ConsecutiveFailures: 1}))
	body := f.do("GET", "/entries", false).Body.String()
	if !strings.Contains(body, `class="feed-error" title="Last fetch failed: HTTP 404 Not Found"`) {
		t.Errorf("sidebar does not mark the failing feed:\n%s", body)
	}
}

func TestAddFeed(t *testing.T) {
	f := newFixture(t)
	rec := f.doForm("POST", "/feeds", url.Values{"url": {" https://new.example/feed "}, "folder": {"Tech"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(f.fetcher.subscribed) != 1 || f.fetcher.subscribed[0] != "https://new.example/feed" {
		t.Errorf("subscribed = %v", f.fetcher.subscribed)
	}
	feed, err := f.store.GetFeed(ctx, mustFindFeed(t, f.store, "https://new.example/feed").ID)
	if err != nil || feed.Folder != "Tech" {
		t.Errorf("added feed = %+v, %v", feed, err)
	}
	body := rec.Body.String()
	if !strings.Contains(elementText(t, body, "notice"), "Added Subscribed https://new.example/feed") {
		t.Errorf("notice = %q", elementText(t, body, "notice"))
	}
	if !strings.Contains(body, `id="sidebar" hx-swap-oob`) {
		t.Error("add does not update the sidebar")
	}
}

func mustFindFeed(t *testing.T, s store.Store, url string) store.Feed {
	t.Helper()
	feeds, err := s.ListFeeds(ctx)
	must(t, err)
	for _, f := range feeds {
		if f.URL == url {
			return f
		}
	}
	t.Fatalf("no feed with URL %s", url)
	return store.Feed{}
}

func TestAddFeedErrors(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		fetchErr   error
		wantStatus int
		wantNotice string
	}{
		{"empty URL", "  ", nil, http.StatusBadRequest, "Enter a feed URL"},
		{"not a feed", "https://x.example/", errors.New("not a valid feed: EOF"), http.StatusUnprocessableEntity, "not a valid feed: EOF"},
		{"already subscribed", "https://x.example/", fmt.Errorf("add: %w", store.ErrExists), http.StatusConflict, "already subscribed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.fetcher.err = tt.fetchErr
			rec := f.doForm("POST", "/feeds", url.Values{"url": {tt.url}})
			if rec.Code != tt.wantStatus {
				t.Errorf("status %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := elementText(t, rec.Body.String(), "notice"); !strings.Contains(got, tt.wantNotice) {
				t.Errorf("notice = %q, want it to contain %q", got, tt.wantNotice)
			}
			if got := f.feedTitles(); len(got) != 2 {
				t.Errorf("feeds after failed add = %v", got)
			}
		})
	}
}

func TestUpdateFeed(t *testing.T) {
	f := newFixture(t)
	form := func(title, folder string) url.Values {
		return url.Values{"title": {title}, "folder": {folder}, "url": {f.blog.URL}}
	}
	rec := f.doForm("POST", "/feeds/"+f.blog.ID, form(" Renamed ", "Reading"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got, _ := f.store.GetFeed(ctx, f.blog.ID)
	if got.Title != "Renamed" || got.Folder != "Reading" {
		t.Errorf("feed after update = %+v", got)
	}
	if len(f.fetcher.changed) != 0 {
		t.Errorf("an unchanged URL was fetched: %v", f.fetcher.changed)
	}
	body := rec.Body.String()
	if !strings.Contains(elementText(t, body, "notice"), "Saved Renamed") || !strings.Contains(body, `id="sidebar" hx-swap-oob`) {
		t.Errorf("update response:\n%s", body)
	}
	if byID(parseHTML(t, body), "folder-Reading") == nil {
		t.Error("sidebar does not show the new folder")
	}

	// An empty folder moves the feed to the top level.
	f.doForm("POST", "/feeds/"+f.blog.ID, form("Renamed", ""))
	if got, _ := f.store.GetFeed(ctx, f.blog.ID); got.Folder != "" {
		t.Errorf("folder after clearing = %q", got.Folder)
	}

	if rec := f.doForm("POST", "/feeds/"+f.blog.ID, form("  ", "")); rec.Code != http.StatusBadRequest {
		t.Errorf("empty title = %d, want 400", rec.Code)
	}
	if rec := f.doForm("POST", "/feeds/nope", form("x", "")); rec.Code != http.StatusNotFound {
		t.Errorf("unknown feed = %d, want 404", rec.Code)
	}
}

func TestUpdateFeedURL(t *testing.T) {
	f := newFixture(t)
	rec := f.doForm("POST", "/feeds/"+f.blog.ID, url.Values{
		"title": {"Renamed"}, "folder": {"Tech"}, "url": {" https://moved.example/feed "},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if want := []string{f.blog.ID + " https://moved.example/feed"}; !slices.Equal(f.fetcher.changed, want) {
		t.Errorf("changed = %v, want %v", f.fetcher.changed, want)
	}
	got, _ := f.store.GetFeed(ctx, f.blog.ID)
	if got.URL != "https://moved.example/feed" || got.Title != "Renamed" {
		t.Errorf("feed after update = %+v", got)
	}
	if notice := elementText(t, rec.Body.String(), "notice"); !strings.Contains(notice, "Saved Renamed") {
		t.Errorf("notice = %q", notice)
	}
	if byID(parseHTML(t, rec.Body.String()), "manage-"+f.blog.ID) == nil {
		t.Error("manage page does not list the feed")
	}
}

func TestUpdateFeedURLErrors(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		fetchErr   error
		wantStatus int
		wantNotice string
	}{
		{"empty URL", "  ", nil, http.StatusBadRequest, "A feed needs a URL"},
		{"not a feed", "https://x.example/", errors.New("not a valid feed: EOF"), http.StatusUnprocessableEntity, "not a valid feed: EOF"},
		{"another feed's URL", "https://news.example/rss", fmt.Errorf("update: %w", store.ErrExists), http.StatusConflict, "already subscribed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.fetcher.err = tt.fetchErr
			rec := f.doForm("POST", "/feeds/"+f.blog.ID, url.Values{"title": {"Renamed"}, "folder": {""}, "url": {tt.url}})
			if rec.Code != tt.wantStatus {
				t.Errorf("status %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := elementText(t, rec.Body.String(), "notice"); !strings.Contains(got, tt.wantNotice) {
				t.Errorf("notice = %q, want it to contain %q", got, tt.wantNotice)
			}
			if got, _ := f.store.GetFeed(ctx, f.blog.ID); got != f.blog {
				t.Errorf("failed update changed the feed to %+v", got)
			}
		})
	}
}

func TestRemoveFeed(t *testing.T) {
	f := newFixture(t)
	rec := f.do("DELETE", "/feeds/"+f.blog.ID, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if _, err := f.store.GetFeed(ctx, f.blog.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("feed still exists: %v", err)
	}
	if _, err := f.store.GetEntry(ctx, f.ids["Blog new"]); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("entries of removed feed still exist: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(elementText(t, body, "notice"), "Removed Example Blog") {
		t.Errorf("notice = %q", elementText(t, body, "notice"))
	}
	if byID(parseHTML(t, body), "feed-"+f.blog.ID) != nil {
		t.Error("sidebar still lists the removed feed")
	}
	if rec := f.do("DELETE", "/feeds/"+f.blog.ID, true); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", rec.Code)
	}
}

func TestRefreshAll(t *testing.T) {
	f := newFixture(t)
	rec := f.do("POST", "/refresh", true)
	if rec.Code != http.StatusOK || f.fetcher.refreshed != 1 {
		t.Errorf("status %d, refreshed %d times", rec.Code, f.fetcher.refreshed)
	}
	if !strings.Contains(elementText(t, rec.Body.String(), "notice"), "Refreshing") {
		t.Errorf("refresh response: %s", rec.Body)
	}
}

func TestExportOPML(t *testing.T) {
	f := newFixture(t)
	rec := f.do("GET", "/opml", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="leanfeed.opml"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	subs, err := opml.Parse(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 || subs[0].Title != "Example Blog" || subs[0].Folder != "Tech" || subs[1].Title != "News" {
		t.Errorf("exported %+v", subs)
	}
}

func TestImportOPML(t *testing.T) {
	f := newFixture(t)
	doc := `<opml version="2.0"><body><outline text="Imported">
		<outline type="rss" text="Fresh" xmlUrl="https://fresh.example/feed"/>
		<outline type="rss" text="Dup" xmlUrl="https://example.com/feed.xml"/>
	</outline></body></opml>`
	rec := f.upload("/opml", "file", []byte(doc))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := mustFindFeed(t, f.store, "https://fresh.example/feed"); got.Folder != "Imported" {
		t.Errorf("imported feed = %+v", got)
	}
	if got := elementText(t, rec.Body.String(), "notice"); !strings.Contains(got, "Imported 1 feed") || !strings.Contains(got, "skipped 1") {
		t.Errorf("notice = %q", got)
	}
}

func TestImportOPMLErrors(t *testing.T) {
	f := newFixture(t)
	if rec := f.upload("/opml", "file", []byte("<opml><body")); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed OPML = %d, want 400", rec.Code)
	}
	if rec := f.upload("/opml", "other", []byte("<opml/>")); rec.Code != http.StatusBadRequest {
		t.Errorf("missing file field = %d, want 400", rec.Code)
	}
	big := bytes.Repeat([]byte(" "), 5<<20+1)
	if rec := f.upload("/opml", "file", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("6 MB upload = %d, want 413", rec.Code)
	}
	if got := f.feedTitles(); len(got) != 2 {
		t.Errorf("feeds after failed imports = %v", got)
	}
}
