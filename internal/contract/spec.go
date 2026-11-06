package contract

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
)

// accountNameRE is the accepted shape of an account name. The name becomes the
// accounts/<name>.toml file stem, so it must stay a plain lowercase word.
var accountNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// FieldError reports a validation failure on one AccountSpec field so the
// wizard can point at the offending entry. Field is a dotted spec path
// ("email", "imap.host", "smtp.port", "password").
type FieldError struct {
	Field   string
	Message string
}

// Error implements error.
func (e *FieldError) Error() string { return e.Message }

// ValidateAccountEmail reports whether email is one bare address: the account
// is written with it as the From address, so display names are not accepted.
func ValidateAccountEmail(email string) error {
	email = strings.TrimSpace(email)
	switch {
	case email == "":
		return fmt.Errorf("email is required")
	case strings.ContainsAny(email, "\r\n"):
		return fmt.Errorf("e-mail address must not contain line breaks")
	case strings.Contains(email, ","):
		return fmt.Errorf("enter one e-mail address, not a list")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return fmt.Errorf("not a valid e-mail address: %w", err)
	}
	if addr.Address != email {
		return fmt.Errorf("email = %q: enter the bare e-mail address, without a display name", email)
	}
	return nil
}

// DeriveAccountName returns the account file stem derived from email's domain:
// only [a-z0-9_-] survives and a leading '-' or '_' is trimmed.
func DeriveAccountName(email string) string {
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

// Normalize validates spec and fills its defaults in place: account name
// derived from the address domain, display name from the local part, ports
// from the TLS mode (993/143 IMAP, 465/587 SMTP) and empty usernames from
// Email. Errors are *FieldError naming the offending field; the file-level
// rules in config.WriteAccount stay the second gate.
func (s *AccountSpec) Normalize() error {
	s.Email = strings.TrimSpace(s.Email)
	if err := ValidateAccountEmail(s.Email); err != nil {
		return &FieldError{Field: "email", Message: err.Error()}
	}

	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		s.Name = DeriveAccountName(s.Email)
	}
	if !accountNameRE.MatchString(s.Name) {
		return &FieldError{
			Field:   "name",
			Message: fmt.Sprintf("name = %q: use lowercase letters, digits, '-' or '_'", s.Name),
		}
	}

	s.DisplayName = strings.TrimSpace(s.DisplayName)
	if s.DisplayName == "" {
		s.DisplayName, _, _ = strings.Cut(s.Email, "@")
	}

	s.PasswordCommand = strings.TrimSpace(s.PasswordCommand)
	if (s.Password == "") == (s.PasswordCommand == "") {
		return &FieldError{Field: "password", Message: "enter either password or password_command"}
	}

	if err := s.IMAP.normalize("imap", 993, 143, s.Email); err != nil {
		return err
	}
	return s.SMTP.normalize("smtp", 465, 587, s.Email)
}

// normalize validates one server and applies its port and username defaults.
func (s *ServerSpec) normalize(which string, implicitPort, starttlsPort int, email string) error {
	s.Host = strings.TrimSpace(s.Host)
	if s.Host == "" {
		return &FieldError{Field: which + ".host", Message: which + ".host is required"}
	}
	if strings.ContainsAny(s.Host+s.Username, "\r\n") {
		return &FieldError{Field: which + ".host", Message: which + " server details must not contain line breaks"}
	}

	s.TLS = strings.ToLower(strings.TrimSpace(s.TLS))
	switch s.TLS {
	case "tls", "starttls":
	default:
		return &FieldError{
			Field:   which + ".tls",
			Message: fmt.Sprintf("%s.tls = %q: must be \"tls\" or \"starttls\"", which, s.TLS),
		}
	}

	if s.Port == 0 {
		s.Port = implicitPort
		if s.TLS == "starttls" {
			s.Port = starttlsPort
		}
	}
	if s.Port < 1 || s.Port > 65535 {
		return &FieldError{
			Field:   which + ".port",
			Message: fmt.Sprintf("%s.port = %d: must be between 1 and 65535", which, s.Port),
		}
	}

	s.Username = strings.TrimSpace(s.Username)
	if s.Username == "" {
		s.Username = email
	}
	return nil
}
