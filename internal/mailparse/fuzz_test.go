package mailparse

import (
	"testing"
	"unicode/utf8"
)

var seeds = [][]byte{
	[]byte(simplePlain),
	[]byte(qpLatin1),
	[]byte(utf8EncodedWords),
	[]byte(htmlOnly),
	[]byte(alternative),
	[]byte(mixedAttachments),
	[]byte(relatedInline),
	[]byte(forwarded),
	[]byte(multiplePlain),
	[]byte(nested(5)),
	[]byte(nested(31)),
	[]byte(wire("Subject: a", "Bcc: evil@example.org", "Content-Type: text/plain", "", "body", "")),
	[]byte(wire("Content-Type: multipart/mixed; boundary=x", "", "--x", "Content-Type: text/plain", "", "never closed")),
	[]byte("Subject: a\x00b\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!"),
	[]byte(""),
}

func FuzzParse(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := ParseBytes(b)
		if err != nil {
			return
		}
		if !utf8.ValidString(m.Plain) || !utf8.ValidString(m.HTML) {
			t.Fatalf("bodies are not valid UTF-8: %q / %q", m.Plain, m.HTML)
		}
		for _, a := range m.Attachments {
			if a.ContentType == "" {
				t.Fatalf("attachment without a content type: %+v", a)
			}
		}
	})
}

func FuzzParseEnvelope(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		env, err := ParseEnvelope(b)
		if err != nil {
			return
		}
		if env.Size != int64(len(b)) {
			t.Fatalf("Size = %d, want %d", env.Size, len(b))
		}
	})
}
