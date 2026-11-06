// Package sanitize turns untrusted email HTML into a conservative safe subset.
//
// A zero-value bluemonday policy is used deliberately: bluemonday.NewPolicy
// pre-populates an allowlist of elements, which is wider than the subset btcx
// renders. The policy below is the entire allowlist.
package sanitize

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var policy = newPolicy()

var allowedElements = []string{
	"p", "div", "span", "br", "b", "strong", "i", "em", "u", "s", "strike",
	"small", "big", "sub", "sup", "code", "pre", "blockquote",
	"h1", "h2", "h3", "h4", "h5", "h6",
	"ul", "ol", "li", "dl", "dt", "dd", "hr",
	"table", "thead", "tbody", "tfoot", "tr", "td", "th", "a", "img",
}

func newPolicy() *bluemonday.Policy {
	p := &bluemonday.Policy{}
	p.AllowElements(allowedElements...)
	// A zero-value policy has no default "allowed without attributes" set,
	// and AllowElements alone does not populate it.
	p.AllowAttrs().AllowNoAttrs().OnElements(allowedElements...)
	p.AllowAttrs("href").OnElements("a")
	// Raw image-source attributes must survive this first stage so
	// cleanNode can resolve them (srcset, lazy-load data-src and friends)
	// and withhold the result behind data-blocked-src. bluemonday alone
	// would drop them, hiding images with no way to ever offer them back.
	// cleanNode's filterAttrs strips them all again in every branch, so
	// none of them ever reach the output.
	p.AllowAttrs("src", "alt", "width", "height",
		"srcset", "data-srcset",
		"data-src", "data-original", "data-lazy-src", "data-lazy",
		"data-cfsrc", "data-actualsrc").OnElements("img")
	p.AllowAttrs("align", "colspan", "rowspan").OnElements("td", "th")
	// cid: is needed for inline images; it is stripped from a[href] below.
	p.AllowURLSchemes("http", "https", "mailto", "cid")
	p.RequireNoFollowOnLinks(true)
	p.SkipElementsContent(
		"script", "style", "iframe", "object", "embed", "frame", "frameset",
		"noembed", "noframes", "noscript", "title", "template", "svg", "math",
		"video", "audio", "select", "textarea", "button",
	)
	return p
}

// HTML sanitizes untrusted email HTML and returns the safe subset documented
// in the package comment. It never returns markup that executes script or
// loads remote content.
func HTML(untrusted string) string {
	untrusted = strings.ToValidUTF8(untrusted, "\uFFFD")
	safe := policy.Sanitize(untrusted)
	nodes, err := fragment(safe)
	if err != nil {
		return ""
	}
	for _, n := range nodes {
		cleanNode(n)
	}
	var b strings.Builder
	for _, n := range nodes {
		_ = html.Render(&b, n)
	}
	return b.String()
}

// fragment parses s as a div fragment. A fresh context node is used per call
// so concurrent sanitization stays safe.
func fragment(s string) ([]*html.Node, error) {
	return html.ParseFragment(strings.NewReader(s), &html.Node{
		Type:     html.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
}

// cleanNode enforces the per-attribute URL rules bluemonday cannot express
// per element, and records blocked remote images for the UI.
func cleanNode(n *html.Node) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "a":
			if href := attr(n, "href"); href != "" && !allowedScheme(href, "http", "https", "mailto") {
				removeAttr(n, "href")
			}
			filterAttrs(n, "href", "rel")
		case "img":
			// Never trust a data-blocked-src supplied by the message.
			removeAttr(n, "data-blocked-src")
			// Resolve what the image really points at: a usable src wins,
			// then common lazy-load attributes, then the first srcset
			// candidate. Anything remote is withheld behind data-blocked-src
			// so the UI can offer to load it; filterAttrs drops the lazy
			// attributes along the way.
			src := imageSource(n)
			switch scheme(src) {
			case "cid":
				setAttr(n, "src", src)
				filterAttrs(n, "src", "alt", "width", "height")
			case "http", "https":
				// Keep the element so the UI can show a placeholder and
				// optionally load the image later.
				setAttr(n, "data-blocked-src", stripControls(src))
				setAttr(n, "src", "")
				filterAttrs(n, "src", "alt", "width", "height", blockedSrcAttr)
			default:
				removeAttr(n, "src")
				filterAttrs(n, "src", "alt", "width", "height")
			}
		case "td", "th":
			filterAttrs(n, "align", "colspan", "rowspan")
		default:
			n.Attr = nil
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		cleanNode(c)
	}
}

