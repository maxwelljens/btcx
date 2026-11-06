package render

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// wellFormed fails when s is not valid XML, which is what Pango markup must be.
func wellFormed(t *testing.T, s string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader("<root>" + s + "</root>"))
	for {
		_, err := d.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("output is not well-formed XML: %v\n%q", err, s)
		}
	}
}

func TestEscaped(t *testing.T) {
	tests := []struct{ in, want string }{
		{`5 < 6 & 7 > 2`, `5 &lt; 6 &amp; 7 &gt; 2`},
		{`say "hi"`, `say &quot;hi&quot;`},
		{`already &amp; escaped`, `already &amp;amp; escaped`},
		{"control\x00\x01", "control"},
		{"tab\tnewline\n", "tab\tnewline\n"},
	}
	for _, tt := range tests {
		if got := Escaped(tt.in); got != tt.want {
			t.Errorf("Escaped(%q) = %q, want %q", tt.in, got, tt.want)
		}
		wellFormed(t, Escaped(tt.in))
	}
}

func TestPangoEscapesText(t *testing.T) {
	got := Pango(`<p>5 &lt; 6 &amp; 7 &gt; 2</p>`)
	if !strings.Contains(got, "5 &lt; 6 &amp; 7 &gt; 2") {
		t.Errorf("Pango did not escape text: %q", got)
	}
	if strings.Contains(got, "5 < 6") {
		t.Errorf("Pango emitted raw markup characters: %q", got)
	}
	wellFormed(t, got)
}

func TestPangoNeverEmitsScriptMarkup(t *testing.T) {
	got := Pango(`hello <script>alert(1)</script> world`)
	if strings.Contains(strings.ToLower(got), "<script") {
		t.Errorf("Pango emitted a script tag: %q", got)
	}
	wellFormed(t, got)

	// Plain text is not HTML: tags are visible, escaped text.
	plain := PangoFromPlain(`<script>alert(1)</script>`)
	if !strings.Contains(plain, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("PangoFromPlain did not escape tags: %q", plain)
	}
	wellFormed(t, plain)
}

func TestPangoFormatting(t *testing.T) {
	got := Pango(`<b>bd</b><strong>st</strong><i>it</i><em>em</em><u>un</u><s>ss</s><strike>sk</strike><code>co</code><sub>sb</sub><sup>sp</sup>`)
	for _, want := range []string{
		"<b>bd</b>", "<b>st</b>", "<i>it</i>", "<i>em</i>",
		"<u>un</u>", "<s>ss</s>", "<s>sk</s>",
		`<span font_family="monospace">co</span>`,
		"<sub>sb</sub>", "<sup>sp</sup>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Pango(...) = %q, missing %q", got, want)
		}
	}
	wellFormed(t, got)
}

func TestPangoLink(t *testing.T) {
	got := Pango(`<a href="https://example.com/x?a=1&amp;b=2">Example</a>`)
	for _, want := range []string{
		`underline="single"`,
		`foreground="#3584e4"`,
		"Example (https://example.com/x?a=1&amp;b=2)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Pango(...) = %q, missing %q", got, want)
		}
	}
	wellFormed(t, got)

	// A javascript: href must not survive sanitization or become text.
	bad := Pango(`<a href="javascript:alert(1)">click</a>`)
	if strings.Contains(strings.ToLower(bad), "javascript") || strings.Contains(bad, "underline=") {
		t.Errorf("Pango emitted a javascript link: %q", bad)
	}
	wellFormed(t, bad)
}

func TestPangoHeadings(t *testing.T) {
	got := Pango(`<h1>Top</h1><h4>Sub</h4>`)
	if !strings.Contains(got, `<span size="x-large"><b>Top</b></span>`) {
		t.Errorf("Pango(...) = %q, missing x-large heading", got)
	}
	if !strings.Contains(got, `<span size="large"><b>Sub</b></span>`) {
		t.Errorf("Pango(...) = %q, missing large heading", got)
	}
	wellFormed(t, got)
}

