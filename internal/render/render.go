// Package render converts untrusted mail content into Pango markup for a
// GtkTextView. HTML is sanitized first; every piece of text is XML-escaped,
// so the output is always well-formed Pango markup.
package render

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"btcx/internal/sanitize"
)

// maxInput caps the amount of markup handed to Pango. Larger bodies are
// truncated; Pango layouts are expensive and the tail is not worth the cost.
const maxInput = 5 << 20

const truncatedNotice = "\n\n[message truncated]"

// Pango converts untrusted HTML to Pango markup. The HTML is sanitized first,
// even if the caller claims it is already clean.
func Pango(untrusted string) string {
	safe := sanitize.HTML(capInput(untrusted))
	nodes, err := fragment(safe)
	if err != nil {
		return Escaped(safe)
	}
	r := &renderer{}
	for _, n := range nodes {
		r.node(n)
	}
	return tidy(r.b.String())
}

// PangoFromPlain converts plain text to Pango markup. Everything is escaped;
// http(s), mailto and www URLs are turned into underlined, coloured spans and
// leading "> " quote levels become italic.
func PangoFromPlain(text string) string {
	text = strings.ReplaceAll(capInput(text), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		var b strings.Builder
		quotes := 0
		for {
			if strings.HasPrefix(line, "> ") {
				line = line[2:]
				quotes++
			} else if strings.HasPrefix(line, ">") {
				line = line[1:]
				quotes++
			} else {
				break
			}
		}
		line = strings.TrimPrefix(line, " ")
		if quotes > 0 {
			b.WriteString("<i>")
			b.WriteString(strings.Repeat("&gt; ", quotes))
		}
		b.WriteString(linkify(line))
		if quotes > 0 {
			b.WriteString("</i>")
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

// Escaped returns s with Pango (XML) entities escaped. Characters that XML 1.0
// forbids are dropped, otherwise the markup would not parse.
func Escaped(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xFFFE || r == 0xFFFF {
			return -1
		}
		return r
	}, s)
	return escaper.Replace(s)
}

var escaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\"", "&quot;",
)

func capInput(s string) string {
	if len(s) > maxInput {
		cut := s[:maxInput]
		// Do not leave the truncation notice inside a half-written tag.
		if i := strings.LastIndexByte(cut, '<'); i > strings.LastIndexByte(cut, '>') {
			cut = cut[:i]
		}
		s = cut + truncatedNotice
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

func fragment(s string) ([]*html.Node, error) {
	return html.ParseFragment(strings.NewReader(s), &html.Node{
		Type:     html.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
}

type renderer struct {
	b     strings.Builder
	lists []list
}

type list struct {
	ordered bool
	n       int
}

func (r *renderer) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		r.b.WriteString(Escaped(n.Data))
	case html.ElementNode:
		switch n.Data {
		case "b", "strong":
			r.inline("b", "b", n)
		case "i", "em":
			r.inline("i", "i", n)
		case "u":
			r.inline("u", "u", n)
		case "s", "strike":
			r.inline("s", "s", n)
		case "small":
			r.inline("small", "small", n)
		case "big":
			r.inline("big", "big", n)
		case "sub":
			r.inline("sub", "sub", n)
		case "sup":
			r.inline("sup", "sup", n)
		case "code", "tt", "pre":
			r.inline(`span font_family="monospace"`, "span", n)
		case "br":
			r.b.WriteString("\n")
		case "hr":
			r.b.WriteString("\n\n---\n\n")
		case "p", "div", "dl":
			r.block(n)
		case "dt":
			r.b.WriteString("\n<b>")
			r.children(n)
			r.b.WriteString("</b>\n")
		case "dd":
			r.block(n)
		case "h1", "h2", "h3", "h4", "h5", "h6":
			r.heading(n)
		case "blockquote":
			r.quote(n)
		case "ul":
			r.list(n, false)
		case "ol":
			r.list(n, true)
		case "li":
			r.item(n)
		case "table":
			r.block(n)
		case "tr":
			r.row(n)
		case "a":
			r.link(n)
		case "img":
			r.image(n)
		default:
			r.children(n)
		}
	}
}

func (r *renderer) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		r.node(c)
	}
}

