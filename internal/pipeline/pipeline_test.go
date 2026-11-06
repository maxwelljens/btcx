package pipeline_test

// End-to-end security pipeline tests (AGENTS.md §10). Every fixture is a real
// RFC 5322 message; each test first proves the fixture still carries its
// attack, then drives the parse -> sanitize -> render pipeline and asserts the
// attack cannot survive. A fixture silently weakened by a later edit fails the
// positive control rather than passing vacuously.

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"btcx/internal/compose"
	"btcx/internal/mailparse"
	"btcx/internal/model"
	"btcx/internal/render"
	"btcx/internal/sanitize"
)

var (
	testDate = time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	sender   = model.Address{Name: "Victim", Addr: "victim@example.org"}

	messageIDRe = regexp.MustCompile(`^<[^@<>\s]+@[^@<>\s]+>$`)
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func mustParse(t *testing.T, raw []byte) *model.Message {
	t.Helper()
	m, err := mailparse.ParseBytes(raw)
	if err != nil {
		t.Fatalf("mailparse.ParseBytes: %v", err)
	}
	return m
}

func buildOpts(from model.Address) compose.Options {
	return compose.Options{
		From:            from,
		MessageIDDomain: "btcx.example",
		UserAgent:       "btcx-test",
		Date:            testDate,
	}
}

func mustBuild(t *testing.T, m *model.Message) []byte {
	t.Helper()
	raw, err := compose.Build(m, buildOpts(sender))
	if err != nil {
		t.Fatalf("compose.Build: %v", err)
	}
	return raw
}

func mustParseBuilt(t *testing.T, raw []byte) *model.Message {
	t.Helper()
	m, err := mailparse.ParseBytes(raw)
	if err != nil {
		t.Fatalf("mailparse.ParseBytes(built): %v\n%q", err, raw)
	}
	return m
}

// deniedElements are the elements that must never reach the UI as live markup.
var deniedElements = map[string]bool{
	"script": true, "iframe": true, "object": true, "embed": true,
	"frame": true, "frameset": true, "form": true, "input": true,
	"meta": true, "base": true, "link": true, "style": true,
	"svg": true, "math": true, "applet": true, "video": true,
	"audio": true, "select": true, "textarea": true, "button": true,
	"template": true, "noscript": true,
}

func fragment(t *testing.T, s string) []*html.Node {
	t.Helper()
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(s), ctx)
	if err != nil {
		t.Fatalf("html.ParseFragment(%q): %v", s, err)
	}
	return nodes
}

func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

// dangerousURL reports a scheme that must not survive sanitization, ignoring
// case and whitespace/control smuggling inside the scheme.
func dangerousURL(v string) bool {
	s := strings.Map(func(r rune) rune {
		if r <= ' ' {
			return -1
		}
		return r
	}, strings.ToLower(v))
	for _, sc := range []string{"javascript:", "vbscript:", "data:"} {
		if strings.HasPrefix(s, sc) {
			return true
		}
	}
	return false
}

func isRemoteURL(v string) bool {
	s := strings.Map(func(r rune) rune {
		if r <= ' ' {
			return -1
		}
		return r
	}, strings.ToLower(v))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "//")
}

func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func findElement(nodes []*html.Node, name string) *html.Node {
	var found *html.Node
	for _, n := range nodes {
		walk(n, func(n *html.Node) {
			if found == nil && n.Type == html.ElementNode && n.Data == name {
				found = n
			}
		})
	}
	return found
}

