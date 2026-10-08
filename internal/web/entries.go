package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"leanfeed/internal/store"
)

var views = map[string]store.View{
	"unread":  store.ViewUnread,
	"all":     store.ViewAll,
	"starred": store.ViewStarred,
}

var viewNames = map[string]string{"unread": "Unread", "all": "All", "starred": "Starred"}

// errBadQuery is returned for list parameters that cannot be parsed.
var errBadQuery = errors.New("bad query")

// listQuery is the list a page shows, as given in the URL.
type listQuery struct {
	View, Feed, Folder string
	Page               int
}

// parseListQuery reads view, feed, folder and page from the URL query.
func parseListQuery(v url.Values) (listQuery, error) {
	q := listQuery{View: v.Get("view"), Feed: v.Get("feed"), Folder: v.Get("folder"), Page: 1}
	if q.View == "" {
		q.View = "unread"
	}
	if _, ok := views[q.View]; !ok {
		return q, errBadQuery
	}
	if p := v.Get("page"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			return q, errBadQuery
		}
		q.Page = n
	}
	return q, nil
}

// values encodes the query, leaving out defaults.
func (q listQuery) values(withPage bool) url.Values {
	v := url.Values{"view": {q.View}}
	if q.Feed != "" {
		v.Set("feed", q.Feed)
	}
	if q.Folder != "" {
		v.Set("folder", q.Folder)
	}
	if withPage && q.Page > 1 {
		v.Set("page", strconv.Itoa(q.Page))
	}
	return v
}

// Encode returns the query string including the page.
func (q listQuery) Encode() string { return q.values(true).Encode() }

// Filter returns the query string without the page, for actions on the
// whole list.
func (q listQuery) Filter() string { return q.values(false).Encode() }

// URL returns the list's address.
func (q listQuery) URL() template.URL { return template.URL("/entries?" + q.Encode()) }

// MarkReadURL returns the address that marks the whole list read.
func (q listQuery) MarkReadURL() template.URL {
	return template.URL("/entries/mark-read?" + q.Filter())
}

func (q listQuery) withPage(p int) listQuery {
	q.Page = p
	return q
}

func (q listQuery) storeQuery() store.Query {
	return store.Query{View: views[q.View], FeedID: q.Feed, Folder: q.Folder}
}

type rowData struct {
	store.Entry
	FeedTitle string
	Query     listQuery // the list the row is shown in, for links
	OOB       bool
}

// feedHref returns the address of a feed's entries in the given view.
func feedHref(view, feedID string) template.URL {
	return listQuery{View: view, Feed: feedID, Page: 1}.URL()
}

// FeedHref returns the address of the entry's feed, in the current view.
func (r rowData) FeedHref() template.URL { return feedHref(r.Query.View, r.FeedID) }

// Href returns the entry's address, remembering the list it was opened from.
func (r rowData) Href() template.URL {
	return template.URL("/entries/" + url.PathEscape(r.ID) + "?" + r.Query.Encode())
}

type listData struct {
	Query            listQuery
	Heading          string
	Rows             []rowData
	PrevURL, NextURL template.URL
}

type entryData struct {
	store.Entry
	FeedTitle string
	Content   template.HTML // sanitized by the fetcher
	Query     listQuery
	OOB       bool
}

// FeedHref returns the address of the entry's feed, in the view the entry
// was opened from.
func (e entryData) FeedHref() template.URL { return feedHref(e.Query.View, e.FeedID) }

type pageData struct {
	Title      string
	Sidebar    sidebarData
	List       *listData
	Entry      *entryData
	Manage     *manageData
	Appearance *appearanceData
}

// listData loads one page of the list q describes.
func (s *Server) listData(ctx context.Context, q listQuery) (*listData, error) {
	heading := viewNames[q.View]
	if q.Feed != "" {
		feed, err := s.store.GetFeed(ctx, q.Feed)
		if err != nil {
			return nil, err
		}
		heading = feed.Title
	} else if q.Folder != "" {
		heading = q.Folder
	}

	sq := q.storeQuery()
	sq.Limit, sq.Offset = s.pageSize, (q.Page-1)*s.pageSize
	page, err := s.store.ListEntries(ctx, sq)
	if err != nil {
		return nil, err
	}
	titles, err := s.feedTitles(ctx)
	if err != nil {
		return nil, err
	}
	d := &listData{Query: q, Heading: heading}
	for _, e := range page.Entries {
		d.Rows = append(d.Rows, rowData{Entry: e, FeedTitle: titles[e.FeedID], Query: q})
	}
	if q.Page > 1 {
		d.PrevURL = q.withPage(q.Page - 1).URL()
	}
	if q.Page*s.pageSize < page.Total {
		d.NextURL = q.withPage(q.Page + 1).URL()
	}
	return d, nil
}