func (r *renderer) inline(open, close string, n *html.Node) {
	r.b.WriteString("<" + open + ">")
	r.children(n)
	r.b.WriteString("</" + close + ">")
}

func (r *renderer) block(n *html.Node) {
	r.b.WriteString("\n\n")
	r.children(n)
	r.b.WriteString("\n\n")
}

func (r *renderer) heading(n *html.Node) {
	size := "large"
	switch n.Data {
	case "h1", "h2", "h3":
		size = "x-large"
	}
	r.b.WriteString("\n\n<span size=\"" + size + "\"><b>")
	r.children(n)
	r.b.WriteString("</b></span>\n\n")
}

func (r *renderer) quote(n *html.Node) {
	sub := &renderer{lists: r.lists}
	sub.children(n)
	content := strings.Trim(sub.b.String(), "\n")
	content = "&gt; " + strings.ReplaceAll(content, "\n", "\n&gt; ")
	r.b.WriteString("\n\n<i>" + content + "</i>\n\n")
}

func (r *renderer) list(n *html.Node, ordered bool) {
	r.b.WriteString("\n")
	r.lists = append(r.lists, list{ordered: ordered})
	r.children(n)
	r.lists = r.lists[:len(r.lists)-1]
	r.b.WriteString("\n")
}

func (r *renderer) item(n *html.Node) {
	r.b.WriteString("\n")
	indent := 0
	if len(r.lists) > 0 {
		indent = len(r.lists) - 1
	}
	r.b.WriteString(strings.Repeat("  ", indent))
	if len(r.lists) > 0 && r.lists[len(r.lists)-1].ordered {
		r.lists[len(r.lists)-1].n++
		fmt.Fprintf(&r.b, "%d. ", r.lists[len(r.lists)-1].n)
	} else {
		r.b.WriteString("• ")
	}
	r.children(n)
}

func (r *renderer) row(n *html.Node) {
	r.b.WriteString("\n")
	first := true
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || (c.Data != "td" && c.Data != "th") {
			continue
		}
		if !first {
			r.b.WriteString(" | ")
		}
		first = false
		cell := &renderer{lists: r.lists}
		cell.children(c)
		text := strings.TrimSpace(strings.ReplaceAll(cell.b.String(), "\n", " "))
		if c.Data == "th" {
			r.b.WriteString("<b>" + text + "</b>")
		} else {
			r.b.WriteString(text)
		}
	}
}

func (r *renderer) link(n *html.Node) {
	href := attr(n, "href")
	if !allowedLink(href) {
		r.children(n)
		return
	}
	r.b.WriteString(`<span underline="single" foreground="#3584e4">`)
	r.children(n)
	r.b.WriteString(" (" + Escaped(href) + ")</span>")
}

func (r *renderer) image(n *html.Node) {
	alt := attr(n, "alt")
	label := alt
	if label == "" {
		label = "no description"
	}
	switch {
	case attr(n, "data-blocked-src") != "":
		r.b.WriteString("[blocked remote image: " + Escaped(label) + "]")
	case strings.EqualFold(scheme(attr(n, "src")), "cid"):
		r.b.WriteString("[inline image: " + Escaped(label) + "]")
	case alt != "":
		r.b.WriteString("[image: " + Escaped(alt) + "]")
	}
}

// tidy trims trailing whitespace, collapses runs of blank lines, and drops
// leading/trailing newlines. It never touches tags: generated tags contain no
// newlines.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	s = strings.Join(lines, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.Trim(s, "\n")
}

func allowedLink(href string) bool {
	switch scheme(href) {
	case "http", "https", "mailto":
		return true
	}
	return false
}

// scheme mirrors the sanitizer's parsing so render never trusts an attribute
// merely because it arrived pre-sanitized.
func scheme(raw string) string {
	s := strings.ToLower(strings.TrimFunc(raw, func(r rune) bool { return r <= ' ' }))
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

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
