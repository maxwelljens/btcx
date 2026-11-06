package config

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// Example file paths, relative to the configuration directory. The UI can
// show or open them once Bootstrap has written them.
const (
	ExampleConfigPath  = "config.toml"
	ExampleAccountPath = "accounts/example.toml.example"
	ExampleScriptPath  = "scripts/example.lua.example"
)

//go:embed templates/*.example
var templateFS embed.FS

// exampleTemplates maps the path written by Bootstrap to the embedded source
// that supplies its content.
var exampleTemplates = map[string]string{
	ExampleConfigPath:  "templates/config.toml.example",
	ExampleAccountPath: "templates/accounts.toml.example",
	ExampleScriptPath:  "templates/scripts.lua.example",
}

// FirstRun reports whether dir holds no btcx configuration yet: no
// config.toml and no accounts/*.toml. A missing dir counts as a first run.
func FirstRun(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, "config.toml"))
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("config: first run check %s: %w", dir, err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "accounts"))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("config: first run check %s: %w", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && !strings.HasPrefix(name, ".") && filepath.Ext(name) == ".toml" {
			return false, nil
		}
	}
	return true, nil
}

// Bootstrap creates the configuration skeleton in dir (0700, parents as
// needed) exactly once: config.toml, accounts/example.toml.example and
// scripts/example.lua.example. Existing files are never overwritten. It
// returns the slash-separated paths, relative to dir and sorted, of the files
// it actually created.
func Bootstrap(dir string) (created []string, err error) {
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("config: bootstrap %s: %w", dir, err)
	}

	rels := make([]string, 0, len(exampleTemplates))
	for rel := range exampleTemplates {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		body, err := ExampleContent(rel)
		if err != nil {
			return created, err
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return created, fmt.Errorf("config: bootstrap %s: %w", dir, err)
		}
		ok, err := writeNew(dst, body)
		if err != nil {
			return created, err
		}
		if ok {
			created = append(created, rel)
		}
	}
	return created, nil
}

// ExampleContent returns the embedded template for one of the example paths
// (ExampleConfigPath, ExampleAccountPath or ExampleScriptPath), so a UI can
// display it without reading the file system.
func ExampleContent(rel string) ([]byte, error) {
	name, ok := exampleTemplates[rel]
	if !ok {
		return nil, fmt.Errorf("config: %q is not an example file", rel)
	}
	return templateFS.ReadFile(name)
}

// WriteAccount validates a and writes it as dir/accounts/<name>.toml with
// mode 0600, creating dir and accounts/ as needed. It refuses to overwrite an
// existing file. The file loads back through LoadDir unchanged: zero ports
// and empty mailbox names are filled with the same defaults loading applies.
func WriteAccount(dir string, a Account) (string, error) {
	if !validAccountName(a.Name) {
		return "", fmt.Errorf("config: account name %q: use letters, digits, '-', '_' or '.', "+
			"without a leading dot or a .toml suffix", a.Name)
	}
	normalizeAccount(&a)
	if err := a.validate(); err != nil {
		return "", fmt.Errorf("config: account %s: %w", a.Name, err)
	}

	accountDir := filepath.Join(dir, "accounts")
	if err := os.MkdirAll(accountDir, 0o700); err != nil {
		return "", fmt.Errorf("config: write account %s: %w", a.Name, err)
	}
	path := filepath.Join(accountDir, a.Name+".toml")

	var buf bytes.Buffer
	buf.WriteString("# btcx account. Keep this file private (chmod 600) when it\n" +
		"# contains a password. Field reference: example.toml.example.\n\n")
	if err := toml.NewEncoder(&buf).Encode(accountFile(a)); err != nil {
		return "", fmt.Errorf("config: encode account %s: %w", a.Name, err)
	}

	// #nosec G304 -- path is the fixed accounts dir joined to a name already
	// validated by a.validate(); O_EXCL prevents overwriting.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("config: %s already exists; refusing to overwrite", path)
	}
	if err != nil {
		return "", fmt.Errorf("config: create %s: %w", path, err)
	}
	_, werr := f.Write(buf.Bytes())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("config: write %s: %w", path, werr)
	}
	return path, nil
}

// writeNew creates path with mode 0600 and writes body. It reports false and
// no error when path already exists.
func writeNew(path string, body []byte) (bool, error) {
	// #nosec G304 -- path is built by Bootstrap from the config dir and fixed
	// template names; O_EXCL prevents overwriting.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("config: create %s: %w", path, err)
	}
	_, werr := f.Write(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, fmt.Errorf("config: write %s: %w", path, werr)
	}
	return true, nil
}

// validAccountName reports whether name is usable as accounts/<name>.toml.
func validAccountName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".toml") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// normalizeAccount fills the settings loading would default, so the written
// file states them explicitly and reads back identically.
func normalizeAccount(a *Account) {
	if a.IMAP.TLS == "" {
		a.IMAP.TLS = defaultIMAPTLS
	}
	if a.SMTP.TLS == "" {
		a.SMTP.TLS = defaultSMTPTLS
	}
	if a.IMAP.Port == 0 {
		a.IMAP.Port = defaultPort(a.IMAP.TLS, 143, 993)
	}
	if a.SMTP.Port == 0 {
		a.SMTP.Port = defaultPort(a.SMTP.TLS, 587, 465)
	}
	if a.SentMailbox == "" {
		a.SentMailbox = defaultSentBox
	}
	if a.DraftsMailbox == "" {
		a.DraftsMailbox = defaultDraftsBox
	}
	if a.TrashMailbox == "" {
		a.TrashMailbox = defaultTrashBox
	}
}

// accountFile converts the domain form into the TOML form.
func accountFile(a Account) fileAccount {
	return fileAccount{
		DisplayName: a.DisplayName,
		Email:       a.Email,
		RealName:    a.RealName,
		IMAP:        a.IMAP.fileServer(),
		SMTP:        a.SMTP.fileServer(),
		Sent:        fileMailbox{Mailbox: a.SentMailbox},
		Drafts:      fileMailbox{Mailbox: a.DraftsMailbox},
		Trash:       fileMailbox{Mailbox: a.TrashMailbox},
		Signature:   a.Signature,
	}
}

func (s Server) fileServer() fileServer {
	f := fileServer{
		Host:               s.Host,
		Port:               s.Port,
		Username:           s.Username,
		Password:           s.Password,
		TLS:                string(s.TLS),
		InsecureSkipVerify: s.InsecureSkipVerify,
		CAFile:             s.CAFile,
	}
	if len(s.PasswordCommand) > 0 {
		f.PasswordCommand = joinCommand(s.PasswordCommand)
	}
	return f
}

// joinCommand renders argv so SplitCommand parses it back to the same argv:
// arguments made only of safe characters stay bare, the rest are single
// quoted, with embedded single quotes escaped as '\”.
func joinCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		if needsQuote(arg) {
			parts[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		} else {
			parts[i] = arg
		}
	}
	return strings.Join(parts, " ")
}

func needsQuote(arg string) bool {
	if arg == "" {
		return true
	}
	for _, r := range arg {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r),
			r == '-', r == '_', r == '.', r == '/', r == ':', r == '@', r == '+', r == '=', r == ',':
		default:
			return true
		}
	}
	return false
}
