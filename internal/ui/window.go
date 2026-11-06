package ui

import (
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"btcx/internal/contract"
)

const (
	windowWidth  = 1100
	windowHeight = 720
	foldersWidth = 190
	searchDelay  = 300 // ms of typing silence before a search runs
	statusDelay  = 6000
)

// MainWindow is the application window: folder sidebar, message list, reader
// pane and status bar.
type MainWindow struct {
	*gtk.ApplicationWindow

	app        *app
	controller contract.Controller

	accounts  []contract.AccountInfo
	accountDD *gtk.DropDown
	search    *gtk.SearchEntry
	searchSrc glib.SourceHandle

	folders     *gtk.ListBox
	folderNames []string
	folderCache map[string][]contract.FolderInfo
	account     string
	folder      string

	msgs     *messageList
	reader   *readerPane
	spinner  *gtk.Spinner
	status   *gtk.Label
	statusTk glib.SourceHandle
}

func newMainWindow(a *app) *MainWindow {
	w := &MainWindow{
		ApplicationWindow: gtk.NewApplicationWindow(a.gtkApp),
		app:               a,
		controller:        a.controller,
		folderCache:       make(map[string][]contract.FolderInfo),
	}

	title := a.opts.Title
	if title == "" {
		title = "btcx"
	}
	w.SetTitle(title)
	w.SetDefaultSize(windowWidth, windowHeight)
	w.SetTitlebar(w.buildHeader())

	w.msgs = newMessageList(w)
	w.reader = newReaderPane(w)
	w.SetChild(w.buildBody())
	w.addActions()

	w.account = w.controller.ActiveAccount()
	w.setAccounts(w.controller.Accounts())
	return w
}

func (w *MainWindow) buildHeader() *gtk.HeaderBar {
	bar := gtk.NewHeaderBar()

	w.accountDD = gtk.NewDropDown(gtk.NewStringList(nil), nil)
	w.accountDD.SetTooltipText("Switch account")

	menu := gio.NewMenu()
	menu.Append("About btcx", "app.about")
	menu.Append("Quit", "app.quit")
	menuBtn := gtk.NewMenuButton()
	menuBtn.SetIconName("open-menu-symbolic")
	popover := gtk.NewPopoverMenuFromModel(menu)
	menuBtn.SetPopover(&popover.Popover)
	menuBtn.SetTooltipText("Main menu")

	left := gtk.NewBox(gtk.OrientationHorizontal, 6)
	left.Append(menuBtn)
	left.Append(w.accountDD)
	bar.PackStart(left)

	w.search = gtk.NewSearchEntry()
	w.search.SetPlaceholderText("Search mail…")
	w.search.SetSizeRequest(320, -1)
	w.search.ConnectSearchChanged(w.searchChanged)
	bar.SetTitleWidget(w.search)

	refresh := gtk.NewButtonFromIconName("view-refresh-symbolic")
	refresh.SetTooltipText("Refresh (Ctrl+F5)")
	refresh.ConnectClicked(func() { w.controller.Refresh(w.account) })

	compose := gtk.NewButtonWithLabel("✎ Compose")
	compose.AddCSSClass("suggested-action")
	compose.SetTooltipText("New message (Ctrl+N)")
	compose.ConnectClicked(func() { w.compose(w.controller.NewCompose(w.account)) })

	right := gtk.NewBox(gtk.OrientationHorizontal, 6)
	right.Append(refresh)
	right.Append(compose)
	bar.PackEnd(right)

	return bar
}

func (w *MainWindow) buildBody() gtk.Widgetter {
	w.folders = gtk.NewListBox()
	w.folders.SetSelectionMode(gtk.SelectionSingle)
	w.folders.ConnectRowActivated(func(row *gtk.ListBoxRow) {
		if i := int(row.Index()); i >= 0 && i < len(w.folderNames) {
			w.selectFolder(w.folderNames[i])
		}
	})
	folderScroll := gtk.NewScrolledWindow()
	folderScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	folderScroll.SetSizeRequest(foldersWidth, -1)
	folderScroll.SetChild(w.folders)

	vertical := gtk.NewPaned(gtk.OrientationVertical)
	vertical.SetStartChild(w.msgs.scroll())
	vertical.SetEndChild(w.reader.widget())
	vertical.SetPosition(windowHeight / 2)

	paned := gtk.NewPaned(gtk.OrientationHorizontal)
	paned.SetStartChild(folderScroll)
	paned.SetEndChild(vertical)
	paned.SetPosition(foldersWidth)
	paned.SetResizeStartChild(false)

	w.spinner = gtk.NewSpinner()
	w.spinner.SetVisible(false)
	w.status = gtk.NewLabel("")
	w.status.SetXAlign(0)
	w.status.SetEllipsize(pango.EllipsizeEnd)

	statusBar := gtk.NewBox(gtk.OrientationHorizontal, 6)
	statusBar.AddCSSClass("toolbar")
	statusBar.SetMarginTop(4)
	statusBar.SetMarginBottom(4)
	statusBar.SetMarginStart(8)
	statusBar.Append(w.spinner)
	statusBar.Append(w.status)

	root := gtk.NewBox(gtk.OrientationVertical, 0)
	root.Append(paned)
	root.Append(statusBar)
	return &root.Widget
}