// checkSanitized walks the real DOM of sanitized output: no denied element,
// no event-handler or style attribute, no script-capable URL scheme, and no
// remote resource left loadable.
func checkSanitized(t *testing.T, safe string) {
	t.Helper()
	if strings.Contains(strings.ToLower(safe), "<script") {
		t.Errorf("sanitized HTML still contains '<script': %q", safe)
	}
	for _, root := range fragment(t, safe) {
		walk(root, func(n *html.Node) {
			if n.Type != html.ElementNode {
				return
			}
			name := strings.ToLower(n.Data)
			if deniedElements[name] {
				t.Errorf("sanitized HTML kept <%s>: %q", name, safe)
			}
			for _, a := range n.Attr {
				key := strings.ToLower(a.Key)
				if strings.HasPrefix(key, "on") {
					t.Errorf("<%s> kept event handler %q: %q", name, key, safe)
				}
				if key == "style" {
					t.Errorf("<%s> kept style attribute: %q", name, safe)
				}
				if key == "href" || key == "src" {
					if dangerousURL(a.Val) {
						t.Errorf("<%s> kept dangerous URL %q: %q", name, a.Val, safe)
					}
				}
				if name == "img" && key == "src" && isRemoteURL(a.Val) {
					t.Errorf("remote image source survived sanitization: %q", safe)
				}
			}
		})
	}
}

// checkPango proves the renderer output is well-formed Pango markup carrying no
// executable element or event-handler attribute.
func checkPango(t *testing.T, markup string) {
	t.Helper()
	if strings.Contains(strings.ToLower(markup), "<script") {
		t.Errorf("Pango markup contains '<script': %q", markup)
	}
	dec := xml.NewDecoder(strings.NewReader("<markup>" + markup + "</markup>"))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("Pango markup is not well-formed XML: %v\nmarkup: %q", err, markup)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		name := strings.ToLower(se.Name.Local)
		if deniedElements[name] {
			t.Errorf("Pango markup contains denied element <%s>: %q", name, markup)
		}
		for _, a := range se.Attr {
			if strings.HasPrefix(strings.ToLower(a.Name.Local), "on") {
				t.Errorf("Pango markup contains event handler %q: %q", a.Name.Local, markup)
			}
		}
	}
}

func checkHeaderValues(t *testing.T, h model.Header) {
	t.Helper()
	for _, f := range h.Fields() {
		if strings.ContainsAny(f.Key, ": \t\r\n") {
			t.Errorf("header name %q could split into two fields", f.Key)
		}
		if strings.ContainsAny(f.Value, "\r\n\x00") {
			t.Errorf("header %s value carries a line break or NUL: %q", f.Key, f.Value)
		}
	}
}

// TestXSSPipeline pushes every hostile HTML fixture through parse -> sanitize
// -> render and proves the attack cannot survive as live markup.
func TestXSSPipeline(t *testing.T) {
	fixtures := []struct {
		name   string
		file   string
		attack string
	}{
		{"script", "hostile_script.eml", "<script>alert(1)</script>"},
		{"img onerror", "hostile_img_onerror.eml", `onerror="alert(1)"`},
		{"javascript href", "hostile_javascript_href.eml", "JaVaScRiPt:alert(1)"},
		{"iframe", "hostile_iframe.eml", `<iframe src="https://evil.example/phish">`},
		{"svg onload", "hostile_svg_onload.eml", `<svg onload="alert(1)">`},
		{"style import", "hostile_style_import.eml", `@import url("https://evil.example/x.css")`},
		{"meta refresh", "hostile_meta_refresh.eml", `<meta http-equiv="refresh"`},
		{"mxss comment", "hostile_mxss_comment.eml", "<!--><script>alert(1)</script>"},
		{"mxss table", "hostile_mxss_table.eml", "onerror=alert(1)"},
		{"data url", "hostile_data_url.eml", `href="data:text/html,<script>alert(1)</script>"`},
	}

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			raw := readFixture(t, tc.file)
			// Positive control: the fixture must still carry the attack.
			if !bytes.Contains(raw, []byte(tc.attack)) {
				t.Fatalf("fixture %s no longer contains %q; refusing to pass vacuously", tc.file, tc.attack)
			}
			m := mustParse(t, raw)
			if m.HTML == "" {
				t.Fatalf("fixture %s produced no HTML body", tc.file)
			}

			safe := sanitize.HTML(m.HTML)
			checkSanitized(t, safe)

			pango := render.Pango(m.HTML)
			checkPango(t, pango)

			plain := sanitize.PlainText(m.HTML)
			if strings.Contains(plain, "<") {
				t.Errorf("plain text contains markup: %q", plain)
			}
		})
	}
}

