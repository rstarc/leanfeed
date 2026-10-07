package web

import (
	"slices"
	"testing"
)

func TestSidebarSections(t *testing.T) {
	f := newFixture(t)
	p := f.open("/entries")
	if got := p.texts("#sidebar .nav-section h2"); !slices.Equal(got, []string{"Views", "Feeds", "Configuration"}) {
		t.Errorf("sidebar sections = %q", got)
	}
	if got := p.texts(`#sidebar [aria-labelledby="nav-views"] a`); !slices.Equal(got, []string{"Unread 2", "All", "Starred"}) {
		t.Errorf("views section links = %q", got)
	}
	if got := p.texts(`#sidebar [aria-labelledby="nav-feeds"] a`); !slices.Equal(got, []string{"Tech 1", "Example Blog 1", "News 1"}) {
		t.Errorf("feeds section links = %q", got)
	}
	if got := p.texts(`#sidebar [aria-labelledby="nav-config"] a`); !slices.Equal(got, []string{"Manage feeds", "Export OPML"}) {
		t.Errorf("configuration section links = %q", got)
	}
}

func TestSidebarHighlightsCurrentPage(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		path string
		want []string // ids or hrefs of highlighted links
	}{
		{"/entries?view=unread", []string{"view-unread"}},
		{"/entries?view=all&folder=Tech", []string{"folder-Tech"}},
		{"/feeds", []string{"/feeds"}},
	}
	for _, tt := range tests {
		p := f.open(tt.path)
		var got []string
		for _, n := range p.all(`#sidebar a[aria-current="page"]`) {
			got = append(got, attrOr(n, "id", attrOr(n, "href", "")))
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s highlights %q, want %q", tt.path, got, tt.want)
		}
	}

	// The sidebar that updates after adding a feed keeps the highlight.
	p := f.open("/feeds")
	p.fill(`form[hx-post="/feeds"] input[name="url"]`, "https://new.example/feed")
	p.click(`form[hx-post="/feeds"] button`)
	if got := len(p.all(`#sidebar a[aria-current="page"][href="/feeds"]`)); got != 1 {
		t.Errorf("after adding a feed, Manage feeds highlighted %d times, want 1", got)
	}
	if p.exists(`#sidebar .views a[aria-current="page"]`) {
		t.Error("after adding a feed, a view is highlighted")
	}
}

func TestLayoutHasResizeAndMenuControls(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/entries", "/feeds"} {
		p := f.open(path)
		for _, sel := range []string{
			`.splitter[data-resize="sidebar"][role="separator"][tabindex="0"]`,
			`.splitter[data-resize="list"][role="separator"][tabindex="0"]`,
			`#sidebar button[data-menu-toggle][aria-label="Hide menu"]`,
			`button.menu-show[data-menu-toggle][aria-label="Show menu"]`,
			`head script[src="/static/ui.js"]`,
		} {
			if !p.exists(sel) {
				t.Errorf("%s: no %s", path, sel)
			}
		}
	}
	rec := f.do("GET", "/static/ui.js", false)
	if rec.Code != 200 || rec.Body.Len() == 0 {
		t.Errorf("GET /static/ui.js = %d (%d bytes)", rec.Code, rec.Body.Len())
	}
}