// setAccounts fills the account switcher.
func (w *MainWindow) setAccounts(accounts []contract.AccountInfo) {
	w.accounts = accounts
	names := make([]string, len(accounts))
	selected := uint(0)
	for i, a := range accounts {
		names[i] = a.Name
		if a.Name == w.account {
			selected = uint(i)
		}
	}
	if w.account == "" && len(accounts) > 0 {
		w.account = accounts[0].Name
		selected = 0
	}
	w.accountDD.SetModel(gtk.NewStringList(names))
	w.accountDD.SetSelected(selected)
	w.accountDD.NotifyProperty("selected", func() {
		i := int(w.accountDD.Selected())
		if i < 0 || i >= len(w.accounts) || w.accounts[i].Name == w.account {
			return
		}
		w.selectAccount(w.accounts[i].Name)
	})
}

// selectAccount switches account. The previous account's mail, reader page and
// images are not the new account's, so they are cleared until the app delivers
// results; that also stops Reply from acting on the account we just left.
func (w *MainWindow) selectAccount(account string) {
	w.account = account
	w.folder = "" // the account's current folder is not known yet
	w.msgs.set(nil)
	w.reader.clear()
	w.setStatus("Loading "+account+"…", false)
	if cached, ok := w.folderCache[account]; ok {
		w.setFolders(account, cached)
	}
	w.controller.SelectAccount(account)
}

// selectFolder remembers the folder and asks the app for it. Results for any
// other folder are dropped until the user changes selection again.
func (w *MainWindow) selectFolder(folder string) {
	w.folder = folder
	w.controller.SelectFolder(w.account, folder)
}

// setFolders replaces the sidebar for an account and keeps the selection when
// the folder list is re-delivered.
func (w *MainWindow) setFolders(account string, folders []contract.FolderInfo) {
	w.folderCache[account] = folders
	if account != w.account {
		return
	}
	w.folders.RemoveAll()
	w.folderNames = w.folderNames[:0]
	var selected, first *gtk.ListBoxRow
	for _, f := range folders {
		label := gtk.NewLabel("")
		label.SetUseMarkup(true)
		label.SetXAlign(0)
		text := glib.MarkupEscapeText(folderLabel(f))
		if f.Role != "" {
			text = "<b>" + text + "</b>"
		}
		label.SetMarkup(text)
		row := gtk.NewListBoxRow()
		row.SetChild(label)
		w.folders.Append(row)
		w.folderNames = append(w.folderNames, f.Name)
		if first == nil {
			first = row
		}
		if f.Name == w.folder {
			selected = row
		}
	}
	// The app decides which folder is current; the sidebar only highlights it,
	// falling back to the first row before the first OnMessages arrives.
	if selected == nil {
		selected = first
	}
	if selected != nil {
		w.folders.SelectRow(selected)
	}
}

// staleResult reports whether an async delivery belongs to a selection other
// than the current one. Events may arrive out of order, so a result is only
// shown while its account and folder still match what the user last asked for;
// an empty side is "not known yet" and matches anything.
func staleResult(account, folder, curAccount, curFolder string) bool {
	if account != "" && curAccount != "" && account != curAccount {
		return true
	}
	return folder != "" && curFolder != "" && folder != curFolder
}

// setMessages replaces the message list of the current folder or search.
func (w *MainWindow) setMessages(account, folder string, rows []contract.MessageRow) {
	if staleResult(account, folder, w.account, w.folder) {
		return // late result for an account or folder the user has left
	}
	if folder != "" {
		w.folder = folder
	}
	w.msgs.set(rows)
}

// showMessage paints the reader, unless the view belongs to another account
// and outlived the switch to the current one.
func (w *MainWindow) showMessage(view contract.MessageView) {
	if staleResult(view.Account, "", w.account, "") {
		return
	}
	w.reader.show(view)
}

