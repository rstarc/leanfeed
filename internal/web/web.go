// Package web serves leanfeed's HTML interface: server-rendered pages
// enhanced with htmx. Every page works as a full page load; htmx requests
// get partials.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"leanfeed/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const defaultPageSize = 50

// Fetcher is the part of the fetcher the web UI uses.
type Fetcher interface {
	Subscribe(ctx context.Context, url, folder string) (store.Feed, error)
	RefreshAll(ctx context.Context) error
}

// Server is the web UI. It implements http.Handler.
type Server struct {
	store    store.Store
	fetcher  Fetcher
	log      *slog.Logger
	tmpl     *template.Template
	mux      *http.ServeMux
	pageSize int
}

func New(s store.Store, f Fetcher, log *slog.Logger) (*Server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"date": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") },
		// link pairs an element ID with a sidebar item for the nav-link template.
		"link": func(id string, item NavItem) map[string]any { return map[string]any{"ID": id, "Item": item} },
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	srv := &Server{store: s, fetcher: f, log: log, tmpl: tmpl, mux: http.NewServeMux(), pageSize: defaultPageSize}
	srv.routes()
	return srv, nil
}

func (s *Server) routes() {
	static, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/entries?view=unread", http.StatusFound)
	})
	s.mux.HandleFunc("GET /entries", s.handleList)
	s.mux.HandleFunc("GET /entries/{id}", s.handleEntry)
	s.mux.HandleFunc("POST /entries/{id}/{action}", s.handleEntryAction)
	s.mux.HandleFunc("POST /entries/mark-read", s.handleMarkAllRead)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// isPartial reports whether the request wants a partial: htmx requests do,
// except history restores, which need the full page.
func isPartial(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
}

// part is one template to render with its data.
type part struct {
	name string
	data any
}

// render executes the parts into a buffer, then writes them, so a template
// error never sends half a page.
func (s *Server) render(w http.ResponseWriter, parts ...part) {
	var buf bytes.Buffer
	for _, p := range parts {
		if err := s.tmpl.ExecuteTemplate(&buf, p.name, p.data); err != nil {
			s.serverError(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// fail maps store errors to HTTP errors.
func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "Not found", http.StatusNotFound)
	case errors.Is(err, store.ErrExists):
		http.Error(w, "Already exists", http.StatusConflict)
	default:
		s.serverError(w, err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "err", err)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}
