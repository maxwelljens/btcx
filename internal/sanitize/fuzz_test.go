package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzHTML(f *testing.F) {
	seeds := []string{
		`<p>hello <b>world</b></p>`,
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<a href="JaVaScRiPt:alert(1)">x</a>`,
		`<table><tr><td>cell</td></tr></table>`,
		`<svg onload="alert(1)"></svg>`,
		`<style>@import "x";</style>`,
		`<!-- <script>alert(1)</script> -->`,
		`<img src="https://tracker.example/p.gif" alt="p">`,
		`<img src="cid:part1" alt="inline">`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := HTML(in)
		lower := strings.ToLower(got)
		for _, tag := range []string{"<script", "<iframe", "<object", "<embed", "<form", "<svg", "<math", "<style", "<meta", "<base", "<link"} {
			if strings.Contains(lower, tag) {
				t.Fatalf("HTML(%q) = %q, contains forbidden %q", in, got, tag)
			}
		}
		// Output must be re-parseable: the UI feeds it to a parser later.
		if _, err := fragment(got); err != nil {
			t.Fatalf("HTML(%q) produced unparseable output: %v", in, err)
		}
	})
}

func FuzzPlainText(f *testing.F) {
	for _, s := range []string{
		`<p>hello</p>`,
		`<script>alert(1)</script>`,
		`<ul><li>one</li></ul>`,
		`<a href="https://example.com">x</a>`,
		`<table><tr><td>a</td></tr></table>`,
		"\x00\xff<p>bad utf8",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if got := PlainText(in); !utf8.ValidString(got) {
			t.Fatalf("PlainText(%q) produced invalid UTF-8", in)
		}
	})
}
