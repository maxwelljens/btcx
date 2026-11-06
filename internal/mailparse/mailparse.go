// Package mailparse turns raw RFC 5322 / MIME messages into the btcx domain
// model.
//
// Mail is hostile input, so every parse is bounded by the caps below. A part
// with a corrupt transfer encoding or an undecodable charset degrades to the
// bytes decoded up to that point instead of failing the whole message; only
// resource caps and unreadable headers are errors. Attachment filenames are
// stored exactly as sent — sanitize them before using them as filesystem
// paths.
package mailparse

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registers extended charset decoders

	"btcx/internal/model"
)

const (
	// maxMIMEDepth bounds how many nested multiparts are descended into.
	maxMIMEDepth = 30
	// maxPartCount bounds the number of MIME entities in one message.
	maxPartCount = 500
	// maxDecodedBytes bounds the total decoded part payload kept in memory.
	maxDecodedBytes = 64 << 20
	// maxHeaderBytes bounds the raw header block of a message.
	maxHeaderBytes = 1 << 20
)

var (
	errTooDeep      = errors.New("mailparse: MIME nesting too deep")
	errTooManyParts = errors.New("mailparse: too many MIME parts")
	errTooLarge     = errors.New("mailparse: message body too large")
)

// limits are the resource caps applied to a single parse.
type limits struct {
	depth int
	parts int
	bytes int64
}

// Parse reads a complete RFC 5322 message from r and returns the domain model.
func Parse(r io.Reader) (*model.Message, error) {
	return parse(r, limits{maxMIMEDepth, maxPartCount, maxDecodedBytes})
}

// ParseBytes is Parse over raw bytes.
func ParseBytes(b []byte) (*model.Message, error) {
	return parse(bytes.NewReader(b), limits{maxMIMEDepth, maxPartCount, maxDecodedBytes})
}

func parse(r io.Reader, lim limits) (*model.Message, error) {
	e, err := message.ReadWithOptions(r, &message.ReadOptions{MaxHeaderBytes: maxHeaderBytes})
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil, fmt.Errorf("mailparse: read message: %w", err)
	}

	p := &parser{lim: lim, left: lim.bytes}
	if err := e.Walk(p.walk); err != nil {
		return nil, fmt.Errorf("mailparse: walk message: %w", err)
	}

	return &model.Message{
		Header:      headerFrom(e.Header),
		Plain:       strings.Join(p.plain, "\n"),
		HTML:        strings.Join(p.html, "\n"),
		Attachments: p.atts,
	}, nil
}

// parser accumulates body parts and attachments over one message walk.
type parser struct {
	lim   limits
	parts int
	left  int64
	plain []string
	html  []string
	atts  []model.Attachment
}

// walk is the message.WalkFunc: it classifies one MIME entity. The path length
// is the nesting depth, counting the top-level entity as zero.
func (p *parser) walk(path []int, e *message.Entity, _ error) error {
	if len(path) > p.lim.depth {
		return errTooDeep
	}
	p.parts++
	if p.parts > p.lim.parts {
		return errTooManyParts
	}

	mediaType, params, _ := e.Header.ContentType()
	if strings.HasPrefix(mediaType, "multipart/") {
		return nil // container: its children are walked next
	}

	body, err := p.read(e.Body)
	if err != nil {
		return err
	}

	disp, dispParams, _ := e.Header.ContentDisposition()
	if (mediaType == "text/plain" || mediaType == "text/html") && disp != "attachment" {
		// Replace invalid UTF-8 (undecodable charset) so nothing broken
		// reaches the UI.
		s := strings.ToValidUTF8(string(body), "\uFFFD")
		if mediaType == "text/plain" {
			p.plain = append(p.plain, s)
		} else {
			p.html = append(p.html, s)
		}
		return nil
	}

	// Everything else — attachments, inline resources, message/rfc822
	// submessages, non-text/* parts — is kept whole.
	filename := dispParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	cid := contentID(e.Header.Get("Content-ID"))
	p.atts = append(p.atts, model.Attachment{
		Filename:    filename,
		ContentType: mediaType,
		ContentID:   cid,
		Data:        body,
		Inline:      disp == "inline" || cid != "",
	})
	return nil
}

// read consumes a part body under the shared byte budget. Bytes decoded before
// a corrupt transfer encoding are kept: losing one broken part is better than
// failing the whole message.
func (p *parser) read(r io.Reader) ([]byte, error) {
	if p.left <= 0 {
		return nil, errTooLarge
	}
	data, _ := io.ReadAll(io.LimitReader(r, p.left+1))
	if int64(len(data)) > p.left {
		return nil, errTooLarge
	}
	p.left -= int64(len(data))
	return data, nil
}

// headerFrom copies every field in wire order, keeping raw unfolded values.
func headerFrom(h message.Header) model.Header {
	out := model.NewHeader()
	fields := h.Fields()
	for fields.Next() {
		out.Add(fields.Key(), fields.Value())
	}
	return out
}

// contentID strips the angle brackets RFC 5322 requires around an identifier.
func contentID(v string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "<"), ">")
}

// ParseEnvelope extracts a lightweight summary from raw message bytes. Only the
// header is read, so a header-only or body-truncated fetch works.
func ParseEnvelope(b []byte) (*model.Envelope, error) {
	e, err := message.ReadWithOptions(bytes.NewReader(b), &message.ReadOptions{MaxHeaderBytes: maxHeaderBytes})
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil, fmt.Errorf("mailparse: read header: %w", err)
	}

	env := EnvelopeFrom(&model.Message{Header: headerFrom(e.Header)})
	env.Size = int64(len(b))
	env.HasAttachments = hasAttachments(e.Header)
	return &env, nil
}

// hasAttachments guesses from the top-level headers alone, without walking the
// body: mixed/related/digest containers and non-text roots mean attachments.
func hasAttachments(h message.Header) bool {
	mediaType, _, _ := h.ContentType()
	disp, _, _ := h.ContentDisposition()
	if disp == "attachment" {
		return true
	}
	switch {
	case mediaType == "multipart/mixed", mediaType == "multipart/related", mediaType == "multipart/digest":
		return true
	case strings.HasPrefix(mediaType, "multipart/"):
		return false // multipart/alternative carries bodies only
	}
	return !strings.HasPrefix(mediaType, "text/")
}

// EnvelopeFrom builds an Envelope from an already-parsed Message. Size is left
// zero: the model keeps no raw bytes.
func EnvelopeFrom(m *model.Message) model.Envelope {
	env := model.Envelope{
		MessageID:      m.MessageID(),
		Subject:        m.Subject(),
		From:           m.From(),
		To:             m.To(),
		Cc:             m.Cc(),
		HasAttachments: len(m.Attachments) > 0,
	}
	if d, ok := m.Date(); ok {
		env.Date = d
	}
	return env
}
