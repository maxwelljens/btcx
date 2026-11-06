package ui

import (
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"btcx/internal/contract"
	"btcx/internal/model"
)

const (
	setupWidth  = 600
	setupHeight = 720
)

// SetupWindow walks the user through creating an account without touching a
// file: it collects an AccountSpec and hands it to the controller, which owns
// validation of the written TOML. Widgets here hold no mail logic.
type SetupWindow struct {
	*gtk.ApplicationWindow

	app *app

	display  *gtk.Entry
	email    *gtk.Entry
	name     *gtk.Entry
	imapHost *gtk.Entry
	imapPort *gtk.SpinButton
	imapUser *gtk.Entry
	imapTLS  *gtk.ComboBoxText
	smtpHost *gtk.Entry
	smtpPort *gtk.SpinButton
	smtpUser *gtk.Entry
	smtpTLS  *gtk.ComboBoxText

	passStack   *gtk.Stack
	password    *gtk.PasswordEntry
	passCommand *gtk.Entry
	signature   *gtk.TextView

	testBtn *gtk.Button
	spinner *gtk.Spinner
	status  *gtk.Label
	imapOK  *gtk.Label
	smtpOK  *gtk.Label

	// autoUser and derivedName remember the values this window filled in
	// itself, so typing an address only overwrites what the user did not edit.
	autoUser    string
	derivedName string
}

func newSetupWindow(a *app, w *MainWindow) *SetupWindow {
	s := &SetupWindow{
		ApplicationWindow: gtk.NewApplicationWindow(a.gtkApp),
		app:               a,
	}
	s.SetTitle("Set up an e-mail account")
	s.SetDefaultSize(setupWidth, setupHeight)
	s.SetModal(true)
	if w != nil {
		s.SetTransientFor(&w.Window)
	}

	bar := gtk.NewHeaderBar()
	title := gtk.NewLabel("Set up an e-mail account")
	title.AddCSSClass("bold")
	bar.SetTitleWidget(title)
	s.SetTitlebar(bar)

	s.SetChild(s.buildBody())
	s.SetDefaultWidget(s.testBtn)
	s.ConnectDestroy(func() {
		if s.app.setup == s {
			s.app.setup = nil
		}
	})
	return s
}

