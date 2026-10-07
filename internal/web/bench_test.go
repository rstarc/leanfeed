package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"leanfeed/internal/store/filestore"
	"leanfeed/internal/store/filestore/corpus"
)

// largeServer returns a server over 10,000 entries in 50 feeds.
func largeServer(tb testing.TB) *Server {
	tb.Helper()
	dir := tb.TempDir()
	corpus.Write(tb, dir, 50, 200)
	s, err := filestore.Open(dir, quietLog)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	srv, err := New(s, &fakeFetcher{store: s}, quietLog)
	if err != nil {
		tb.Fatal(err)
	}
	return srv
}

var largePages = []string{
	"/entries?view=unread",
	"/entries?view=all",
	"/entries?view=all&page=150",
	"/entries?view=starred",
	"/entries?view=unread&folder=Folder+3",
	"/entries/0000000000000064?view=all", // entry view, also marks read
}

// TestRender10k guards N4: pages render in under 100 ms server-side for
// 10,000 entries.
func TestRender10k(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 30,000 files")
	}
	srv := largeServer(t)
	for _, path := range largePages {
		start := time.Now()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		elapsed := time.Since(start)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", path, rec.Code)
		}
		t.Logf("GET %s: %v", path, elapsed)
		if elapsed > 100*time.Millisecond && !raceEnabled {
			t.Errorf("GET %s took %v, want under 100 ms (N4)", path, elapsed)
		}
	}
}

func BenchmarkRenderUnread10k(b *testing.B) {
	srv := largeServer(b)
	for b.Loop() {
		srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/entries?view=unread", nil))
	}
}
