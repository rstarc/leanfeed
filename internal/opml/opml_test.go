package opml

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head><title>Subscriptions</title></head>
  <body>
    <outline text="Tech" title="Tech">
      <outline type="rss" text="Example Blog" xmlUrl="https://example.com/feed.xml" htmlUrl="https://example.com/" leanfeedId="example-blog-7a775db7"/>
      <outline text="Nested">
        <outline type="rss" text="Deep" xmlUrl="https://deep.example/rss"/>
      </outline>
    </outline>
    <outline type="rss" title="Title Only" xmlUrl="http://top.example/atom"/>
    <outline text="Empty folder"/>
  </body>
</opml>`
	got, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := []Subscription{
		{ID: "example-blog-7a775db7", Title: "Example Blog", XMLURL: "https://example.com/feed.xml", HTMLURL: "https://example.com/", Folder: "Tech"},
		{Title: "Deep", XMLURL: "https://deep.example/rss", Folder: "Tech"},
		{Title: "Title Only", XMLURL: "http://top.example/atom"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseRejectsMalformedXML(t *testing.T) {
	if _, err := Parse(strings.NewReader("<opml><body><outline")); err == nil {
		t.Fatal("Parse of truncated XML returned no error")
	}
}

func TestParseRejectsNonOPML(t *testing.T) {
	if _, err := Parse(strings.NewReader(`<rss version="2.0"><channel/></rss>`)); err == nil {
		t.Fatal("Parse of an RSS document returned no error")
	}
}

func TestWrite(t *testing.T) {
	subs := []Subscription{
		{ID: "b-2", Title: "B & Co", XMLURL: "https://b.example/feed", HTMLURL: "https://b.example/", Folder: "Tech"},
		{ID: "top-1", Title: "Top", XMLURL: "https://top.example/feed"},
		{ID: "a-3", Title: "a", XMLURL: "https://a.example/feed", Folder: "Tech"},
		{ID: "n-4", Title: "News", XMLURL: "https://n.example/feed", Folder: "Alpha"},
	}
	var buf bytes.Buffer
	if err := Write(&buf, subs); err != nil {
		t.Fatal(err)
	}
	// Folders sorted by name, then top-level feeds; feeds sorted by title, ignoring case.
	want := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head>
    <title>leanfeed subscriptions</title>
  </head>
  <body>
    <outline text="Alpha" title="Alpha">
      <outline type="rss" text="News" title="News" xmlUrl="https://n.example/feed" leanfeedId="n-4"></outline>
    </outline>
    <outline text="Tech" title="Tech">
      <outline type="rss" text="a" title="a" xmlUrl="https://a.example/feed" leanfeedId="a-3"></outline>
      <outline type="rss" text="B &amp; Co" title="B &amp; Co" xmlUrl="https://b.example/feed" htmlUrl="https://b.example/" leanfeedId="b-2"></outline>
    </outline>
    <outline type="rss" text="Top" title="Top" xmlUrl="https://top.example/feed" leanfeedId="top-1"></outline>
  </body>
</opml>
`
	if buf.String() != want {
		t.Errorf("Write =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestWriteThenParseKeepsSubscriptions(t *testing.T) {
	subs := []Subscription{
		{ID: "x-1", Title: "X", XMLURL: "https://x.example/feed", HTMLURL: "https://x.example/", Folder: "F"},
		{ID: "y-2", Title: "Y", XMLURL: "https://y.example/feed"},
	}
	var buf bytes.Buffer
	if err := Write(&buf, subs); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, subs) {
		t.Errorf("round trip =\n%+v\nwant\n%+v", got, subs)
	}
}