func (s *SetupWindow) buildBody() gtk.Widgetter {
	box := gtk.NewBox(gtk.OrientationVertical, 18)
	box.SetMarginTop(18)
	box.SetMarginBottom(18)
	box.SetMarginStart(20)
	box.SetMarginEnd(20)

	if s.app.opts.FirstRun {
		box.Append(s.buildFirstRunBanner())
	}

	box.Append(sectionLabel("Your details"))
	details := newFormGrid()
	s.display = gtk.NewEntry()
	s.display.SetPlaceholderText("Jane Doe")
	s.email = gtk.NewEntry()
	s.email.SetPlaceholderText("jane@example.org")
	s.email.SetInputPurpose(gtk.InputPurposeEmail)
	s.email.ConnectChanged(s.emailChanged)
	s.name = gtk.NewEntry()
	s.name.SetPlaceholderText("derived from your e-mail domain")
	appendRow(details, 0, "Display name", s.display)
	appendRow(details, 1, "E-mail address", s.email)
	appendRow(details, 2, "Account name", s.name)
	box.Append(details)

	detect := gtk.NewButtonWithLabel("Detect settings")
	detect.SetHAlign(gtk.AlignStart)
	detect.SetTooltipText("btcx guesses imap./smtp.<your domain>; many providers differ — check their IMAP/SMTP help page")
	detect.ConnectClicked(s.detect)
	box.Append(detect)

	box.Append(sectionLabel("Incoming server (IMAP)"))
	imap := newFormGrid()
	s.imapHost = gtk.NewEntry()
	s.imapHost.SetPlaceholderText("imap.example.org")
	s.imapPort = gtk.NewSpinButtonWithRange(1, 65535, 1)
	s.imapPort.SetNumeric(true)
	s.imapPort.SetValue(993)
	s.imapUser = gtk.NewEntry()
	s.imapUser.SetPlaceholderText("jane@example.org")
	s.imapTLS = tlsCombo(0)
	appendRow(imap, 0, "Host", s.imapHost)
	appendRow(imap, 1, "Port", s.imapPort)
	appendRow(imap, 2, "Username", s.imapUser)
	appendRow(imap, 3, "Security", s.imapTLS)
	box.Append(imap)

	box.Append(sectionLabel("Outgoing server (SMTP)"))
	smtp := newFormGrid()
	s.smtpHost = gtk.NewEntry()
	s.smtpHost.SetPlaceholderText("smtp.example.org")
	s.smtpPort = gtk.NewSpinButtonWithRange(1, 65535, 1)
	s.smtpPort.SetNumeric(true)
	s.smtpPort.SetValue(587)
	s.smtpUser = gtk.NewEntry()
	s.smtpUser.SetPlaceholderText("jane@example.org")
	s.smtpTLS = tlsCombo(1)
	appendRow(smtp, 0, "Host", s.smtpHost)
	appendRow(smtp, 1, "Port", s.smtpPort)
	appendRow(smtp, 2, "Username", s.smtpUser)
	appendRow(smtp, 3, "Security", s.smtpTLS)
	box.Append(smtp)

	testRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	s.testBtn = gtk.NewButton()
	s.testBtn.SetHAlign(gtk.AlignStart)
	s.testBtn.ConnectClicked(s.test)
	btnBox := gtk.NewBox(gtk.OrientationHorizontal, 6)
	btnBox.Append(gtk.NewLabel("Test connection"))
	s.spinner = gtk.NewSpinner()
	s.spinner.SetVisible(false)
	btnBox.Append(s.spinner)
	s.testBtn.SetChild(btnBox)
	testRow.Append(s.testBtn)
	box.Append(testRow)

	s.imapOK = verifyLabel()
	s.smtpOK = verifyLabel()
	box.Append(s.imapOK)
	box.Append(s.smtpOK)

	box.Append(sectionLabel("Password"))
	s.passStack = gtk.NewStack()
	s.password = gtk.NewPasswordEntry()
	s.password.SetShowPeekIcon(true)
	passPage := gtk.NewBox(gtk.OrientationVertical, 4)
	passPage.Append(s.password)
	passPage.Append(hintLabel("Stored in your account file (mode 0600)."))
	s.passCommand = gtk.NewEntry()
	s.passCommand.SetPlaceholderText("pass show mail/example.org")
	cmdPage := gtk.NewBox(gtk.OrientationVertical, 4)
	cmdPage.Append(s.passCommand)
	cmdPage.Append(hintLabel("Run without a shell each time btcx connects — works with pass, gopass, secret-tool…"))
	s.passStack.AddNamed(passPage, "password")
	s.passStack.AddNamed(cmdPage, "command")
	switcher := gtk.NewStackSwitcher()
	switcher.SetStack(s.passStack)
	switcher.SetHAlign(gtk.AlignStart)
	box.Append(switcher)
	box.Append(s.passStack)

	box.Append(sectionLabel("Signature (optional)"))
	s.signature = gtk.NewTextView()
	s.signature.SetWrapMode(gtk.WrapWordChar)
	s.signature.SetTopMargin(6)
	s.signature.SetBottomMargin(6)
	s.signature.SetLeftMargin(6)
	s.signature.SetRightMargin(6)
	sigScroll := gtk.NewScrolledWindow()
	sigScroll.SetPolicy(gtk.PolicyAutomatic, gtk.PolicyAutomatic)
	sigScroll.SetChild(s.signature)
	sigScroll.SetSizeRequest(-1, 70)
	box.Append(sigScroll)

	s.status = gtk.NewLabel("")
	s.status.SetXAlign(0)
	s.status.SetWrap(true)
	box.Append(s.status)

	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetVExpand(true)
	scroll.SetChild(box)

	footer := gtk.NewBox(gtk.OrientationHorizontal, 8)
	footer.AddCSSClass("toolbar")
	footer.SetMarginTop(6)
	footer.SetMarginBottom(6)
	footer.SetMarginStart(12)
	footer.SetMarginEnd(12)

	hand := gtk.NewButtonWithLabel("Edit files by hand instead")
	hand.AddCSSClass("flat")
	hand.ConnectClicked(s.handEdit)
	footer.Append(hand)

	spacer := gtk.NewBox(gtk.OrientationHorizontal, 0)
	spacer.SetHExpand(true)
	footer.Append(spacer)

	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.ConnectClicked(s.close)
	footer.Append(cancel)

	create := gtk.NewButtonWithLabel("Create account")
	create.AddCSSClass("suggested-action")
	create.ConnectClicked(s.create)
	footer.Append(create)

	root := gtk.NewBox(gtk.OrientationVertical, 0)
	root.Append(scroll)
	root.Append(footer)
	return &root.Widget
}

