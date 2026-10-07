package fetcher

import (
	"bytes"
	"mime"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// maxFeedLinks caps how many links from one page are tried as feeds.
const maxFeedLinks = 5

// feedTypes are the link types that announce a feed. application/json is
// left out because WordPress uses it for its REST API on every page.
var feedTypes = []string{"application/rss+xml", "application/atom+xml", "application/feed+json"}

// feedLinks returns the feeds an HTML page announces with
// <link rel="alternate" type="..." href="...">, in document order and
// resolved against base.
func feedLinks(page []byte, base *url.URL) []string {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil
	}
	var links []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Link {
			if link := feedLink(n, base); link != "" && !slices.Contains(links, link) {
				links = append(links, link)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return links
}

// feedLink returns the absolute feed URL of a <link> element, or "" if
// the element does not announce a feed.
func feedLink(n *html.Node, base *url.URL) string {
	var rel, typ, href string
	for _, a := range n.Attr {
		switch a.Key {
		case "rel":
			rel = a.Val
		case "type":
			typ = a.Val
		case "href":
			href = strings.TrimSpace(a.Val)
		}
	}
	mediaType, _, _ := mime.ParseMediaType(typ)
	if href == "" || !slices.Contains(strings.Fields(strings.ToLower(rel)), "alternate") || !slices.Contains(feedTypes, mediaType) {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}
