package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"btcx/internal/config"
	"btcx/internal/model"
)

func TestTrustImageDomainPersists(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	cfg := testConfig(t, testAccount("alice"))
	c, _, _ := newTestController(t, cfg, srv)

	if err := c.TrustImageDomain("Example.ORG"); err != nil {
		t.Fatalf("TrustImageDomain: %v", err)
	}
	if err := c.TrustImageDomain("example.org"); err != nil {
		t.Fatalf("dedup trust: %v", err)
	}
	if err := c.TrustImageDomain("news.example.org"); err != nil {
		t.Fatalf("second domain: %v", err)
	}

	got, err := config.LoadTrustedImages(cfg.Dir)
	if err != nil {
		t.Fatalf("LoadTrustedImages: %v", err)
	}
	if len(got) != 2 || got[0] != "example.org" || got[1] != "news.example.org" {
		t.Errorf("trusted domains = %v, want [example.org news.example.org]", got)
	}

	for _, bad := range []string{"", "bad domain", "x..y", "-x.example", "a@b", "e*xample.org"} {
		if err := c.TrustImageDomain(bad); err == nil {
			t.Errorf("TrustImageDomain(%q) accepted, want error", bad)
		}
	}
	// Invalid input must not corrupt the persisted list.
	got, err = config.LoadTrustedImages(cfg.Dir)
	if err != nil || len(got) != 2 {
		t.Errorf("after invalid input: %v, %v", got, err)
	}
}

func TestTrustedSenderAutoLoadsRemoteImages(t *testing.T) {
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(mustBase64(t, pngBase64))
	}))
	defer img.Close()

	html := "From: Alice <alice@example.org>\r\n" +
		"To: user@example.org\r\n" +
		"Subject: News\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		`<p>hi</p><img src="` + img.URL + `/p.png">`

	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)
	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	srv.setRaw(ref, html)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	allowLoopbackHTTP(c)
	startApp(t, c, ev, "alice")
	ev.wait(t, "rows", func() bool { return len(ev.messageRows("alice", "INBOX")) > 0 })

	// Untrusted: bar offered, nothing fetched.
	c.OpenMessage("alice", ref)
	ev.wait(t, "view", func() bool { _, ok := ev.openedView(ref); return ok })
	view, _ := ev.openedView(ref)
	if !view.ShowRemoteContentBar {
		t.Fatal("untrusted sender must show the remote-content bar")
	}

	// Trusting the sender's domain persists and auto-loads from now on.
	if err := c.TrustImageDomain("example.org"); err != nil {
		t.Fatalf("TrustImageDomain: %v", err)
	}
	c.OpenMessage("alice", ref)
	ev.wait(t, "trusted view", func() bool {
		v, ok := ev.openedView(ref)
		return ok && !v.ShowRemoteContentBar
	})
	view, _ = ev.openedView(ref)
	if view.ShowRemoteContentBar {
		t.Error("trusted sender still shows the bar")
	}
	fetched := false
	for _, im := range view.RemoteImages {
		if strings.HasPrefix(im.URL, img.URL) && !im.Failed && len(im.Data) > 0 {
			fetched = true
		}
	}
	if !fetched {
		t.Errorf("trusted sender's images were not fetched: %+v", view.RemoteImages)
	}
	_ = context.Background()
}