// buildFirstRunBanner explains what btcx generated on this launch.
func (s *SetupWindow) buildFirstRunBanner() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 6)
	box.AddCSSClass("card")
	box.SetMarginTop(8)
	box.SetMarginBottom(8)
	box.SetMarginStart(12)
	box.SetMarginEnd(12)

	text := gtk.NewLabel(generatedFilesText(s.app.opts.GeneratedFiles))
	text.SetXAlign(0)
	text.SetWrap(true)
	text.SetSelectable(true)
	box.Append(text)

	view := gtk.NewButtonWithLabel("View example")
	view.AddCSSClass("flat")
	view.SetHAlign(gtk.AlignStart)
	if path := s.app.examplePath(); path != "" {
		view.ConnectClicked(func() { s.app.openPath(path) })
	} else {
		view.SetSensitive(false)
	}
	box.Append(view)
	return box
}

// close dismisses the wizard and leaves the main window on its empty state if
// no account exists yet.
func (s *SetupWindow) close() {
	s.app.setup = nil
	s.Close()
}

// emailChanged live-validates the address and fills the fields that are a
// direct consequence of it, until the user edits them.
func (s *SetupWindow) emailChanged() {
	email := strings.TrimSpace(s.email.Text())
	if _, err := parseEmail(email); err != nil {
		s.email.SetIconFromIconName(gtk.EntryIconPrimary, "dialog-error-symbolic")
		s.email.SetIconTooltipText(gtk.EntryIconPrimary, err.Error())
	} else {
		s.email.SetIconFromIconName(gtk.EntryIconPrimary, "")
	}
	if s.imapUser.Text() == "" || s.imapUser.Text() == s.autoUser {
		s.imapUser.SetText(email)
		s.smtpUser.SetText(email)
		s.autoUser = email
	}
	if s.name.Text() == "" || s.name.Text() == s.derivedName {
		s.derivedName = deriveAccountName(email)
		s.name.SetText(s.derivedName)
	}
}

// detect fills the server fields from the address domain and immediately
// tests the guess.
func (s *SetupWindow) detect() {
	email := strings.TrimSpace(s.email.Text())
	if _, err := parseEmail(email); err != nil {
		s.showFormError(err)
		return
	}
	imap, smtp := guessServers(email)
	s.imapHost.SetText(imap.Host)
	s.imapPort.SetValue(float64(imap.Port))
	s.imapUser.SetText(imap.Username)
	s.imapTLS.SetActive(0)
	s.smtpHost.SetText(smtp.Host)
	s.smtpPort.SetValue(float64(smtp.Port))
	s.smtpUser.SetText(smtp.Username)
	s.smtpTLS.SetActive(1)
	s.autoUser = email
	s.test()
}

// collect reads the widgets into an AccountSpec and validates its shape.
func (s *SetupWindow) collect() (contract.AccountSpec, error) {
	spec := contract.AccountSpec{
		Name:        strings.TrimSpace(s.name.Text()),
		DisplayName: strings.TrimSpace(s.display.Text()),
		Email:       strings.TrimSpace(s.email.Text()),
		IMAP: contract.ServerSpec{
			Host:     strings.TrimSpace(s.imapHost.Text()),
			Port:     s.imapPort.ValueAsInt(),
			Username: strings.TrimSpace(s.imapUser.Text()),
			TLS:      tlsAt(s.imapTLS),
		},
		SMTP: contract.ServerSpec{
			Host:     strings.TrimSpace(s.smtpHost.Text()),
			Port:     s.smtpPort.ValueAsInt(),
			Username: strings.TrimSpace(s.smtpUser.Text()),
			TLS:      tlsAt(s.smtpTLS),
		},
		Signature: s.signatureText(),
	}
	if spec.Name == "" {
		spec.Name = deriveAccountName(spec.Email)
	}
	if s.passStack.VisibleChildName() == "command" {
		spec.PasswordCommand = strings.TrimSpace(s.passCommand.Text())
	} else {
		spec.Password = s.password.Text()
	}
	if err := validateSpec(spec); err != nil {
		return spec, err
	}
	return spec, nil
}

