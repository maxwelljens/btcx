package ui

import (
	"strings"
	"testing"
)

func TestGuessServers(t *testing.T) {
	imap, smtp := guessServers(" jane@Example.ORG ")
	if imap.Host != "imap.example.org" || imap.Port != 993 || imap.TLS != "tls" || imap.Username != "jane@Example.ORG" {
		t.Errorf("imap = %+v", imap)
	}
	if smtp.Host != "smtp.example.org" || smtp.Port != 587 || smtp.TLS != "starttls" {
		t.Errorf("smtp = %+v", smtp)
	}

	if imap, smtp := guessServers("not-an-address"); imap.Host != "" || smtp.Host != "" {
		t.Errorf("guessed from an address without @: %+v %+v", imap, smtp)
	}
}

// TestTLSComboRoundTrip pins the index-to-mode mapping the account file gets.
func TestTLSComboRoundTrip(t *testing.T) {
	requireGTK(t)
	for active, want := range map[uint]string{0: "tls", 1: "starttls"} {
		c := tlsCombo(active)
		if got := c.Selected(); got != active {
			t.Errorf("tlsCombo(%d).Selected() = %d", active, got)
		}
		if got := tlsAt(c); got != want {
			t.Errorf("tlsCombo(%d) -> %q, want %q", active, got, want)
		}
	}
}

func TestGeneratedFilesText(t *testing.T) {
	got := generatedFilesText([]string{"config.toml", "accounts/example.toml.example"})
	want := "First launch — btcx created example configuration for you:\n" +
		"• config.toml\n• accounts/example.toml.example"
	if got != want {
		t.Errorf("generatedFilesText = %q, want %q", got, want)
	}
	if got := generatedFilesText(nil); !strings.HasPrefix(got, "First launch") || strings.Contains(got, "•") {
		t.Errorf("empty generatedFilesText = %q", got)
	}
}
