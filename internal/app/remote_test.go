package app

import (
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"btcx/internal/model"
)

const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// allowLoopbackHTTP swaps the production (public-address-only) fetcher for a
// plain client so tests can use loopback httptest servers. The refusal
// behaviour itself is covered by TestNewHTTPClientRefusesLoopback.
func allowLoopbackHTTP(c *Controller) {
	c.http = &http.Client{Timeout: remoteTimeout}
}

func TestIsPublicIP(t *testing.T) {
	public := []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", "2606:4700::1111"}
	private := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "fe80::1",
		"0.0.0.0", "0.1.2.3", "::",
		"fc00::1", "fd12:3456::1",
		"224.0.0.1", "ff02::1",
	}
	for _, s := range public {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("isPublicIP(%s) = false, want true", s)
		}
	}
	for _, s := range private {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("isPublicIP(%s) = true, want false", s)
		}
	}
	if isPublicIP(nil) {
		t.Error("isPublicIP(nil) = true, want false")
	}
}

func TestNewHTTPClientRefusesLoopback(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(mustBase64(t, pngBase64))
	}))
	defer httpSrv.Close()

	// The production fetcher must refuse loopback even though the server
	// would happily answer.
	resp, err := newHTTPClient().Get(httpSrv.URL + "/x.png")
	if err == nil {
		resp.Body.Close()
		t.Fatal("production fetcher reached a loopback server; SSRF filter failed")
	}
	if !strings.Contains(err.Error(), "no public address") && !strings.Contains(err.Error(), "refused") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

func TestLoadRemoteContentFetchesBoundedImages(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/ok.png", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(mustBase64(t, pngBase64))
	})
	mux.HandleFunc("/missing.png", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		http.NotFound(w, r)
	})
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	var extra strings.Builder
	for i := 0; i < 10; i++ {
		extra.WriteString(`<img src="` + httpSrv.URL + `/ok.png?i=` + string(rune('a'+i)) + `">`)
	}
	raw := "From: Alice <alice@example.org>\r\nSubject: Pics\r\nMessage-ID: <pics@example.org>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		`<img src="` + httpSrv.URL + `/ok.png"><img src="` + httpSrv.URL + `/missing.png">` +
		extra.String() + `<img src="cid:logo@example">` + "\r\n" +
		"--b\r\nContent-Type: image/png\r\nContent-ID: <logo@example>\r\n" +
		"Content-Disposition: inline\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		pngBase64 + "\r\n--b--\r\n"

	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)
	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	srv.setRaw(ref, raw)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	allowLoopbackHTTP(c)
	startApp(t, c, ev, "alice")

	c.LoadRemoteContent("alice", ref)
	ev.wait(t, "remote view", func() bool {
		view, ok := ev.openedView(ref)
		return ok && len(view.RemoteImages) > 0
	})
	view, _ := ev.openedView(ref)
	if view.ShowRemoteContentBar {
		t.Error("opt-in bar still shown after loading remote content")
	}

	images := view.RemoteImages
	if len(images) != maxRemoteImages+1 { // ten remote plus the cid part
		t.Fatalf("images = %d, want %d", len(images), maxRemoteImages+1)
	}
	if images[0].URL != httpSrv.URL+"/ok.png" || images[0].Failed || len(images[0].Data) == 0 {
		t.Errorf("first image = %+v, want a fetched png", images[0])
	}
	if images[0].ContentType != "image/png" {
		t.Errorf("content type = %q", images[0].ContentType)
	}
	if !images[1].Failed || images[1].URL != httpSrv.URL+"/missing.png" {
		t.Errorf("404 image = %+v, want Failed", images[1])
	}

	last := images[len(images)-1]
	if last.URL != "cid:logo@example" || len(last.Data) == 0 || last.ContentType != "image/png" {
		t.Errorf("cid image = %+v, want bytes from the message", last)
	}

	mu.Lock()
	got := requests
	mu.Unlock()
	if got != maxRemoteImages {
		t.Errorf("requests = %d, want the cap %d", got, maxRemoteImages)
	}
}

func TestLoadRemoteContentIgnoresNonHTTPURLs(t *testing.T) {
	raw := "From: a@example.org\r\nSubject: x\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		`<img src="file:///etc/passwd"><img src="//example.org/x.png">` + "\r\n"
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)
	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	srv.setRaw(ref, raw)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	allowLoopbackHTTP(c)
	startApp(t, c, ev, "alice")

	c.LoadRemoteContent("alice", ref)
	ev.wait(t, "view", func() bool { _, ok := ev.openedView(ref); return ok })
	view, _ := ev.openedView(ref)
	for _, img := range view.RemoteImages {
		if !img.Failed {
			t.Errorf("non-http image %q was not refused: %+v", img.URL, img)
		}
	}
}

func mustBase64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return b
}