func TestRemoteContentPrivacy(t *testing.T) {
	raw := readFixture(t, "hostile_remote_img.eml")
	if !bytes.Contains(raw, []byte("https://tracker.example/p.gif")) ||
		!bytes.Contains(raw, []byte("https://example.com/story")) {
		t.Fatal("fixture lost its remote references")
	}
	m := mustParse(t, raw)

	if !sanitize.ReferencesRemoteContent(m.HTML) {
		t.Error("ReferencesRemoteContent = false, want true for a tracking pixel")
	}

	safe := sanitize.HTML(m.HTML)
	img := findElement(fragment(t, safe), "img")
	if img == nil {
		t.Fatalf("sanitized HTML dropped the image placeholder: %q", safe)
	}
	if got := attrOf(img, "src"); got != "" {
		t.Errorf("img src = %q, want empty (remote content blocked)", got)
	}
	if got, want := attrOf(img, "data-blocked-src"), "https://tracker.example/p.gif"; got != want {
		t.Errorf("data-blocked-src = %q, want %q", got, want)
	}

	pango := render.Pango(m.HTML)
	checkPango(t, pango)
	if !strings.Contains(pango, "[blocked remote image") {
		t.Errorf("no blocked-image placeholder in %q", pango)
	}
	if !strings.Contains(pango, "https://example.com/story") {
		t.Errorf("link URL not visible to the user in %q", pango)
	}
	if strings.Contains(pango, "tracker.example") {
		t.Errorf("tracking URL leaked into rendered output: %q", pango)
	}
}

func TestInlineCIDImage(t *testing.T) {
	raw := readFixture(t, "hostile_inline_cid.eml")
	if !bytes.Contains(raw, []byte(`src="cid:logo"`)) {
		t.Fatal("fixture lost its cid image")
	}
	m := mustParse(t, raw)
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(m.Attachments))
	}
	att := m.Attachments[0]
	if !att.Inline {
		t.Error("inline attachment not marked Inline")
	}
	if att.ContentID != "logo" {
		t.Errorf("ContentID = %q, want logo", att.ContentID)
	}
	if att.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", att.ContentType)
	}

	safe := sanitize.HTML(m.HTML)
	img := findElement(fragment(t, safe), "img")
	if img == nil || attrOf(img, "src") != "cid:logo" {
		t.Errorf("cid image not preserved: %q", safe)
	}

	pango := render.Pango(m.HTML)
	checkPango(t, pango)
	if !strings.Contains(pango, "[inline image: logo]") {
		t.Errorf("no inline-image placeholder in %q", pango)
	}
	if strings.Contains(pango, "iVBOR") || len(pango) > 4096 {
		t.Errorf("inline image rendered as data, not a placeholder (%d bytes)", len(pango))
	}
}

func TestHeaderInjection(t *testing.T) {
	raw := readFixture(t, "hostile_header_injection.eml")
	if !bytes.Contains(raw, []byte("\r\n Bcc: shadow@evil.example")) {
		t.Fatal("fixture lost its folded Bcc injection")
	}
	m := mustParse(t, raw)

	if got, want := m.Header.Len(), 7; got != want {
		t.Errorf("parsed header has %d fields, want %d: %+v", got, want, m.Header.Fields())
	}
	if m.Header.Has("Bcc") {
		t.Error("folded injection created a real Bcc field")
	}
	if strings.ContainsAny(m.Subject(), "\r\n") {
		t.Errorf("Subject carries a line break: %q", m.Subject())
	}
	checkHeaderValues(t, m.Header)

	reply := compose.Reply(m, sender, "no thanks")
	built := mustBuild(t, reply)
	back := mustParseBuilt(t, built)

	if back.Header.Has("Bcc") {
		t.Error("built reply grew a Bcc field")
	}
	if back.Header.Has("X-Injected") {
		t.Error("built reply grew an injected X-Injected field")
	}
	checkHeaderValues(t, back.Header)
}

