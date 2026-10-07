package fetcher

import (
	"strings"
	"testing"
)

func TestSanitizeRemovesHostileMarkup(t *testing.T) {
	tests := []struct {
		name, in string
		banned   []string // case-insensitive substrings that must not survive
	}{
		{"script", `<p>ok</p><script>alert(1)</script>`, []string{"<script", "alert"}},
		{"script in svg", `<svg><script>alert(1)</script></svg>`, []string{"<script", "<svg", "alert"}},
		{"event handlers", `<p onclick="steal()" onmouseover="x()">t</p><img src="https://e.x/a.png" onerror="steal()">`, []string{"onclick", "onmouseover", "onerror", "steal"}},
		{"javascript URL", `<a href="javascript:alert(1)">x</a><a href=" JaVaScRiPt:alert(1)">y</a>`, []string{"javascript:"}},
		{"vbscript and data URLs", `<a href="vbscript:x">a</a><img src="data:text/html;base64,PHNjcmlwdD4=">`, []string{"vbscript:", "data:"}},
		{"iframe", `<iframe src="https://evil.example/"></iframe>`, []string{"<iframe", "evil"}},
		{"object and embed", `<object data="x.swf"></object><embed src="x.swf">`, []string{"<object", "<embed", "swf"}},
		{"styles", `<style>body{display:none}</style><p style="position:fixed">t</p>`, []string{"<style", "style=", "display", "position"}},
		{"forms", `<form action="https://evil.example/"><input name="pw"><button>go</button></form>`, []string{"<form", "<input", "<button", "evil"}},
		{"meta refresh and base", `<meta http-equiv="refresh" content="0;url=https://evil.example/"><base href="https://evil.example/">`, []string{"<meta", "<base", "evil"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.ToLower(Sanitize(tt.in, "https://example.com/"))
			for _, b := range tt.banned {
				if strings.Contains(got, strings.ToLower(b)) {
					t.Errorf("Sanitize(%q) = %q, contains %q", tt.in, got, b)
				}
			}
		})
	}
}

func TestSanitizeKeepsSafeMarkup(t *testing.T) {
	in := `<p>Hello <b>world</b></p><ul><li>one</li></ul><blockquote>q</blockquote><pre><code>x := 1</code></pre>`
	if got := Sanitize(in, "https://example.com/"); got != in {
		t.Errorf("Sanitize = %q, want unchanged %q", got, in)
	}
}

func TestSanitizeResolvesRelativeURLs(t *testing.T) {
	got := Sanitize(`<a href="../about">a</a><img src="img/b.png" alt="b"><a href="//cdn.example/c">c</a><a href="#top">d</a>`,
		"https://example.com/blog/post/")
	for _, want := range []string{
		`href="https://example.com/blog/about"`,
		`src="https://example.com/blog/post/img/b.png"`,
		`href="https://cdn.example/c"`,
		`href="https://example.com/blog/post/#top"`,
		`alt="b"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Sanitize = %q, missing %q", got, want)
		}
	}
}

func TestSanitizeLinksOpenInNewTabWithoutReferrer(t *testing.T) {
	got := Sanitize(`<a href="https://other.example/x" target="_self" rel="opener">x</a>`, "https://example.com/")
	for _, want := range []string{`target="_blank"`, `noopener`, `noreferrer`} {
		if !strings.Contains(got, want) {
			t.Errorf("Sanitize = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "_self") || strings.Contains(got, `"opener"`) {
		t.Errorf("Sanitize = %q, kept the feed's target or rel", got)
	}
}
