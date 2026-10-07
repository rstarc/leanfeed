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
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
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
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /entries", s.handleList)
	s.mux.HandleFunc("GET /entries/{id}", s.handleEntry)
	s.mux.HandleFunc("POST /entries/{id}/{action}", s.handleEntryAction)
	s.mux.HandleFunc("POST /entries/mark-read", s.handleMarkAllRead)
	s.mux.HandleFunc("GET /feeds", s.handleFeeds)
	s.mux.HandleFunc("POST /feeds", s.handleAddFeed)
	s.mux.HandleFunc("POST /feeds/{id}", s.handleUpdateFeed)
	s.mux.HandleFunc("DELETE /feeds/{id}", s.handleRemoveFeed)
	s.mux.HandleFunc("POST /refresh", s.handleRefresh)
	s.mux.HandleFunc("GET /opml", s.handleExportOPML)
	s.mux.HandleFunc("POST /opml", s.handleImportOPML)
}

// handleHealthz reports whether the store answers. Monitors and container
// health checks use it.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.ListFeeds(r.Context()); err != nil {
		s.log.Error("health check failed", "err", err)
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok\n")
}

// contentSecurityPolicy allows scripts and styles only from leanfeed
// itself. Images and media in feed content load from their origin.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' http: https:; media-src http: https:; connect-src 'self'; " +
	"form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// ServeHTTP sets security headers on every response and rejects
// cross-site requests that change state.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	if !safeMethod(r.Method) && !sameSiteHTMX(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameSiteHTMX reports whether a request carries the HX-Request header and,
// if it has an Origin, comes from this host. Browsers do not let other
// sites add custom headers without CORS, which leanfeed never enables.
func sameSiteHTMX(r *http.Request) bool {
	if r.Header.Get("HX-Request") != "true" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && u.Host == r.Host
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
	s.renderStatus(w, http.StatusOK, parts...)
}

func (s *Server) renderStatus(w http.ResponseWriter, status int, parts ...part) {
	var buf bytes.Buffer
	for _, p := range parts {
		if err := s.tmpl.ExecuteTemplate(&buf, p.name, p.data); err != nil {
			s.serverError(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
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
