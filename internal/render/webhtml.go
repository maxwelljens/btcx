package render

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"

	xhtml "golang.org/x/net/html"

	"btcx/internal/sanitize"
)

// DataURI encodes data as a base64 data URI with the given content type.
// An empty content type falls back to application/octet-stream.
func DataURI(contentType string, data []byte) string {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// Strip any parameters (e.g. "image/png; name=x").
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = strings.TrimSpace(contentType[:i])
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// WebHTML prepares untrusted HTML for the embedded WebKit view.
//
// The input is sanitized first (defense in depth, like Pango), then every
// image reference is resolved to an inline data URI so the view never
// performs network requests: cid: references and previously blocked remote
// URLs are looked up in images (keys "cid:<id>" and the absolute URL); any
// remaining or unresolvable image loses its src (alt text remains).
// The result is a complete UTF-8 document with a minimal btcx stylesheet —
// never attacker-controlled <style> (the sanitizer strips those).
func WebHTML(untrusted string, images map[string]string) string {
	safe := sanitize.HTML(capInput(untrusted))
	doc, err := xhtml.Parse(strings.NewReader(safe))
	if err != nil {
		// Unparseable input: fall back to escaped text, never raw markup.
		return fullDocument("<pre>" + Escaped(safe) + "</pre>")
	}
	forEachElement(doc, func(n *xhtml.Node) {
		if !strings.EqualFold(n.Data, "img") {
			return
		}
		src := htmlAttr(n, "src")
		key := ""
		switch {
		case strings.HasPrefix(src, "cid:"):
			key = src
		case htmlAttr(n, "data-blocked-src") != "":
			key = htmlAttr(n, "data-blocked-src")
		}
		if key != "" {
			if uri, ok := images[key]; ok {
				setAttr(n, "src", uri)
				removeAttr(n, "data-blocked-src")
				return
			}
		}
		// Unresolvable or unexpected reference: show alt text only. Ensure
		// an alt exists so the web view renders a visible placeholder
		// instead of an empty box.
		removeAttr(n, "src")
		removeAttr(n, "data-blocked-src")
		if htmlAttr(n, "alt") == "" {
			setAttr(n, "alt", "[image]")
		}
	})

	var body bytes.Buffer
	body.WriteString("<!doctype html>\n<html><head><meta charset=\"utf-8\">")
	body.WriteString("<style>body{font-family:sans-serif;margin:10px;}")
	body.WriteString("pre{white-space:pre-wrap;}</style></head><body>")
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		// Skip the html/head/body wrappers xhtml.Parse adds; render the
		// document element's children directly.
		if c.Type == xhtml.ElementNode && strings.EqualFold(c.Data, "html") {
			for gc := c.FirstChild; gc != nil; gc = gc.NextSibling {
				if gc.Type == xhtml.ElementNode && strings.EqualFold(gc.Data, "head") {
					continue
				}
				if gc.Type == xhtml.ElementNode && strings.EqualFold(gc.Data, "body") {
					for b := gc.FirstChild; b != nil; b = b.NextSibling {
						if err := xhtml.Render(&body, b); err != nil {
							fmt.Fprintf(&body, "<!-- render error: %v -->", err)
						}
					}
					continue
				}
				if err := xhtml.Render(&body, gc); err != nil {
					fmt.Fprintf(&body, "<!-- render error: %v -->", err)
				}
			}
			continue
		}
		if err := xhtml.Render(&body, c); err != nil {
			fmt.Fprintf(&body, "<!-- render error: %v -->", err)
		}
	}
	body.WriteString("</body></html>")
	return body.String()
}

// forEachElement visits every element node in the tree.
func forEachElement(n *xhtml.Node, fn func(*xhtml.Node)) {
	if n.Type == xhtml.ElementNode {
		fn(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		forEachElement(c, fn)
	}
}

func htmlAttr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *xhtml.Node, key, value string) {
	for i, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = append(n.Attr, xhtml.Attribute{Key: key, Val: value})
}

func removeAttr(n *xhtml.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if !strings.EqualFold(a.Key, key) {
			out = append(out, a)
		}
	}
	n.Attr = out
}

func fullDocument(inner string) string {
	return "<!doctype html>\n<html><head><meta charset=\"utf-8\"></head><body>" + inner + "</body></html>"
}
