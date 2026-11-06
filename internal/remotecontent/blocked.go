package remotecontent

import (
	"strings"

	xhtml "golang.org/x/net/html"

	"btcx/internal/sanitize"
)

// BlockedImages extracts the remote image URLs the sanitizer withheld, in
// document order and without duplicates. The body is sanitized first, so the
// attributes are the ones the display pipeline would have produced.
func BlockedImages(htmlBody string) []string {
	if strings.TrimSpace(htmlBody) == "" {
		return nil
	}
	doc, err := xhtml.Parse(strings.NewReader(sanitize.HTML(htmlBody)))
	if err != nil {
		return nil
	}
	var (
		out  []string
		seen = map[string]bool{}
	)
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		// Any element the sanitizer withheld an image on (today: img).
		if n.Type == xhtml.ElementNode && hasAttrNode(n, "data-blocked-src") {
			for _, a := range n.Attr {
				if a.Key == "data-blocked-src" && a.Val != "" && !seen[a.Val] {
					seen[a.Val] = true
					out = append(out, a.Val)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return out
}

// hasAttrNode reports whether n carries key (case-insensitively).
func hasAttrNode(n *xhtml.Node, key string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return true
		}
	}
	return false
}
