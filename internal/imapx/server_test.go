package imapx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	testUser = "user"
	testPass = "secret"
)

// testServer runs the go-imap in-memory backend on 127.0.0.1. mode selects the
// transport: TLSImplicit, TLSStartTLS, or 0 for a plaintext server (used only
// to prove that STARTTLS is never silently skipped).
type testServer struct {
	mode    TLSMode
	port    int
	certPEM []byte
}

func newTestServer(t *testing.T, mode TLSMode) *testServer {
	t.Helper()

	cert, certPEM := selfSignedCert(t)

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testUser, testPass)
	for _, name := range []string{"INBOX", "Sent", "Archive"} {
		var opts *imap.CreateOptions
		if name == "Sent" {
			opts = &imap.CreateOptions{SpecialUse: []imap.MailboxAttr{imap.MailboxAttrSent}}
		}
		if err := user.Create(name, opts); err != nil {
			t.Fatalf("create mailbox %s: %v", name, err)
		}
	}
	if err := user.Subscribe("Sent"); err != nil {
		t.Fatalf("subscribe Sent: %v", err)
	}
	mem.AddUser(user)

	opts := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1:  {},
			imap.CapIdle:       {},
			imap.CapMove:       {},
			imap.CapUIDPlus:    {},
			imap.CapSpecialUse: {},
		},
		InsecureAuth: true,
		Logger:       log.New(io.Discard, "", 0),
	}
	if mode == TLSStartTLS {
		opts.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if mode == TLSImplicit {
		ln = tls.NewListener(ln, &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		})
	}

	srv := imapserver.New(opts)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return &testServer{
		mode:    mode,
		port:    ln.Addr().(*net.TCPAddr).Port,
		certPEM: certPEM,
	}
}

// config returns a base configuration with verification disabled so tests can
// opt into stricter verification where they test it.
func (ts *testServer) config() DialConfig {
	return DialConfig{
		Host:               "127.0.0.1",
		Port:               ts.port,
		Username:           testUser,
		Password:           testPass,
		TLS:                ts.mode,
		InsecureSkipVerify: true,
	}
}

func (ts *testServer) dial(t *testing.T, mutate func(*DialConfig)) *Client {
	t.Helper()
	cfg := ts.config()
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	})
	return c
}

// caFile writes the server certificate to a temporary PEM file.
func (ts *testServer) caFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, ts.certPEM, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	return path
}

// selfSignedCert returns a server certificate valid for 127.0.0.1, plus its
// PEM encoding for use as a trust root.
func selfSignedCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "btcx imapx test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load key pair: %v", err)
	}
	return cert, certPEM
}
