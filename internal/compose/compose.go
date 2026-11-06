// Package compose renders model.Message values as complete RFC 5322 / MIME
// messages ready for SMTP submission.
//
// btcx owns the envelope headers: Date, Message-ID, MIME-Version and every
// Content-* field are generated here, never taken from the input message.
// Header values are validated for CR/LF/NUL first: mail headers are a trust
// boundary, and a newline smuggled into a Subject or display name is a header
// injection.
package compose

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/go-message"

	"btcx/internal/model"
)

// Options control message generation.
type Options struct {
	From            model.Address // required
	MessageIDDomain string        // used to mint Message-ID; default "localhost"
	UserAgent       string        // X-Mailer header value; default "btcx"
	Date            time.Time     // zero = time.Now()
}

// Build renders m as a complete RFC 5322 message ready for SMTP submission.
// The MIME structure is chosen from the message content: plain, alternative,
// mixed with attachments, and related when inline parts are present.
func Build(m *model.Message, opts Options) ([]byte, error) {
	if m == nil {
		return nil, errors.New("compose: nil message")
	}
	if opts.From.Addr == "" {
		return nil, errors.New("compose: From address is required")
	}
	if err := validateAddress(opts.From); err != nil {
		return nil, err
	}
	header, err := messageHeader(m, opts)
	if err != nil {
		return nil, err
	}
	root := messageTree(m)
	header.SetContentType(root.media, root.params)

	var buf bytes.Buffer
	if err := writeEntity(&buf, nil, header, root); err != nil {
		return nil, fmt.Errorf("compose: build: %w", err)
	}
	return buf.Bytes(), nil
}

// ValidateHeader checks a single user-supplied header value for CR/LF/NUL.
func ValidateHeader(value string) error {
	if i := strings.IndexAny(value, "\r\n\x00"); i >= 0 {
		return fmt.Errorf("compose: header value contains illegal byte %q", value[i])
	}
	return nil
}

// messageHeader assembles the top-level header: the envelope fields btcx
// owns, plus every other field carried by m.Header (so In-Reply-To,
// References and custom X-* headers survive a round trip).
func messageHeader(m *model.Message, opts Options) (message.Header, error) {
	var h message.Header

	domain := opts.MessageIDDomain
	if domain == "" {
		domain = "localhost"
	}
	if err := validateToken(domain, "Message-ID domain"); err != nil {
		return h, err
	}
	agent := opts.UserAgent
	if agent == "" {
		agent = "btcx"
	}
	if err := ValidateHeader(agent); err != nil {
		return h, fmt.Errorf("compose: X-Mailer: %w", err)
	}

	h.Set("From", opts.From.String())
	for _, key := range []string{"To", "Cc", "Bcc", "Reply-To"} {
		addrs, err := headerAddresses(m.Header.Get(key))
		if err != nil {
			return h, fmt.Errorf("compose: %s: %w", key, err)
		}
		if len(addrs) > 0 {
			h.Set(key, model.FormatAddressList(addrs))
		}
	}

	subject := m.Subject()
	if err := ValidateHeader(subject); err != nil {
		return h, fmt.Errorf("compose: Subject: %w", err)
	}
	if subject != "" {
		h.SetText("Subject", subject) // RFC 2047 for non-ASCII
	}

	date := opts.Date
	if date.IsZero() {
		date = time.Now()
	}
	h.Set("Date", date.Format(time.RFC1123Z))

	id, err := mintMessageID(domain)
	if err != nil {
		return h, err
	}
	h.Set("Message-ID", id)
	h.Set("X-Mailer", agent)

	for _, f := range m.Header.Fields() {
		if ownedHeader(f.Key) {
			continue
		}
		if err := validateFieldName(f.Key); err != nil {
			return h, err
		}
		if err := ValidateHeader(f.Value); err != nil {
			return h, fmt.Errorf("compose: header %s: %w", f.Key, err)
		}
		h.Add(f.Key, f.Value)
	}
	return h, nil
}

// validateFieldName rejects a field name that could split into two headers.
func validateFieldName(key string) error {
	if key == "" || strings.ContainsAny(key, ": \t\r\n\x00") {
		return fmt.Errorf("compose: invalid header name %q", key)
	}
	return nil
}

// ownedHeader reports whether Build generates the field itself instead of
// copying it from the input message.
func ownedHeader(key string) bool {
	switch strings.ToLower(key) {
	case "from", "to", "cc", "bcc", "reply-to", "subject", "date",
		"message-id", "mime-version", "x-mailer":
		return true
	}
	return strings.HasPrefix(strings.ToLower(key), "content-")
}

// headerAddresses validates and parses a raw address header value.
func headerAddresses(value string) ([]model.Address, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	if err := ValidateHeader(value); err != nil {
		return nil, err
	}
	addrs, err := model.ParseAddressList(value)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if err := validateAddress(a); err != nil {
			return nil, err
		}
	}
	return addrs, nil
}

