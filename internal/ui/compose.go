package ui

import (
	"os"
	"path/filepath"
	"strings"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"btcx/internal/contract"
	"btcx/internal/model"
)

// ComposeWindow edits one outgoing message.
type ComposeWindow struct {
	*gtk.ApplicationWindow

	app *app

	to      *gtk.Entry
	cc      *gtk.Entry
	bcc     *gtk.Entry
	subject *gtk.Entry
	body    *gtk.TextView
	attList *gtk.ListBox
	status  *gtk.Label

	// draft keeps the parts the widgets do not show: account, reply target,
	// generated HTML body and the attachment bytes.
	draft   contract.ComposeDraft
	initial string
	closing bool
}

func newComposeWindow(w *MainWindow, draft contract.ComposeDraft) *ComposeWindow {
	c := &ComposeWindow{
		ApplicationWindow: gtk.NewApplicationWindow(w.app.gtkApp),
		app:               w.app,
		draft:             draft,
	}
	c.SetTransientFor(&w.Window)
	c.SetDefaultSize(700, 520)
	c.SetTitle("Compose")
	c.SetTitlebar(c.buildHeader())

	closeAction := gio.NewSimpleAction("close", nil)
	closeAction.ConnectActivate(func(*glib.Variant) { c.Close() })
	c.AddAction(closeAction)
	c.SetChild(c.buildBody())
	c.fill(draft)

	c.initial = c.snapshot()
	c.ConnectCloseRequest(func() bool {
		if !c.closing && c.dirty() {
			c.confirmDiscard()
			return true
		}
		c.app.removeCompose(c)
		return false
	})
	w.app.addCompose(c)
	return c
}

func (c *ComposeWindow) buildHeader() *gtk.HeaderBar {
	bar := gtk.NewHeaderBar()
	title := gtk.NewLabel("New message")
	title.AddCSSClass("bold")
	bar.SetTitleWidget(title)

	closeBtn := gtk.NewButtonFromIconName("window-close-symbolic")
	closeBtn.SetTooltipText("Close (Ctrl+W)")
	closeBtn.ConnectClicked(func() { c.Close() })
	bar.PackStart(closeBtn)

	editor := gtk.NewButtonWithLabel("External editor")
	editor.SetTooltipText("Edit this draft in $EDITOR")
	editor.ConnectClicked(c.externalEdit)

	save := gtk.NewButtonWithLabel("Save Draft")
	save.SetTooltipText("Save this message in the Drafts folder")
	save.ConnectClicked(func() {
		if draft, ok := c.collect(); ok {
			c.app.controller.SaveDraft(draft)
			c.app.setStatusOnMain("Draft saved")
		}
	})

	send := gtk.NewButtonWithLabel("Send")
	send.AddCSSClass("suggested-action")
	send.SetTooltipText("Send this message")
	send.ConnectClicked(c.send)

	bar.PackEnd(send)
	bar.PackEnd(save)
	bar.PackEnd(editor)
	return bar
}

