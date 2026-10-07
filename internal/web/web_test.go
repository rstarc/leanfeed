package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"leanfeed/internal/store"
	"leanfeed/internal/store/filestore"
)

var (
	ctx      = context.Background()
	quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))
)

// fakeFetcher stands in for the network: Subscribe adds the feed directly.
type fakeFetcher struct {
	store      store.Store
	refreshed  int
	subscribed []string
	err        error
}

func (f *fakeFetcher) Subscribe(ctx context.Context, url, folder string) (store.Feed, error) {
	if f.err != nil {
		return store.Feed{}, f.err
	}
	f.subscribed = append(f.subscribed, url)
	return f.store.AddFeed(ctx, store.NewFeed{URL: url, Title: "Subscribed " + url, Folder: folder})
}

func (f *fakeFetcher) RefreshAll(ctx context.Context) error {
	f.refreshed++
	return f.err
}

type fixture struct {
	t       *testing.T
	store   store.Store
	fetcher *fakeFetcher
	srv     *Server
	blog    store.Feed // folder Tech
	news    store.Feed // no folder
	ids     map[string]string
}

func day(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }

// newFixture creates a store with:
//
//	Tech/Example Blog: "Blog new" (day 3, unread), "Blog old" (day 1, read)
//	News:              "News item" (day 2, unread, starred)
func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := filestore.Open(t.TempDir(), quietLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f := &fixture{t: t, store: s, fetcher: &fakeFetcher{store: s}, ids: map[string]string{}}
	f.srv, err = New(s, f.fetcher, quietLog)
	if err != nil {
		t.Fatal(err)
	}
	f.blog = f.addFeed("https://example.com/feed.xml", "Example Blog", "Tech")
	f.news = f.addFeed("https://news.example/rss", "News", "")
	f.addEntry(f.blog.ID, "Blog new", day(3))
	f.addEntry(f.blog.ID, "Blog old", day(1))
	f.addEntry(f.news.ID, "News item", day(2))
	must(t, s.SetRead(ctx, f.ids["Blog old"], true))
	must(t, s.SetStarred(ctx, f.ids["News item"], true))
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) addFeed(url, title, folder string) store.Feed {
	f.t.Helper()
	feed, err := f.store.AddFeed(ctx, store.NewFeed{URL: url, Title: title, Folder: folder, SiteURL: "https://example.com/"})
	must(f.t, err)
	return feed
}

func (f *fixture) addEntry(feedID, title string, published time.Time) string {
	f.t.Helper()
	_, err := f.store.UpsertEntries(ctx, feedID, []store.IncomingEntry{{
		GUID: title, URL: "https://example.com/" + url.PathEscape(title), Title: title, Author: "Jane Doe",
		PublishedAt: published, RawContent: "<p>" + title + "</p>", Content: "<p>" + title + "</p>",
	}})
	must(f.t, err)
	page, err := f.store.ListEntries(ctx, store.Query{View: store.ViewAll, FeedID: feedID})
	must(f.t, err)
	for _, e := range page.Entries {
		f.ids[e.Title] = e.ID
	}
	return f.ids[title]
}

// do sends a request. htmx requests carry HX-Request, as htmx does.
func (f *fixture) do(method, target string, htmx bool) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) entry(title string) store.Entry {
	f.t.Helper()
	e, err := f.store.GetEntry(ctx, f.ids[title])
	must(f.t, err)
	return e
}

func parseHTML(t *testing.T, body string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// byID returns the element with the given id, or nil.
func byID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == id {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := byID(c, id); found != nil {
			return found
		}
	}
	return nil
}

// textOf returns the whitespace-normalized text inside n.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data + " ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// elementText returns the text of the element with id, failing if absent.
func elementText(t *testing.T, body, id string) string {
	t.Helper()
	n := byID(parseHTML(t, body), id)
	if n == nil {
		t.Fatalf("no element with id %q in:\n%s", id, body)
	}
	return textOf(n)
}

// listedTitles returns the entry titles in the #list element, in order.
func listedTitles(t *testing.T, body string) []string {
	t.Helper()
	list := byID(parseHTML(t, body), "list")
	if list == nil {
		t.Fatalf("no #list in:\n%s", body)
	}
	var titles []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "class" && strings.Contains(a.Val, "entry-title") {
					titles = append(titles, textOf(n))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(list)
	return titles
}

func TestRootRedirectsToUnread(t *testing.T) {
	f := newFixture(t)
	rec := f.do("GET", "/", false)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/entries?view=unread" {
		t.Errorf("GET / = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := f.do("GET", "/nope", false); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", rec.Code)
	}
}

