// Package config loads and validates btcx configuration from
// $XDG_CONFIG_HOME/btcx (see DefaultDir).
//
// The layout is a single config.toml plus one accounts/<name>.toml per
// account, where the account name is the file stem. Files are TOML. Unknown
// keys never fail loading; they are reported as Warnings so that a config
// written for a newer btcx still loads here. Invalid values are hard errors
// that name the offending key and the accepted values.
//
// A missing config.toml yields built-in defaults plus a Warning, so btcx can
// start before anything is configured. A missing accounts directory yields an
// empty account list plus a Warning for the same reason.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// TLSMode is the transport security mode for an IMAP or SMTP connection.
// Plaintext is deliberately not representable: btcx never sends credentials
// unencrypted.
type TLSMode string

// Supported TLS modes.
const (
	// TLSImplicit is TLS from the first byte (IMAP 993, SMTP 465).
	TLSImplicit TLSMode = "tls"
	// TLSStartTLS upgrades a plaintext connection before authentication
	// (IMAP 143, SMTP 587).
	TLSStartTLS TLSMode = "starttls"
)

// Built-in defaults for keys absent from a file.
const (
	defaultTheme      = "system"
	defaultLogLevel   = "info"
	defaultSentBox    = "Sent"
	defaultDraftsBox  = "Drafts"
	defaultTrashBox   = "Trash"
	defaultIMAPTLS    = TLSImplicit
	defaultSMTPTLS    = TLSStartTLS
	passwordTimeout   = 10 * time.Second
	worldReadableBits = 0o077
)

// Config is the validated general configuration plus every loaded account.
type Config struct {
	Theme              string
	Editor             string
	Watch              bool
	BlockRemoteContent bool
	ScriptDir          string
	ScriptEnabled      bool
	LogLevel           string
	Accounts           []Account // sorted by Name
	Dir                string    // the directory loaded from
}

// Account is one validated mail account.
type Account struct {
	Name          string // file stem of accounts/<name>.toml
	DisplayName   string
	Email         string
	RealName      string // From name; falls back to DisplayName when empty
	IMAP          Server
	SMTP          Server
	SentMailbox   string
	DraftsMailbox string
	TrashMailbox  string
	Signature     string
}

// Server is the connection settings for one mail protocol endpoint.
type Server struct {
	Host               string
	Port               int
	Username           string
	Password           string   // resolved statically at load time
	PasswordCommand    []string // argv, never run through a shell
	TLS                TLSMode
	InsecureSkipVerify bool
	CAFile             string
}

// Warning is a non-fatal problem in a config file: an unknown key, a
// dangerous setting, or a file mode that exposes a static password.
type Warning struct {
	File    string // file the warning refers to, relative to the config dir
	Key     string // offending key, may be empty
	Message string
}

// fileConfig mirrors config.toml. Its zero value is not meaningful: defaults
// are applied by seeding before decoding.
type fileConfig struct {
	Theme              string `toml:"theme"`
	Editor             string `toml:"editor"`
	Watch              bool   `toml:"watch"`
	BlockRemoteContent bool   `toml:"block_remote_content"`
	ScriptDir          string `toml:"script_dir"`
	ScriptEnabled      bool   `toml:"script_enabled"`
	LogLevel           string `toml:"log_level"`
}

// fileAccount mirrors accounts/<name>.toml.
type fileAccount struct {
	DisplayName string      `toml:"display_name"`
	Email       string      `toml:"email"`
	RealName    string      `toml:"real_name"`
	IMAP        fileServer  `toml:"imap"`
	SMTP        fileServer  `toml:"smtp"`
	Sent        fileMailbox `toml:"sent"`
	Drafts      fileMailbox `toml:"drafts"`
	Trash       fileMailbox `toml:"trash"`
	Signature   string      `toml:"signature"`
}

type fileServer struct {
	Host               string `toml:"host"`
	Port               int    `toml:"port"`
	Username           string `toml:"username"`
	Password           string `toml:"password"`
	PasswordCommand    string `toml:"password_command"`
	TLS                string `toml:"tls"`
	InsecureSkipVerify bool   `toml:"insecure_skip_verify"`
	CAFile             string `toml:"ca_file"`
}

type fileMailbox struct {
	Mailbox string `toml:"mailbox"`
}

// DefaultDir returns the btcx configuration directory:
// $XDG_CONFIG_HOME/btcx, or ~/.config/btcx when XDG_CONFIG_HOME is unset or
// relative (the XDG spec says relative values must be ignored).
func DefaultDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "btcx"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "btcx"), nil
}