func (s *SetupWindow) signatureText() string {
	buf := s.signature.Buffer()
	return buf.Text(buf.StartIter(), buf.EndIter(), true)
}

// test asks the controller to verify the current form contents.
func (s *SetupWindow) test() {
	spec, err := s.collect()
	if err != nil {
		s.showFormError(err)
		return
	}
	s.status.SetText("")
	s.status.RemoveCSSClass("error")
	s.imapOK.SetText("")
	s.imapOK.RemoveCSSClass("success")
	s.imapOK.RemoveCSSClass("error")
	s.smtpOK.SetText("")
	s.smtpOK.RemoveCSSClass("success")
	s.smtpOK.RemoveCSSClass("error")
	s.setTesting(true)
	s.app.controller.VerifyAccount(spec)
}

// showVerify paints the outcome of VerifyAccount.
func (s *SetupWindow) showVerify(result contract.VerifyResult) {
	s.setTesting(false)
	showCheck(s.imapOK, "IMAP", result.IMAP)
	showCheck(s.smtpOK, "SMTP", result.SMTP)
}

func (s *SetupWindow) setTesting(on bool) {
	s.testBtn.SetSensitive(!on)
	if on {
		s.spinner.SetVisible(true)
		s.spinner.Start()
	} else {
		s.spinner.Stop()
		s.spinner.SetVisible(false)
	}
}

// create writes the account and returns the main window to configured mail.
func (s *SetupWindow) create() {
	spec, err := s.collect()
	if err != nil {
		s.showFormError(err)
		return
	}
	if _, err := s.app.controller.CreateAccount(spec); err != nil {
		s.showError("Cannot create account", err.Error())
		return
	}
	name := spec.Name
	if name == "" {
		name = "account"
	}
	s.close()
	if s.app.win != nil {
		s.app.controller.Reload()
		s.app.win.setAccounts(s.app.controller.Accounts())
		s.app.win.setStatus("Account "+name+" created", false)
	}
}

// handEdit points the user at the config files and closes the wizard.
func (s *SetupWindow) handEdit() {
	s.app.openPath(s.app.opts.ConfigDir)
	s.close()
	s.app.showDialog("Edit files by hand",
		"Copy accounts/example.toml.example to accounts/yourname.toml and edit it, then press Reload configuration.", false)
}

func (s *SetupWindow) showFormError(err error) {
	s.status.AddCSSClass("error")
	s.status.SetText(err.Error())
}

func (s *SetupWindow) showError(title, message string) {
	dlg := gtk.NewMessageDialog(&s.Window, gtk.DialogModal, gtk.MessageError, gtk.ButtonsClose)
	dlg.SetTitle(title)
	dlg.SetMarkup("<b>" + glib.MarkupEscapeText(title) + "</b>\n\n" + glib.MarkupEscapeText(message))
	dlg.ConnectResponse(func(int) { dlg.Destroy() })
	dlg.Present()
}

func newFormGrid() *gtk.Grid {
	grid := gtk.NewGrid()
	grid.SetRowSpacing(8)
	grid.SetColumnSpacing(12)
	return grid
}

func appendRow(grid *gtk.Grid, row int, label string, child gtk.Widgetter) {
	l := gtk.NewLabel(label)
	l.SetXAlign(1)
	grid.Attach(l, 0, row, 1, 1)
	if w, ok := child.(interface{ SetHExpand(bool) }); ok {
		w.SetHExpand(true)
	}
	grid.Attach(child, 1, row, 1, 1)
}

func sectionLabel(text string) *gtk.Label {
	l := gtk.NewLabel(text)
	l.SetXAlign(0)
	l.AddCSSClass("title-4")
	return l
}

func hintLabel(text string) *gtk.Label {
	l := gtk.NewLabel(text)
	l.SetXAlign(0)
	l.SetWrap(true)
	l.AddCSSClass("dim-label")
	return l
}

func verifyLabel() *gtk.Label {
	l := gtk.NewLabel("")
	l.SetXAlign(0)
	l.SetWrap(true)
	l.SetVisible(false)
	return l
}