func TestEntriesFullPage(t *testing.T) {
	f := newFixture(t)
	rec := f.do("GET", "/entries", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(strings.ToLower(body), "<!doctype html>") {
		t.Errorf("full page does not start with a doctype: %.80q", body)
	}
	if got := strings.Join(listedTitles(t, body), "|"); got != "Blog new|News item" {
		t.Errorf("default view lists %q, want unread entries newest first", got)
	}
	if got := elementText(t, body, "feed-"+f.blog.ID); got != "Example Blog 1" {
		t.Errorf("sidebar blog = %q, want title and unread count 1", got)
	}
	if got := elementText(t, body, "folder-Tech"); !strings.HasPrefix(got, "Tech 1") {
		t.Errorf("sidebar folder = %q, want folder name and unread count 1", got)
	}
	if got := elementText(t, body, "view-unread"); got != "Unread 2" {
		t.Errorf("sidebar unread view = %q, want total unread 2", got)
	}
}

func TestEntriesViewsAndFilters(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		query string
		want  string
	}{
		{"view=unread", "Blog new|News item"},
		{"view=all", "Blog new|News item|Blog old"},
		{"view=starred", "News item"},
		{"view=all&feed=" + f.blog.ID, "Blog new|Blog old"},
		{"view=unread&folder=Tech", "Blog new"},
		{"view=all&folder=Nope", ""},
	}
	for _, tt := range tests {
		rec := f.do("GET", "/entries?"+tt.query, true)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", tt.query, rec.Code)
			continue
		}
		if got := strings.Join(listedTitles(t, rec.Body.String()), "|"); got != tt.want {
			t.Errorf("%s lists %q, want %q", tt.query, got, tt.want)
		}
	}
	for _, bad := range []string{"view=bogus", "view=all&page=0", "view=all&page=x", "view=all&feed=nope"} {
		if rec := f.do("GET", "/entries?"+bad, false); rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 400 or 404", bad, rec.Code)
		}
	}
}

func TestEntriesHTMXPartial(t *testing.T) {
	f := newFixture(t)
	body := f.do("GET", "/entries?view=all", true).Body.String()
	if strings.Contains(strings.ToLower(body), "<html") || strings.Contains(body, `id="sidebar"`) {
		t.Errorf("htmx request got a full page:\n%s", body)
	}
	if len(listedTitles(t, body)) != 3 {
		t.Errorf("partial lists %v", listedTitles(t, body))
	}

	// History restores need the full page.
	req := httptest.NewRequest("GET", "/entries?view=all", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-History-Restore-Request", "true")
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if !strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
		t.Error("history restore request got a partial")
	}
}

func TestEntriesPagination(t *testing.T) {
	f := newFixture(t)
	f.srv.pageSize = 2
	page1 := f.do("GET", "/entries?view=all", true).Body.String()
	if got := strings.Join(listedTitles(t, page1), "|"); got != "Blog new|News item" {
		t.Errorf("page 1 = %q", got)
	}
	if !strings.Contains(page1, `href="/entries?page=2&amp;view=all"`) {
		t.Errorf("page 1 has no link to page 2:\n%s", page1)
	}
	page2 := f.do("GET", "/entries?view=all&page=2", true).Body.String()
	if got := strings.Join(listedTitles(t, page2), "|"); got != "Blog old" {
		t.Errorf("page 2 = %q", got)
	}
	if strings.Contains(page2, "page=3") {
		t.Error("last page links to a next page")
	}
}

