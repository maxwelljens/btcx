package render

import (
	"strings"
	"testing"

	xhtml "golang.org/x/net/html"
)

func TestWebHTMLInlinesImages(t *testing.T) {
	in := `<p>Hello <img src="cid:logo@example" alt="logo"></p>` +
		`<img src="https://tracker.example/p.gif" data-blocked-src="https://tracker.example/p.gif" alt="t">` +
		`<img src="https://unresolved.example/x.png" alt="missing">`
	images := map[string]string{
		"cid:logo@example":              DataURI("image/png", []byte{1, 2, 3}),
		"https://tracker.example/p.gif": DataURI("image/gif", []byte{9}),
	}
	out := WebHTML(in, images)

	for _, want := range []string{
		"src=\"data:image/png;base64,AQID\"", // cid resolved
		"src=\"data:image/gif;base64\",",     // marker check below
	} {
		if want == "src=\"data:image/gif;base64\"," {
			if !strings.Contains(out, "src=\"data:image/gif;base64,") {
				t.Errorf("remote image not inlined:\n%s", out)
			}
			continue
		}
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "cid:") || strings.Contains(out, "data-blocked-src") {
		t.Errorf("unresolved references survived:\n%s", out)
	}
	// The unresolved remote image keeps its alt text but no src.
	if !strings.Contains(out, "alt=\"missing\"") {
		t.Errorf("alt text lost:\n%s", out)
	}
	if strings.Contains(out, "unresolved.example") {
		t.Errorf("unresolved URL survived in output:\n%s", out)
	}
	// No network references may remain anywhere.
	if strings.Contains(out, "src=\"http") {
		t.Errorf("network src survived:\n%s", out)
	}
}

func TestWebHTMLDocument(t *testing.T) {
	out := WebHTML(`<p>5 &lt; 6 &amp; 7</p>`, nil)
	if !strings.HasPrefix(out, "<!doctype html>") {
		t.Errorf("missing doctype:\n%s", out)
	}
	if !strings.Contains(out, `<meta charset="utf-8">`) {
		t.Errorf("missing charset:\n%s", out)
	}
	// Output must be well-formed HTML (round-trips through the parser).
	if _, err := xhtml.Parse(strings.NewReader(out)); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	// Escaped text stays escaped: visible "5 < 6 & 7", no stray tags.
	if !strings.Contains(out, "5 &lt; 6 &amp; 7") {
		t.Errorf("text escaping lost:\n%s", out)
	}
	if strings.Contains(out, "<p>5 < 6") {
		t.Errorf("raw < survived:\n%s", out)
	}
}

func TestWebHTMLUnparseableInput(t *testing.T) {
	out := WebHTML("\x00\x01<script>alert(1)</script>", nil)
	if strings.Contains(out, "<script") || strings.Contains(out, "alert(1)") {
		t.Errorf("hostile input leaked into the webview document:\n%s", out)
	}
}

func TestDataURI(t *testing.T) {
	got := DataURI("image/png; name=x", []byte{0xFF})
	want := "data:image/png;base64,/w=="
	if got != want {
		t.Errorf("DataURI = %q, want %q", got, want)
	}
	if got := DataURI("", []byte{1}); !strings.HasPrefix(got, "data:application/octet-stream;base64,") {
		t.Errorf("empty content type: %q", got)
	}
}
