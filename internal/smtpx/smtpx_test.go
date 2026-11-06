package smtpx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"btcx/internal/model"
)

const testUser = "alice"
const testPass = "s3cr3t-not-in-errors"

// rawMsg is a well-formed CRLF-terminated message with no Bcc header.
const rawMsg = "From: Alice <alice@example.org>\r\n" +
	"To: Bob <bob@example.net>\r\n" +
	"Subject: hello\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"body line one\r\nbody line two\r\n"

type testMsg struct {
	from  string
	rcpts []string
	data  []byte
}

type testBackend struct {
	mu       sync.Mutex
	cert     *tls.Certificate
	implicit bool

	noAuth        bool
	onlyLogin     bool
	allowInsecure bool
	rejectRcpt    map[string]bool
	dataDelay     time.Duration

	msgs  []testMsg
	auths []string
}

func (b *testBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &testSession{be: b}, nil
}

func (b *testBackend) messages() []testMsg {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]testMsg(nil), b.msgs...)
}

func (b *testBackend) authAttempts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.auths...)
}

func (b *testBackend) add(m testMsg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, m)
}

type testSession struct {
	be    *testBackend
	from  string
	rcpts []string
}

func (s *testSession) Mail(from string, _ *smtp.MailOptions) error {
	s.from, s.rcpts = from, nil
	return nil
}

func (s *testSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.be.mu.Lock()
	reject := s.be.rejectRcpt[to]
	s.be.mu.Unlock()
	if reject {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
	}
	s.rcpts = append(s.rcpts, to)
	return nil
}

func (s *testSession) Data(r io.Reader) error {
	s.be.mu.Lock()
	delay := s.be.dataDelay
	s.be.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.be.add(testMsg{from: s.from, rcpts: append([]string(nil), s.rcpts...), data: data})
	return nil
}

func (s *testSession) Reset()        { s.from, s.rcpts = "", nil }
func (s *testSession) Logout() error { return nil }

func (s *testSession) AuthMechanisms() []string {
	switch {
	case s.be.noAuth:
		return nil
	case s.be.onlyLogin:
		return []string{sasl.Login}
	default:
		return []string{sasl.Plain, sasl.Login}
	}
}

func (s *testSession) Auth(mech string) (sasl.Server, error) {
	s.be.mu.Lock()
	s.be.auths = append(s.be.auths, mech)
	s.be.mu.Unlock()
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, user, pass string) error { return checkCreds(user, pass) }), nil
	case sasl.Login:
		return &loginServer{check: checkCreds}, nil
	}
	return nil, smtp.ErrAuthUnknownMechanism
}

func checkCreds(user, pass string) error {
	if user != testUser || pass != testPass {
		return &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "invalid credentials"}
	}
	return nil
}

// loginServer is the LOGIN mechanism server side; go-sasl only ships a client.
type loginServer struct {
	step  int
	user  string
	check func(user, pass string) error
}

func (l *loginServer) Next(resp []byte) ([]byte, bool, error) {
	if l.step == 0 {
		l.step++
		l.user = string(resp)
		return []byte("Password:"), false, nil
	}
	return nil, true, l.check(l.user, string(resp))
}

