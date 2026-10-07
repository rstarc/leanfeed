package web

// This file is a small stand-in for htmx in the browser. A page keeps the
// HTML as a tree; click and submit send the request that htmx would send
// and apply the response the way htmx would. It covers the htmx features
// leanfeed uses: hx-get, hx-post and hx-delete; inherited hx-target,
// hx-swap, hx-push-url and hx-confirm; innerHTML, outerHTML and none swaps;
// out-of-band swaps; and the responseHandling codes set in layout.html.
//
// It does not run JavaScript or apply CSS. The browser tests cover those.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// page is a browser tab showing leanfeed.
type page struct {
	t   *testing.T
	srv http.Handler
	doc *html.Node
	url string // path and query shown in the address bar

	// answerConfirm is the answer to hx-confirm prompts; prompts records them.
	answerConfirm bool
	prompts       []string
	// status is the HTTP status of the last request.
	status int
}

// open loads path as a full page, as typing it into the address bar does.
func (f *fixture) open(path string) *page {
	f.t.Helper()
	p := &page{t: f.t, srv: f.srv, answerConfirm: true}
	p.navigate(path)
	return p
}

// navigate loads a full page, following redirects.
func (p *page) navigate(path string) {
	p.t.Helper()
	for range 5 {
		rec := httptest.NewRecorder()
		p.srv.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		p.status = rec.Code
		if rec.Code == http.StatusFound || rec.Code == http.StatusSeeOther {
			path = rec.Header().Get("Location")
			continue
		}
		if rec.Code != http.StatusOK {
			p.t.Fatalf("GET %s: status %d", path, rec.Code)
		}
		p.doc = parseHTML(p.t, rec.Body.String())
		p.url = path
		return
	}
	p.t.Fatalf("GET %s: too many redirects", path)
}

// click clicks the element that sel matches.
func (p *page) click(sel string) {
	p.t.Helper()
	el := p.find(sel)
	if verb, _ := htmxVerb(el); verb != "" {
		p.request(el, closest(el, "form"))
		return
	}
	if el.DataAtom == atom.Button && attrOr(el, "type", "submit") == "submit" {
		if form := closest(el, "form"); form != nil {
			p.request(form, form)
			return
		}
	}
	if el.DataAtom == atom.A {
		if href, ok := attr(el, "href"); ok {
			p.navigate(href)
			return
		}
	}
	p.t.Fatalf("click %s: the element does nothing when clicked", sel)
}

// fill types value into the input that sel matches.
func (p *page) fill(sel, value string) {
	p.t.Helper()
	setAttr(p.find(sel), "value", value)
}

// htmxVerb returns the request method and URL an element sends, if any.
func htmxVerb(n *html.Node) (method, url string) {
	for _, m := range []string{"get", "post", "delete"} {
		if v, ok := attr(n, "hx-"+m); ok {
			return strings.ToUpper(m), v
		}
	}
	return "", ""
}

// inherited returns the value of an htmx attribute on n or its closest
// ancestor that sets it.
func inherited(n *html.Node, key string) string {
	for ; n != nil; n = n.Parent {
		if v, ok := attr(n, key); ok {
			return v
		}
	}
	return ""
}

// request sends el's htmx request, with the values of form if it is not
// nil, and applies the response.
func (p *page) request(el, form *html.Node) {
	p.t.Helper()
	method, target := htmxVerb(el)
	if prompt := inherited(el, "hx-confirm"); prompt != "" {
		p.prompts = append(p.prompts, prompt)
		if !p.answerConfirm {
			return
		}
	}

	values := url.Values{}
	if form != nil && (method != "GET" || form == el) {
		values = formValues(form)
	}
	var req *http.Request
	if method == "GET" || method == "DELETE" {
		if len(values) > 0 {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + values.Encode()
		}
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://example.com"+p.url)

	swap := inherited(el, "hx-swap")
	if swap == "" {
		swap = "innerHTML"
	}
	var swapTarget *html.Node
	if swap != "none" {
		swapTarget = el
		if sel := inherited(el, "hx-target"); sel != "" {
			swapTarget = byID(p.doc, strings.TrimPrefix(sel, "#"))
			if swapTarget == nil {
				// htmx stops here; the click does nothing.
				p.t.Fatalf("%s %s: htmx:targetError, no element %s on the page", method, target, sel)
			}
		}
		if id, ok := attr(swapTarget, "id"); ok {
			req.Header.Set("HX-Target", id)
		}
	}

	rec := httptest.NewRecorder()
	p.srv.ServeHTTP(rec, req)
	p.status = rec.Code
	if !swapsResponse(rec.Code) {
		return
	}
	if inherited(el, "hx-push-url") == "true" {
		p.url = target
	}
	p.swap(rec.Body.String(), swapTarget, swap)
}

// swapsResponse mirrors htmx.config.responseHandling in layout.html.
func swapsResponse(code int) bool {
	switch {
	case code == http.StatusNoContent:
		return false
	case code >= 200 && code < 400:
		return true
	default:
		return code == 400 || code == 409 || code == 413 || code == 422
	}
}

// swap applies a response: out-of-band elements replace the elements with
// their id, then the rest goes into target.
func (p *page) swap(body string, target *html.Node, style string) {
	p.t.Helper()
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(body), ctx)
	if err != nil {
		p.t.Fatal(err)
	}
	var main []*html.Node
	for _, n := range nodes {
		if _, oob := attr(n, "hx-swap-oob"); oob && n.Type == html.ElementNode {
			id, _ := attr(n, "id")
			// htmx ignores out-of-band elements whose target is missing.
			if old := byID(p.doc, id); old != nil {
				old.Parent.InsertBefore(n, old)
				old.Parent.RemoveChild(old)
			}
			continue
		}
		main = append(main, n)
	}
	switch style {
	case "none":
	case "innerHTML":
		for c := target.FirstChild; c != nil; c = target.FirstChild {
			target.RemoveChild(c)
		}
		for _, n := range main {
			target.AppendChild(n)
		}
	case "outerHTML":
		for _, n := range main {
			target.Parent.InsertBefore(n, target)
		}
		target.Parent.RemoveChild(target)
	default:
		p.t.Fatalf("the harness does not support hx-swap=%q", style)
	}
}

