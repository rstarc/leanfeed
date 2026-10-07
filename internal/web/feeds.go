package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"leanfeed/internal/store"
)

// maxOPMLSize caps OPML uploads.
const maxOPMLSize = 5 << 20

type manageData struct {
	Feeds       []store.Feed
	Folders     []string // existing folder names, for suggestions
	Notice      string
	NoticeError bool
}

func (s *Server) manageData(r *http.Request, notice string, isErr bool) (*manageData, error) {
	feeds, err := s.store.ListFeeds(r.Context())
	if err != nil {
		return nil, err
	}
	d := &manageData{Feeds: feeds, Notice: notice, NoticeError: isErr}
	seen := map[string]bool{}
	for _, f := range feeds {
		if f.Folder != "" && !seen[f.Folder] {
			seen[f.Folder] = true
			d.Folders = append(d.Folders, f.Folder)
		}
	}
	sort.Strings(d.Folders)
	return d, nil
}

// renderManage renders the feed management section with a notice. htmx
// requests get the section and an out-of-band sidebar; others get the
// full page.
func (s *Server) renderManage(w http.ResponseWriter, r *http.Request, status int, notice string, isErr bool) {
	manage, err := s.manageData(r, notice, isErr)
	if err != nil {
		s.fail(w, err)
		return
	}
	sidebar, err := s.sidebarData(r.Context(), listQuery{})
	if err != nil {
		s.fail(w, err)
		return
	}
	sidebar.Manage = true
	if isPartial(r) {
		sidebar.OOB = true
		s.renderStatus(w, status, part{"manage", manage}, part{"sidebar", sidebar})
		return
	}
	s.renderStatus(w, status, part{"layout", pageData{Title: "Feeds", Sidebar: sidebar, Manage: manage}})
}

func (s *Server) handleFeeds(w http.ResponseWriter, r *http.Request) {
	s.renderManage(w, r, http.StatusOK, "", false)
}

// handleAddFeed subscribes through the fetcher, which fetches the feed
// first and rejects URLs that are not feeds.
func (s *Server) handleAddFeed(w http.ResponseWriter, r *http.Request) {
	feedURL := strings.TrimSpace(r.FormValue("url"))
	folder := strings.TrimSpace(r.FormValue("folder"))
	if feedURL == "" {
		s.renderManage(w, r, http.StatusBadRequest, "Enter a feed URL.", true)
		return
	}
	feed, err := s.fetcher.Subscribe(r.Context(), feedURL, folder)
	switch {
	case errors.Is(err, store.ErrExists):
		s.renderManage(w, r, http.StatusConflict, "You are already subscribed to "+feedURL+".", true)
	case err != nil:
		s.renderManage(w, r, http.StatusUnprocessableEntity, "Could not add "+feedURL+": "+err.Error(), true)
	default:
		s.renderManage(w, r, http.StatusOK, "Added "+feed.Title+".", false)
	}
}

// handleUpdateFeed renames a feed, moves it to a folder and changes its
// URL. An empty folder moves it to the top level. A new URL is fetched
// first; if it is not a feed, nothing changes.
func (s *Server) handleUpdateFeed(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.FormValue("title"))
	folder := strings.TrimSpace(r.FormValue("folder"))
	feedURL := strings.TrimSpace(r.FormValue("url"))
	switch {
	case title == "":
		s.renderManage(w, r, http.StatusBadRequest, "A feed needs a title.", true)
		return
	case feedURL == "":
		s.renderManage(w, r, http.StatusBadRequest, "A feed needs a URL.", true)
		return
	}
	id := r.PathValue("id")
	feed, err := s.store.GetFeed(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if feedURL != feed.URL {
		_, err := s.fetcher.ChangeURL(r.Context(), id, feedURL)
		switch {
		case errors.Is(err, store.ErrExists):
			s.renderManage(w, r, http.StatusConflict, "You are already subscribed to "+feedURL+".", true)
			return
		case err != nil:
			s.renderManage(w, r, http.StatusUnprocessableEntity, "Could not change the URL to "+feedURL+": "+err.Error(), true)
			return
		}
	}
	feed, err = s.store.UpdateFeed(r.Context(), id, store.FeedUpdate{Title: &title, Folder: &folder})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.renderManage(w, r, http.StatusOK, "Saved "+feed.Title+".", false)
}

func (s *Server) handleRemoveFeed(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	feed, err := s.store.GetFeed(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.RemoveFeed(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.renderManage(w, r, http.StatusOK, "Removed "+feed.Title+".", false)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.fetcher.RefreshAll(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	s.renderManage(w, r, http.StatusOK, "Refreshing all feeds. New entries appear as each feed is fetched.", false)
}

func (s *Server) handleExportOPML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-opml+xml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="leanfeed.opml"`)
	if err := s.store.ExportOPML(r.Context(), w); err != nil {
		s.log.Error("exporting OPML", "err", err)
	}
}

// handleImportOPML imports an uploaded OPML file of at most maxOPMLSize.
// New feeds are due at once, so the scheduler fetches them on its next check.
func (s *Server) handleImportOPML(w http.ResponseWriter, r *http.Request) {
	tooLarge := fmt.Sprintf("The file is larger than %d MB.", maxOPMLSize>>20)
	// Allow room for the multipart framing around the file.
	r.Body = http.MaxBytesReader(w, r.Body, maxOPMLSize+64<<10)
	file, _, err := r.FormFile("file")
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr):
		s.renderManage(w, r, http.StatusRequestEntityTooLarge, tooLarge, true)
		return
	case err != nil:
		s.renderManage(w, r, http.StatusBadRequest, "Choose an OPML file to import.", true)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxOPMLSize+1))
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(data) > maxOPMLSize {
		s.renderManage(w, r, http.StatusRequestEntityTooLarge, tooLarge, true)
		return
	}
	stats, err := s.store.ImportOPML(r.Context(), strings.NewReader(string(data)))
	if err != nil {
		s.renderManage(w, r, http.StatusBadRequest, "Could not read the OPML file: "+err.Error(), true)
		return
	}
	notice := fmt.Sprintf("Imported %d %s, skipped %d. New feeds are fetched within a minute.",
		stats.Added, plural(stats.Added, "feed", "feeds"), stats.Skipped)
	s.renderManage(w, r, http.StatusOK, notice, false)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
