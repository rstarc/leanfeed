// Package ids generates the feed and entry IDs shared by all store
// implementations, so data can move between stores with IDs intact.
package ids

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const maxSlugLen = 40

// FeedID returns a readable slug of the title plus the first 8 hex
// characters of SHA-256 of the feed URL, for example "example-blog-3f9a2c1e".
func FeedID(title, url string) string {
	return slug(title) + "-" + hash(url)[:8]
}

// EntryID returns the first 16 hex characters of SHA-256 of
// feedID + "\n" + key. The key is the GUID, else the link, else the title
// plus the published date in RFC 3339 UTC.
func EntryID(feedID, guid, link, title string, published time.Time) string {
	key := guid
	if key == "" {
		key = link
	}
	if key == "" {
		key = title + published.UTC().Format(time.RFC3339)
	}
	return hash(feedID + "\n" + key)[:16]
}

// slug keeps lowercase ASCII letters and digits, joins runs of anything
// else with a single hyphen, and falls back to "feed".
func slug(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	s := b.String()
	if len(s) > maxSlugLen {
		s = s[:maxSlugLen]
	}
	s = strings.Trim(s, "-")
	if s == "" {
		return "feed"
	}
	return s
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
