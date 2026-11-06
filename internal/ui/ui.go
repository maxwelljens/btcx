// Package ui is the GTK4 front end of btcx.
//
// It is deliberately dumb: values arrive as contract view models and are put
// into widgets, widget events turn back into contract.Controller calls. No
// mail parsing, no address formatting beyond plain string joining, no HTML
// handling — bodies arrive as ready-to-display Pango markup.
//
// Threading: contract.Events methods may be called from any goroutine, so
// every one of them hops onto the GTK main loop with glib.IdleAdd before
// touching a widget. contract.Controller calls are made on the main loop only.
//
// Message list: a gtk.ColumnView over a gtk.StringList of subjects, with the
// full rows kept in a parallel Go slice indexed by list position. gotk4 has no
// low-cost way to expose a Go struct as a GObject list item (a GObject subclass
// plus one property per column), so the position doubles as the row index. The
// list is rebuilt wholesale via OnMessages, so the two stay in step.
//
// Deliberate gotk4 avoidance: the generated getters that cast a GObject to a
// concrete Go type (Widget.FirstChild, ListItem.Child, ListModel.Item, ...) go
// through a GValue walk that trips -race's checkptr mode. Cells are therefore
// rebuilt and re-attached by pointer instead of being looked up again.
package ui

import (
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"btcx/internal/contract"
)

// Options configures the UI process.
type Options struct {
	Title   string // window title, e.g. "btcx"
	Theme   string // "system" | "dark" | "light"
	Version string // shown in the About dialog and the window subtitle

	// ConfigDir is the btcx configuration directory. The welcome page opens
	// it in the file manager and offers the example account file from it.
	ConfigDir string
	// FirstRun is true when the program generated the example configuration
	// on this launch; the setup window then explains what was created.
	FirstRun bool
	// GeneratedFiles lists what FirstRun created, relative to ConfigDir.
	GeneratedFiles []string
	// AppID is the GApplication id; empty means "org.btcx.Mail".
	// Tests set a unique id so a running btcx instance (GApplication is
	// single-instance) cannot swallow the test activation.
	AppID string
}

// Run builds the GTK application, wires controller and events, runs the main
// loop on argv and returns the process exit code.
func Run(opts Options, controller contract.Controller, argv []string) int {
	a := &app{opts: opts, controller: controller}

	// The app layer needs a handle on the UI to deliver Events, but the
	// contract has no setter: a Controller that wants events exposes
	// SetEvents(contract.Events) (internal/app.Controller must do so).
	if sink, ok := controller.(interface{ SetEvents(contract.Events) }); ok {
		sink.SetEvents(a)
	}

	appID := opts.AppID
	if appID == "" {
		appID = "org.btcx.Mail"
	}
	gtkApp := gtk.NewApplication(appID, gio.ApplicationDefaultFlags)
	a.gtkApp = gtkApp

	gtkApp.ConnectActivate(func() {
		applyTheme(opts.Theme)
		if a.win != nil {
			a.win.Present()
			return
		}
		a.win = newMainWindow(a)
		if err := controller.Start(); err != nil {
			a.win.showError(err)
		}
		a.win.Present()
	})
	// Quitting the window must tear down connections and watchers.
	gtkApp.ConnectShutdown(func() { controller.Shutdown() })

	return gtkApp.Run(argv)
}

// applyTheme sets the dark/light preference; "system" leaves GTK's default.
// It runs on activate, after GTK has initialised its settings.
func applyTheme(theme string) {
	if theme != "dark" && theme != "light" {
		return
	}
	if settings := gtk.SettingsGetDefault(); settings != nil {
		settings.SetObjectProperty("gtk-application-prefer-dark-theme", theme == "dark")
	}
}

// app owns the GTK application and the windows that outlive a single message,
// and implements contract.Events for all of them.
type app struct {
	opts       Options
	controller contract.Controller
	gtkApp     *gtk.Application
	win        *MainWindow
	setup      *SetupWindow // main-loop only, guarded by being created there

	mu       sync.Mutex
	composes map[*ComposeWindow]struct{}
	edits    map[contract.EditRequestID]*ComposeWindow
	sends    map[contract.SendID]*ComposeWindow
}