func (c *ComposeWindow) buildBody() *gtk.Widget {
	grid := gtk.NewGrid()
	grid.SetRowSpacing(6)
	grid.SetColumnSpacing(8)
	grid.SetMarginTop(8)
	grid.SetMarginStart(12)
	grid.SetMarginEnd(12)

	c.to = gtk.NewEntry()
	c.cc = gtk.NewEntry()
	c.bcc = gtk.NewEntry()
	c.subject = gtk.NewEntry()
	for i, e := range []*gtk.Entry{c.to, c.cc, c.bcc, c.subject} {
		e.SetHExpand(true)
		e.SetPlaceholderText("Comma-separated addresses")
		grid.Attach(e, 1, i, 1, 1)
	}
	c.subject.SetPlaceholderText("")
	for i, name := range []string{"To", "Cc", "Bcc", "Subject"} {
		label := gtk.NewLabel(name)
		label.SetXAlign(1)
		grid.Attach(label, 0, i, 1, 1)
	}

	c.body = gtk.NewTextView()
	c.body.SetWrapMode(gtk.WrapWordChar)
	c.body.SetMonospace(true)
	c.body.SetLeftMargin(8)
	c.body.SetRightMargin(8)
	c.body.SetTopMargin(6)
	c.body.SetBottomMargin(6)
	bodyScroll := gtk.NewScrolledWindow()
	bodyScroll.SetPolicy(gtk.PolicyAutomatic, gtk.PolicyAutomatic)
	bodyScroll.SetChild(c.body)
	bodyScroll.SetVExpand(true)
	bodyScroll.SetSizeRequest(-1, 240)

	c.attList = gtk.NewListBox()
	c.attList.SetSelectionMode(gtk.SelectionNone)

	add := gtk.NewButtonWithLabel("📎 Add attachment")
	add.AddCSSClass("flat")
	add.SetHAlign(gtk.AlignStart)
	add.ConnectClicked(c.addAttachment)

	c.status = gtk.NewLabel("")
	c.status.SetXAlign(0)
	c.status.SetEllipsize(pango.EllipsizeEnd)

	root := gtk.NewBox(gtk.OrientationVertical, 8)
	root.SetMarginBottom(8)
	root.Append(grid)
	root.Append(bodyScroll)
	root.Append(add)
	root.Append(c.attList)
	root.Append(c.status)
	return &root.Widget
}

// fill writes a draft into the widgets.
func (c *ComposeWindow) fill(draft contract.ComposeDraft) {
	c.draft = draft
	c.to.SetText(addresses(draft.To))
	c.cc.SetText(addresses(draft.Cc))
	c.bcc.SetText(addresses(draft.Bcc))
	c.subject.SetText(draft.Subject)
	c.body.Buffer().SetText(draft.Plain)
	c.rebuildAttachments()
}

// applyDraft replaces the widgets with what the external editor returned.
func (c *ComposeWindow) applyDraft(draft contract.ComposeDraft) {
	// Keep the correlation fields the dialog does not own.
	draft.Account = c.draft.Account
	draft.ReplyTo = c.draft.ReplyTo
	c.fill(draft)
	c.initial = c.snapshot()
	c.setStatus("Editor closed")
}

func (c *ComposeWindow) rebuildAttachments() {
	c.attList.RemoveAll()
	for i, a := range c.draft.Attachments {
		row := gtk.NewListBoxRow()
		box := gtk.NewBox(gtk.OrientationHorizontal, 6)
		label := gtk.NewLabel("📎 " + a.Filename + " (" + formatSize(int64(len(a.Data))) + ")")
		label.SetXAlign(0)
		label.SetHExpand(true)
		label.SetEllipsize(pango.EllipsizeEnd)
		remove := gtk.NewButtonFromIconName("edit-delete-symbolic")
		remove.AddCSSClass("flat")
		remove.SetTooltipText("Remove attachment")
		index := i
		remove.ConnectClicked(func() {
			c.draft.Attachments = append(c.draft.Attachments[:index], c.draft.Attachments[index+1:]...)
			c.rebuildAttachments()
		})
		box.Append(label)
		box.Append(remove)
		row.SetChild(box)
		c.attList.Append(row)
	}
}

// addAttachment reads files chosen by the user into the draft.
func (c *ComposeWindow) addAttachment() {
	chooser := gtk.NewFileChooserNative("Attach file", &c.Window,
		gtk.FileChooserActionOpen, "Attach", "Cancel")
	chooser.SetSelectMultiple(true)
	chooser.ConnectResponse(func(response int) {
		if response != int(gtk.ResponseAccept) {
			return
		}
		files := chooser.Files()
		if files == nil {
			return
		}
		for i := uint(0); i < files.NItems(); i++ {
			path := asFile(files.Item(i)).Path()
			if path == "" {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				c.app.showDialog("Cannot attach file", err.Error(), true)
				continue
			}
			c.draft.Attachments = append(c.draft.Attachments, contract.DraftAttachment{
				Filename:    filepath.Base(path),
				ContentType: "",
				Data:        data,
			})
		}
		c.rebuildAttachments()
	})
	chooser.Show()
}

