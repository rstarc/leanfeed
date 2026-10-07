package web

import (
	"errors"
	"slices"
	"testing"
)

// These tests click through the UI like a user, using the htmx stand-in in
// uiharness_test.go. The fixture has:
//
//	Tech/Example Blog: "Blog new" (unread), "Blog old" (read)
//	News:              "News item" (unread, starred)

func wantText(t *testing.T, p *page, sel, want string) {
	t.Helper()
	if got := p.text(sel); got != want {
		t.Errorf("%s = %q, want %q", sel, got, want)
	}
}

func wantTitles(t *testing.T, p *page, want ...string) {
	t.Helper()
	if got := p.texts("#list .entry-title"); !slices.Equal(got, want) {
		t.Errorf("list shows %q, want %q", got, want)
	}
}

func wantURL(t *testing.T, p *page, want string) {
	t.Helper()
	if p.url != want {
		t.Errorf("address bar shows %s, want %s", p.url, want)
	}
}

func TestFlowLeaveFeedsPageThroughSidebar(t *testing.T) {
	f := newFixture(t)
	p := f.open("/entries")
	p.click(`a[href="/feeds"]`)
	wantText(t, p, "#manage h1", "Feeds")

	p.click("#view-unread")
	wantText(t, p, "#list h1", "Unread")
	wantTitles(t, p, "Blog new", "News item")
	wantURL(t, p, "/entries?view=unread")
	if p.exists("#manage") {
		t.Error("the feeds page is still shown")
	}
}

func TestFlowAddFeedThenOpenIt(t *testing.T) {
	f := newFixture(t)
	p := f.open("/feeds")
	p.fill(`form[hx-post="/feeds"] input[name="url"]`, "https://new.example/feed")
	p.fill(`form[hx-post="/feeds"] input[name="folder"]`, "Tech")
	p.click(`form[hx-post="/feeds"] button`)
	wantText(t, p, "#notice", "Added Subscribed https://new.example/feed.")
	if got := len(p.all(".feed-table tbody tr")); got != 3 {
		t.Errorf("feed table has %d rows, want 3", got)
	}

	feed := mustFindFeed(t, f.store, "https://new.example/feed")
	if got := p.texts("li.folder ul a"); !slices.Contains(got, "Subscribed https://new.example/feed") {
		t.Errorf("sidebar folder Tech lists %q, want the new feed", got)
	}
	p.click("#feed-" + feed.ID)
	wantText(t, p, "#list h1", "Subscribed https://new.example/feed")
	wantText(t, p, "#list .empty", "No entries.")
}

func TestFlowAddFeedFailureShowsError(t *testing.T) {
	f := newFixture(t)
	f.fetcher.err = errors.New("not a valid feed: failed to detect feed type")
	p := f.open("/feeds")
	p.fill(`form[hx-post="/feeds"] input[name="url"]`, "https://example.com/page.html")
	p.click(`form[hx-post="/feeds"] button`)
	if p.status != 422 {
		t.Errorf("status %d, want 422", p.status)
	}
	wantText(t, p, "#notice", "Could not add https://example.com/page.html: not a valid feed: failed to detect feed type")
	if !p.hasClass("#notice", "error") {
		t.Error("the notice is not styled as an error")
	}
	if got := len(p.all(".feed-table tbody tr")); got != 2 {
		t.Errorf("feed table has %d rows, want 2", got)
	}
}

func TestFlowOpenEntryMarksItRead(t *testing.T) {
	f := newFixture(t)
	id := f.ids["Blog new"]
	p := f.open("/entries?view=unread")
	wantText(t, p, "#view-unread .count", "2")

	p.click("#entry-" + id + " a.entry-title")
	wantText(t, p, "#entry h1", "Blog new")
	wantURL(t, p, "/entries/"+id+"?view=unread")
	if p.hasClass("#entry-"+id, "unread") {
		t.Error("the row is still shown as unread")
	}
	wantText(t, p, "#view-unread .count", "1")
	wantText(t, p, "#feed-"+f.blog.ID, "Example Blog")
}

func TestFlowStarFromEntryViewThenOpenStarred(t *testing.T) {
	f := newFixture(t)
	id := f.ids["Blog new"]
	p := f.open("/entries?view=all")
	p.click("#entry-" + id + " a.entry-title")

	p.click("#entry-actions-" + id + ` [hx-post="/entries/` + id + `/star"]`)
	wantText(t, p, "#entry-actions-"+id, "Mark unread ★ Starred")
	if !p.hasClass("#entry-"+id, "starred") {
		t.Error("the row does not show the star")
	}

	p.click("#view-starred")
	wantTitles(t, p, "Blog new", "News item")
	if p.exists("#entry") {
		t.Error("the entry stays open after switching lists")
	}
}

func TestFlowMarkUnreadFromRow(t *testing.T) {
	f := newFixture(t)
	id := f.ids["Blog old"]
	p := f.open("/entries?view=all")
	p.click("#entry-" + id + ` [title="Mark unread"]`)
	if !p.hasClass("#entry-"+id, "unread") {
		t.Error("the row is not shown as unread")
	}
	wantText(t, p, "#view-unread .count", "3")
	wantText(t, p, "#feed-"+f.blog.ID, "Example Blog 2")
	// The row's button now offers the opposite action.
	p.click("#entry-" + id + ` [title="Mark read"]`)
	wantText(t, p, "#view-unread .count", "2")
}

