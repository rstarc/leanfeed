package web

import (
	"fmt"
	"strings"
	"testing"
)

// fontOf returns the computed font-family of the element sel matches.
func (b *browser) fontOf(sel string) string {
	b.t.Helper()
	return eval[string](b, fmt.Sprintf(`getComputedStyle(document.querySelector(%s)).fontFamily`, js(sel)))
}

// TestBrowserFontRoles checks that every element takes its font from the
// custom property for its role, so a theme can set each role on its own.
func TestBrowserFontRoles(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.subscribe("rss2.xml")
	b.open("/entries?view=all")
	b.click("#list .row a.entry-title")
	b.waitText("#entry h1", "Third post")

	// Feed content can contain headings and code; the fixtures have none.
	eval[bool](b, `(() => {
		document.querySelector('#entry .content').insertAdjacentHTML('beforeend',
			'<h2>Heading</h2><p><code>inline</code></p><pre><code>block</code></pre>');
		const style = document.documentElement.style;
		style.setProperty('--font-text', 'RoleText');
		style.setProperty('--font-title', 'RoleTitle');
		style.setProperty('--font-heading', 'RoleHeading');
		style.setProperty('--font-code', 'RoleCode');
		return true;
	})()`)

	wantRole := func(sel, role string) {
		t.Helper()
		if got := b.fontOf(sel); !strings.Contains(got, role) {
			t.Errorf("%s uses font %q, want %s", sel, got, role)
		}
	}
	wantRole("body", "RoleText")
	wantRole("#sidebar .views a", "RoleText")
	wantRole("#entry .content p", "RoleText")
	wantRole("#entry .entry-actions button", "RoleText")
	wantRole("#sidebar .brand", "RoleTitle")
	wantRole("#list .entry-title", "RoleTitle")
	wantRole("#entry h1", "RoleTitle")
	wantRole("#sidebar .nav-section h2", "RoleHeading")
	wantRole("#list h1", "RoleHeading")
	wantRole("#entry .content h2", "RoleHeading")
	wantRole("#entry .content p code", "RoleCode")
	wantRole("#entry .content pre", "RoleCode")

	b.open("/feeds")
	eval[bool](b, `(document.documentElement.style.setProperty('--font-heading', 'RoleHeading'), true)`)
	wantRole("#manage h1", "RoleHeading")
	wantRole("#manage h2", "RoleHeading")
}