func TestPangoBlockquote(t *testing.T) {
	got := Pango(`<blockquote>one<br>two</blockquote>`)
	if !strings.Contains(got, "<i>&gt; one\n&gt; two</i>") {
		t.Errorf("Pango(...) = %q, missing quoted lines", got)
	}
	wellFormed(t, got)

	nested := Pango(`<blockquote>a<blockquote>b</blockquote></blockquote>`)
	if strings.Count(nested, "&gt; ") < 3 {
		t.Errorf("nested quote lost a level: %q", nested)
	}
	wellFormed(t, nested)
}

func TestPangoLists(t *testing.T) {
	ul := Pango(`<ul><li>one</li><li>two</li></ul>`)
	if !strings.Contains(ul, "• one") || !strings.Contains(ul, "• two") {
		t.Errorf("Pango(ul) = %q, missing bullets", ul)
	}
	wellFormed(t, ul)

	ol := Pango(`<ol><li>one</li><li>two</li></ol>`)
	if !strings.Contains(ol, "1. one") || !strings.Contains(ol, "2. two") {
		t.Errorf("Pango(ol) = %q, missing numbering", ol)
	}
	wellFormed(t, ol)
}

func TestPangoHRAndSpacing(t *testing.T) {
	got := Pango(`<p>a</p><p>b</p><hr>`)
	if !strings.Contains(got, "a\n\nb") {
		t.Errorf("Pango(...) = %q, missing paragraph spacing", got)
	}
	if !strings.Contains(got, "---") {
		t.Errorf("Pango(...) = %q, missing horizontal rule", got)
	}
	wellFormed(t, got)
}

func TestPangoTable(t *testing.T) {
	got := Pango(`<table><tr><th>H1</th><th>H2</th></tr><tr><td>a</td><td>b</td></tr></table>`)
	if !strings.Contains(got, "<b>H1</b> | <b>H2</b>") {
		t.Errorf("Pango(...) = %q, missing bold header row", got)
	}
	if !strings.Contains(got, "a | b") {
		t.Errorf("Pango(...) = %q, missing cell separator", got)
	}
	wellFormed(t, got)
}

func TestPangoImages(t *testing.T) {
	inline := Pango(`<img src="cid:part1" alt="logo">`)
	if !strings.Contains(inline, "[inline image: logo]") {
		t.Errorf("Pango(cid) = %q, missing inline placeholder", inline)
	}
	wellFormed(t, inline)

	blocked := Pango(`<img src="https://tracker.example/p.gif" alt="pixel">`)
	if !strings.Contains(blocked, "[blocked remote image: pixel]") {
		t.Errorf("Pango(remote) = %q, missing blocked placeholder", blocked)
	}
	wellFormed(t, blocked)

	unknown := Pango(`<img alt="x">`)
	if !strings.Contains(unknown, "[image: x]") {
		t.Errorf("Pango(img) = %q, missing image placeholder", unknown)
	}
}

func TestPangoTruncatesHugeInput(t *testing.T) {
	got := Pango(strings.Repeat("a", maxInput+100))
	if !strings.Contains(got, "message truncated") {
		t.Errorf("Pango did not truncate a %d byte input", maxInput+100)
	}
	wellFormed(t, got)
}

