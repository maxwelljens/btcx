package render

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func FuzzPango(f *testing.F) {
	for _, s := range []string{
		`<p>hello</p>`,
		`<b>bold</b> and <i>italic</i>`,
		`<script>alert(1)</script>`,
		`<a href="https://example.com">x</a>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<img src="cid:1" alt="i">`,
		`<table><tr><th>h</th><td>c</td></tr></table>`,
		`<blockquote>q</blockquote>`,
		"5 < 6 & 7 > 2",
		"control\x00\x01\x1f",
		"bad utf8 \xff\xfe",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := Pango(in)
		if strings.Contains(strings.ToLower(out), "<script") {
			t.Fatalf("Pango(%q) emitted script markup: %q", in, out)
		}
		assertWellFormed(t, in, out)

		plain := PangoFromPlain(in)
		assertWellFormed(t, in, plain)
	})
}

func assertWellFormed(t *testing.T, in, out string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader("<root>" + out + "</root>"))
	for {
		_, err := d.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("Pango(%q) = %q is not well-formed XML: %v", in, out, err)
		}
	}
}