func TestComposeRejectsInjection(t *testing.T) {
	t.Run("subject CRLF", func(t *testing.T) {
		m := &model.Message{Header: model.NewHeader(), Plain: "body"}
		m.Header.Set("Subject", "a\r\nBcc: evil@x")
		if _, err := compose.Build(m, buildOpts(sender)); err == nil {
			t.Error("Build accepted a CRLF in Subject")
		}
	})
	t.Run("address name CRLF", func(t *testing.T) {
		m := &model.Message{Header: model.NewHeader(), Plain: "body"}
		bad := model.Address{Name: "Alice\r\nBcc: evil@x", Addr: "alice@example.org"}
		if _, err := compose.Build(m, buildOpts(bad)); err == nil {
			t.Error("Build accepted a CRLF in the From display name")
		}
	})
	t.Run("header NUL", func(t *testing.T) {
		if err := compose.ValidateHeader("a\x00b"); err == nil {
			t.Error("ValidateHeader accepted NUL")
		}
	})
	t.Run("message id charset", func(t *testing.T) {
		seen := make(map[string]bool, 50)
		for i := 0; i < 50; i++ {
			built := mustBuild(t, &model.Message{Header: model.NewHeader(), Plain: "hi"})
			id := mustParseBuilt(t, built).Header.Get("Message-ID")
			if !messageIDRe.MatchString(id) {
				t.Fatalf("Message-ID %q does not match %s", id, messageIDRe)
			}
			if seen[id] {
				t.Fatalf("duplicate Message-ID %q", id)
			}
			seen[id] = true
		}
	})
}

