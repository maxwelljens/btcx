package ui

import (
	"time"

	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	gv2 "github.com/diamondburned/gotk4/pkg/glib/v2"

	"btcx/internal/contract"
)

// messageList is the message table. Rows live in a plain Go slice; the model
// only carries subjects, so a row's list position is its index in rows. Both
// are replaced together by set(), which is why they cannot drift apart.
type messageList struct {
	win   *MainWindow
	view  *gtk.ColumnView
	sel   *gtk.SingleSelection
	model *gtk.StringList
	rows  []contract.MessageRow
}

func newMessageList(w *MainWindow) *messageList {
	l := &messageList{win: w, model: gtk.NewStringList(nil)}
	l.sel = gtk.NewSingleSelection(l.model)
	l.sel.SetAutoselect(false)
	l.sel.SetCanUnselect(true)
	l.view = gtk.NewColumnView(l.sel)

	l.column("From", 180, func(row contract.MessageRow) gtk.Widgetter {
		return l.text(row.From.String(), row.Unread)
	})
	l.column("Subject", 0, func(row contract.MessageRow) gtk.Widgetter {
		return l.text(row.Subject, row.Unread)
	})
	l.column("Date", 110, func(row contract.MessageRow) gtk.Widgetter {
		label := gtk.NewLabel(formatDate(row.Date, time.Now()))
		label.SetXAlign(1)
		label.AddCSSClass("dim-label")
		return label
	})
	l.column("", 60, func(row contract.MessageRow) gtk.Widgetter {
		return l.flags(row)
	})

	l.sel.NotifyProperty("selected", l.openSelected)
	l.view.ConnectActivate(func(uint) { l.openSelected() })
	return l
}

// column adds one column whose cell is rebuilt by fill on every bind.
func (l *messageList) column(title string, width int, fill func(row contract.MessageRow) gtk.Widgetter) {
	factory := gtk.NewSignalListItemFactory()
	factory.ConnectSetup(func(obj *glib.Object) {
		item := &gtk.ListItem{Object: obj}
		cell := gtk.NewBox(gtk.OrientationHorizontal, 4)
		cell.SetMarginTop(2)
		cell.SetMarginBottom(2)
		item.SetChild(cell)
	})
	factory.ConnectBind(func(obj *glib.Object) {
		item := &gtk.ListItem{Object: obj}
		pos := int(item.Position())
		if pos < 0 || pos >= len(l.rows) {
			return
		}
		// A fresh cell per bind: replacing the child is cheaper than walking
		// the old widget tree, and binds are rare (one per visible row).
		cell := gtk.NewBox(gtk.OrientationHorizontal, 4)
		cell.SetMarginTop(2)
		cell.SetMarginBottom(2)
		cell.Append(fill(l.rows[pos]))
		item.SetChild(cell)
	})
	col := gtk.NewColumnViewColumn(title, &factory.ListItemFactory)
	col.SetExpand(width == 0)
	col.SetResizable(true)
	if width > 0 {
		col.SetFixedWidth(width)
	}
	l.view.AppendColumn(col)
}

// text is one text cell, bold and undimmed while the message is unread.
func (l *messageList) text(text string, unread bool) *gtk.Label {
	label := gtk.NewLabel("")
	label.SetUseMarkup(true)
	if unread {
		label.SetMarkup("<b>" + gv2.MarkupEscapeText(text) + "</b>")
	} else {
		label.SetMarkup(gv2.MarkupEscapeText(text))
	}
	label.SetXAlign(0)
	label.SetEllipsize(pango.EllipsizeEnd)
	label.SetHExpand(true)
	return label
}

// flags is the attachment/flag icon cell.
func (l *messageList) flags(row contract.MessageRow) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 4)
	if row.HasAttachments {
		box.Append(gtk.NewImageFromIconName("mail-attachment-symbolic"))
	}
	if row.Flagged {
		box.Append(gtk.NewImageFromIconName("starred-symbolic"))
	}
	return box
}

func (l *messageList) scroll() *gtk.ScrolledWindow {
	sc := gtk.NewScrolledWindow()
	sc.SetPolicy(gtk.PolicyAutomatic, gtk.PolicyAutomatic)
	sc.SetChild(l.view)
	sc.SetVExpand(true)
	return sc
}

// set replaces the rows and the model in one go.
func (l *messageList) set(rows []contract.MessageRow) {
	l.rows = rows
	subjects := make([]string, len(rows))
	for i, r := range rows {
		subjects[i] = r.Subject
	}
	l.model.Splice(0, l.model.NItems(), subjects)
}

// update replaces a single row in place, keeping the selection.
func (l *messageList) update(row contract.MessageRow) {
	for i := range l.rows {
		if l.rows[i].Ref != row.Ref {
			continue
		}
		l.rows[i] = row
		// Splicing the row back in forces GTK to rebind that cell.
		l.model.Splice(uint(i), 1, []string{row.Subject})
		return
	}
}

func (l *messageList) selected() (contract.MessageRow, bool) {
	if l.sel == nil {
		return contract.MessageRow{}, false
	}
	pos := int(l.sel.Selected())
	if pos < 0 || pos >= len(l.rows) {
		return contract.MessageRow{}, false
	}
	return l.rows[pos], true
}

func (l *messageList) openSelected() {
	if row, ok := l.selected(); ok {
		l.win.controller.OpenMessage(l.win.account, row.Ref)
	}
}
