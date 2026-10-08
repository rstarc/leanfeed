package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
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

// styleOf returns a computed style property of the element sel matches.
func (b *browser) styleOf(sel, property string) string {
	b.t.Helper()
	return eval[string](b, fmt.Sprintf(`getComputedStyle(document.querySelector(%s))[%s]`, js(sel), js(property)))
}

func (b *browser) wantStyle(sel, property, want string) {
	b.t.Helper()
	if got := b.styleOf(sel, property); got != want {
		b.t.Errorf("%s %s = %s, want %s", sel, property, got, want)
	}
}

func (b *browser) theme() string {
	b.t.Helper()
	return eval[string](b, `document.documentElement.dataset.theme`)
}

// waitFontLoaded waits until the browser has loaded the bundled font family.
func (b *browser) waitFontLoaded(family string) {
	b.t.Helper()
	b.waitUntil(family+" to load", fmt.Sprintf(
		`[...document.fonts].some(f => f.family.replaceAll('"', '') === %s && f.status === 'loaded')`, js(family)))
}

func TestBrowserSepiaTheme(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.subscribe("rss2.xml")
	b.open("/appearance")
	if got := b.theme(); got != "default" {
		t.Errorf("theme before choosing = %q, want default", got)
	}
	if page, sidebar := b.rect("#appearance"), b.rect("#sidebar"); !near(page.Left, sidebar.Right+5) || !near(page.Right, 1280) {
		t.Errorf("appearance page spans %v to %v px, want %v to 1280", page.Left, page.Right, sidebar.Right+5)
	}

	b.click(`#appearance input[value="sepia"]`)
	b.waitUntil("the sepia theme", `document.documentElement.dataset.theme === 'sepia'`)
	b.wantStyle("body", "backgroundColor", "rgb(244, 236, 216)")
	b.waitFontLoaded("Lora")
	// Each choice previews its own theme, whichever theme is chosen.
	b.wantStyle(`#appearance .theme[data-theme="default"]`, "backgroundColor", "rgb(253, 253, 252)")
	b.wantStyle(`#appearance .theme[data-theme="sepia"]`, "backgroundColor", "rgb(244, 236, 216)")

	// The choice survives a reload and applies to every page.
	b.reload()
	if b.theme() != "sepia" || !eval[bool](b, `document.querySelector('#appearance input[value="sepia"]').checked`) {
		t.Errorf("after a reload the theme is %q and sepia is not checked", b.theme())
	}
	b.open("/entries?view=all")
	if got := b.theme(); got != "sepia" {
		t.Errorf("theme on the entry list = %q, want sepia", got)
	}
	b.click("#list .row a.entry-title")
	b.waitText("#entry h1", "Third post")
	accent := "rgb(70, 96, 74)"
	b.wantStyle(`#sidebar a[aria-current="page"]`, "color", accent)
	b.wantStyle("#list .row[aria-current] .entry-title", "color", accent)
	b.wantStyle("#entry .content a", "color", accent)
	eval[bool](b, `(document.querySelector('#entry .content').insertAdjacentHTML('beforeend', '<pre><code>x := 1</code></pre>'), true)`)
	b.waitFontLoaded("Fira Code")

	// The theme follows the system's dark mode.
	if _, err := chromedp.Call(b.ctx, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
		Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "dark"}},
	}); err != nil {
		t.Fatal(err)
	}
	b.wantStyle("body", "backgroundColor", "rgb(34, 28, 21)")
	b.wantStyle(`#sidebar a[aria-current="page"]`, "color", "rgb(163, 184, 154)")

	b.open("/appearance")
	b.click(`#appearance input[value="default"]`)
	b.waitUntil("the default theme", `document.documentElement.dataset.theme === 'default'`)
	b.wantStyle("body", "backgroundColor", "rgb(24, 24, 26)")
}