// safeAttachmentName is the documented consumer operation on an untrusted
// attachment filename (AGENTS.md § Security 5): strip controls and path
// separators, then take the base name.
func safeAttachmentName(name string) string {
	name = strings.NewReplacer("\\", "/", "\r", "", "\n", "", "\x00", "").Replace(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = filepath.Base(name)
	switch name {
	case ".", "..", "/":
		return ""
	}
	return strings.TrimSpace(name)
}

func TestAttachmentFilenameTraversal(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		attack string
		want   string
	}{
		{"rfc2231", "hostile_filename_2231.eml", "%2F..%2Fetc%2Fpasswd", "passwd"},
		{"quoted", "hostile_filename_traversal.eml", `filename="../../etc/passwd"`, "passwd"},
		{"crlf", "hostile_filename_crlf.eml", "%0D%0A", "evilInjected.txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := readFixture(t, tc.file)
			if !bytes.Contains(raw, []byte(tc.attack)) {
				t.Fatalf("fixture lost its attack %q", tc.attack)
			}
			m := mustParse(t, raw)
			if len(m.Attachments) != 1 {
				t.Fatalf("attachments = %d, want 1", len(m.Attachments))
			}
			parsed := m.Attachments[0].Filename
			safe := safeAttachmentName(parsed)
			if safe != tc.want {
				t.Errorf("safeAttachmentName(%q) = %q, want %q", parsed, safe, tc.want)
			}
			if strings.ContainsAny(safe, `/\`) || strings.Contains(safe, "..") {
				t.Errorf("sanitized filename %q still escapes its directory", safe)
			}
			if strings.ContainsAny(safe, "\r\n") {
				t.Errorf("sanitized filename %q carries a line break", safe)
			}
		})
	}
}

// fillerReader produces an endless 'a' body without allocating a 64 MiB
// fixture; the parser's byte budget is what we are testing.
type fillerReader struct{}

func (fillerReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func nestedMessage(depth int) string {
	part := "Content-Type: text/plain\r\n\r\ndeep"
	for i := 0; i < depth; i++ {
		b := "b" + strings.Repeat("x", i+1)
		part = "Content-Type: multipart/mixed; boundary=" + b + "\r\n\r\n--" + b + "\r\n" +
			part + "\r\n--" + b + "--\r\n"
	}
	return "From: x@example.org\r\nMIME-Version: 1.0\r\n" + part
}

func TestResourceCaps(t *testing.T) {
	// mailparse keeps its sentinel errors unexported, so the messages are the
	// contract observed from outside the package.
	t.Run("nesting depth", func(t *testing.T) {
		raw := nestedMessage(35)
		if !strings.Contains(raw, "multipart/mixed") {
			t.Fatal("fixture is not nested")
		}
		_, err := mailparse.ParseBytes([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), "too deep") {
			t.Fatalf("depth 35: err = %v, want a nesting-too-deep error", err)
		}
	})
	t.Run("part count", func(t *testing.T) {
		raw := "Content-Type: multipart/mixed; boundary=x\r\n\r\n" +
			strings.Repeat("--x\r\n\r\n", 600) + "--x--\r\n"
		_, err := mailparse.ParseBytes([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), "too many") {
			t.Fatalf("600 parts: err = %v, want a too-many-parts error", err)
		}
	})
	t.Run("decoded size", func(t *testing.T) {
		if testing.Short() {
			t.Skip("reads a 64 MiB body")
		}
		head := "From: x@example.org\r\nMIME-Version: 1.0\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n"
		r := io.MultiReader(strings.NewReader(head), io.LimitReader(fillerReader{}, 64<<20+1))
		_, err := mailparse.Parse(r)
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("64 MiB + 1 part: err = %v, want a body-too-large error", err)
		}
	})
}

func TestRoundTripQuotesHostileBody(t *testing.T) {
	raw := readFixture(t, "hostile_roundtrip.eml")
	if !bytes.Contains(raw, []byte(`<script>alert("rt")</script>`)) {
		t.Fatal("fixture lost its script payload")
	}
	orig := mustParse(t, raw)
	if orig.HTML == "" || orig.Plain == "" {
		t.Fatalf("fixture must carry both bodies: plain=%q html=%q", orig.Plain, orig.HTML)
	}

	built := mustBuild(t, compose.Reply(orig, sender, "thanks"))
	back := mustParseBuilt(t, built)

	// The hostile markup survives only as quoted plain text.
	if !strings.Contains(back.Plain, `> Look: <script>alert("rt")</script>`) {
		t.Errorf("quoted body lost the payload: %q", back.Plain)
	}
	if strings.Contains(back.HTML, "<script") {
		t.Errorf("reply HTML carries live markup: %q", back.HTML)
	}

	pango := render.PangoFromPlain(back.Plain)
	checkPango(t, pango)
	if !strings.Contains(pango, "&lt;script&gt;") {
		t.Errorf("quoted payload not escaped for display: %q", pango)
	}
}

func TestSMTPStuffingShape(t *testing.T) {
	body := "first line\r\n.\r\nsecond line\r\n.hidden\r\nline .hidden\r\nlast"
	m := &model.Message{Header: model.NewHeader(), Plain: body}
	built := mustBuild(t, m)

	// Build must leave the dot-stuffing shape for the transport to handle.
	if !bytes.Contains(built, []byte("\r\n.\r\n")) {
		t.Errorf("lone dot line lost by Build: %q", built)
	}
	if !bytes.Contains(built, []byte("\r\n.hidden\r\n")) {
		t.Errorf("leading-dot line lost by Build: %q", built)
	}

	back := mustParseBuilt(t, built)
	got := strings.ReplaceAll(back.Plain, "\r\n", "\n")
	want := strings.ReplaceAll(body, "\r\n", "\n")
	if got != want {
		t.Errorf("body corrupted by Build round trip:\n got %q\nwant %q", got, want)
	}
}
