package remotecontent

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsPublicIP(t *testing.T) {
	public := []string{
		"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", "2606:4700::1111",
		// just outside the refused special-purpose blocks
		"100.63.255.255", "100.128.0.0", "198.17.255.255", "198.20.0.0",
		"192.0.1.1", "223.255.255.255", "64:ff9b:0:1::1",
	}
	private := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "fe80::1",
		"0.0.0.0", "0.1.2.3", "::",
		"fc00::1", "fd12:3456::1",
		"224.0.0.1", "ff02::1",
		// special-purpose blocks hostile mail must not reach
		"100.64.0.1", "100.127.255.255", // CGNAT
		"198.18.0.1", "198.19.255.255", // benchmarking
		"192.0.0.8", "192.0.0.255", // IETF protocol assignments
		"240.0.0.1", "255.255.255.255", // reserved
		"64:ff9b::a00:1", "64:ff9b::1.2.3.4", // NAT64
	}
	for _, s := range public {
		if !IsPublicIP(net.ParseIP(s)) {
			t.Errorf("IsPublicIP(%s) = false, want true", s)
		}
	}
	for _, s := range private {
		if IsPublicIP(net.ParseIP(s)) {
			t.Errorf("IsPublicIP(%s) = true, want false", s)
		}
	}
	if IsPublicIP(nil) {
		t.Error("IsPublicIP(nil) = true, want false")
	}
}

func TestNewHTTPClientRefusesLoopback(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
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

func TestBlockedImages(t *testing.T) {
	// The sanitizer rewrites remote <img src> to data-blocked-src; duplicates
	// collapse and cid: references are not remote content.
	body := `<html><body><p>hi</p>` +
		`<img src="https://b.example/2.png">` +
		`<img src="https://a.example/1.png">` +
		`<img src="https://b.example/2.png">` +
		`<img src="cid:logo">` +
		`</body></html>`
	got := BlockedImages(body)
	want := []string{"https://b.example/2.png", "https://a.example/1.png"}
	if len(got) != len(want) {
		t.Fatalf("BlockedImages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("BlockedImages[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := BlockedImages("  "); got != nil {
		t.Errorf("empty body = %v, want nil", got)
	}
}
