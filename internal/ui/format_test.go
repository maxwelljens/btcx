package ui

import (
	"testing"
	"time"

	"btcx/internal/contract"
	"btcx/internal/model"
)

func TestFormatSize(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{-1, ""},
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1 KB"},
		{12 * 1024, "12 KB"},
		{1536 * 1024, "1.5 MB"},
	}
	for _, tc := range tests {
		if got := formatSize(tc.in); got != tc.want {
			t.Errorf("formatSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatDate(t *testing.T) {
	now := time.Date(2026, time.November, 6, 14, 30, 0, 0, time.UTC)
	tests := []struct {
		in   time.Time
		want string
	}{
		{time.Time{}, ""},
		{time.Date(2026, time.November, 6, 9, 5, 0, 0, time.UTC), "09:05"},
		{time.Date(2026, time.March, 2, 9, 5, 0, 0, time.UTC), "2 Mar"},
		{time.Date(2019, time.December, 31, 9, 5, 0, 0, time.UTC), "31 Dec 2019"},
	}
	for _, tc := range tests {
		if got := formatDate(tc.in, now); got != tc.want {
			t.Errorf("formatDate(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAddresses(t *testing.T) {
	list := []model.Address{
		{Name: "Alice", Addr: "alice@example.org"},
		{Addr: "bob@example.org"},
	}
	want := `"Alice" <alice@example.org>, bob@example.org`
	if got := addresses(list); got != want {
		t.Errorf("addresses() = %q, want %q", got, want)
	}
	if got := addresses(nil); got != "" {
		t.Errorf("addresses(nil) = %q, want empty", got)
	}
}

func TestFolderLabel(t *testing.T) {
	tests := []struct {
		in   contract.FolderInfo
		want string
	}{
		{contract.FolderInfo{Name: "INBOX"}, "INBOX"},
		{contract.FolderInfo{Name: "INBOX", Unread: 3}, "INBOX (3)"},
	}
	for _, tc := range tests {
		if got := folderLabel(tc.in); got != tc.want {
			t.Errorf("folderLabel(%+v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAttachmentLabel(t *testing.T) {
	a := contract.AttachmentInfo{Filename: "report.pdf", Size: 2048}
	if got, want := attachmentLabel(a), "📎 report.pdf (2 KB)"; got != want {
		t.Errorf("attachmentLabel() = %q, want %q", got, want)
	}
	b := contract.AttachmentInfo{Filename: "empty.txt"}
	if got, want := attachmentLabel(b), "📎 empty.txt"; got != want {
		t.Errorf("attachmentLabel() = %q, want %q", got, want)
	}
}
