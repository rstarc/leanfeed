package filestore

import (
	"testing"
	"time"

	"leanfeed/internal/store"
	"leanfeed/internal/store/filestore/corpus"
)

// TestIndexBuild10k guards N3: building the index for 10,000 entries takes
// under 1 s with a warm OS cache.
func TestIndexBuild10k(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 30,000 files")
	}
	dir := t.TempDir()
	corpus.Write(t, dir, 50, 200)
	s := open(t, dir) // warm the OS cache
	s.Close()

	start := time.Now()
	s = open(t, dir)
	elapsed := time.Since(start)
	defer s.Close()

	page, _ := s.ListEntries(ctx, store.Query{View: store.ViewAll, Limit: 1})
	if page.Total != 10_000 {
		t.Fatalf("loaded %d entries, want 10000", page.Total)
	}
	t.Logf("index build for 10,000 entries: %v", elapsed)
	if elapsed > time.Second && !raceEnabled {
		t.Errorf("index build took %v, want under 1 s (N3)", elapsed)
	}
}

func BenchmarkOpen10k(b *testing.B) {
	dir := b.TempDir()
	corpus.Write(b, dir, 50, 200)
	for b.Loop() {
		s, err := Open(dir, quietLog)
		if err != nil {
			b.Fatal(err)
		}
		s.Close()
	}
}

func BenchmarkListEntries10k(b *testing.B) {
	dir := b.TempDir()
	corpus.Write(b, dir, 50, 200)
	s, err := Open(dir, quietLog)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	for b.Loop() {
		s.ListEntries(ctx, store.Query{View: store.ViewUnread, Limit: 50})
	}
}