func TestPangoFromPlainLinkify(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		contains []string
		missing  []string
	}{
		{
			name:     "url at start",
			in:       "https://example.com rest",
			contains: []string{`<span underline="single" foreground="#3584e4">https://example.com</span>`, " rest"},
		},
		{
			name:     "url in middle",
			in:       "see https://example.com now",
			contains: []string{"see ", `>https://example.com</span>`, " now"},
		},
		{
			name:     "url at end",
			in:       "visit www.example.com",
			contains: []string{`>www.example.com</span>`},
		},
		{
			name:     "trailing punctuation excluded",
			in:       "end https://example.com.",
			contains: []string{`>https://example.com</span>.`},
			missing:  []string{"example.com.</span>"},
		},
		{
			name:     "trailing bracket excluded",
			in:       "(https://example.com)",
			contains: []string{`>https://example.com</span>)`},
		},
		{
			name:     "mailto",
			in:       "mail mailto:a@b.example",
			contains: []string{`>mailto:a@b.example</span>`},
		},
		{
			name:     "quoted url stops at quote",
			in:       `"https://example.com"`,
			contains: []string{`>https://example.com</span>&quot;`},
		},
		{
			name:     "www glued to word is not a url",
			in:       "awww.example.com",
			contains: []string{"awww.example.com"},
			missing:  []string{`<span underline`},
		},
		{
			name:     "plain text untouched",
			in:       "nothing to see",
			contains: []string{"nothing to see"},
			missing:  []string{`<span underline`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PangoFromPlain(tt.in)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("PangoFromPlain(%q) = %q, missing %q", tt.in, got, want)
				}
			}
			for _, bad := range tt.missing {
				if strings.Contains(got, bad) {
					t.Errorf("PangoFromPlain(%q) = %q, must not contain %q", tt.in, got, bad)
				}
			}
			wellFormed(t, got)
		})
	}
}

func TestPangoFromPlainDoesNotLinkifyMarkup(t *testing.T) {
	got := PangoFromPlain(`<b>https://example.com</b>`)
	if strings.Contains(got, "<b>") {
		t.Errorf("PangoFromPlain interpreted tags as markup: %q", got)
	}
	if !strings.Contains(got, "&lt;b&gt;") || !strings.Contains(got, `<span underline="single"`) {
		t.Errorf("PangoFromPlain(...) = %q, want escaped tags and a link", got)
	}
	wellFormed(t, got)
}

func TestPangoFromPlainQuotes(t *testing.T) {
	got := PangoFromPlain("> quoted\n>> nested\nplain")
	if !strings.Contains(got, "<i>&gt; quoted</i>") {
		t.Errorf("PangoFromPlain quotes = %q, missing italic quote", got)
	}
	if !strings.Contains(got, "<i>&gt; &gt; nested</i>") {
		t.Errorf("PangoFromPlain quotes = %q, missing nested quote", got)
	}
	if !strings.HasSuffix(got, "\nplain") {
		t.Errorf("PangoFromPlain quotes = %q, lost trailing line", got)
	}
	wellFormed(t, got)
}

func TestPangoFromPlainPreservesBreaks(t *testing.T) {
	got := PangoFromPlain("one\ntwo\r\nthree")
	if got != "one\ntwo\nthree" {
		t.Errorf("PangoFromPlain breaks = %q", got)
	}
	wellFormed(t, got)
}

func TestPangoFromPlainTruncates(t *testing.T) {
	got := PangoFromPlain(strings.Repeat("b", maxInput+100))
	if !strings.Contains(got, "message truncated") {
		t.Errorf("PangoFromPlain did not truncate a %d byte input", maxInput+100)
	}
	wellFormed(t, got)
}

func TestMarkupValidityCorpus(t *testing.T) {
	corpus := []string{
		``,
		`plain`,
		`<p>Hello <b>world</b></p>`,
		`<script>alert(1)</script>`,
		`<a href="https://example.com/?a=1&amp;b=2">x &amp; y</a>`,
		`<img src="https://tracker.example/p.gif" alt="p&quot;x">`,
		`<img src="cid:1" alt="i">`,
		`<blockquote><p>a</p><blockquote>b</blockquote></blockquote>`,
		`<ol><li>a<ul><li>b</li></ul></li></ol>`,
		`<table><thead><tr><th>h</th></tr></thead><tbody><tr><td>c</td></tr></tbody></table>`,
		`<h1>t</h1><pre>x\n y</pre><hr>`,
		`<li>orphan</li>`,
		`<td>orphan cell</td>`,
		`<tr><td>orphan row</td></tr>`,
		`<dd>orphan dd</dd>`,
		`<p title="</p><script>alert(1)</script>">x</p>`,
		"nul\x00and\x01controls",
		"emoji 😀 and \uFFFD",
	}
	for _, in := range corpus {
		p := Pango(in)
		wellFormed(t, p)
		pp := PangoFromPlain(in)
		wellFormed(t, pp)
	}
}
