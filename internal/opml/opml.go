// Package opml reads and writes OPML 2.0 subscription lists with one level
// of folders.
package opml

import (
	"encoding/xml"
	"io"
	"sort"
	"strings"
)

// Subscription is one feed in an OPML file. Folder is empty for feeds at
// the top level. ID is leanfeed's own leanfeedId attribute, empty in files
// written by other readers.
type Subscription struct {
	ID, Title, XMLURL, HTMLURL, Folder string
}

type document struct {
	XMLName xml.Name  `xml:"opml"`
	Version string    `xml:"version,attr"`
	Title   string    `xml:"head>title"`
	Body    []outline `xml:"body>outline"`
}

type outline struct {
	Type     string    `xml:"type,attr,omitempty"`
	Text     string    `xml:"text,attr"`
	Title    string    `xml:"title,attr,omitempty"`
	XMLURL   string    `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string    `xml:"htmlUrl,attr,omitempty"`
	ID       string    `xml:"leanfeedId,attr,omitempty"`
	Outlines []outline `xml:"outline"`
}

func (o outline) name() string {
	if o.Text != "" {
		return o.Text
	}
	return o.Title
}

// Parse returns the feeds in an OPML document in document order. Folders
// nested deeper than one level are flattened into their top-level folder.
func Parse(r io.Reader) ([]Subscription, error) {
	var doc document
	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return nil, err
	}
	var subs []Subscription
	var walk func(outlines []outline, folder string)
	walk = func(outlines []outline, folder string) {
		for _, o := range outlines {
			if o.XMLURL != "" {
				subs = append(subs, Subscription{
					ID:      o.ID,
					Title:   o.name(),
					XMLURL:  o.XMLURL,
					HTMLURL: o.HTMLURL,
					Folder:  folder,
				})
				continue
			}
			child := folder
			if child == "" {
				child = o.name()
			}
			walk(o.Outlines, child)
		}
	}
	walk(doc.Body, "")
	return subs, nil
}

// Write writes subs as an OPML 2.0 document: folders sorted by name, then
// top-level feeds, with feeds sorted by title ignoring case.
func Write(w io.Writer, subs []Subscription) error {
	sorted := append([]Subscription(nil), subs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return strings.ToLower(sorted[i].Title) < strings.ToLower(sorted[j].Title)
	})

	folders := map[string]*outline{}
	var folderNames []string
	var top []outline
	for _, s := range sorted {
		o := outline{Type: "rss", Text: s.Title, Title: s.Title, XMLURL: s.XMLURL, HTMLURL: s.HTMLURL, ID: s.ID}
		if s.Folder == "" {
			top = append(top, o)
			continue
		}
		f, ok := folders[s.Folder]
		if !ok {
			f = &outline{Text: s.Folder, Title: s.Folder}
			folders[s.Folder] = f
			folderNames = append(folderNames, s.Folder)
		}
		f.Outlines = append(f.Outlines, o)
	}
	sort.Strings(folderNames)

	doc := document{Version: "2.0", Title: "leanfeed subscriptions"}
	for _, name := range folderNames {
		doc.Body = append(doc.Body, *folders[name])
	}
	doc.Body = append(doc.Body, top...)

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