func TestFlowMarkAllReadAsksFirst(t *testing.T) {
	f := newFixture(t)
	p := f.open("/entries?view=unread&folder=Tech")
	wantTitles(t, p, "Blog new")

	p.answerConfirm = false
	p.click("#list .list-header button")
	if len(p.prompts) != 1 || p.prompts[0] != "Mark all entries in this list as read?" {
		t.Errorf("prompts = %q", p.prompts)
	}
	wantTitles(t, p, "Blog new")
	if f.entry("Blog new").Read {
		t.Error("declining the prompt still marked entries read")
	}

	p.answerConfirm = true
	p.click("#list .list-header button")
	wantText(t, p, "#list .empty", "No entries.")
	wantText(t, p, "#folder-Tech", "Tech")
	wantText(t, p, "#view-unread .count", "1") // "News item" is not in the folder
}

func TestFlowPagination(t *testing.T) {
	f := newFixture(t)
	f.srv.pageSize = 2
	p := f.open("/entries?view=all")
	wantTitles(t, p, "Blog new", "News item")

	p.click(".pager a")
	wantTitles(t, p, "Blog old")
	wantURL(t, p, "/entries?page=2&view=all")

	p.click(".pager a")
	wantTitles(t, p, "Blog new", "News item")
	wantURL(t, p, "/entries?view=all")
}

func TestFlowRenameAndMoveFeed(t *testing.T) {
	f := newFixture(t)
	row := "#manage-" + f.blog.ID
	p := f.open("/feeds")
	p.fill(row+` input[name="title"]`, "Renamed")
	p.fill(row+` input[name="folder"]`, "Reading")
	p.click(row + ` button[type="submit"]`)

	wantText(t, p, "#notice", "Saved Renamed.")
	if !p.exists("#folder-Reading") || p.exists("#folder-Tech") {
		t.Errorf("sidebar folders = %q, want Reading instead of Tech", p.texts("li.folder > a"))
	}
	wantText(t, p, "#feed-"+f.blog.ID, "Renamed 1")
}

func TestFlowRemoveFeedAsksFirst(t *testing.T) {
	f := newFixture(t)
	row := "#manage-" + f.blog.ID
	p := f.open("/feeds")

	p.answerConfirm = false
	p.click(row + " [hx-delete]")
	if len(p.prompts) != 1 || p.prompts[0] != "Remove Example Blog and all its entries?" {
		t.Errorf("prompts = %q", p.prompts)
	}
	if !p.exists(row) {
		t.Fatal("declining the prompt removed the feed")
	}

	p.answerConfirm = true
	p.click(row + " [hx-delete]")
	wantText(t, p, "#notice", "Removed Example Blog.")
	if p.exists(row) || p.exists("#feed-"+f.blog.ID) {
		t.Error("the removed feed is still shown")
	}
	p.click("#view-all")
	wantTitles(t, p, "News item")
}

func TestFlowRefreshAll(t *testing.T) {
	f := newFixture(t)
	p := f.open("/feeds")
	p.click(`form[hx-post="/refresh"] button`)
	wantText(t, p, "#notice", "Refreshing all feeds. New entries appear as each feed is fetched.")
	if f.fetcher.refreshed != 1 {
		t.Errorf("refreshed %d times, want 1", f.fetcher.refreshed)
	}
}

func TestFlowBackLinkFromEntry(t *testing.T) {
	f := newFixture(t)
	p := f.open("/entries/" + f.ids["News item"] + "?view=starred")
	wantText(t, p, "#entry h1", "News item")
	p.click("#entry a.back")
	wantURL(t, p, "/entries?view=starred")
	wantTitles(t, p, "News item")
}

// highlighted returns the id, or else the href, of each highlighted
// sidebar link.
func highlighted(p *page) []string {
	var out []string
	for _, n := range p.all(`#sidebar a[aria-current="page"]`) {
		out = append(out, attrOr(n, "id", attrOr(n, "href", "")))
	}
	return out
}

func wantHighlighted(t *testing.T, p *page, want string) {
	t.Helper()
	if got := highlighted(p); !slices.Equal(got, []string{want}) {
		t.Errorf("on %s the menu highlights %q, want only %q", p.url, got, want)
	}
}

func TestFlowMenuHighlightsTheCurrentPage(t *testing.T) {
	f := newFixture(t)
	f.srv.pageSize = 2
	p := f.open("/entries?view=unread")
	wantHighlighted(t, p, "view-unread")

	p.click("#view-all")
	wantHighlighted(t, p, "view-all")
	p.click(".pager a")
	wantHighlighted(t, p, "view-all")
	p.click("#view-starred")
	wantHighlighted(t, p, "view-starred")
	p.click("#folder-Tech")
	wantHighlighted(t, p, "folder-Tech")
	p.click("#feed-" + f.news.ID)
	wantHighlighted(t, p, "feed-"+f.news.ID)

	// Opening an entry and acting on it keeps the list it came from.
	p.click("#list .row a.entry-title")
	wantHighlighted(t, p, "feed-"+f.news.ID)
	p.click(`#entry .entry-actions [hx-post="/entries/` + f.ids["News item"] + `/unstar"]`)
	wantHighlighted(t, p, "feed-"+f.news.ID)

	p.click(`#sidebar a[href="/feeds"]`)
	wantHighlighted(t, p, "/feeds")
	p.click("#view-unread")
	wantHighlighted(t, p, "view-unread")
}
