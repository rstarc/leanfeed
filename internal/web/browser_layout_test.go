package web

import (
	"fmt"
	"math"
	"testing"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

const (
	sidebarSplitter = `.splitter[data-resize="sidebar"]`
	listSplitter    = `.splitter[data-resize="list"]`
)

type rect struct{ Left, Right, Width float64 }

func (b *browser) rect(sel string) rect {
	b.t.Helper()
	return eval[rect](b, fmt.Sprintf(`(() => { const r = document.querySelector(%s).getBoundingClientRect(); return {Left: r.left, Right: r.right, Width: r.width} })()`, js(sel)))
}

// drag presses the mouse on the middle of sel, moves it dx pixels and
// releases it.
func (b *browser) drag(sel string, dx float64) {
	b.t.Helper()
	r := b.rect(sel)
	x := r.Left + r.Width/2
	y := 300.0
	b.do(
		chromedp.MouseEvent(input.DispatchMouseEventTypeMouseMoved, x, y),
		chromedp.MouseEvent(input.DispatchMouseEventTypeMousePressed, x, y, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent(input.DispatchMouseEventTypeMouseMoved, x+dx/2, y, chromedp.ButtonLeft),
		chromedp.MouseEvent(input.DispatchMouseEventTypeMouseMoved, x+dx, y, chromedp.ButtonLeft),
		chromedp.MouseEvent(input.DispatchMouseEventTypeMouseReleased, x+dx, y, chromedp.ButtonLeft, chromedp.ClickCount(1)),
	)
}

func (b *browser) doubleClick(sel string) {
	b.t.Helper()
	r := b.rect(sel)
	x := r.Left + r.Width/2
	b.do(
		chromedp.MouseEvent(input.DispatchMouseEventTypeMousePressed, x, 300, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent(input.DispatchMouseEventTypeMouseReleased, x, 300, chromedp.ButtonLeft, chromedp.ClickCount(1)),
		chromedp.MouseEvent(input.DispatchMouseEventTypeMousePressed, x, 300, chromedp.ButtonLeft, chromedp.ClickCount(2)),
		chromedp.MouseEvent(input.DispatchMouseEventTypeMouseReleased, x, 300, chromedp.ButtonLeft, chromedp.ClickCount(2)),
	)
}

func (b *browser) reload() {
	b.t.Helper()
	b.do(chromedp.Reload())
	b.waitReady()
}

func near(got, want float64) bool { return math.Abs(got-want) <= 2 }

func TestBrowserResizeColumns(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.subscribe("rss2.xml")
	b.open("/entries?view=all")
	b.click("#list .row a.entry-title")
	b.waitText("#entry h1", "Third post")

	sidebar, list := b.rect("#sidebar").Width, b.rect("#list").Width
	b.drag(sidebarSplitter, 100)
	if got := b.rect("#sidebar").Width; !near(got, sidebar+100) {
		t.Errorf("sidebar width after dragging 100 px = %v, want %v", got, sidebar+100)
	}
	b.drag(listSplitter, -80)
	if got := b.rect("#list").Width; !near(got, list-80) {
		t.Errorf("list width after dragging -80 px = %v, want %v", got, list-80)
	}
	if got := b.rect("#entry-pane").Right; !near(got, 1280) {
		t.Errorf("entry pane ends at %v, want it to fill the window to 1280", got)
	}

	b.reload()
	if got := b.rect("#sidebar").Width; !near(got, sidebar+100) {
		t.Errorf("sidebar width after reload = %v, want %v", got, sidebar+100)
	}
	if got := b.rect("#list").Width; !near(got, list-80) {
		t.Errorf("list width after reload = %v, want %v", got, list-80)
	}

	b.doubleClick(sidebarSplitter)
	if got := b.rect("#sidebar").Width; !near(got, sidebar) {
		t.Errorf("sidebar width after double-click = %v, want the default %v", got, sidebar)
	}
}

func TestBrowserResizeLimits(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.open("/entries")
	b.drag(sidebarSplitter, -1000)
	if got := b.rect("#sidebar").Width; got < 150 {
		t.Errorf("sidebar shrank to %v px, want at least 150", got)
	}
	b.drag(listSplitter, 2000)
	if got := b.rect("#entry-pane").Width; got < 250 {
		t.Errorf("entry pane shrank to %v px, want at least 250", got)
	}
}

func TestBrowserResizeWithKeyboard(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.open("/entries")
	before := b.rect("#sidebar").Width
	b.do(chromedp.Focus(sidebarSplitter), chromedp.KeyEvent(kb.ArrowRight), chromedp.KeyEvent(kb.ArrowRight))
	if got := b.rect("#sidebar").Width; !near(got, before+32) {
		t.Errorf("sidebar width after two right arrows = %v, want %v", got, before+32)
	}
}

func TestBrowserHideAndShowMenu(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.subscribe("rss2.xml")
	b.open("/entries?view=all")
	if b.visible("button.menu-show") {
		t.Error("the Show menu button is visible while the menu is shown")
	}

	b.click(`#sidebar [data-menu-toggle]`)
	if b.visible("#sidebar") || b.visible(sidebarSplitter) {
		t.Error("the menu is still visible after hiding it")
	}
	if got := b.rect("#list").Left; got > 2 {
		t.Errorf("list starts at %v px, want it to move to the left edge", got)
	}
	if !b.visible("button.menu-show") {
		t.Fatal("no Show menu button while the menu is hidden")
	}

	// The choice survives a reload and htmx updates of the sidebar.
	b.reload()
	if b.visible("#sidebar") {
		t.Error("the menu is visible again after a reload")
	}
	b.click("#list .row button.star")
	b.waitUntil("the star", `document.querySelector('#list .row').classList.contains('starred')`)
	if b.visible("#sidebar") {
		t.Error("the menu is visible again after the sidebar was updated")
	}

	b.click("button.menu-show")
	if !b.visible("#sidebar") || b.visible("button.menu-show") {
		t.Error("Show menu did not bring the menu back")
	}
}

func TestBrowserNarrowScreenHasNoColumnControls(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.do(chromedp.EmulateViewport(390, 800))
	b.open("/entries")
	for _, sel := range []string{sidebarSplitter, listSplitter, "button.menu-show", `#sidebar [data-menu-toggle]`} {
		if b.visible(sel) {
			t.Errorf("%s is visible on a narrow screen", sel)
		}
	}
}

func TestBrowserFeedsPageFillsTheWindow(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.open("/feeds")
	sidebar := b.rect("#sidebar")
	manage := b.rect("#manage")
	if !near(manage.Left, sidebar.Right+5) || !near(manage.Right, 1280) {
		t.Errorf("feeds page spans %v to %v px, want %v to 1280", manage.Left, manage.Right, sidebar.Right+5)
	}
	b.click(`#sidebar [data-menu-toggle]`)
	if manage := b.rect("#manage"); !near(manage.Left, 0) || !near(manage.Right, 1280) {
		t.Errorf("with the menu hidden, feeds page spans %v to %v px, want 0 to 1280", manage.Left, manage.Right)
	}
}