var _ contract.Events = (*app)(nil)

// idle runs f on the GTK main loop. Safe from any goroutine.
func (a *app) idle(f func()) {
	glib.IdleAdd(f)
}

// onWindow runs f on the main loop with the main window, if it exists yet.
func (a *app) onWindow(f func(w *MainWindow)) {
	a.idle(func() {
		if a.win != nil {
			f(a.win)
		}
	})
}

func (a *app) OnStatus(message string) {
	a.onWindow(func(w *MainWindow) { w.setStatus(message, false) })
}

func (a *app) OnError(err error) {
	if err == nil {
		return
	}
	a.onWindow(func(w *MainWindow) { w.showError(err) })
}

func (a *app) OnFolders(account string, folders []contract.FolderInfo) {
	a.onWindow(func(w *MainWindow) { w.setFolders(account, folders) })
}

func (a *app) OnMessages(account, folder string, rows []contract.MessageRow) {
	a.onWindow(func(w *MainWindow) { w.setMessages(account, folder, rows) })
}

func (a *app) OnMessageChanged(row contract.MessageRow) {
	a.onWindow(func(w *MainWindow) { w.msgs.update(row) })
}

func (a *app) OnNewMail(account string, row contract.MessageRow) {
	a.onWindow(func(w *MainWindow) {
		if account == w.account {
			w.setStatus("New mail: "+row.Subject, false)
		}
	})
}

func (a *app) OnMessageOpened(view contract.MessageView) {
	a.onWindow(func(w *MainWindow) { w.showMessage(view) })
}

func (a *app) OnVerifyResult(result contract.VerifyResult) {
	a.idle(func() {
		if a.setup != nil {
			a.setup.showVerify(result)
		}
	})
}

func (a *app) OnSendResult(id contract.SendID, success bool, message string) {
	a.idle(func() {
		if message == "" {
			message = "Message sent"
			if !success {
				message = "Sending failed"
			}
		}
		a.mu.Lock()
		sender := a.sends[id]
		delete(a.sends, id)
		a.mu.Unlock()
		if sender == nil {
			slog.Default().Debug("ui: send result for an unknown compose window", "id", int(id))
			return
		}
		if success {
			sender.forceClose()
			a.setStatusOnMain(message)
			return
		}
		if a.win != nil {
			a.win.setStatus(message, true)
		}
		a.showDialog("Send failed", message)
	})
}

// setStatusOnMain shows a status message, if the main window exists yet.
func (a *app) setStatusOnMain(message string) {
	if a.win != nil {
		a.win.setStatus(message, false)
	}
}

// trackSend remembers which compose window is waiting for send result id.
func (a *app) trackSend(id contract.SendID, c *ComposeWindow) {
	a.mu.Lock()
	if a.sends == nil {
		a.sends = make(map[contract.SendID]*ComposeWindow)
	}
	a.sends[id] = c
	a.mu.Unlock()
}

// OnDraftReady opens the draft StartReply/StartForward built. Any ready draft
// is the one the user asked for, so it needs no window correlation.
func (a *app) OnDraftReady(_ contract.EditRequestID, draft contract.ComposeDraft) {
	a.onWindow(func(w *MainWindow) { w.compose(draft) })
}

func (a *app) OnExternalEditDone(id contract.EditRequestID, draft contract.ComposeDraft, err error) {
	a.idle(func() {
		a.mu.Lock()
		win := a.edits[id]
		delete(a.edits, id)
		a.mu.Unlock()
		if win == nil {
			return
		}
		if err != nil {
			a.showDialog("External editor failed", err.Error())
			return
		}
		win.applyDraft(draft)
		if a.win != nil {
			a.win.setStatus("Editor closed", false)
		}
	})
}

// showDialog reports a failure to the user. Modal dialogs are reserved for
// errors the user has to acknowledge; everything else goes to the status bar.
func (a *app) showDialog(title, message string) {
	if a.win == nil {
		return // nothing to parent a dialog to; there is no UI yet
	}
	alertWindow(&a.win.Window, title, message, "", "Close", nil)
}