// validateAddress rejects CR/LF/NUL in either half of an address rather than
// silently stripping it: the caller asked for a specific address.
func validateAddress(a model.Address) error {
	if err := ValidateHeader(a.Name); err != nil {
		return fmt.Errorf("compose: address name: %w", err)
	}
	if err := ValidateHeader(a.Addr); err != nil {
		return fmt.Errorf("compose: address: %w", err)
	}
	return nil
}

// mintMessageID returns a unique, validated Message-ID with angle brackets.
func mintMessageID(domain string) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("compose: message id: %w", err)
	}
	token := fmt.Sprintf("%d.%s@%s", time.Now().UnixNano(), hex.EncodeToString(nonce[:]), domain)
	if err := validateToken(token, "Message-ID"); err != nil {
		return "", err
	}
	return "<" + token + ">", nil
}

// validateToken rejects whitespace, angle brackets and control bytes, which
// have no business inside a Message-ID token.
func validateToken(v, what string) error {
	if v == "" {
		return fmt.Errorf("compose: empty %s", what)
	}
	if strings.ContainsAny(v, " \t\r\n\x00<>") {
		return fmt.Errorf("compose: invalid character in %s %q", what, v)
	}
	return nil
}

// node is one entity in the MIME tree.
type node struct {
	media  string
	params map[string]string
	cte    string // Content-Transfer-Encoding for leaf parts
	text   string // body of a text leaf
	att    *model.Attachment
	kids   []*node
}

// messageTree builds the MIME structure required by the package contract.
func messageTree(m *model.Message) *node {
	var body *node
	if m.Plain != "" && m.HTML != "" {
		body = &node{media: "multipart/alternative", kids: []*node{
			textNode("text/plain", m.Plain),
			textNode("text/html", m.HTML),
		}}
	} else if m.HTML != "" {
		body = textNode("text/html", m.HTML)
	} else {
		body = textNode("text/plain", m.Plain) // empty body is valid
	}

	var inline, attached []*node
	for i := range m.Attachments {
		a := &m.Attachments[i]
		n := &node{media: mediaType(a.ContentType), cte: "base64", att: a}
		if a.Inline {
			inline = append(inline, n)
		} else {
			attached = append(attached, n)
		}
	}
	if len(inline) > 0 {
		body = &node{
			media:  "multipart/related",
			params: map[string]string{"type": body.media},
			kids:   append([]*node{body}, inline...),
		}
	}
	if len(attached) > 0 {
		body = &node{media: "multipart/mixed", kids: append([]*node{body}, attached...)}
	}
	return body
}

func textNode(media, text string) *node {
	return &node{
		media:  media,
		params: map[string]string{"charset": "UTF-8"},
		cte:    "quoted-printable",
		text:   text,
	}
}

func mediaType(ct string) string {
	if ct == "" {
		return "application/octet-stream"
	}
	if t, _, err := mime.ParseMediaType(ct); err == nil {
		return t
	}
	return "application/octet-stream"
}

// header renders the entity header for n.
func (n *node) header() message.Header {
	var h message.Header
	h.SetContentType(n.media, n.params)
	if n.cte != "" {
		h.Set("Content-Transfer-Encoding", n.cte)
	}
	if n.att == nil {
		return h
	}
	disp := "attachment"
	if n.att.Inline {
		disp = "inline"
	}
	params := map[string]string{}
	if name := sanitizeFilename(n.att.Filename); name != "" {
		params["filename"] = name
	}
	// mime.FormatMediaType emits RFC 2231 parameters (filename*) for
	// non-ASCII names, which every mail parser decodes.
	h.Set("Content-Disposition", mime.FormatMediaType(disp, params))
	if n.att.Inline {
		if id := sanitizeContentID(n.att.ContentID); id != "" {
			h.Set("Content-ID", "<"+id+">")
		}
	}
	return h
}

// writeEntity writes n, and its subtree, below parent. A nil parent means n is
// the top-level entity, whose header is already on the wire.
func writeEntity(dst io.Writer, parent *message.Writer, h message.Header, n *node) error {
	var w *message.Writer
	var err error
	if parent == nil {
		w, err = message.CreateWriter(dst, h)
	} else {
		w, err = parent.CreatePart(h)
	}
	if err != nil {
		return err
	}
	if len(n.kids) == 0 {
		if n.att != nil {
			_, err = w.Write(n.att.Data)
		} else {
			_, err = io.WriteString(w, toCRLF(n.text))
		}
	} else {
		for _, kid := range n.kids {
			if err = writeEntity(dst, w, kid.header(), kid); err != nil {
				break
			}
		}
	}
	if err != nil {
		return err
	}
	return w.Close()
}

// toCRLF canonicalises body line endings for the wire (RFC 5322 §2.1).
func toCRLF(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
}

// sanitizeFilename makes an attachment name safe for a Content-Disposition
// parameter: no controls, no path, no traversal.
func sanitizeFilename(name string) string {
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

// sanitizeContentID strips the characters that could break the header field;
// the stored form has no angle brackets.
func sanitizeContentID(id string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', '\r', '\n', 0, ' ', '\t':
			return -1
		}
		return r
	}, id)
}

