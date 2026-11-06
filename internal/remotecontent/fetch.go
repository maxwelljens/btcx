// Package remotecontent implements the policy and plumbing for loading
// remote images in mail: SSRF-safe fetching and the trusted-sender-domain
// allowlist. GTK-free and Controller-free.
package remotecontent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	// MaxImages bounds how many images one opt-in fetches.
	MaxImages = 10
	// maxImageBytes caps one fetched image.
	maxImageBytes = 5 << 20
	// timeout bounds one fetch batch.
	timeout = 10 * time.Second
)

// Image is the outcome of one remote image fetch. A failed URL is reported in
// place so the rest of the batch still loads.
type Image struct {
	URL         string // the http(s) URL requested
	ContentType string // e.g. "image/png"
	Data        []byte // encoded image bytes; empty when Failed
	Failed      bool
}

// Fetcher downloads remote images referenced by hostile mail. It is safe to
// share between goroutines.
type Fetcher struct {
	// Client performs the GETs; NewFetcher installs the SSRF-safe client.
	// Tests substitute a plain client to reach loopback servers.
	Client *http.Client
}

// NewFetcher returns a fetcher that only reaches public addresses.
func NewFetcher() *Fetcher {
	return &Fetcher{Client: newHTTPClient()}
}

// Fetch downloads urls in order, bounded by MaxImages and one batch timeout.
// Invalid or failed URLs are reported in their Image rather than aborting the
// batch.
func (f *Fetcher) Fetch(ctx context.Context, urls []string) []Image {
	if len(urls) > MaxImages {
		urls = urls[:MaxImages]
	}
	if len(urls) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	images := make([]Image, 0, len(urls))
	for _, u := range urls {
		images = append(images, f.fetch(ctx, u))
	}
	return images
}

// fetch downloads one image. Only http(s) URLs with a host are attempted.
func (f *Fetcher) fetch(ctx context.Context, rawURL string) Image {
	img := Image{URL: rawURL}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		img.Failed = true
		return img
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		img.Failed = true
		return img
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		img.Failed = true
		return img
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		img.Failed = true
		return img
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil || len(data) > maxImageBytes {
		img.Failed = true
		return img
	}
	img.Data = data
	img.ContentType = resp.Header.Get("Content-Type")
	return img
}

// newHTTPClient returns the fetcher used for opted-in remote images: GET
// only, bounded time, redirects kept on http(s), and direct connections to
// public addresses only. The image URLs come from hostile mail, so the
// fetcher must never be usable to probe loopback, link-local or private
// networks (SSRF), and environment proxies are deliberately ignored for the
// same reason.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialPublic,
			Proxy:                 nil,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          4,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("remotecontent: too many redirects")
			}
			if s := req.URL.Scheme; s != "http" && s != "https" {
				return fmt.Errorf("remotecontent: redirect to %q refused", s)
			}
			return nil
		},
	}
}

// dialPublic resolves addr and refuses every non-public address before
// dialing. The check runs on the exact IP being connected to, so DNS
// rebinding between resolution and connection cannot bypass it.
func dialPublic(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("remotecontent: bad address %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("remotecontent: resolve %s: %w", host, err)
	}
	dialer := &net.Dialer{Timeout: timeout}
	var lastErr error
	for _, ipa := range ips {
		if !IsPublicIP(ipa.IP) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("remotecontent: %s resolves to no public address (refused)", host)
	}
	return nil, lastErr
}

// IsPublicIP reports whether ip is safe to dial for remote content: it must
// not be loopback, private (RFC 1918 / unique-local), link-local, multicast,
// unspecified or inside a non-global special-purpose block.
func IsPublicIP(ip net.IP) bool {
	if ip == nil ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 { // 0.0.0.0/8 beyond 0.0.0.0 itself
			return false
		}
	} else if len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc {
		return false // fc00::/7 unique-local, belt and braces
	}
	for _, block := range nonPublicBlocks {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

// nonPublicBlocks are the special-purpose ranges net.IP does not classify:
// they are routable to infrastructure, not to a mail sender's image.
var nonPublicBlocks = []*net.IPNet{
	mustParseCIDR("100.64.0.0/10"), // shared address space (CGNAT)
	mustParseCIDR("198.18.0.0/15"), // benchmarking
	mustParseCIDR("192.0.0.0/24"),  // IETF protocol assignments
	mustParseCIDR("240.0.0.0/4"),   // reserved, includes broadcast
	mustParseCIDR("64:ff9b::/96"),  // NAT64
}

// mustParseCIDR parses a package-level block; a typo is a programmer error.
func mustParseCIDR(s string) *net.IPNet {
	_, block, err := net.ParseCIDR(s)
	if err != nil {
		panic("remotecontent: bad CIDR " + s)
	}
	return block
}
