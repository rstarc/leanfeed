package web

// Browser tests drive headless Chrome through the main user journeys. They
// run real htmx, CSS and the Content Security Policy, which the simulated
// flows in flows_test.go cannot. They are skipped with -short and when no
// Chrome is found; `nix develop` provides one and sets LEANFEED_CHROME.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"leanfeed/internal/fetcher"
	"leanfeed/internal/store/filestore"
)

const problemPrefix = "leanfeed-problem: "

// reportProblems runs before every page's own scripts. It reports CSP
// violations, uncaught errors and htmx errors to the test through the
// console. htmx:oobErrorNoTarget is expected: entry actions also update
// the entry view, which is not always open.
const reportProblems = `(() => {
  const report = msg => console.error(` + "`" + problemPrefix + "${msg}`" + `);
  document.addEventListener('securitypolicyviolation', e => report('CSP ' + e.violatedDirective + ' blocked ' + e.blockedURI));
  window.addEventListener('error', e => report('JS error: ' + e.message));
  for (const name of ['htmx:targetError', 'htmx:swapError', 'htmx:sendError', 'htmx:responseError', 'htmx:evalDisallowedError']) {
    document.addEventListener(name, () => report(name));
  }
})();`

func findChrome() string {
	if p := os.Getenv("LEANFEED_CHROME"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	mac := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(mac); err == nil {
		return mac
	}
	return ""
}

type browser struct {
	t       *testing.T
	ctx     context.Context
	base    string           // URL of the leanfeed server
	feeds   string           // URL of the fixture feed server
	fetcher *fetcher.Fetcher // subscribes to fixture feeds for setup
	accept  atomic.Bool      // answer to confirm dialogs

	mu       sync.Mutex
	problems []string
	dialogs  []string
}

// newBrowser starts leanfeed with a real store and fetcher, a server for
// the fixture feeds, and headless Chrome with the given window size.
func newBrowser(t *testing.T, width, height int) *browser {
	t.Helper()
	if testing.Short() {
		t.Skip("browser tests are slow")
	}
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome found; set LEANFEED_CHROME or run the tests in `nix develop`")
	}

	feeds := httptest.NewServer(http.FileServer(http.Dir("../../testdata/feeds")))
	t.Cleanup(feeds.Close)
	st, err := filestore.Open(t.TempDir(), quietLog)
	must(t, err)
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	})
	f := fetcher.New(st, fetcher.Config{UserAgent: "leanfeed/test"}, quietLog)
	srv, err := New(st, f, quietLog)
	must(t, err)
	app := httptest.NewServer(srv)
	t.Cleanup(app.Close)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.WindowSize(width, height),
		chromedp.UserDataDir(t.TempDir()),
		// Feed content links to images on other hosts; never contact them.
		chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1"),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(func() { cancelCtx(); cancelAlloc() })

	b := &browser{t: t, ctx: ctx, base: app.URL, feeds: feeds.URL, fetcher: f}
	b.accept.Store(true)
	console := chromedp.Events(ctx, runtime.ConsoleAPICalled)
	exceptions := chromedp.Events(ctx, runtime.ExceptionThrown)
	dialogs := chromedp.Events(ctx, cdppage.JavascriptDialogOpening)
	b.do(chromedp.Navigate("about:blank"))
	if _, err := chromedp.Call(ctx, cdppage.AddScriptToEvaluateOnNewDocument, cdppage.AddScriptToEvaluateOnNewDocumentParams{Source: reportProblems}); err != nil {
		t.Fatal(err)
	}

	go func() {
		for ev, err := range console {
			if err != nil {
				return
			}
			for _, arg := range ev.Args {
				var s string
				if json.Unmarshal(arg.Value, &s) == nil && strings.HasPrefix(s, problemPrefix) {
					b.addProblem(strings.TrimPrefix(s, problemPrefix))
				}
			}
		}
	}()
	go func() {
		for ev, err := range exceptions {
			if err != nil {
				return
			}
			b.addProblem("uncaught exception: " + ev.ExceptionDetails.Text)
		}
	}()
	go func() {
		for ev, err := range dialogs {
			if err != nil {
				return
			}
			b.mu.Lock()
			b.dialogs = append(b.dialogs, ev.Message)
			b.mu.Unlock()
			// A failure leaves the dialog open, and the test waiting on it fails.
			_, _ = chromedp.Call(ctx, cdppage.HandleJavaScriptDialog, cdppage.HandleJavaScriptDialogParams{Accept: b.accept.Load()})
		}
	}()
	t.Cleanup(b.checkNoProblems)
	return b
}

