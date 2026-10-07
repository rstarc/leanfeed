package ids

import (
	"testing"
	"time"
)

func TestFeedID(t *testing.T) {
	const url = "https://example.com/feed.xml" // sha256 prefix: 7a775db7
	tests := []struct {
		name, title, want string
	}{
		{"simple title", "Example Blog", "example-blog-7a775db7"},
		{"punctuation collapses to one hyphen", "  Hello, World!! (Daily)  ", "hello-world-daily-7a775db7"},
		{"non-ASCII letters are dropped", "Café Ñews", "caf-ews-7a775db7"},
		{"empty title falls back", "", "feed-7a775db7"},
		{"unusable title falls back", "!!! ???", "feed-7a775db7"},
		{"long title is cut to 40 characters", "The Quite Long Name Of A Weblog About Many Different Things", "the-quite-long-name-of-a-weblog-about-ma-7a775db7"},
		{"cut mid-word", "abcdefghij abcdefghij abcdefghij abcdefghijk", "abcdefghij-abcdefghij-abcdefghij-abcdefg-7a775db7"},
		{"cut at a hyphen leaves no trailing hyphen", "abcdefghij abcdefghij abcdefghij abcdef ghij", "abcdefghij-abcdefghij-abcdefghij-abcdef-7a775db7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FeedID(tt.title, url); got != tt.want {
				t.Errorf("FeedID(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

func TestEntryID(t *testing.T) {
	const feed = "example-blog-3f9a2c1e"
	published := time.Date(2026, 10, 6, 16, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	tests := []struct {
		name              string
		guid, link, title string
		published         time.Time
		want              string
	}{
		{"GUID is the key", "https://example.com/posts/42", "https://example.com/other", "T", published, "faabc0c8ece903a3"},
		{"link when GUID is empty", "", "https://example.com/p/1", "T", published, "b1330bda03c0caa1"},
		{"title and UTC date when both are empty", "", "", "Hello world", published, "752c9732e874390e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EntryID(feed, tt.guid, tt.link, tt.title, tt.published); got != tt.want {
				t.Errorf("EntryID = %q, want %q", got, tt.want)
			}
		})
	}
}
