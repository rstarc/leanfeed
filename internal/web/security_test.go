package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/entries", "/feeds", "/static/style.css", "/nope"} {
		rec := f.do("GET", path, false)
		h := rec.Header()
		for name, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
			"X-Frame-Options":        "DENY",
		} {
			if got := h.Get(name); got != want {
				t.Errorf("GET %s: %s = %q, want %q", path, name, got, want)
			}
		}
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("GET %s: CSP %q missing %q", path, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe") {
			t.Errorf("GET %s: CSP %q allows unsafe sources", path, csp)
		}
	}
}

func TestCSRFProtection(t *testing.T) {
	f := newFixture(t)
	send := func(method, target string, headers map[string]string) int {
		req := httptest.NewRequest(method, target, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)
		return rec.Code
	}
	id := f.ids["Blog new"]

	rejected := []struct {
		name, method, target string
		headers              map[string]string
	}{
		{"POST without HX-Request", "POST", "/refresh", nil},
		{"DELETE without HX-Request", "DELETE", "/feeds/" + f.blog.ID, nil},
		{"form post from another site", "POST", "/entries/" + id + "/star", map[string]string{"Origin": "https://evil.example"}},
		{"HX-Request from another origin", "POST", "/refresh", map[string]string{"HX-Request": "true", "Origin": "https://evil.example"}},
		{"HX-Request from a null origin", "POST", "/refresh", map[string]string{"HX-Request": "true", "Origin": "null"}},
		{"HX-Request from another port", "POST", "/refresh", map[string]string{"HX-Request": "true", "Origin": "http://example.com:8081"}},
	}
	for _, tt := range rejected {
		if code := send(tt.method, tt.target, tt.headers); code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", tt.name, code)
		}
	}
	if f.fetcher.refreshed != 0 {
		t.Errorf("a rejected request refreshed feeds")
	}
	if _, err := f.store.GetFeed(ctx, f.blog.ID); err != nil {
		t.Errorf("a rejected request removed a feed: %v", err)
	}
	if f.entry("Blog new").Starred {
		t.Error("a rejected request starred an entry")
	}

	// httptest requests have Host example.com.
	if code := send("POST", "/refresh", map[string]string{"HX-Request": "true", "Origin": "http://example.com"}); code != http.StatusOK {
		t.Errorf("same-origin htmx request: status %d, want 200", code)
	}
	if code := send("POST", "/refresh", map[string]string{"HX-Request": "true"}); code != http.StatusOK {
		t.Errorf("htmx request without Origin: status %d, want 200", code)
	}
	if code := send("GET", "/entries", map[string]string{"Origin": "https://evil.example"}); code != http.StatusOK {
		t.Errorf("cross-origin GET: status %d, want 200 (GETs change nothing)", code)
	}
}