// asFile re-wraps a list-model item as a gio.File. gotk4 does not export the
// generated wrap functions, so the item is wrapped by hand.
func asFile(obj *coreglib.Object) *gio.File { return &gio.File{Object: obj} }

func (c *ComposeWindow) bodyText() string {
	buf := c.body.Buffer()
	return buf.Text(buf.StartIter(), buf.EndIter(), true)
}

func (c *ComposeWindow) snapshot() string {
	names := make([]string, len(c.draft.Attachments))
	for i, a := range c.draft.Attachments {
		names[i] = a.Filename
	}
	return c.to.Text() + "\x00" + c.cc.Text() + "\x00" + c.bcc.Text() + "\x00" +
		c.subject.Text() + "\x00" + c.bodyText() + "\x00" + strings.Join(names, "\x00")
}

func (c *ComposeWindow) dirty() bool { return c.snapshot() != c.initial }

func (c *ComposeWindow) setStatus(message string) {
	c.status.SetLabel(message)
	c.status.RemoveCSSClass("error")
}

func (c *ComposeWindow) setInvalid(e *gtk.Entry, message string) {
	e.SetIconFromIconName(gtk.EntryIconPrimary, "dialog-error-symbolic")
	e.SetIconTooltipText(gtk.EntryIconPrimary, message)
	e.GrabFocus()
}

func (c *ComposeWindow) setValid(e *gtk.Entry) {
	e.SetIconFromIconName(gtk.EntryIconPrimary, "")
}

// collect reads the widgets into a draft, flagging invalid addresses inline.
func (c *ComposeWindow) collect() (contract.ComposeDraft, bool) {
	draft := c.draft
	ok := true
	for _, f := range []struct {
		entry *gtk.Entry
		dest  *[]model.Address
	}{
		{c.to, &draft.To},
		{c.cc, &draft.Cc},
		{c.bcc, &draft.Bcc},
	} {
		list, err := model.ParseAddressList(f.entry.Text())
		if err != nil {
			c.setInvalid(f.entry, err.Error())
			ok = false
			continue
		}
		c.setValid(f.entry)
		*f.dest = list
	}
	if !ok {
		c.status.AddCSSClass("error")
		c.status.SetLabel("Fix the highlighted addresses")
		return draft, false
	}
	draft.Subject = c.subject.Text()
	draft.Plain = c.bodyText()
	if draft.Plain != c.draft.Plain {
		// The plain part no longer matches the generated HTML alternative.
		draft.HTML = ""
	}
	return draft, true
}

func (c *ComposeWindow) send() {
	draft, ok := c.collect()
	if !ok {
		return
	}
	c.app.trackSend(c)
	c.app.controller.Send(draft)
}

func (c *ComposeWindow) externalEdit() {
	draft, ok := c.collect()
	if !ok {
		return
	}
	id := c.app.controller.StartExternalEdit(draft)
	c.app.trackEdit(id, c)
	c.setStatus("Waiting for editor…")
}

func (c *ComposeWindow) confirmDiscard() {
	dlg := gtk.NewMessageDialog(&c.Window, gtk.DialogModal, gtk.MessageQuestion, gtk.ButtonsNone)
	dlg.SetTitle("Discard message?")
	dlg.SetMarkup("<b>Discard this message?</b>\n\nIts content will be lost.")
	dlg.AddButton("Keep editing", int(gtk.ResponseCancel))
	dlg.AddButton("Discard", int(gtk.ResponseClose))
	dlg.ConnectResponse(func(response int) {
		dlg.Destroy()
		if response == int(gtk.ResponseClose) {
			c.forceClose()
		}
	})
	dlg.Present()
}

// forceClose closes the window without asking.
func (c *ComposeWindow) forceClose() {
	c.closing = true
	c.app.removeCompose(c)
	c.Close()
}
