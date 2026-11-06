package ui

import (
	"fmt"
	"strings"
	"time"

	"btcx/internal/contract"
	"btcx/internal/model"
)

// formatSize renders a byte count the way a mail client lists attachments:
// "512 B", "12 KB", "1.4 MB".
func formatSize(n int64) string {
	switch {
	case n < 0:
		return ""
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// formatDate renders a message date compactly: a time of day for messages from
// today, day and month for the current year, full date otherwise.
func formatDate(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	if t.Year() == now.Year() {
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
}

// addresses joins addresses in RFC 5322 form.
func addresses(list []model.Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

// folderLabel is the sidebar text for a folder: name plus unread count.
func folderLabel(f contract.FolderInfo) string {
	if f.Unread > 0 {
		return fmt.Sprintf("%s (%d)", f.Name, f.Unread)
	}
	return f.Name
}

// attachmentLabel is the button text for one attachment.
func attachmentLabel(a contract.AttachmentInfo) string {
	label := "📎 " + a.Filename
	if a.Size > 0 {
		label += " (" + formatSize(a.Size) + ")"
	}
	return label
}