func (b *browser) addProblem(p string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.problems = append(b.problems, p)
}

// checkNoProblems fails the test if the page reported CSP violations,
// script errors or htmx errors.
func (b *browser) checkNoProblems() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.problems {
		b.t.Errorf("browser reported: %s", p)
	}
}

func (b *browser) do(steps ...chromedp.Action[chromedp.Void]) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
	defer cancel()
	if err := chromedp.Do(ctx, steps...); err != nil {
		b.t.Fatal(err)
	}
}

// eval returns the value of a JavaScript expression.
func eval[T any](b *browser, expr string) T {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
	defer cancel()
	v, err := chromedp.Run(ctx, chromedp.Evaluate[T](expr))
	if err != nil {
		b.t.Fatalf("evaluating %s: %v", expr, err)
	}
	return v
}

// htmxIdle is true when htmx has no request in flight and has settled all
// swapped content. htmx attaches its handlers to new content only when it
// settles, about 20 ms after the swap; a click before that is a plain
// click, which a person is never fast enough to make.
const htmxIdle = `!document.querySelector('.htmx-request, .htmx-swapping, .htmx-settling')`

// waitUntil waits until a JavaScript expression is true and htmx is idle.
// While a click loads a new page, the old page's JavaScript context
// disappears; the poll then starts again in the new page.
func (b *browser) waitUntil(what, expr string) {
	b.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithDeadline(b.ctx, deadline)
		_, err := chromedp.Run(ctx, chromedp.Poll[bool]("("+expr+") && "+htmxIdle))
		cancel()
		if err == nil {
			return
		}
		if !strings.Contains(err.Error(), "Cannot find context") || time.Now().After(deadline) {
			b.t.Fatalf("waiting for %s: %v", what, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// open loads a page and waits until htmx is ready.
func (b *browser) open(path string) {
	b.t.Helper()
	b.do(chromedp.Navigate(b.base + path))
	b.waitReady()
}

func (b *browser) waitReady() {
	b.t.Helper()
	b.waitUntil("htmx to load", `document.readyState === 'complete' && window.htmx !== undefined`)
}

func (b *browser) click(sel string) {
	b.t.Helper()
	b.do(chromedp.Click(sel))
}

// waitText waits until the element sel matches contains want.
func (b *browser) waitText(sel, want string) {
	b.t.Helper()
	b.waitUntil(fmt.Sprintf("%s to contain %q", sel, want),
		fmt.Sprintf(`(document.querySelector(%s)?.textContent || '').includes(%s)`, js(sel), js(want)))
}

func (b *browser) count(sel string) int {
	b.t.Helper()
	return eval[int](b, fmt.Sprintf(`document.querySelectorAll(%s).length`, js(sel)))
}

func (b *browser) visible(sel string) bool {
	b.t.Helper()
	return eval[bool](b, fmt.Sprintf(`!!document.querySelector(%s)?.checkVisibility()`, js(sel)))
}

func (b *browser) path() string {
	b.t.Helper()
	return eval[string](b, `location.pathname + location.search`)
}

// wantHighlighted checks that the menu highlights only the link with id.
func (b *browser) wantHighlighted(id string) {
	b.t.Helper()
	got := eval[[]string](b, `[...document.querySelectorAll('#sidebar a[aria-current="page"]')].map(a => a.id || a.getAttribute('href'))`)
	if len(got) != 1 || got[0] != id {
		b.t.Errorf("the menu highlights %q, want only %q", got, id)
	}
}

// subscribe adds a fixture feed directly, for tests that start with data.
func (b *browser) subscribe(fixture string) {
	b.t.Helper()
	if _, err := b.fetcher.Subscribe(context.Background(), b.feeds+"/"+fixture, "Tech"); err != nil {
		b.t.Fatal(err)
	}
}

func TestBrowserAddFeedOpenAndStar(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.open("/")
	b.waitText("#list .empty", "No entries.")

	b.click(`a[href="/feeds"]`)
	b.waitReady()
	b.do(
		chromedp.SendKeys(`form[hx-post="/feeds"] input[name="url"]`, b.feeds+"/rss2.xml"),
		chromedp.SendKeys(`form[hx-post="/feeds"] input[name="folder"]`, "Tech"),
	)
	b.click(`form[hx-post="/feeds"] button`)
	b.waitText("#notice", "Added Example RSS.")

	// Leaving the feeds page through the sidebar.
	b.click("#view-unread")
	b.waitText("#list h1", "Unread")
	b.wantHighlighted("view-unread")
	if got := b.count("#list .row.unread"); got != 3 {
		t.Fatalf("unread rows = %d, want 3", got)
	}
	if got := b.path(); got != "/entries?view=unread" {
		t.Errorf("address bar = %s", got)
	}

	rowID := eval[string](b, `document.querySelector('#list .row').id`)
	entryID := strings.TrimPrefix(rowID, "entry-")
	b.click("#" + rowID + " a.entry-title")
	b.waitText("#entry h1", "Third post")
	b.waitUntil("the row to show as read", fmt.Sprintf(`!document.getElementById(%s).classList.contains('unread')`, js(rowID)))
	b.waitText("#view-unread .count", "2")
	if got := b.path(); !strings.HasPrefix(got, "/entries/"+entryID) {
		t.Errorf("address bar = %s, want the entry", got)
	}
	if b.count("#entry .content script") != 0 || b.count(`#entry .content a[target="_blank"]`) == 0 {
		t.Error("entry content is not sanitized as expected")
	}

	b.click(fmt.Sprintf(`#entry-actions-%s [hx-post="/entries/%s/star"]`, entryID, entryID))
	b.waitText("#entry-actions-"+entryID, "Starred")
	b.waitUntil("the row to show the star", fmt.Sprintf(`document.getElementById(%s).classList.contains('starred')`, js(rowID)))
}

func TestBrowserMarkAllReadAsksFirst(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.subscribe("rss2.xml")
	b.open("/entries?view=unread")
	if got := b.count("#list .row"); got != 3 {
		t.Fatalf("rows = %d, want 3", got)
	}

	b.accept.Store(false)
	b.click("#list .list-header button")
	time.Sleep(300 * time.Millisecond) // a declined prompt sends nothing to wait for
	if got := b.count("#list .row"); got != 3 {
		t.Errorf("rows after declining = %d, want 3", got)
	}

	b.accept.Store(true)
	b.click("#list .list-header button")
	b.waitText("#list .empty", "No entries.")
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.dialogs) != 2 || b.dialogs[0] != "Mark all entries in this list as read?" {
		t.Errorf("dialogs = %q", b.dialogs)
	}
}

func TestBrowserNarrowScreenShowsOnePaneAtATime(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.do(chromedp.EmulateViewport(390, 800)) // windows cannot be narrower than 500 px
	b.subscribe("rss2.xml")
	b.open("/entries?view=all")
	if !b.visible("#list") || b.visible("#sidebar .views") {
		t.Errorf("list visible %v, sidebar menu visible %v; want only the list", b.visible("#list"), b.visible("#sidebar .views"))
	}

	b.click("#list .row a.entry-title")
	b.waitText("#entry h1", "Third post")
	if b.visible("#list") || !b.visible("#entry") {
		t.Errorf("list visible %v, entry visible %v; want only the entry", b.visible("#list"), b.visible("#entry"))
	}

	b.click("#entry a.back")
	b.waitReady()
	if !b.visible("#list") || b.visible("#entry") {
		t.Errorf("after Back: list visible %v, entry visible %v", b.visible("#list"), b.visible("#entry"))
	}
}

func TestBrowserBackButtonRestoresList(t *testing.T) {
	b := newBrowser(t, 1280, 800)
	b.subscribe("rss2.xml")
	b.open("/entries?view=unread")
	b.click("#view-starred")
	b.waitText("#list h1", "Starred")
	b.wantHighlighted("view-starred")

	b.do(chromedp.NavigateBack())
	b.waitText("#list h1", "Unread")
	b.wantHighlighted("view-unread")
	if got := b.count("#list .row"); got != 3 {
		t.Errorf("rows after Back = %d, want 3", got)
	}
}