// startServer runs an in-process go-smtp server and returns its port.
func startServer(t *testing.T, be *testBackend) int {
	t.Helper()
	srv := smtp.NewServer(be)
	srv.Domain = "localhost"
	srv.AllowInsecureAuth = be.allowInsecure
	srv.ErrorLog = log.New(io.Discard, "", 0)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if be.cert != nil {
		tc := &tls.Config{Certificates: []tls.Certificate{*be.cert}, MinVersion: tls.VersionTLS12}
		if be.implicit {
			l = tls.NewListener(l, tc)
		} else {
			srv.TLSConfig = tc
		}
	}
	go srv.Serve(l) //nolint:errcheck // returns on Close
	t.Cleanup(func() { srv.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// selfSignedCert returns a self-signed certificate valid for 127.0.0.1 and
// localhost, plus its PEM encoding for use as a CA file.
func selfSignedCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "smtpx test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func tlsBackend(t *testing.T) *testBackend {
	t.Helper()
	cert, _ := selfSignedCert(t)
	return &testBackend{cert: &cert}
}

func baseConfig(port int) SendConfig {
	return SendConfig{
		Host:               "127.0.0.1",
		Port:               port,
		Username:           testUser,
		Password:           testPass,
		TLS:                TLSStartTLS,
		InsecureSkipVerify: true,
		From:               model.Address{Name: "Alice", Addr: "alice@example.org"},
		Timeout:            5 * time.Second,
	}
}

func recipients(addrs ...string) []model.Address {
	out := make([]model.Address, len(addrs))
	for i, a := range addrs {
		out[i] = model.Address{Addr: a}
	}
	return out
}

func TestSendSuccess(t *testing.T) {
	be := tlsBackend(t)
	port := startServer(t, be)
	// bcc@example.org appears only in the envelope, never in rawMsg.
	rcpts := recipients("bob@example.net", "bcc@example.org")

	if err := Send(context.Background(), baseConfig(port), []byte(rawMsg), rcpts); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := be.messages()
	if len(msgs) != 1 {
		t.Fatalf("server got %d messages, want 1", len(msgs))
	}
	if msgs[0].from != "alice@example.org" {
		t.Errorf("MAIL FROM = %q, want alice@example.org", msgs[0].from)
	}
	wantRcpts := []string{"bob@example.net", "bcc@example.org"}
	if strings.Join(msgs[0].rcpts, ",") != strings.Join(wantRcpts, ",") {
		t.Errorf("RCPT TO = %v, want %v", msgs[0].rcpts, wantRcpts)
	}
	if string(msgs[0].data) != rawMsg {
		t.Errorf("payload = %q, want %q", msgs[0].data, rawMsg)
	}
}

func TestSendImplicitTLS(t *testing.T) {
	cert, _ := selfSignedCert(t)
	be := &testBackend{cert: &cert, implicit: true}
	port := startServer(t, be)

	cfg := baseConfig(port)
	cfg.TLS = TLSImplicit
	if err := Send(context.Background(), cfg, []byte(rawMsg), recipients("bob@example.net")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := len(be.messages()); got != 1 {
		t.Fatalf("server got %d messages, want 1", got)
	}
	if len(be.authAttempts()) == 0 {
		t.Error("expected AUTH over implicit TLS")
	}
}

func TestTLSPolicy(t *testing.T) {
	cert, caPEM := selfSignedCert(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*SendConfig)
		wantErr string
	}{
		{name: "self-signed rejected by default", mutate: func(c *SendConfig) { c.InsecureSkipVerify = false }, wantErr: "certificate"},
		{name: "insecure skip verify", mutate: func(c *SendConfig) { c.InsecureSkipVerify = true }},
		{name: "ca file verifies", mutate: func(c *SendConfig) { c.CAFile = caFile }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			be := &testBackend{cert: &cert}
			port := startServer(t, be)
			cfg := baseConfig(port)
			tt.mutate(&cfg)

			err := Send(context.Background(), cfg, []byte(rawMsg), recipients("bob@example.net"))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestStartTLSRequired(t *testing.T) {
	// The backend would accept plaintext AUTH, so an attempted AUTH is
	// observable rather than rejected by the server.
	be := &testBackend{allowInsecure: true}
	port := startServer(t, be)

	err := Send(context.Background(), baseConfig(port), []byte(rawMsg), recipients("bob@example.net"))
	if err == nil || !strings.Contains(err.Error(), "does not support STARTTLS") {
		t.Fatalf("err = %v, want STARTTLS refusal", err)
	}
	if got := be.authAttempts(); len(got) != 0 {
		t.Errorf("AUTH attempted over plaintext: %v", got)
	}
	if got := len(be.messages()); got != 0 {
		t.Errorf("message delivered over plaintext: %d", got)
	}
}

func TestAuthFailure(t *testing.T) {
	be := tlsBackend(t)
	port := startServer(t, be)
	cfg := baseConfig(port)
	cfg.Password = "wrong-password"

	err := Send(context.Background(), cfg, []byte(rawMsg), recipients("bob@example.net"))
	if err == nil {
		t.Fatal("Send succeeded with a bad password")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("err = %v, want an authentication failure", err)
	}
	if strings.Contains(err.Error(), cfg.Password) || strings.Contains(err.Error(), testPass) {
		t.Errorf("err leaks the password: %v", err)
	}
}

func TestAuthLoginFallback(t *testing.T) {
	be := tlsBackend(t)
	be.onlyLogin = true
	port := startServer(t, be)

	if err := Send(context.Background(), baseConfig(port), []byte(rawMsg), recipients("bob@example.net")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	auths := be.authAttempts()
	if len(auths) == 0 || auths[len(auths)-1] != sasl.Login {
		t.Errorf("auth attempts = %v, want a LOGIN fallback", auths)
	}
}

func TestNoAuthWithoutUsername(t *testing.T) {
	be := tlsBackend(t)
	port := startServer(t, be)
	cfg := baseConfig(port)
	cfg.Username, cfg.Password = "", ""

	if err := Send(context.Background(), cfg, []byte(rawMsg), recipients("bob@example.net")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := be.authAttempts(); len(got) != 0 {
		t.Errorf("AUTH attempted without a username: %v", got)
	}
}

func TestRecipientValidation(t *testing.T) {
	cfg := baseConfig(587)
	tests := []struct {
		name  string
		raw   []byte
		rcpts []model.Address
	}{
		{name: "no recipients", raw: []byte(rawMsg), rcpts: nil},
		{name: "crlf injection", raw: []byte(rawMsg), rcpts: recipients("a@b.c\r\nRCPT TO: <evil@x>")},
		{name: "no at sign", raw: []byte(rawMsg), rcpts: recipients("no-at-sign")},
		{name: "space", raw: []byte(rawMsg), rcpts: recipients("a b@c.d")},
		{name: "empty local part", raw: []byte(rawMsg), rcpts: recipients("@c.d")},
		{name: "angle brackets", raw: []byte(rawMsg), rcpts: recipients("<a@b.c>")},
		{name: "empty message", raw: nil, rcpts: recipients("bob@example.net")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Send(context.Background(), cfg, tt.raw, tt.rcpts)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
		})
	}
}

func TestRecipientRejected(t *testing.T) {
	be := tlsBackend(t)
	be.rejectRcpt = map[string]bool{"bad@example.org": true}
	port := startServer(t, be)

	rcpts := recipients("bob@example.net", "bad@example.org")
	err := Send(context.Background(), baseConfig(port), []byte(rawMsg), rcpts)

	var re *RecipientError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RecipientError", err)
	}
	if len(re.Failed) != 1 || re.Failed[0].Addr != "bad@example.org" {
		t.Errorf("Failed = %v, want only bad@example.org", re.Failed)
	}
	if !strings.Contains(err.Error(), "550") {
		t.Errorf("err = %v, want the server reply", err)
	}
	if got := len(be.messages()); got != 0 {
		t.Errorf("partial batch delivered %d messages, want 0", got)
	}
}

func TestContextCancellation(t *testing.T) {
	be := tlsBackend(t)
	be.dataDelay = 5 * time.Second
	port := startServer(t, be)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := Send(ctx, baseConfig(port), []byte(rawMsg), recipients("bob@example.net"))
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Send blocked for %v after cancellation", elapsed)
	}
}

func TestDotStuffingRoundTrip(t *testing.T) {
	be := tlsBackend(t)
	port := startServer(t, be)

	raw := "Subject: dots\r\n\r\n" +
		".leading dot\r\n" +
		"trailing text\r\n" +
		".\r\n" +
		"..double\r\n"
	if err := Send(context.Background(), baseConfig(port), []byte(raw), recipients("bob@example.net")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	msgs := be.messages()
	if len(msgs) != 1 {
		t.Fatalf("server got %d messages, want 1", len(msgs))
	}
	if string(msgs[0].data) != raw {
		t.Errorf("payload changed in transit:\n got %q\nwant %q", msgs[0].data, raw)
	}
}

func TestMissingTrailingCRLF(t *testing.T) {
	be := tlsBackend(t)
	port := startServer(t, be)

	raw := "Subject: no final newline\r\n\r\nlast line"
	if err := Send(context.Background(), baseConfig(port), []byte(raw), recipients("bob@example.net")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	msgs := be.messages()
	if len(msgs) != 1 {
		t.Fatalf("server got %d messages, want 1", len(msgs))
	}
	if want := raw + "\r\n"; string(msgs[0].data) != want {
		t.Errorf("payload = %q, want %q", msgs[0].data, want)
	}
}

func TestPortDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  SendConfig
		want int
	}{
		{name: "implicit default", cfg: SendConfig{TLS: TLSImplicit}, want: 465},
		{name: "starttls default", cfg: SendConfig{TLS: TLSStartTLS}, want: 587},
		{name: "explicit wins", cfg: SendConfig{TLS: TLSStartTLS, Port: 2525}, want: 2525},
	}
	for _, tt := range tests {
		if got := tt.cfg.port(); got != tt.want {
			t.Errorf("%s: port() = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SendConfig)
	}{
		{name: "empty host", mutate: func(c *SendConfig) { c.Host = "" }},
		{name: "bad tls mode", mutate: func(c *SendConfig) { c.TLS = 0 }},
		{name: "bad sender", mutate: func(c *SendConfig) { c.From.Addr = "alice@example.org\r\nRCPT TO: <x@y>" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig(587)
			tt.mutate(&cfg)
			err := Send(context.Background(), cfg, []byte(rawMsg), recipients("bob@example.net"))
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
		})
	}
}

func TestVerifySuccess(t *testing.T) {
	be := tlsBackend(t)
	port := startServer(t, be)

	if err := Verify(context.Background(), baseConfig(port)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := len(be.authAttempts()); got == 0 {
		t.Error("Verify did not authenticate")
	}
	if got := len(be.messages()); got != 0 {
		t.Errorf("Verify delivered %d messages, want 0", got)
	}
}

func TestVerifyWithoutUsername(t *testing.T) {
	be := tlsBackend(t)
	port := startServer(t, be)
	cfg := baseConfig(port)
	cfg.Username, cfg.Password = "", ""

	if err := Verify(context.Background(), cfg); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := be.authAttempts(); len(got) != 0 {
		t.Errorf("Verify authenticated without a username: %v", got)
	}
}

func TestVerifyAuthFailure(t *testing.T) {
	be := tlsBackend(t)
	port := startServer(t, be)
	cfg := baseConfig(port)
	cfg.Password = "wrong-password"

	err := Verify(context.Background(), cfg)
	if err == nil {
		t.Fatal("Verify succeeded with a bad password")
	}
	if !strings.Contains(err.Error(), "smtpx: verify:") || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("err = %v, want an authentication-stage failure", err)
	}
	if strings.Contains(err.Error(), cfg.Password) || strings.Contains(err.Error(), testPass) {
		t.Errorf("err leaks the password: %v", err)
	}
}

func TestVerifyRequiresSTARTTLS(t *testing.T) {
	be := &testBackend{allowInsecure: true}
	port := startServer(t, be)

	err := Verify(context.Background(), baseConfig(port))
	if err == nil || !strings.Contains(err.Error(), "does not support STARTTLS") {
		t.Fatalf("err = %v, want STARTTLS refusal", err)
	}
	if got := be.authAttempts(); len(got) != 0 {
		t.Errorf("Verify authenticated over plaintext: %v", got)
	}
}

func TestVerifyTLSPolicy(t *testing.T) {
	cert, caPEM := selfSignedCert(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*SendConfig)
		wantErr string
	}{
		{name: "self-signed rejected by default", mutate: func(c *SendConfig) { c.InsecureSkipVerify = false }, wantErr: "certificate"},
		{name: "insecure skip verify", mutate: func(c *SendConfig) { c.InsecureSkipVerify = true }},
		{name: "ca file verifies", mutate: func(c *SendConfig) { c.CAFile = caFile }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			be := &testBackend{cert: &cert}
			port := startServer(t, be)
			cfg := baseConfig(port)
			tt.mutate(&cfg)

			err := Verify(context.Background(), cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SendConfig)
	}{
		{name: "empty host", mutate: func(c *SendConfig) { c.Host = "" }},
		{name: "bad tls mode", mutate: func(c *SendConfig) { c.TLS = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig(587)
			tt.mutate(&cfg)
			var ve *ValidationError
			if err := Verify(context.Background(), cfg); !errors.As(err, &ve) {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
		})
	}
}

// stallingServer accepts SMTP connections and never sends a greeting, so both
// the TLS handshake and the SMTP dialogue stall forever.
func stallingServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestSendStalledServerTimesOut(t *testing.T) {
	port := stallingServer(t)
	const timeout = time.Second

	for _, tt := range []struct {
		name string
		mode TLSMode
	}{
		{"implicit", TLSImplicit},
		{"starttls", TLSStartTLS},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig(port)
			cfg.TLS = tt.mode
			cfg.Timeout = timeout

			start := time.Now()
			err := Send(context.Background(), cfg, []byte(rawMsg), recipients("bob@example.net"))
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("Send succeeded against a server that never responds")
			}
			if elapsed > 5*time.Second {
				t.Errorf("Send blocked for %v with a %v timeout", elapsed, timeout)
			}
		})
	}
}
