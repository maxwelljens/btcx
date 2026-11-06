// Package contract defines the boundary between the application layer
// (internal/app) and the user interface (internal/ui): view models and the
// two interfaces they exchange. Keeping this in its own package lets the
// two sides be developed and compiled independently.
//
// Threading model:
//
//   - Every Controller method is called on the UI (GTK main) thread and
//     must return promptly. Long work is performed asynchronously by the
//     controller.
//   - Every Events method may be called from ANY goroutine. The UI
//     implementation must marshal the call onto the GTK main loop before
//     touching widgets.
package contract

import (
	"time"

	"btcx/internal/model"
)

// AccountInfo describes a configured account for account pickers.
type AccountInfo struct {
	Name  string // account name (config file stem)
	Email string
}

// FolderInfo describes a mailbox in the folder list. Total and Unread are
// filled in as the application learns them (0 when not yet known).
type FolderInfo struct {
	Name   string
	Role   string // "sent", "drafts", "trash", "junk", "archive", "flagged" or ""
	Total  int
	Unread int
}

// MessageRow is one entry of the message list.
type MessageRow struct {
	Ref            model.Ref
	Subject        string
	From           model.Address
	Date           time.Time
	Unread         bool
	Flagged        bool
	HasAttachments bool
}

// AttachmentInfo describes a message attachment. Filename is already
// sanitized (basename, no separators) and safe to offer as a save target.
type AttachmentInfo struct {
	Index       int
	Filename    string
	ContentType string
	Size        int64
	Inline      bool // inline resource (cid:) rather than a downloadable attachment
}

// InlineImage is a remote image fetched after the user opted in to remote
// content, or an inline cid: image resolved to bytes.
type InlineImage struct {
	URL         string // remote URL or cid: reference ("" for cid without id)
	ContentType string // e.g. "image/png"
	Data        []byte // encoded image bytes; empty when Failed
	Failed      bool
}

// MessageView is the full reader-pane content for a message.
type MessageView struct {
	Account   string
	Ref       model.Ref
	Subject   string
	From      []model.Address
	To        []model.Address
	Cc        []model.Address
	Date      time.Time
	PangoBody string // ready-to-display Pango markup (sanitized + rendered)

	// ShowRemoteContentBar is true when the body references remote content
	// that is blocked. After LoadRemoteContent the bar hides and
	// RemoteImages carries the fetched bytes.
	ShowRemoteContentBar bool
	RemoteImages         []InlineImage

	Attachments []AttachmentInfo
	Unread      bool
	Flagged     bool
}

// DraftAttachment is an attachment of a message being composed.
type DraftAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// ComposeDraft is the editable state of a compose window. Plain is the
// authoritative body the user edits; HTML may be set for HTML mail
// (alternative part generated from it).
type ComposeDraft struct {
	Account     string
	ReplyTo     model.Ref // zero when composing a new message
	To          []model.Address
	Cc          []model.Address
	Bcc         []model.Address
	Subject     string
	Plain       string
	HTML        string
	Attachments []DraftAttachment
}

// EditRequestID correlates an asynchronous external-editor session with
// its OnExternalEditDone result.
type EditRequestID int

// Events is implemented by the UI. The controller calls these from
// arbitrary goroutines; implementations marshal onto the GTK main loop.
type Events interface {
	// OnStatus shows a transient status message ("Refreshing…", "Draft saved").
	OnStatus(message string)
	// OnError reports a user-visible failure.
	OnError(err error)
	// OnFolders replaces the folder list for an account.
	OnFolders(account string, folders []FolderInfo)
	// OnMessages replaces the message list of the current folder/search.
	OnMessages(account, folder string, rows []MessageRow)
	// OnMessageChanged updates one row (flags, read state).
	OnMessageChanged(row MessageRow)
	// OnNewMail announces a newly arrived message.
	OnNewMail(account string, row MessageRow)
	// OnMessageOpened delivers the full message for the reader pane.
	OnMessageOpened(view MessageView)
	// OnSendResult reports the outcome of Send (script hooks may veto).
	OnSendResult(success bool, message string)
	// OnExternalEditDone returns the draft after the external editor exits.
	OnExternalEditDone(id EditRequestID, draft ComposeDraft, err error)
}

// Controller is implemented by the application layer (internal/app) and
// called by the UI on the main thread. Methods return promptly; results
// arrive through Events.
type Controller interface {
	// Start connects configured accounts, loads folder lists, starts
	// new-mail watchers and runs script on_startup hooks.
	Start() error
	// Shutdown cancels watchers, closes connections and cleans up sessions.
	Shutdown()

	Accounts() []AccountInfo
	ActiveAccount() string
	SelectAccount(name string)
	SelectFolder(account, folder string)
	OpenMessage(account string, ref model.Ref)
	Refresh(account string)
	Search(account, folder, query string)

	MarkSeen(account string, ref model.Ref, seen bool)
	SetFlagged(account string, ref model.Ref, flagged bool)
	DeleteMessage(account string, ref model.Ref)

	// LoadRemoteContent opts in to remote content for one message:
	// remote images are fetched (bounded) and the view re-delivered via
	// OnMessageOpened with RemoteImages populated.
	LoadRemoteContent(account string, ref model.Ref)
	// SaveAttachment writes attachment index of the message to destPath
	// (a full file path chosen by the UI with the suggested safe name).
	SaveAttachment(account string, ref model.Ref, index int, destPath string) error

	NewCompose(account string) ComposeDraft
	ReplyTo(account string, ref model.Ref, replyAll bool) ComposeDraft
	Forward(account string, ref model.Ref) ComposeDraft
	// Send queues the message for delivery; the result is reported via
	// OnSendResult. before_send script hooks may veto.
	Send(draft ComposeDraft)
	// SaveDraft appends the draft to the account's Drafts mailbox.
	SaveDraft(draft ComposeDraft)
	// StartExternalEdit opens the draft in the configured external editor
	// and returns a correlation id; the result arrives via
	// OnExternalEditDone when the editor exits.
	StartExternalEdit(draft ComposeDraft) EditRequestID
}
