package sanitize

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestHTMLHostileCorpus(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		forbid  []string
		allowed []string
	}{
		{
			name:   "script tag",
			in:     `<script>alert(1)</script>`,
			forbid: []string{"<script", "alert(1)"},
		},
		{
			name:   "script breakout",
			in:     `</script><script>alert(1)</script>`,
			forbid: []string{"<script", "alert(1)"},
		},
		{
			name:   "img onerror",
			in:     `<img src=x onerror=alert(1)>`,
			forbid: []string{"onerror", "src=\"x\""},
		},
		{
			name:    "javascript href",
			in:      `<a href="javascript:alert(1)">click</a>`,
			forbid:  []string{"javascript", "href="},
			allowed: []string{">click</a>"},
		},
		{
			name:   "mixed case javascript href",
			in:     `<a href="JaVaScRiPt:alert(1)">click</a>`,
			forbid: []string{"JaVaScRiPt", "href="},
		},
		{
			name:   "tab smuggled javascript",
			in:     "<a href=\"java\tscript:alert(1)\">click</a>",
			forbid: []string{"script:", "href="},
		},
		{
			name:   "newline smuggled javascript",
			in:     "<a href=\"java\nscript:alert(1)\">click</a>",
			forbid: []string{"script:", "href="},
		},
		{
			name:   "entity encoded javascript",
			in:     `<a href="&#106;avascript:alert(1)">click</a>`,
			forbid: []string{"javascript", "href="},
		},
		{
			name:   "data text html href",
			in:     `<a href="data:text/html,<script>alert(1)</script>">click</a>`,
			forbid: []string{"data:", "href=", "<script"},
		},
		{
			name:   "iframe",
			in:     `<iframe src="https://evil.example"></iframe>`,
			forbid: []string{"<iframe", "evil.example"},
		},
		{
			name:   "svg onload",
			in:     `<svg onload="alert(1)"><circle/></svg>`,
			forbid: []string{"<svg", "onload"},
		},
		{
			name:   "math script",
			in:     `<math><mtext><script>alert(1)</script></mtext></math>`,
			forbid: []string{"<math", "<script", "alert(1)"},
		},
		{
			name:   "style expression import",
			in:     `<style>@import "x"; a{background:url(javascript:alert(1))}</style>`,
			forbid: []string{"<style", "@import", "javascript"},
		},
		{
			name:   "nested table mXSS",
			in:     `<table><noscript><p title="</noscript><img src=x onerror=alert(1)>">`,
			forbid: []string{"onerror", "noscript", "title="},
		},
		{
			name:    "comment hiding markup",
			in:      `<!--<script>alert(1)</script>--><p>ok</p>`,
			forbid:  []string{"<!--", "<script", "alert(1)"},
			allowed: []string{"<p>ok</p>"},
		},
		{
			name:   "meta refresh",
			in:     `<meta http-equiv="refresh" content="0;url=javascript:alert(1)">`,
			forbid: []string{"<meta", "http-equiv", "javascript"},
		},
		{
			name:   "base href",
			in:     `<base href="https://evil.example/">`,
			forbid: []string{"<base", "evil.example"},
		},
		{
			name:    "event handlers on allowed element",
			in:      `<p onclick="alert(1)" class="x" id="y" style="color:red" data-x="1">hi</p>`,
			forbid:  []string{"onclick", "class=", "id=", "style=", "data-x"},
			allowed: []string{"<p>hi</p>"},
		},
		{
			name:   "form action",
			in:     `<form action="https://evil.example"><input name="x"><button formaction="javascript:alert(1)">go</button></form>`,
			forbid: []string{"<form", "<input", "<button", "action", "javascript"},
		},
		{
			name:   "object embed video audio",
			in:     `<object data="https://evil.example/x"></object><embed src="x"><video src="https://evil.example/v"><source src="https://evil.example/s"></video><audio src="x"></audio>`,
			forbid: []string{"<object", "<embed", "<video", "<audio", "<source", "evil.example"},
		},
		{
			name:   "noscript image",
			in:     `<noscript><img src="https://evil.example/p.gif"></noscript>`,
			forbid: []string{"<noscript", "<img", "evil.example"},
		},
		{
			name:   "protocol relative href",
			in:     `<a href="//evil.example/x">x</a>`,
			forbid: []string{"evil.example", "href="},
		},
		{
			name:   "tel scheme href",
			in:     `<a href="tel:+123">call</a>`,
			forbid: []string{"href=", "tel:"},
		},
		{
			name:   "cid href is not allowed",
			in:     `<a href="cid:part1">x</a>`,
			forbid: []string{"href=", "cid:"},
		},
		{
			name:   "relative href is dropped",
			in:     `<a href="/local">x</a>`,
			forbid: []string{"href="},
		},
		{
			name:   "remote img blocked",
			in:     `<img src="https://tracker.example/p.gif" alt="pixel" width="1" height="1">`,
			forbid: []string{` src="https`},
			allowed: []string{
				`src=""`,
				`data-blocked-src="https://tracker.example/p.gif"`,
				`alt="pixel"`,
			},
		},
		{
			name:    "cid img kept",
			in:      `<img src="cid:part1.abc" alt="inline">`,
			allowed: []string{`src="cid:part1.abc"`, `alt="inline"`},
		},
		{
			name:    "javascript img src stripped",
			in:      `<img src="javascript:alert(1)" alt="x">`,
			forbid:  []string{"javascript", "src="},
			allowed: []string{`alt="x"`},
		},
		{
			name:    "allowed formatting and table attrs",
			in:      `<b>bold</b><table><tr><td colspan="2" rowspan="3" align="left">cell</td></tr></table>`,
			allowed: []string{"<b>bold</b>", `<td colspan="2" rowspan="3" align="left">`, "cell"},
		},
		{
			name:    "https link gets nofollow",
			in:      `<a href="https://example.com/x">x</a>`,
			allowed: []string{`href="https://example.com/x"`, `rel="nofollow"`},
		},
		{
			name:    "mailto link allowed",
			in:      `<a href="mailto:a@b.example">mail</a>`,
			allowed: []string{`href="mailto:a@b.example"`},
		},
		{
			name:    "plain text survives",
			in:      `hello world`,
			allowed: []string{"hello world"},
		},
		{
			name:    "doctype dropped",
			in:      `<!DOCTYPE html><p>x</p>`,
			forbid:  []string{"DOCTYPE"},
			allowed: []string{"<p>x</p>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HTML(tt.in)
			lower := strings.ToLower(got)
			for _, f := range tt.forbid {
				if strings.Contains(lower, strings.ToLower(f)) {
					t.Errorf("HTML(%q) = %q, must not contain %q", tt.in, got, f)
				}
			}
			for _, a := range tt.allowed {
				if !strings.Contains(got, a) {
					t.Errorf("HTML(%q) = %q, must contain %q", tt.in, got, a)
				}
			}
			if _, err := html.ParseFragment(strings.NewReader(got), &html.Node{
				Type: html.ElementNode, Data: "div", DataAtom: atom.Div,
			}); err != nil {
				t.Errorf("HTML(%q) output does not parse: %v", tt.in, err)
			}
		})
	}
}

