// Package store defines the storage interface that the web server and the
// fetcher use, plus its domain types and errors. Implementations live in
// subpackages; storetest holds the contract suite they must all pass.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// Store is the only way to read or change leanfeed's persistent state.
// Methods return copies, never pointers into the implementation.
type Store interface {
	// Feeds and subscriptions
	ListFeeds(ctx context.Context) ([]Feed, error)
	GetFeed(ctx context.Context, id string) (Feed, error)
	AddFeed(ctx context.Context, f NewFeed) (Feed, error)
	UpdateFeed(ctx context.Context, id string, u FeedUpdate) (Feed, error) // title, folder, URL
	RemoveFeed(ctx context.Context, id string) error                       // deletes feed and all its entries
	DueFeeds(ctx context.Context, now time.Time) ([]Feed, error)
	RecordFetch(ctx context.Context, id string, r FetchResult) error // ETag, Last-Modified, status, error, next fetch
	ImportOPML(ctx context.Context, r io.Reader) (ImportStats, error)
	ExportOPML(ctx context.Context, w io.Writer) error

	// Entries
	UpsertEntries(ctx context.Context, feedID string, in []IncomingEntry) (UpsertStats, error)
	ListEntries(ctx context.Context, q Query) (EntryPage, error)
	GetEntry(ctx context.Context, id string) (Entry, error)
	EntryContent(ctx context.Context, id string) (string, error) // sanitized HTML
	SetRead(ctx context.Context, id string, read bool) error
	SetStarred(ctx context.Context, id string, starred bool) error
	MarkAllRead(ctx context.Context, q Query) (int, error)
	UnreadCounts(ctx context.Context) (map[string]int, error) // by feed ID

	Close() error // waits for in-flight writes
}

// Feed is a subscription plus its fetch state. Folder is empty for feeds
// outside any folder.
type Feed struct {
	ID                  string    `json:"id"`
	URL                 string    `json:"url"`
	Title               string    `json:"title"`
	SiteURL             string    `json:"site_url"`
	Folder              string    `json:"-"` // stored in the OPML only
	ETag                string    `json:"etag"`
	LastModified        string    `json:"last_modified"`
	LastFetchedAt       time.Time `json:"last_fetched_at"`
	NextFetchAt         time.Time `json:"next_fetch_at"`
	LastStatus          string    `json:"last_status"`
	LastError           string    `json:"last_error"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// Fetch status values for Feed.LastStatus.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// NewFeed describes a feed to subscribe to.
type NewFeed struct {
	URL, Title, SiteURL, Folder string
}

// FeedUpdate changes a feed's user-facing fields. Nil fields stay as they are.
type FeedUpdate struct {
	Title, Folder, URL *string
}

// FetchResult is the outcome of one fetch, recorded verbatim. URL and
// SiteURL change the feed only when non-empty.
type FetchResult struct {
	FetchedAt           time.Time
	NextFetchAt         time.Time
	Status              string
	Error               string
	ETag, LastModified  string
	ConsecutiveFailures int
	URL                 string // new URL after a permanent redirect
	SiteURL             string
}

// Entry is an entry's metadata and state, without content.
type Entry struct {
	ID            string    `json:"id"`
	FeedID        string    `json:"feed_id"`
	GUID          string    `json:"guid"`
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	Author        string    `json:"author"`
	PublishedAt   time.Time `json:"published_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	FetchedAt     time.Time `json:"fetched_at"`
	ContentSHA256 string    `json:"content_sha256"`
	Read          bool      `json:"read"`
	Starred       bool      `json:"starred"`
}

// EntryPage is one page of a listing. Total counts all matching entries.
type EntryPage struct {
	Entries []Entry
	Total   int
}

type UpsertStats struct {
	Added, Updated, Unchanged int
}

type ImportStats struct {
	Added, Skipped int
}

type View int

const (
	ViewUnread View = iota
	ViewAll
	ViewStarred
)

// Query selects entries. FeedID and Folder are optional filters. A Limit of
// zero means no limit. MarkAllRead ignores Limit and Offset.
type Query struct {
	View   View
	FeedID string // optional
	Folder string // optional
	Limit  int
	Offset int
}

// IncomingEntry is an entry as parsed from a feed. Content is already
// sanitized by the fetcher.
type IncomingEntry struct {
	GUID, URL, Title, Author string
	PublishedAt, UpdatedAt   time.Time
	RawContent               string // as received
	Content                  string // sanitized by the fetcher
}

// CheckFeedURL returns an error unless u is an absolute http or https URL
// with a host.
func CheckFeedURL(u string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("invalid feed URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("invalid feed URL %q: only http and https are allowed", u)
	}
	if parsed.Host == "" {
		return fmt.Errorf("invalid feed URL %q: no host", u)
	}
	return nil
}