// LoadDir loads config.toml and accounts/*.toml from dir. Unknown keys and
// dangerous settings are returned as warnings; malformed or invalid values
// are returned as an error, in which case the Config is nil.
func LoadDir(dir string) (*Config, []Warning, error) {
	dir = filepath.Clean(dir)
	cfg := &Config{Dir: dir}

	raw := fileConfig{
		Theme:              defaultTheme,
		Watch:              true,
		BlockRemoteContent: true,
		ScriptEnabled:      true,
		LogLevel:           defaultLogLevel,
	}
	var warnings []Warning
	md, err := toml.DecodeFile(filepath.Join(dir, "config.toml"), &raw)
	switch {
	case errors.Is(err, os.ErrNotExist):
		warnings = append(warnings, Warning{
			File:    "config.toml",
			Message: "not found; using built-in defaults",
		})
	case err != nil:
		return nil, nil, fmt.Errorf("config: %s: %w", filepath.Join(dir, "config.toml"), err)
	default:
		warnings = append(warnings, unknownKeys("config.toml", md)...)
	}

	if err := validateGeneral(raw); err != nil {
		return nil, nil, fmt.Errorf("config: config.toml: %w", err)
	}
	cfg.Theme = raw.Theme
	cfg.Editor = raw.Editor
	cfg.Watch = raw.Watch
	cfg.BlockRemoteContent = raw.BlockRemoteContent
	cfg.ScriptEnabled = raw.ScriptEnabled
	cfg.LogLevel = raw.LogLevel
	cfg.ScriptDir = raw.ScriptDir
	if cfg.ScriptDir == "" {
		cfg.ScriptDir = filepath.Join(dir, "scripts")
	}

	accounts, accountWarnings, err := loadAccounts(dir)
	if err != nil {
		return nil, nil, err
	}
	cfg.Accounts = accounts
	warnings = append(warnings, accountWarnings...)
	return cfg, warnings, nil
}

// Account looks up a loaded account by name.
func (c *Config) Account(name string) (Account, bool) {
	for _, a := range c.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return Account{}, false
}

// ResolvePassword returns the account password: the output of
// PasswordCommand when one is configured, otherwise the static Password. The
// IMAP credentials are used, falling back to SMTP when IMAP has neither, since
// the two normally share a password. Error messages never contain the
// password or the command's output.
func (a Account) ResolvePassword(ctx context.Context) (string, error) {
	s := a.IMAP
	if s.Password == "" && len(s.PasswordCommand) == 0 {
		s = a.SMTP
	}
	if len(s.PasswordCommand) > 0 {
		return runPasswordCommand(ctx, a.Name, s.PasswordCommand)
	}
	if s.Password == "" {
		return "", fmt.Errorf("config: account %s: no password or password_command configured", a.Name)
	}
	return s.Password, nil
}

// runPasswordCommand executes argv directly, never through a shell, and
// returns its trimmed stdout. On timeout the process is killed.
func runPasswordCommand(ctx context.Context, account string, argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, passwordTimeout)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("config: account %s: password_command %s: %w", account, argv[0], ctxErr)
		}
		return "", fmt.Errorf("config: account %s: password_command %s: %w", account, argv[0], err)
	}
	pw := strings.TrimSpace(out.String())
	if pw == "" {
		return "", fmt.Errorf("config: account %s: password_command %s produced no output", account, argv[0])
	}
	return pw, nil
}

// loadAccounts reads every *.toml file in dir/accounts, skipping hidden files
// and subdirectories. The result is sorted by account name.
func loadAccounts(dir string) ([]Account, []Warning, error) {
	accountDir := filepath.Join(dir, "accounts")
	entries, err := os.ReadDir(accountDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, []Warning{{
			File:    "accounts",
			Message: "directory not found; no accounts configured",
		}}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("config: read %s: %w", accountDir, err)
	}

	var (
		accounts []Account
		warnings []Warning
	)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".toml" {
			continue
		}
		account, warns, err := loadAccount(accountDir, name)
		if err != nil {
			return nil, nil, err
		}
		accounts = append(accounts, account)
		warnings = append(warnings, warns...)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
	return accounts, warnings, nil
}