func TestPlainText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"paragraphs", `<p>Hello <b>world</b></p><p>Second</p>`, "Hello world\n\nSecond"},
		{"line break", `a<br>b`, "a\nb"},
		{"entities", `5 &lt; 6 &amp; 7 &gt; 2`, "5 < 6 & 7 > 2"},
		{"unordered list", `<ul><li>one</li><li>two</li></ul>`, "- one\n- two"},
		{"ordered list", `<ol><li>one</li><li>two</li></ol>`, "- one\n- two"},
		{"link", `<a href="https://example.com">Example</a>`, "Example (https://example.com)"},
		{"image", `<img src="cid:x" alt="pic">`, "[image: pic]"},
		{"script removed", `<script>alert(1)</script><p>safe</p>`, "safe"},
		{"style removed", `<style>p{color:red}</style><p>safe</p>`, "safe"},
		{"blockquote", `<blockquote>quoted</blockquote>`, "quoted"},
		{"table row", `<table><tr><td>a</td><td>b</td></tr></table>`, "a b"},
		{"empty", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PlainText(tt.in); got != tt.want {
				t.Errorf("PlainText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestReferencesRemoteContent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"remote img", `<img src="https://tracker.example/p.gif">`, true},
		{"mixed case", `<IMG SRC="HTTPS://tracker.example/p.gif">`, true},
		{"scheme with tab", "<img src=\"ht\ttp://tracker.example/p.gif\">", true},
		{"body background", `<body background="http://tracker.example/b.gif">`, true},
		{"style attribute", `<p style="background:url(https://tracker.example/x.png)">x</p>`, true},
		{"style element", `<style>@import url(//cdn.example/x.css);</style>`, true},
		{"link href", `<link rel="stylesheet" href="https://cdn.example/x.css">`, true},
		{"protocol relative", `<img src="//tracker.example/p.gif">`, true},
		{"cid image", `<img src="cid:part1">`, false},
		{"href link", `<a href="https://example.com">x</a>`, true},
		{"plain text", `see https://example.com`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReferencesRemoteContent(tt.in); got != tt.want {
				t.Errorf("ReferencesRemoteContent(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestInitialismsRemoved(t *testing.T) {
	// A dedicated regression: sanitized output must never contain an element
	// the policy does not list, even when bluemonday's parser recovers it.
	for _, in := range []string{
		`<marquee>hi</marquee>`,
		`<article>hi</article>`,
		`<center>hi</center>`,
		`<font color="red">hi</font>`,
		`<canvas>hi</canvas>`,
	} {
		got := HTML(in)
		if strings.Contains(got, "<marquee") || strings.Contains(got, "<article") ||
			strings.Contains(got, "<center") || strings.Contains(got, "<font") ||
			strings.Contains(got, "<canvas") {
			t.Errorf("HTML(%q) = %q, kept a disallowed element", in, got)
		}
	}
}