// imageSource resolves the URL an <img> really points at: a usable src
// wins, then common lazy-load attributes, then the first candidate of
// srcset/data-srcset. It returns the raw attribute value (possibly junk,
// which the caller's scheme switch rejects).
func imageSource(n *html.Node) string {
	src := attr(n, "src")
	if usableImageURL(src) {
		return src
	}
	for _, k := range []string{"data-src", "data-original", "data-lazy-src", "data-lazy", "data-cfsrc", "data-actualsrc"} {
		if v := attr(n, k); usableImageURL(v) {
			return v
		}
	}
	for _, k := range []string{"srcset", "data-srcset"} {
		if u := firstSetCandidate(attr(n, k)); usableImageURL(u) {
			return u
		}
	}
	return src
}

// usableImageURL reports whether v names an image btcx can render or
// withhold (inline cid: reference or http(s)).
func usableImageURL(v string) bool {
	switch scheme(v) {
	case "cid", "http", "https":
		return true
	}
	return false
}

// firstSetCandidate returns the first candidate URL of a srcset value.
func firstSetCandidate(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	f := strings.Fields(v)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// PlainText converts untrusted HTML to readable plain text.
func PlainText(untrusted string) string {
	nodes, err := fragment(HTML(untrusted))
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, n := range nodes {
		writePlainText(&b, n)
	}
	return tidyPlain(b.String())
}

func writePlainText(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(collapseSpaces(n.Data))
		return
	case html.ElementNode:
		switch n.Data {
		case "br":
			b.WriteString("\n")
			return
		case "hr":
			b.WriteString("\n---\n")
			return
		case "img":
			alt := attr(n, "alt")
			if alt == "" {
				alt = "image"
			}
			b.WriteString("[image: " + alt + "]")
			return
		case "a":
			var inner strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writePlainText(&inner, c)
			}
			text := strings.TrimSpace(inner.String())
			href := attr(n, "href")
			if text == "" {
				text = href
			}
			b.WriteString(text)
			if href != "" && href != text {
				b.WriteString(" (" + href + ")")
			}
			return
		case "li":
			b.WriteString("\n- ")
		case "td", "th":
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writePlainText(b, c)
			}
			b.WriteString(" ")
			return
		case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6",
			"blockquote", "pre", "table", "tr", "ul", "ol", "dl", "dt", "dd":
			b.WriteString("\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writePlainText(b, c)
			}
			b.WriteString("\n")
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writePlainText(b, c)
	}
}

func collapseSpaces(s string) string {
	core := strings.Join(strings.Fields(s), " ")
	if core == "" {
		if s == "" {
			return ""
		}
		return " "
	}
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	if unicode.IsSpace(first) {
		core = " " + core
	}
	if unicode.IsSpace(last) {
		core += " "
	}
	return core
}

func tidyPlain(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	s = strings.Join(lines, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}

// ReferencesRemoteContent reports whether rendering this HTML withholds at
// least one remote image — i.e. the UI must offer "Load remote content".
//
// The answer is defined on the sanitizer's own output (data-blocked-src),
// so the offer can never diverge from what is actually hidden: images btcx
// removes outright (styles, link elements, attributes it strips) are not
// mentioned, because nothing could reload them and a button would lie.
func ReferencesRemoteContent(untrusted string) bool {
	z := html.NewTokenizer(strings.NewReader(HTML(untrusted)))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken, html.SelfClosingTagToken:
			for _, a := range z.Token().Attr {
				if strings.EqualFold(a.Key, blockedSrcAttr) {
					return true
				}
			}
		}
	}
}

// blockedSrcAttr is the attribute the sanitizer sets on withheld remote
// images. It is both the withholding marker and the UI's load key.
const blockedSrcAttr = "data-blocked-src"

// stripControls removes ASCII control characters (tabs, CR, LF and other
// bytes < 0x20), mirroring URL parsing: browsers strip them before scheme
// resolution, so "ht\ttp:" is http — smuggled schemes must not slip past.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
}

// scheme returns the lower-cased URL scheme, or "" when raw has none. Leading
// control characters and whitespace are ignored so smuggled schemes such as
// "java\tscript:" are classified consistently.
func scheme(raw string) string {
	s := strings.ToLower(stripControls(raw))
	s = strings.TrimFunc(s, func(r rune) bool { return r == ' ' })
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return ""
	}
	for _, r := range s[:i] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '+' && r != '-' && r != '.' {
			return ""
		}
	}
	return s[:i]
}

func allowedScheme(raw string, schemes ...string) bool {
	sc := scheme(raw)
	for _, s := range schemes {
		if sc == s {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func removeAttr(n *html.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			out = append(out, a)
		}
	}
	n.Attr = out
}

// filterAttrs drops every attribute not named. It is defence in depth on top
// of the bluemonday allowlist.
func filterAttrs(n *html.Node, keep ...string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		for _, k := range keep {
			if a.Key == k {
				out = append(out, a)
				break
			}
		}
	}
	n.Attr = out
}