func TestEntryView(t *testing.T) {
	f := newFixture(t)
	id := f.ids["Blog new"]
	rec := f.do("GET", "/entries/"+id+"?view=unread", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	article := elementText(t, body, "entry")
	for _, want := range []string{"Blog new", "Example Blog", "Jane Doe", "2026-10-03"} {
		if !strings.Contains(article, want) {
			t.Errorf("entry view %q missing %q", article, want)
		}
	}
	if !strings.Contains(body, "<p>Blog new</p>") {
		t.Error("entry content is not rendered as HTML")
	}
	if !strings.Contains(body, `href="https://example.com/Blog%20new"`) {
		t.Error("entry view has no link to the original")
	}
	if !f.entry("Blog new").Read {
		t.Error("opening an entry did not mark it read")
	}
	if got := elementText(t, body, "feed-"+f.blog.ID); got != "Example Blog" {
		t.Errorf("sidebar after open = %q, want count gone", got)
	}
	if rec := f.do("GET", "/entries/0000000000000000", false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown entry = %d, want 404", rec.Code)
	}
}

func TestEntryViewHTMX(t *testing.T) {
	f := newFixture(t)
	id := f.ids["Blog new"]
	body := f.do("GET", "/entries/"+id, true).Body.String()
	if strings.Contains(strings.ToLower(body), "<html") {
		t.Error("htmx entry view is a full page")
	}
	if byID(parseHTML(t, body), "entry") == nil {
		t.Error("htmx entry view has no #entry")
	}
	// The row and the sidebar counts update out of band.
	for _, want := range []string{`id="entry-` + id + `"`, `id="sidebar"`} {
		i := strings.Index(body, want)
		if i < 0 || !strings.Contains(body[i:min(i+200, len(body))], "hx-swap-oob") {
			t.Errorf("no out-of-band element %s in:\n%s", want, body)
		}
	}
}

func TestEntryActions(t *testing.T) {
	tests := []struct {
		action, title string
		check         func(store.Entry) bool
	}{
		{"read", "Blog new", func(e store.Entry) bool { return e.Read }},
		{"unread", "Blog old", func(e store.Entry) bool { return !e.Read }},
		{"star", "Blog new", func(e store.Entry) bool { return e.Starred }},
		{"unstar", "News item", func(e store.Entry) bool { return !e.Starred }},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			f := newFixture(t)
			id := f.ids[tt.title]
			rec := f.do("POST", "/entries/"+id+"/"+tt.action, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if !tt.check(f.entry(tt.title)) {
				t.Errorf("%s did not change the entry: %+v", tt.action, f.entry(tt.title))
			}
			body := rec.Body.String()
			if byID(parseHTML(t, body), "entry-"+id) == nil || !strings.Contains(body, `id="sidebar" hx-swap-oob`) {
				t.Errorf("response lacks the updated row or sidebar:\n%s", body)
			}
		})
	}

	f := newFixture(t)
	if rec := f.do("POST", "/entries/0000000000000000/read", true); rec.Code != http.StatusNotFound {
		t.Errorf("action on unknown entry = %d, want 404", rec.Code)
	}
	if rec := f.do("GET", "/entries/"+f.ids["Blog new"]+"/read", true); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on action = %d, want 405", rec.Code)
	}
}

func TestRowShowsState(t *testing.T) {
	f := newFixture(t)
	body := f.do("GET", "/entries?view=all", true).Body.String()
	doc := parseHTML(t, body)
	class := func(title string) string {
		n := byID(doc, "entry-"+f.ids[title])
		if n == nil {
			t.Fatalf("no row for %q", title)
		}
		for _, a := range n.Attr {
			if a.Key == "class" {
				return a.Val
			}
		}
		return ""
	}
	if c := class("Blog new"); !strings.Contains(c, "unread") || strings.Contains(c, "starred") {
		t.Errorf("unread row class = %q", c)
	}
	if c := class("Blog old"); strings.Contains(c, "unread") {
		t.Errorf("read row class = %q", c)
	}
	if c := class("News item"); !strings.Contains(c, "starred") {
		t.Errorf("starred row class = %q", c)
	}
}

func TestMarkAllRead(t *testing.T) {
	f := newFixture(t)
	f.addEntry(f.blog.ID, "Blog newer", day(5))
	rec := f.do("POST", "/entries/mark-read?view=unread&folder=Tech", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !f.entry("Blog new").Read || !f.entry("Blog newer").Read {
		t.Error("entries in the folder are still unread")
	}
	if f.entry("News item").Read {
		t.Error("entry outside the folder was marked read")
	}
	body := rec.Body.String()
	if got := listedTitles(t, body); len(got) != 0 {
		t.Errorf("unread list after marking all read = %v", got)
	}
	if !strings.Contains(body, `id="sidebar" hx-swap-oob`) {
		t.Error("mark-read response does not update the sidebar")
	}
}

func TestFeedContentIsEscaped(t *testing.T) {
	f := newFixture(t)
	f.addEntry(f.news.ID, `<script>alert("t")</script>`, day(6))
	body := f.do("GET", "/entries?view=all", false).Body.String()
	if strings.Contains(body, `<script>alert`) {
		t.Error("entry title was not escaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("escaped title is missing")
	}
}

func TestStaticAssets(t *testing.T) {
	f := newFixture(t)
	for path, ctype := range map[string]string{
		"/static/htmx.min.js": "text/javascript",
		"/static/style.css":   "text/css",
	} {
		rec := f.do("GET", path, false)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), ctype) || rec.Body.Len() == 0 {
			t.Errorf("GET %s = %d %q (%d bytes)", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}
}