// formValues collects the named inputs of a form, as a browser submits them.
func formValues(form *html.Node) url.Values {
	v := url.Values{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.DataAtom == atom.Input || n.DataAtom == atom.Textarea || n.DataAtom == atom.Select) {
			name, ok := attr(n, "name")
			if ok && attrOr(n, "type", "text") != "file" {
				v.Add(name, attrOr(n, "value", ""))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(form)
	return v
}

func attrOr(n *html.Node, key, def string) string {
	if v, ok := attr(n, key); ok {
		return v
	}
	return def
}

func setAttr(n *html.Node, key, value string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: value})
}

func closest(n *html.Node, tag string) *html.Node {
	for ; n != nil; n = n.Parent {
		if n.Type == html.ElementNode && n.Data == tag {
			return n
		}
	}
	return nil
}

// Queries

// find returns the single element sel matches, failing if there is none
// or more than one.
func (p *page) find(sel string) *html.Node {
	p.t.Helper()
	found := p.all(sel)
	if len(found) != 1 {
		p.t.Fatalf("%q matches %d elements, want 1 (page %s)", sel, len(found), p.url)
	}
	return found[0]
}

func (p *page) all(sel string) []*html.Node { return querySelectorAll(p.doc, sel) }

func (p *page) exists(sel string) bool { return len(p.all(sel)) > 0 }

// text returns the whitespace-normalized text of the element sel matches.
func (p *page) text(sel string) string {
	p.t.Helper()
	return textOf(p.find(sel))
}

// texts returns the text of every element sel matches.
func (p *page) texts(sel string) []string {
	var out []string
	for _, n := range p.all(sel) {
		out = append(out, textOf(n))
	}
	return out
}

// hasClass reports whether the element sel matches has class c.
func (p *page) hasClass(sel, c string) bool {
	p.t.Helper()
	return hasClass(p.find(sel), c)
}

func hasClass(n *html.Node, c string) bool {
	return strings.Contains(" "+attrOr(n, "class", "")+" ", " "+c+" ")
}

// A tiny CSS selector engine: descendant combinators of compound selectors
// made of a tag, #id, .class and [attr] or [attr="value"] parts.

type compound struct {
	tag, id string
	classes []string
	attrs   [][2]string // name, value; value "" with present-only match
	hasVal  []bool
}

var selectorPart = regexp.MustCompile(`^(?:[a-zA-Z][a-zA-Z0-9]*|\*|#[^.#\[]+|\.[^.#\[]+|\[[^\]]+\])`)

func parseCompound(s string) compound {
	var c compound
	for len(s) > 0 {
		m := selectorPart.FindString(s)
		if m == "" {
			panic("unsupported selector part: " + s)
		}
		switch m[0] {
		case '#':
			c.id = m[1:]
		case '.':
			c.classes = append(c.classes, m[1:])
		case '[':
			inner := m[1 : len(m)-1]
			name, val, ok := strings.Cut(inner, "=")
			c.attrs = append(c.attrs, [2]string{name, strings.Trim(val, `"'`)})
			c.hasVal = append(c.hasVal, ok)
		default:
			if m != "*" {
				c.tag = m
			}
		}
		s = s[len(m):]
	}
	return c
}

func (c compound) matches(n *html.Node) bool {
	if n.Type != html.ElementNode || (c.tag != "" && n.Data != c.tag) {
		return false
	}
	if c.id != "" && attrOr(n, "id", "") != c.id {
		return false
	}
	for _, cl := range c.classes {
		if !hasClass(n, cl) {
			return false
		}
	}
	for i, a := range c.attrs {
		v, ok := attr(n, a[0])
		if !ok || (c.hasVal[i] && v != a[1]) {
			return false
		}
	}
	return true
}

// splitSelector splits on spaces outside brackets.
func splitSelector(sel string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range sel {
		switch {
		case r == '[':
			depth++
		case r == ']':
			depth--
		case r == ' ' && depth == 0:
			if i > start {
				parts = append(parts, sel[start:i])
			}
			start = i + 1
		}
	}
	if start < len(sel) {
		parts = append(parts, sel[start:])
	}
	return parts
}

func querySelectorAll(root *html.Node, sel string) []*html.Node {
	var chain []compound
	for _, part := range splitSelector(sel) {
		chain = append(chain, parseCompound(part))
	}
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if chain[len(chain)-1].matches(n) && ancestorsMatch(n.Parent, chain[:len(chain)-1]) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// ancestorsMatch reports whether the compounds in chain match ancestors of
// n in order, innermost last.
func ancestorsMatch(n *html.Node, chain []compound) bool {
	if len(chain) == 0 {
		return true
	}
	for ; n != nil; n = n.Parent {
		if chain[len(chain)-1].matches(n) {
			return ancestorsMatch(n.Parent, chain[:len(chain)-1])
		}
	}
	return false
}