// searchChanged debounces typing and then either searches or, for an empty
// entry, returns to the selected folder.
func (w *MainWindow) searchChanged() {
	if w.searchSrc != 0 {
		glib.SourceRemove(w.searchSrc)
	}
	w.searchSrc = glib.TimeoutAdd(searchDelay, func() bool {
		w.searchSrc = 0
		query := w.search.Text()
		if query == "" {
			w.controller.SelectFolder(w.account, w.folder)
		} else {
			w.controller.Search(w.account, w.folder, query)
		}
		return false
	})
}

// setStatus shows a transient message in the status bar; errors are red.
func (w *MainWindow) setStatus(message string, isError bool) {
	if w.statusTk != 0 {
		glib.SourceRemove(w.statusTk)
		w.statusTk = 0
	}
	w.status.SetLabel(message)
	if isError {
		w.status.AddCSSClass("error")
	} else {
		w.status.RemoveCSSClass("error")
	}
	w.spinner.SetVisible(message != "")
	if message != "" {
		w.spinner.Start()
	} else {
		w.spinner.Stop()
	}
	if message != "" {
		w.statusTk = glib.TimeoutAdd(statusDelay, func() bool {
			w.statusTk = 0
			w.status.SetLabel("")
			w.status.RemoveCSSClass("error")
			w.spinner.Stop()
			w.spinner.SetVisible(false)
			return false
		})
	}
}

// showError reports a failure without interrupting the user: red status text.
// Only send failures and explicit action failures get a dialog.
func (w *MainWindow) showError(err error) {
	w.setStatus(err.Error(), true)
}

func (w *MainWindow) selected() (contract.MessageRow, bool) {
	if w.msgs == nil {
		return contract.MessageRow{}, false
	}
	return w.msgs.selected()
}

// compose opens a compose window for a draft.
func (w *MainWindow) compose(draft contract.ComposeDraft) {
	if draft.Account == "" {
		draft.Account = w.account
	}
	cw := newComposeWindow(w, draft)
	cw.Present()
}

// selectedRef is the currently selected message, or false.
func (w *MainWindow) selectedRef() (contract.MessageRow, bool) {
	row, ok := w.selected()
	if !ok {
		w.setStatus("No message selected", false)
	}
	return row, ok
}

// addActions registers the named actions and their accelerators.
func (w *MainWindow) addActions() {
	type entry struct {
		name    string
		accels  []string
		handler func()
	}
	entries := []entry{
		{"compose", []string{"<Control>n"}, func() { w.compose(w.controller.NewCompose(w.account)) }},
		{"reply", []string{"<Control>r"}, func() {
			if row, ok := w.selectedRef(); ok {
				w.compose(w.controller.ReplyTo(w.account, row.Ref, false))
			}
		}},
		{"reply-all", []string{"<Control><Shift>r"}, func() {
			if row, ok := w.selectedRef(); ok {
				w.compose(w.controller.ReplyTo(w.account, row.Ref, true))
			}
		}},
		{"forward", []string{"<Control>l"}, func() {
			if row, ok := w.selectedRef(); ok {
				w.compose(w.controller.Forward(w.account, row.Ref))
			}
		}},
		{"search", []string{"<Control>f"}, func() { w.search.GrabFocus() }},
		{"quit", []string{"<Control>q"}, func() { w.app.gtkApp.Quit() }},
		{"refresh", []string{"<Control>F5"}, func() { w.controller.Refresh(w.account) }},
		{"delete", []string{"Delete"}, func() {
			if row, ok := w.selectedRef(); ok {
				w.controller.DeleteMessage(w.account, row.Ref)
			}
		}},
		{"toggle-read", []string{"<Control>u"}, func() {
			if row, ok := w.selectedRef(); ok {
				w.controller.MarkSeen(w.account, row.Ref, row.Unread)
			}
		}},
		{"toggle-flag", []string{"<Control>d"}, func() {
			if row, ok := w.selectedRef(); ok {
				w.controller.SetFlagged(w.account, row.Ref, !row.Flagged)
			}
		}},
		{"about", nil, func() { w.app.showAbout() }},
	}

	// Ctrl+W closes the focused compose window through its own "win.close"
	// action, so no window has to be tracked by identity.
	w.app.gtkApp.SetAccelsForAction("win.close", []string{"<Control>w"})

	for _, e := range entries {
		action := gio.NewSimpleAction(e.name, nil)
		action.ConnectActivate(func(*glib.Variant) { e.handler() })
		w.app.gtkApp.AddAction(action)
		if len(e.accels) > 0 {
			w.app.gtkApp.SetAccelsForAction("app."+e.name, e.accels)
		}
	}
}
