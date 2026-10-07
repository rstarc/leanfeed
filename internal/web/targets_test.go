package web

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// htmxTarget returns the hx-target that applies to n, inherited from its
// ancestors as htmx does, or "" if none is set.
func htmxTarget(n *html.Node) string {
	for ; n != nil; n = n.Parent {
		if v, ok := attr(n, "hx-target"); ok {
			return v
		}
	}
	return ""
}

// TestHTMXTargetsExist checks that every htmx request on each full page
// swaps into an element that exists on that page. htmx cancels a link's
// normal navigation before it looks up the target, so a missing target
// makes the link do nothing.
func TestHTMXTargetsExist(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/entries?view=all", "/entries/" + f.ids["Blog new"] + "?view=all", "/feeds"} {
		doc := parseHTML(t, f.do("GET", path, false).Body.String())
		var walk func(*html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode {
				for _, verb := range []string{"hx-get", "hx-post", "hx-delete"} {
					if _, ok := attr(n, verb); !ok {
						continue
					}
					target := htmxTarget(n)
					if id, ok := strings.CutPrefix(target, "#"); ok && byID(doc, id) == nil {
						url, _ := attr(n, verb)
						t.Errorf("GET %s: %s %s targets %s, which is not on the page", path, verb, url, target)
					}
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(doc)
	}
}

// TestListPartialClosesEntry checks that switching lists with htmx also
// empties the entry pane, so narrow screens, which hide the list while an
// entry is open, show the new list.
func TestListPartialClosesEntry(t *testing.T) {
	f := newFixture(t)
	body := f.do("GET", "/entries?view=starred", true).Body.String()
	doc := parseHTML(t, body)
	if byID(doc, "list") == nil {
		t.Fatalf("partial has no #list:\n%s", body)
	}
	pane := byID(doc, "entry-pane")
	if pane == nil {
		t.Fatalf("partial has no #entry-pane:\n%s", body)
	}
	if pane.FirstChild != nil {
		t.Errorf("entry pane is not empty:\n%s", body)
	}
}
