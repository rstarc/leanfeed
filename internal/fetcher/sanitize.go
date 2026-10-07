package fetcher

import (
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// policy is based on UGCPolicy: no scripts, styles, iframes, forms or event
// handlers. Links open in a new tab without opener or referrer. Images stay
// and load directly from their origin.
var policy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.RequireNoFollowOnLinks(false)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

// Sanitize resolves relative URLs in content against base, then removes
// everything the policy does not allow.
func Sanitize(content, base string) string {
	return policy.Sanitize(resolveURLs(content, base))
}

// resolveURLs rewrites relative href and src attributes to absolute URLs.
// It returns content unchanged if base is not an absolute URL or the
// content cannot be parsed.
func resolveURLs(content, base string) string {
	baseURL, err := url.Parse(base)
	if err != nil || !baseURL.IsAbs() {
		return content
	}
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(content), ctx)
	if err != nil {
		return content
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for i, a := range n.Attr {
				if a.Key != "href" && a.Key != "src" {
					continue
				}
				if ref, err := url.Parse(strings.TrimSpace(a.Val)); err == nil {
					n.Attr[i].Val = baseURL.ResolveReference(ref).String()
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	var b strings.Builder
	for _, n := range nodes {
		walk(n)
		if err := html.Render(&b, n); err != nil {
			return content
		}
	}
	return b.String()
}
