package ui

import (
	"testing"

	"btcx/internal/model"
)

func TestSenderDomain(t *testing.T) {
	tests := []struct {
		name string
		from []model.Address
		want string
	}{
		{"simple", []model.Address{{Addr: "alice@example.org"}}, "example.org"},
		{"name-qualified", []model.Address{{Name: "Alice", Addr: "alice@example.org"}}, "example.org"},
		{"first with at wins", []model.Address{
			{Addr: "list"},
			{Addr: "alice@example.org"},
			{Addr: "bob@other.test"},
		}, "example.org"},
		{"uppercase lowercased", []model.Address{{Addr: "alice@Example.ORG"}}, "example.org"},
		{"no-at skipped", []model.Address{{Addr: "undisclosed-recipients"}, {Addr: "x@ok.test"}}, "ok.test"},
		{"empty", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := senderDomain(tt.from); got != tt.want {
				t.Errorf("senderDomain(%v) = %q, want %q", tt.from, got, tt.want)
			}
		})
	}
}