// loadAccount reads and validates a single account file.
func loadAccount(dir, file string) (Account, []Warning, error) {
	path := filepath.Join(dir, file)
	rel := filepath.Join("accounts", file)
	raw := fileAccount{
		IMAP:   fileServer{TLS: string(defaultIMAPTLS)},
		SMTP:   fileServer{TLS: string(defaultSMTPTLS)},
		Sent:   fileMailbox{Mailbox: defaultSentBox},
		Drafts: fileMailbox{Mailbox: defaultDraftsBox},
		Trash:  fileMailbox{Mailbox: defaultTrashBox},
	}
	md, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return Account{}, nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if !md.IsDefined("imap", "port") {
		raw.IMAP.Port = defaultPort(TLSMode(raw.IMAP.TLS), 143, 993)
	}
	if !md.IsDefined("smtp", "port") {
		raw.SMTP.Port = defaultPort(TLSMode(raw.SMTP.TLS), 587, 465)
	}

	imapServer, err := raw.IMAP.server("imap")
	if err != nil {
		return Account{}, nil, fmt.Errorf("config: %s: %w", path, err)
	}
	smtpServer, err := raw.SMTP.server("smtp")
	if err != nil {
		return Account{}, nil, fmt.Errorf("config: %s: %w", path, err)
	}
	account := Account{
		Name:          strings.TrimSuffix(file, ".toml"),
		DisplayName:   raw.DisplayName,
		Email:         raw.Email,
		RealName:      raw.RealName,
		IMAP:          imapServer,
		SMTP:          smtpServer,
		SentMailbox:   raw.Sent.Mailbox,
		DraftsMailbox: raw.Drafts.Mailbox,
		TrashMailbox:  raw.Trash.Mailbox,
		Signature:     raw.Signature,
	}
	if err := account.validate(); err != nil {
		return Account{}, nil, fmt.Errorf("config: %s: %w", path, err)
	}

	warnings := unknownKeys(rel, md)
	for _, s := range []struct {
		key    string
		server Server
	}{{"imap", account.IMAP}, {"smtp", account.SMTP}} {
		if s.server.InsecureSkipVerify {
			warnings = append(warnings, Warning{
				File: rel,
				Key:  s.key + ".insecure_skip_verify",
				Message: "TLS certificate verification is DISABLED; mail and credentials " +
					"can be intercepted. Remove this unless you fully control the network.",
			})
		}
	}
	if account.IMAP.Password != "" || account.SMTP.Password != "" {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&worldReadableBits != 0 {
			warnings = append(warnings, Warning{
				File:    rel,
				Message: "stores a static password but is group/world readable; run chmod 600 on it",
			})
		}
	}
	return account, warnings, nil
}

// validateGeneral checks the general settings.
func validateGeneral(raw fileConfig) error {
	switch raw.Theme {
	case "system", "dark", "light":
	default:
		return fmt.Errorf("theme = %q: must be one of \"system\", \"dark\", \"light\"", raw.Theme)
	}
	switch raw.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level = %q: must be one of \"debug\", \"info\", \"warn\", \"error\"", raw.LogLevel)
	}
	if raw.Editor != "" {
		if _, err := SplitCommand(raw.Editor); err != nil {
			return fmt.Errorf("editor = %q: %w", raw.Editor, err)
		}
	}
	return nil
}

// validate checks every value that would otherwise fail at connect time.
func (a Account) validate() error {
	if a.Email == "" {
		return errors.New("email is required")
	}
	addr, err := mail.ParseAddress(a.Email)
	if err != nil || addr.Address != a.Email {
		return fmt.Errorf("email = %q: not a bare address (example: max@example.org)", a.Email)
	}
	if err := a.IMAP.validate("imap"); err != nil {
		return err
	}
	return a.SMTP.validate("smtp")
}

// validate checks one endpoint: TLS mode, host, port and credential source.
func (s Server) validate(which string) error {
	switch s.TLS {
	case TLSImplicit, TLSStartTLS:
	case TLSMode("none"):
		return fmt.Errorf("%s.tls = \"none\": plaintext connections are not supported, "+
			"use \"tls\" or \"starttls\"", which)
	default:
		return fmt.Errorf("%s.tls = %q: must be \"tls\" or \"starttls\"", which, string(s.TLS))
	}
	if s.Host == "" {
		return fmt.Errorf("%s.host is required", which)
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("%s.port = %d: must be between 1 and 65535", which, s.Port)
	}
	if s.Password != "" && len(s.PasswordCommand) > 0 {
		return fmt.Errorf("%s: set either password or password_command, not both", which)
	}
	return nil
}

// server converts the decoded file form into the validated Server.
func (f fileServer) server(which string) (Server, error) {
	s := Server{
		Host:               f.Host,
		Port:               f.Port,
		Username:           f.Username,
		Password:           f.Password,
		TLS:                TLSMode(f.TLS),
		InsecureSkipVerify: f.InsecureSkipVerify,
		CAFile:             f.CAFile,
	}
	if f.PasswordCommand != "" {
		argv, err := SplitCommand(f.PasswordCommand)
		if err != nil {
			return Server{}, fmt.Errorf("%s.password_command: %w", which, err)
		}
		s.PasswordCommand = argv
	}
	return s, nil
}

// defaultPort picks the conventional port for a TLS mode.
func defaultPort(mode TLSMode, starttls, implicit int) int {
	if mode == TLSStartTLS {
		return starttls
	}
	return implicit
}

// unknownKeys turns undecoded TOML keys into sorted warnings.
func unknownKeys(file string, md toml.MetaData) []Warning {
	keys := md.Undecoded()
	warnings := make([]Warning, 0, len(keys))
	for _, k := range keys {
		warnings = append(warnings, Warning{File: file, Key: k.String(), Message: "unknown key; ignored"})
	}
	sort.Slice(warnings, func(i, j int) bool { return warnings[i].Key < warnings[j].Key })
	return warnings
}
