// Package model defines the domain types shared by every btcx subsystem.
//
// It is the contract between the mail plumbing (parsing, composing, network
// clients), the scripting layer, and the UI. It must stay dependency-free
// (standard library only) so that every other package can import it without
// pulling in transitive weight.
package model

import (
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strings"
	"time"
)

// Address is a single RFC 5322 mailbox: an optional display name plus an
// addr-spec. The zero Address is valid and renders as the empty string.
type Address struct {
	Name string // display name; may be empty
	Addr string // addr-spec, e.g. "alice@example.org"; may be empty for groups
}

// String renders the address in RFC 5322 form. The display name is quoted
// when required. CR and LF are stripped from both fields so that a hostile
// address can never inject additional header fields.
func (a Address) String() string {
	name := strings.NewReplacer("\r", "", "\n", "").Replace(a.Name)
	addr := strings.NewReplacer("\r", "", "\n", "").Replace(a.Addr)
	switch {
	case name == "":
		return addr
	case addr == "":
		return quoteDisplayName(name)
	default:
		return (&mail.Address{Name: name, Address: addr}).String()
	}
}

// quoteDisplayName renders name as an RFC 5322 quoted-string.
func quoteDisplayName(name string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range name {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\r', '\n':
			// dropped: never allow header injection
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// IsZero reports whether the address carries no information.
func (a Address) IsZero() bool { return a.Name == "" && a.Addr == "" }

// ParseAddressList parses a comma-separated RFC 5322 address list, such as
// the value of a To or From header. Empty input yields nil.
func ParseAddressList(s string) ([]Address, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		return nil, err
	}
	out := make([]Address, len(list))
	for i, m := range list {
		out[i] = Address{Name: m.Name, Addr: m.Address}
	}
	return out, nil
}

// FormatAddressList renders a list of addresses as a comma-separated
// RFC 5322 address list.
func FormatAddressList(addrs []Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if !a.IsZero() {
			parts = append(parts, a.String())
		}
	}
	return strings.Join(parts, ", ")
}

// Field is a single header field. Key is stored in canonical MIME form
// (e.g. "Content-Type"); Value is the raw, unfolded value.
type Field struct {
	Key   string
	Value string
}

// Header is an ordered collection of message header fields. Unlike
// textproto.MIMEHeader it preserves field order and duplicate fields.
// The zero Header is usable; mutation methods require a pointer receiver.
type Header struct {
	fields []Field
}

// NewHeader returns an empty Header.
func NewHeader() Header { return Header{} }

// Len returns the number of header fields.
func (h Header) Len() int { return len(h.fields) }

// Fields returns the header fields in wire order. The returned slice must
// not be mutated.
func (h Header) Fields() []Field { return h.fields }

