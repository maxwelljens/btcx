package render

import (
	"regexp"
	"strings"
)

// urlPattern matches http(s), mailto and bare www URLs. Schemes are
// case-insensitive; quoting and angle brackets end a URL in prose.
var urlPattern = regexp.MustCompile(`(?i)(?:https?://|mailto:|www\.)[^\s<>"']+`)

// trailingPunct is trimmed from a matched URL so "see https://x.example."
// links the URL but leaves the full stop in the sentence.
const trailingPunct = ".,;:!?)]}'\""

// linkify escapes s and wraps URLs in an underlined, coloured span. The URL
// stays visible because a GtkTextView cannot host real link targets here.
func linkify(s string) string {
	matches := urlPattern.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return Escaped(s)
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		// A bare "www." glued to a word ("awww.") is not a URL.
		if start > 0 && strings.HasPrefix(strings.ToLower(s[start:]), "www.") && isWordByte(s[start-1]) {
			continue
		}
		raw := s[start:end]
		trail := ""
		for len(raw) > 0 && strings.IndexByte(trailingPunct, raw[len(raw)-1]) >= 0 {
			trail = raw[len(raw)-1:] + trail
			raw = raw[:len(raw)-1]
		}
		if raw == "" {
			continue
		}
		b.WriteString(Escaped(s[last:start]))
		b.WriteString(`<span underline="single" foreground="#3584e4">`)
		b.WriteString(Escaped(raw))
		b.WriteString("</span>")
		b.WriteString(Escaped(trail))
		last = end
	}
	b.WriteString(Escaped(s[last:]))
	return b.String()
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '@' || c == '.' || c == '-' || c == '_'
}
