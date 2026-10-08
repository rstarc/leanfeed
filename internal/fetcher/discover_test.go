package fetcher

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestFeedLinks(t *testing.T) {
	base, _ := url.Parse("https://example.com/blog/")
	tests := []struct {
		name, html string
		want       []string
	}{
		{
			"RSS, Atom and JSON Feed in document order", `<html><head>
			<link rel="alternate" type="application/atom+xml" href="/atom.xml">
			<link rel="alternate" type="application/rss+xml" href="https://feeds.example/rss">
			<link rel="alternate" type="application/feed+json" href="feed.json">
			</head></html>`,
			[]string{"https://example.com/atom.xml", "https://feeds.example/rss", "https://example.com/blog/feed.json"},
		},
		{
			"rel and type ignore case, extra rel tokens and parameters",
			`<link rel="Alternate Feed" type="Application/RSS+XML; charset=utf-8" href="/rss">`,
			[]string{"https://example.com/rss"},
		},
		{
			"duplicates are listed once",
			`<link rel="alternate" type="application/rss+xml" href="/rss"><link rel="alternate" type="application/rss+xml" href="https://example.com/rss">`,
			[]string{"https://example.com/rss"},
		},
		{
			"other links are ignored", `
			<link rel="stylesheet" type="text/css" href="/style.css">
			<link rel="alternate" hreflang="de" href="/de/">
			<link rel="alternate" type="application/json" href="/wp-json/wp/v2/pages/2">
			<link rel="alternate" type="application/rss+xml">
			<a rel="alternate" type="application/rss+xml" href="/a-tag">`,
			nil,
		},
		{"not HTML", `{"not": "html"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := feedLinks([]byte(tt.html), base); !slices.Equal(got, tt.want) {
				t.Errorf("feedLinks = %q, want %q", got, tt.want)
			}
		})
	}
}

// discoveryServer serves a home page at /blog/ that links to a broken feed,
// then to a working one.
func discoveryServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/blog/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Blog</title>
			<link rel="alternate" type="application/rss+xml" href="/missing.xml">
			<link rel="alternate" type="application/rss+xml" href="feed.xml">
			</head><body>Hello</body></html>`))
	})
	mux.Handle("/blog/feed.xml", serveFixture(t, "rss2.xml"))
	mux.Handle("/home", http.RedirectHandler("/blog/", http.StatusFound))
	return httptest.NewServer(mux)
}

func TestSubscribeDiscoversFeedFromPage(t *testing.T) {
	srv := discoveryServer(t)
	defer srv.Close()
	s := newStore(t)

	// /home redirects to /blog/, so the relative link resolves against /blog/.
	feed, err := newFetcher(t, s).Subscribe(ctx, srv.URL+"/home", "")
	if err != nil {
		t.Fatal(err)
	}
	if feed.URL != srv.URL+"/blog/feed.xml" || feed.Title != "Example RSS" {
		t.Errorf("subscribed feed = %+v", feed)
	}
	if es := entries(t, s, feed.ID); len(es) != 3 {
		t.Errorf("stored %d entries, want 3", len(es))
	}
}

func TestChangeURLDiscoversFeedFromPage(t *testing.T) {
	srv := discoveryServer(t)
	defer srv.Close()
	s := newStore(t)
	old := addFeed(t, s, "https://old.example/feed.xml")

	feed, err := newFetcher(t, s).ChangeURL(ctx, old.ID, srv.URL+"/blog/")
	if err != nil {
		t.Fatal(err)
	}
	if feed.URL != srv.URL+"/blog/feed.xml" {
		t.Errorf("URL = %q, want the discovered feed", feed.URL)
	}
}

func TestSubscribePageWithoutWorkingFeedLinks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><link rel="alternate" type="application/rss+xml" href="/missing.xml"></head></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := newStore(t)

	_, err := newFetcher(t, s).Subscribe(ctx, srv.URL+"/", "")
	if err == nil || !strings.Contains(err.Error(), "links to feeds, but none could be read") || !strings.Contains(err.Error(), "404") {
		t.Errorf("Subscribe error = %v", err)
	}
	if feeds, _ := s.ListFeeds(ctx); len(feeds) != 0 {
		t.Errorf("failed Subscribe stored feeds: %+v", feeds)
	}
}