// alertWindow shows a modal message with one or two buttons. gotk4 v0.4.1
// wraps gtk.AlertDialog without a constructor, so this is the plain window the
// GTK deprecation note of gtk.MessageDialog points at.
func alertWindow(parent *gtk.Window, title, message, cancelLabel, acceptLabel string, onAccept func()) *gtk.Window {
	win := gtk.NewWindow()
	win.SetTitle(title)
	win.SetTransientFor(parent)
	win.SetModal(true)
	win.SetResizable(false)

	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetMarginTop(18)
	box.SetMarginBottom(18)
	box.SetMarginStart(18)
	box.SetMarginEnd(18)
	text := gtk.NewLabel("<b>" + glib.MarkupEscapeText(title) + "</b>\n\n" + glib.MarkupEscapeText(message))
	text.SetUseMarkup(true)
	text.SetWrap(true)
	text.SetMaxWidthChars(60)
	text.SetXAlign(0)
	box.Append(text)

	buttons := gtk.NewBox(gtk.OrientationHorizontal, 8)
	buttons.SetHAlign(gtk.AlignEnd)
	if cancelLabel != "" {
		cancel := gtk.NewButtonWithLabel(cancelLabel)
		cancel.ConnectClicked(func() { win.Close() })
		buttons.Append(cancel)
	}
	accept := gtk.NewButtonWithLabel(acceptLabel)
	accept.ConnectClicked(func() {
		win.Close()
		if onAccept != nil {
			onAccept()
		}
	})
	buttons.Append(accept)
	box.Append(buttons)
	win.SetChild(box)

	// Esc must not accept a destructive action, so it only closes.
	keys := gtk.NewEventControllerKey()
	keys.ConnectKeyPressed(func(keyval, _ uint, _ gdk.ModifierType) bool {
		if keyval != gdk.KEY_Escape {
			return false
		}
		win.Close()
		return true
	})
	win.AddController(keys)
	win.Present()
	return win
}

func (a *app) addCompose(c *ComposeWindow) {
	a.mu.Lock()
	if a.composes == nil {
		a.composes = make(map[*ComposeWindow]struct{})
	}
	a.composes[c] = struct{}{}
	a.mu.Unlock()
}

func (a *app) removeCompose(c *ComposeWindow) {
	a.mu.Lock()
	delete(a.composes, c)
	a.mu.Unlock()
}

func (a *app) trackEdit(id contract.EditRequestID, c *ComposeWindow) {
	a.mu.Lock()
	if a.edits == nil {
		a.edits = make(map[contract.EditRequestID]*ComposeWindow)
	}
	a.edits[id] = c
	a.mu.Unlock()
}

// showAbout opens the About dialog.
func (a *app) showAbout() {
	version := a.opts.Version
	if version == "" {
		version = "development"
	}
	a.showDialog("btcx", "btcx — a lean mail user agent\nVersion "+version)
}

// openSetup raises the setup window, creating it the first time.
func (a *app) openSetup() {
	if a.setup != nil {
		a.setup.Present()
		return
	}
	a.setup = newSetupWindow(a, a.win)
	a.setup.Present()
}

// examplePath is the generated sample account file, or "" when no config
// directory is known.
func (a *app) examplePath() string {
	if a.opts.ConfigDir == "" {
		return ""
	}
	return filepath.Join(a.opts.ConfigDir, "accounts", "example.toml.example")
}

// openPath opens a file or folder in the user's default application.
func (a *app) openPath(path string) {
	if path == "" {
		return
	}
	uri := gio.NewFileForPath(path).URI()
	if err := gio.AppInfoLaunchDefaultForURI(uri, nil); err != nil {
		a.showDialog("Cannot open "+path, err.Error())
	}
}

// openURL opens a link in the user's default handler. Links clicked in mail
// never navigate inside btcx (the web view ignores all navigation).
func (a *app) openURL(uri string) {
	if uri == "" {
		return
	}
	if err := gio.AppInfoLaunchDefaultForURI(uri, nil); err != nil {
		a.showDialog("Cannot open link", err.Error())
	}
}
