package store

import "testing"

func TestCheckFeedURL(t *testing.T) {
	tests := []struct {
		url string
		ok  bool
	}{
		{"https://example.com/feed.xml", true},
		{"http://example.com/rss", true},
		{"HTTPS://EXAMPLE.COM/feed", true},
		{"javascript:alert(1)", false},
		{"file:///etc/passwd", false},
		{"ftp://example.com/feed", false},
		{"https://", false},
		{"example.com/feed", false},
		{"", false},
	}
	for _, tt := range tests {
		err := CheckFeedURL(tt.url)
		if (err == nil) != tt.ok {
			t.Errorf("CheckFeedURL(%q) = %v, want ok=%v", tt.url, err, tt.ok)
		}
	}
}