func tlsCombo(active int) *gtk.ComboBoxText {
	c := gtk.NewComboBoxText()
	c.AppendText("TLS (recommended)")
	c.AppendText("STARTTLS")
	c.SetActive(active)
	return c
}

func tlsAt(c *gtk.ComboBoxText) string {
	if c.Active() == 1 {
		return "starttls"
	}
	return "tls"
}

// showCheck renders one server result; Detail is server-controlled text and
// only ever set as a plain label.
func showCheck(l *gtk.Label, name string, c contract.ServerCheck) {
	l.SetVisible(true)
	l.RemoveCSSClass("success")
	l.RemoveCSSClass("error")
	detail := c.Detail
	if c.OK {
		if detail == "" {
			detail = "connected"
		}
		l.AddCSSClass("success")
		l.SetText("✓ " + name + ": " + detail)
		return
	}
	if detail == "" {
		detail = "connection failed"
	}
	l.AddCSSClass("error")
	l.SetText("✗ " + name + ": " + detail)
}

// parseEmail returns the one bare address in s. The application writes it as
// the account's From address, so display names are not accepted here.
func parseEmail(s string) (model.Address, error) {
	s = strings.TrimSpace(s)
	list, err := model.ParseAddressList(s)
	if err != nil {
		return model.Address{}, fmt.Errorf("not a valid e-mail address: %w", err)
	}
	switch {
	case len(list) == 0:
		return model.Address{}, fmt.Errorf("enter your e-mail address")
	case len(list) > 1:
		return model.Address{}, fmt.Errorf("enter one e-mail address, not a list")
	case list[0].Addr == "" || !strings.Contains(list[0].Addr, "@"):
		return model.Address{}, fmt.Errorf("not a valid e-mail address")
	case list[0].Addr != s:
		return model.Address{}, fmt.Errorf("enter the bare e-mail address, without a display name")
	}
	return list[0], nil
}

// deriveAccountName mirrors the application's derivation of an account file
// stem from the address domain: only [a-z0-9_-] survives.
func deriveAccountName(email string) string {
	_, domain, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToLower(domain) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "-_")
}

// guessServers is the documented heuristic behind "Detect settings": IMAP on
// imap.<domain>:993 TLS, SMTP on smtp.<domain>:587 STARTTLS.
func guessServers(email string) (contract.ServerSpec, contract.ServerSpec) {
	email = strings.TrimSpace(email)
	at := strings.LastIndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return contract.ServerSpec{}, contract.ServerSpec{}
	}
	domain := strings.ToLower(email[at+1:])
	return contract.ServerSpec{
		Host: "imap." + domain, Port: 993, Username: email, TLS: "tls",
	}, contract.ServerSpec{
		Host: "smtp." + domain, Port: 587, Username: email, TLS: "starttls",
	}
}

// validateSpec covers the form's own rules: a single address, two usable
// servers, and exactly one credential source.
func validateSpec(spec contract.AccountSpec) error {
	addr, err := parseEmail(spec.Email)
	if err != nil {
		return err
	}
	if strings.ContainsAny(addr.Addr, "\r\n") {
		return fmt.Errorf("e-mail address must not contain line breaks")
	}
	if err := validateServer("incoming", spec.IMAP); err != nil {
		return err
	}
	if err := validateServer("outgoing", spec.SMTP); err != nil {
		return err
	}
	if (spec.Password == "") == (spec.PasswordCommand == "") {
		return fmt.Errorf("enter either a password or a password command")
	}
	return nil
}

func validateServer(kind string, s contract.ServerSpec) error {
	if strings.TrimSpace(s.Host) == "" {
		return fmt.Errorf("%s server host is required", kind)
	}
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("%s server port must be between 1 and 65535", kind)
	}
	if s.TLS != "tls" && s.TLS != "starttls" {
		return fmt.Errorf("%s server security must be TLS or STARTTLS", kind)
	}
	if strings.ContainsAny(s.Host+s.Username, "\r\n") {
		return fmt.Errorf("%s server details must not contain line breaks", kind)
	}
	return nil
}

// generatedFilesText is the first-run banner: what btcx created this launch.
func generatedFilesText(files []string) string {
	const lead = "First launch — btcx created example configuration for you"
	if len(files) == 0 {
		return lead + "."
	}
	return lead + ":\n• " + strings.Join(files, "\n• ")
}