// Get returns the first value stored under key (case-insensitive), or "".
func (h Header) Get(key string) string {
	key = canonicalKey(key)
	for _, f := range h.fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// Values returns every value stored under key, in order.
func (h Header) Values(key string) []string {
	key = canonicalKey(key)
	var out []string
	for _, f := range h.fields {
		if f.Key == key {
			out = append(out, f.Value)
		}
	}
	return out
}

// Has reports whether at least one field matches key.
func (h Header) Has(key string) bool {
	key = canonicalKey(key)
	for _, f := range h.fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

// Set replaces all fields matching key with a single field. If no field
// matches, the field is appended. Any slices previously returned by Fields
// remain valid snapshots.
func (h *Header) Set(key, value string) {
	key = canonicalKey(key)
	found := false
	out := make([]Field, 0, len(h.fields)+1)
	for _, f := range h.fields {
		if f.Key == key {
			if !found {
				found = true
				out = append(out, Field{Key: key, Value: value})
			}
			continue
		}
		out = append(out, f)
	}
	if !found {
		out = append(out, Field{Key: key, Value: value})
	}
	h.fields = out
}

// Add appends a new field without removing existing ones.
func (h *Header) Add(key, value string) {
	h.fields = append(h.fields, Field{Key: canonicalKey(key), Value: value})
}

// Delete removes all fields matching key.
func (h *Header) Delete(key string) {
	key = canonicalKey(key)
	out := make([]Field, 0, len(h.fields))
	for _, f := range h.fields {
		if f.Key != key {
			out = append(out, f)
		}
	}
	h.fields = out
}

// Clone returns a deep copy of the header.
func (h Header) Clone() Header {
	fields := make([]Field, len(h.fields))
	copy(fields, h.fields)
	return Header{fields: fields}
}

// canonicalKey converts a header key to canonical MIME form
// ("content-type" -> "Content-Type").
func canonicalKey(key string) string {
	parts := strings.Split(key, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	return strings.Join(parts, "-")
}

// Attachment is a non-body part of a message: a file attachment or an inline
// resource referenced from an HTML body via its ContentID.
type Attachment struct {
	Filename    string // filename from Content-Disposition/Content-Type; may be empty
	ContentType string // media type, e.g. "image/png"; never empty after parsing
	ContentID   string // Content-ID without angle brackets; empty if none
	Data        []byte // complete decoded content
	Inline      bool   // true when shown in place (Content-Disposition: inline)
}

// Message is a parsed or composed e-mail. Plain and HTML hold the decoded
// text bodies; when both are present they are alternative representations of
// the same content.
type Message struct {
	Header      Header
	Plain       string       // text/plain body, decoded to UTF-8
	HTML        string       // text/html body, decoded to UTF-8
	Attachments []Attachment // non-body parts and inline resources
}

// decodeHeader decodes RFC 2047 encoded words in a header value to UTF-8.
// When a word uses a charset we cannot decode, the raw value is returned
// unchanged so nothing is silently lost.
func decodeHeader(v string) string {
	dec := &mime.WordDecoder{
		CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
			switch charset {
			case "utf-8", "us-ascii", "":
				return input, nil // pass through unchanged
			case "iso-8859-1", "iso-8859-15", "windows-1252":
				b, err := io.ReadAll(input)
				if err != nil {
					return nil, err
				}
				r := make([]rune, len(b))
				for i, c := range b {
					r[i] = rune(c) // latin-1 code points
				}
				return strings.NewReader(string(r)), nil
			default:
				return nil, fmt.Errorf("model: unsupported header charset %q", charset)
			}
		},
	}
	s, err := dec.DecodeHeader(v)
	if err != nil {
		return v
	}
	return s
}

// Subject returns the decoded Subject header.
func (m *Message) Subject() string {
	return decodeHeader(m.Header.Get("Subject"))
}

// messageIDToken strips surrounding angle brackets from a message
// identifier and returns the bare "local@domain" token.
func messageIDToken(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "<")
	if i := strings.IndexByte(v, '>'); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// MessageID returns the bare Message-ID of the message without angle
// brackets, or "" when absent or malformed.
func (m *Message) MessageID() string {
	return messageIDToken(m.Header.Get("Message-ID"))
}

// InReplyTo returns the bare identifier of the message this one replies to
// (the first entry of the In-Reply-To header), or "".
func (m *Message) InReplyTo() string {
	v := m.Header.Get("In-Reply-To")
	if i := strings.IndexAny(v, " \t,"); i >= 0 {
		v = v[:i]
	}
	return messageIDToken(v)
}

// References returns the bare identifiers of the messages in the thread
// this message belongs to, in wire order.
func (m *Message) References() []string {
	fields := strings.Fields(m.Header.Get("References"))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if id := messageIDToken(f); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// IsReplyTo reports whether the message is a direct or indirect reply to
// the message identified by id (bare form, no angle brackets).
func (m *Message) IsReplyTo(id string) bool {
	if id == "" {
		return false
	}
	if m.InReplyTo() == id {
		return true
	}
	for _, r := range m.References() {
		if r == id {
			return true
		}
	}
	return false
}

// Addresses returns the decoded, parsed address list stored under key.
// Malformed addresses are skipped rather than failing the whole message.
func (m *Message) Addresses(key string) []Address {
	list, err := ParseAddressList(decodeHeader(m.Header.Get(key)))
	if err != nil {
		// Salvage whatever parses: fall back to per-comma entries.
		var out []Address
		for _, part := range strings.Split(m.Header.Get(key), ",") {
			if a, err := ParseAddressList(part); err == nil {
				out = append(out, a...)
			}
		}
		return out
	}
	return list
}

// From, To, Cc and Bcc return the corresponding decoded address lists.
func (m *Message) From() []Address { return m.Addresses("From") }
func (m *Message) To() []Address   { return m.Addresses("To") }
func (m *Message) Cc() []Address   { return m.Addresses("Cc") }
func (m *Message) Bcc() []Address  { return m.Addresses("Bcc") }

// Date returns the parsed Date header and whether it was present and valid.
func (m *Message) Date() (time.Time, bool) {
	v := m.Header.Get("Date")
	if v == "" {
		return time.Time{}, false
	}
	d, err := mail.ParseDate(v)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

// Ref uniquely identifies a message on a server across sessions. UIDValidity
// guards against UID reuse when a mailbox is rebuilt.
type Ref struct {
	Mailbox     string
	UIDValidity uint32
	UID         uint32
}

// String renders the reference compactly for logs and scripting.
func (r Ref) String() string {
	return r.Mailbox + "/" + itoa(r.UIDValidity) + "/" + itoa(r.UID)
}

// Flags is the set of IMAP system flags carried by a message.
type Flags struct {
	Seen     bool
	Answered bool
	Flagged  bool
	Deleted  bool
	Draft    bool
	Recent   bool
}

// Envelope is the lightweight summary used to populate the message list
// without fetching bodies.
type Envelope struct {
	Ref            Ref
	MessageID      string
	Subject        string
	From           []Address
	To             []Address
	Cc             []Address
	Date           time.Time
	Size           int64
	Flags          Flags
	HasAttachments bool
}

// Mailbox describes a server-side folder.
type Mailbox struct {
	Name       string   // full hierarchical name as returned by LIST
	Delimiter  string   // hierarchy separator, e.g. "/" or "."
	Attributes []string // e.g. "\\Sent", "\\Drafts", "\\Trash", "\\Junk", "\\Noselect"
}

// Role returns the special-use role of the mailbox based on its attributes,
// or "" when the mailbox has no special role.
func (m Mailbox) Role() string {
	for _, a := range m.Attributes {
		switch strings.ToLower(a) {
		case "\\sent":
			return "sent"
		case "\\drafts":
			return "drafts"
		case "\\trash":
			return "trash"
		case "\\junk", "\\spam":
			return "junk"
		case "\\archive", "\\all":
			return "archive"
		case "\\flagged", "\\important":
			return "flagged"
		}
	}
	return ""
}

// itoa avoids pulling strconv into this file's hot paths; values are small.
func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