func (s *Server) feedTitles(ctx context.Context) (map[string]string, error) {
	feeds, err := s.store.ListFeeds(ctx)
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	for _, f := range feeds {
		titles[f.ID] = f.Title
	}
	return titles, nil
}

// entryData loads an entry with its content.
func (s *Server) entryData(ctx context.Context, id string, q listQuery) (*entryData, error) {
	e, err := s.store.GetEntry(ctx, id)
	if err != nil {
		return nil, err
	}
	content, err := s.store.EntryContent(ctx, id)
	if err != nil {
		return nil, err
	}
	feed, err := s.store.GetFeed(ctx, e.FeedID)
	if err != nil {
		return nil, err
	}
	return &entryData{Entry: e, FeedTitle: feed.Title, Content: template.HTML(content), Query: q}, nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	q, err := parseListQuery(r.URL.Query())
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	list, err := s.listData(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	if isPartial(r) {
		// Sidebar and pager links replace the whole main area, which
		// also closes any open entry. The sidebar is sent along so it
		// highlights the new list.
		sidebar, err := s.sidebarData(r.Context(), q)
		if err != nil {
			s.fail(w, err)
			return
		}
		sidebar.OOB = true
		s.render(w, part{"list", list}, part{"entry-pane", (*entryData)(nil)}, part{"sidebar", sidebar})
		return
	}
	s.renderPage(w, r, q, pageData{Title: list.Heading, List: list})
}

// renderPage renders the full layout with the sidebar.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, q listQuery, page pageData) {
	sidebar, err := s.sidebarData(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	page.Sidebar = sidebar
	s.render(w, part{"layout", page})
}

// handleEntry shows an entry and marks it read. The URL query names the
// list the entry was opened from.
func (s *Server) handleEntry(w http.ResponseWriter, r *http.Request) {
	q, err := parseListQuery(r.URL.Query())
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	if err := s.store.SetRead(r.Context(), id, true); err != nil {
		s.fail(w, err)
		return
	}
	entry, err := s.entryData(r.Context(), id, q)
	if err != nil {
		s.fail(w, err)
		return
	}
	if isPartial(r) {
		parts, err := s.updatedParts(r, entry.Entry, q)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.render(w, append([]part{{"entry", entry}}, parts...)...)
		return
	}
	list, err := s.listData(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.renderPage(w, r, q, pageData{Title: entry.Title, List: list, Entry: entry})
}

// updatedParts returns out-of-band swaps for everything that shows an
// entry's state: its row, the entry view's actions and the sidebar counts.
func (s *Server) updatedParts(r *http.Request, e store.Entry, q listQuery) ([]part, error) {
	feed, err := s.store.GetFeed(r.Context(), e.FeedID)
	if err != nil {
		return nil, err
	}
	sidebar, err := s.sidebarData(r.Context(), currentQuery(r))
	if err != nil {
		return nil, err
	}
	sidebar.OOB = true
	return []part{
		{"row", rowData{Entry: e, FeedTitle: feed.Title, Query: q, OOB: true}},
		{"entry-actions", entryData{Entry: e, OOB: true}},
		{"sidebar", sidebar},
	}, nil
}

// currentQuery returns the list shown in the browser, from htmx's
// HX-Current-URL header, so the sidebar keeps its highlight.
func currentQuery(r *http.Request) listQuery {
	if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil {
		if q, err := parseListQuery(u.Query()); err == nil {
			return q
		}
	}
	q, _ := parseListQuery(url.Values{})
	return q
}

func (s *Server) handleEntryAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var err error
	switch r.PathValue("action") {
	case "read":
		err = s.store.SetRead(r.Context(), id, true)
	case "unread":
		err = s.store.SetRead(r.Context(), id, false)
	case "star":
		err = s.store.SetStarred(r.Context(), id, true)
	case "unstar":
		err = s.store.SetStarred(r.Context(), id, false)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	e, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	parts, err := s.updatedParts(r, e, currentQuery(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, parts...)
}

// handleMarkAllRead marks every entry in the list read and returns the
// list, which then shows only what the view still selects.
func (s *Server) handleMarkAllRead(w http.ResponseWriter, r *http.Request) {
	q, err := parseListQuery(r.URL.Query())
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if _, err := s.store.MarkAllRead(r.Context(), q.storeQuery()); err != nil {
		s.fail(w, err)
		return
	}
	q.Page = 1
	list, err := s.listData(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	sidebar, err := s.sidebarData(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	sidebar.OOB = true
	s.render(w, part{"list", list}, part{"sidebar", sidebar})
}