// Reply builds a reply draft to orig. To defaults to Reply-To, falling back to
// From; the thread headers extend orig's chain.
func Reply(orig *model.Message, from model.Address, bodyText string) *model.Message {
	if orig == nil {
		orig = &model.Message{Header: model.NewHeader()}
	}
	m := &model.Message{Header: model.NewHeader()}
	m.Header.Set("From", from.String())

	to := orig.Addresses("Reply-To")
	if len(to) == 0 {
		to = orig.From()
	}
	if v := model.FormatAddressList(to); v != "" {
		m.Header.Set("To", v)
	}
	m.Header.Set("Subject", cleanHeader(replySubject(orig.Subject())))

	if id := cleanID(orig.MessageID()); id != "" {
		m.Header.Set("In-Reply-To", "<"+id+">")
		refs := orig.References()
		if len(refs) == 0 || refs[len(refs)-1] != id {
			refs = append(refs, id)
		}
		parts := make([]string, len(refs))
		for i, r := range refs {
			parts[i] = "<" + cleanID(r) + ">"
		}
		m.Header.Set("References", strings.Join(parts, " "))
	}

	m.Plain = quotedReply(orig, bodyText)
	return m
}

// Forward builds a forward draft: the original is attached as message/rfc822
// and its headers are quoted in the body.
func Forward(orig *model.Message, from model.Address, bodyText string) *model.Message {
	if orig == nil {
		orig = &model.Message{Header: model.NewHeader()}
	}
	m := &model.Message{Header: model.NewHeader()}
	m.Header.Set("From", from.String())
	m.Header.Set("Subject", cleanHeader(forwardSubject(orig.Subject())))

	var b strings.Builder
	b.WriteString(bodyText)
	if bodyText != "" {
		b.WriteString("\n\n")
	}
	b.WriteString("---------- Forwarded message ----------\n")
	fmt.Fprintf(&b, "From: %s\n", model.FormatAddressList(orig.From()))
	if d, ok := orig.Date(); ok {
		fmt.Fprintf(&b, "Date: %s\n", d.Format(time.RFC1123Z))
	}
	fmt.Fprintf(&b, "Subject: %s\n", cleanHeader(orig.Subject()))
	fmt.Fprintf(&b, "To: %s\n\n", model.FormatAddressList(orig.To()))
	b.WriteString(orig.Plain)
	m.Plain = b.String()

	// shortcut: the original is re-serialized, not byte-identical; keep raw
	// bytes on model.Message if exact archival matters.
	inner := Options{From: from}
	if f := orig.From(); len(f) > 0 {
		inner.From = f[0]
	}
	if d, ok := orig.Date(); ok {
		inner.Date = d
	}
	data, err := Build(orig, inner)
	if err != nil {
		data = nil
	}
	m.Attachments = []model.Attachment{{
		Filename:    "message.eml",
		ContentType: "message/rfc822",
		Data:        data,
	}}
	return m
}

// quotedReply returns bodyText followed by the attribution line and orig's
// plain body quoted with "> ".
func quotedReply(orig *model.Message, bodyText string) string {
	body := strings.TrimRight(strings.ReplaceAll(orig.Plain, "\r\n", "\n"), "\n")
	if body == "" {
		return bodyText
	}
	var b strings.Builder
	b.WriteString(bodyText)
	if bodyText != "" {
		b.WriteString("\n\n")
	}
	if d, ok := orig.Date(); ok {
		fmt.Fprintf(&b, "On %s, %s wrote:\n", d.Format(time.RFC1123Z), model.FormatAddressList(orig.From()))
	} else {
		fmt.Fprintf(&b, "On %s wrote:\n", model.FormatAddressList(orig.From()))
	}
	for i, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("> ")
		b.WriteString(line)
	}
	return b.String()
}

// replySubject adds exactly one "Re: " prefix, collapsing any already present.
func replySubject(subject string) string { return prefixedSubject("Re", subject) }

// forwardSubject adds exactly one "Fwd: " prefix, collapsing "Fw:" too.
func forwardSubject(subject string) string {
	rest := subject
	for {
		trimmed := strings.TrimSpace(rest)
		lower := strings.ToLower(trimmed)
		var prefix string
		switch {
		case strings.HasPrefix(lower, "fwd:"):
			prefix = "fwd:"
		case strings.HasPrefix(lower, "fw:"):
			prefix = "fw:"
		default:
			return "Fwd: " + trimmed
		}
		rest = trimmed[len(prefix):]
	}
}

func prefixedSubject(prefix, subject string) string {
	rest := subject
	for strings.HasPrefix(strings.ToLower(strings.TrimSpace(rest)), strings.ToLower(prefix)+":") {
		trimmed := strings.TrimSpace(rest)
		rest = trimmed[len(prefix)+1:]
	}
	return prefix + ": " + strings.TrimSpace(rest)
}

// cleanHeader replaces bytes that must never reach a header line.
func cleanHeader(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', 0:
			return ' '
		}
		return r
	}, s)
}

// cleanID drops whitespace and angle brackets from a message identifier.
func cleanID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	if i := strings.IndexByte(id, '>'); i >= 0 {
		id = id[:i]
	}
	return strings.TrimSpace(id)
}
