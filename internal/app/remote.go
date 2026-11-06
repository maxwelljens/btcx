package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

	"btcx/internal/config"
	"btcx/internal/contract"
	"btcx/internal/model"
	"btcx/internal/render"
	"btcx/internal/sanitize"
)

const (
	// maxRemoteImages bounds how many images one opt-in fetches.
	maxRemoteImages = 10
	// maxRemoteImageBytes caps one fetched image.
	maxRemoteImageBytes = 5 << 20
	// remoteTimeout bounds the whole batch.
	remoteTimeout = 10 * time.Second
)

// newHTTPClient returns the fetcher used for opted-in remote images: GET
// only, bounded time, redirects kept on http(s), and direct connections to
// public addresses only. The image URLs come from hostile mail, so the
// fetcher must never be usable to probe loopback, link-local or private
// networks (SSRF), and environment proxies are deliberately ignored for the
// same reason.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: remoteTimeout,
		Transport: &http.Transport{
			DialContext:           dialPublic,
			Proxy:                 nil,
			TLSHandshakeTimeout:   remoteTimeout,
			ResponseHeaderTimeout: remoteTimeout,
			MaxIdleConns:          4,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("app: too many redirects")
			}
			if s := req.URL.Scheme; s != "http" && s != "https" {
				return fmt.Errorf("app: redirect to %q refused", s)
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
		return nil, fmt.Errorf("app: remote content: bad address %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("app: remote content: resolve %s: %w", host, err)
	}
	dialer := &net.Dialer{Timeout: remoteTimeout}
	var lastErr error
	for _, ipa := range ips {
		if !isPublicIP(ipa.IP) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("app: remote content: %s resolves to no public address (refused)", host)
	}
	return nil, lastErr
}

// isPublicIP reports whether ip is safe to dial for remote content: it must
// not be loopback, private (RFC 1918 / unique-local), link-local, multicast,
// unspecified or inside a non-global special-purpose block.
func isPublicIP(ip net.IP) bool {
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
		panic("app: bad CIDR " + s)
	}
	return block
}

// LoadRemoteContent opts in to remote content for one message: the images the
// sanitizer blocked are fetched (bounded) and the view is re-delivered with
// RemoteImages populated and the opt-in bar hidden. Inline cid: resources are
// resolved from the message itself. Fetches only reach public addresses
// (see dialPublic); private and loopback targets are refused.
// TrustImageDomain remembers a sender domain whose remote images always
// load, persisting the change immediately (remote_images.toml, 0600).
func (c *Controller) TrustImageDomain(domain string) error {
	norm, err := config.NormalizeImageDomain(domain)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.trustedImages[norm] = true
	domains := make([]string, 0, len(c.trustedImages))
	for d := range c.trustedImages {
		domains = append(domains, d)
	}
	c.mu.Unlock()
	if err := config.SaveTrustedImages(c.cfg.Dir, domains); err != nil {
		return fmt.Errorf("app: trust image domain %s: %w", norm, err)
	}
	c.log.Debug("app: trusted image domain", "domain", norm)
	return nil
}

// trustedImagesAllowed reports whether remote images load without asking
// for this message: either the global policy is off, or a From sender
// domain is on the trusted list.
func (c *Controller) trustedImagesAllowed(msg *model.Message) bool {
	if !c.cfg.BlockRemoteContent {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, from := range msg.From() {
		at := strings.LastIndexByte(from.Addr, '@')
		if at < 0 {
			continue
		}
		if c.trustedImages[strings.ToLower(from.Addr[at+1:])] {
			return true
		}
	}
	return false
}

func (c *Controller) LoadRemoteContent(account string, ref model.Ref) {
	st := c.state(account)
	if st == nil {
		c.fail(fmt.Errorf("app: remote content: unknown account %q", account))
		return
	}
	c.goRun(func(ctx context.Context) {
		msg, err := c.message(ctx, st, ref)
		if err != nil {
			c.fail(fmt.Errorf("app: remote content %s: %w", ref, err))
			return
		}
		view := c.view(ctx, st, ref, msg)
		view.ShowRemoteContentBar = false
		view.RemoteImages = c.remoteImages(ctx, msg)
		if strings.TrimSpace(msg.HTML) != "" {
			view.HTMLBody = render.WebHTML(msg.HTML, imageURIs(msg, view.RemoteImages))
		}
		c.events.OnMessageOpened(view)
	})
}

// remoteImages fetches the blocked remote images and resolves inline cid:
// parts.
func (c *Controller) remoteImages(ctx context.Context, msg *model.Message) []contract.InlineImage {
	var images []contract.InlineImage

	urls := blockedImageURLs(msg.HTML)
	if len(urls) > maxRemoteImages {
		urls = urls[:maxRemoteImages]
	}
	if len(urls) > 0 {
		ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
		defer cancel()
		for _, u := range urls {
			images = append(images, c.fetchImage(ctx, u))
		}
	}
	return append(images, inlineImages(msg)...)
}

// inlineImages resolves the message's inline cid: resources (local bytes,
// no network involved) for display in the image strip.
func inlineImages(msg *model.Message) []contract.InlineImage {
	var images []contract.InlineImage
	for _, a := range msg.Attachments {
		if a.ContentID == "" || !strings.HasPrefix(a.ContentType, "image/") {
			continue
		}
		images = append(images, contract.InlineImage{
			URL:         "cid:" + a.ContentID,
			ContentType: a.ContentType,
			Data:        a.Data,
		})
	}
	return images
}

// fetchImage downloads one image, reporting failures in the result rather than
// failing the whole batch.
func (c *Controller) fetchImage(ctx context.Context, rawURL string) contract.InlineImage {
	img := contract.InlineImage{URL: rawURL}
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
	resp, err := c.http.Do(req)
	if err != nil {
		img.Failed = true
		return img
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		img.Failed = true
		return img
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteImageBytes+1))
	if err != nil || len(data) > maxRemoteImageBytes {
		img.Failed = true
		return img
	}
	img.Data = data
	img.ContentType = resp.Header.Get("Content-Type")
	return img
}

// blockedImageURLs extracts the remote image URLs the sanitizer recorded, in
// document order and without duplicates.

// hasAttrNode reports whether n carries key (case-insensitively).
func hasAttrNode(n *xhtml.Node, key string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return true
		}
	}
	return false
}

func blockedImageURLs(htmlBody string) []string {
	if strings.TrimSpace(htmlBody) == "" {
		return nil
	}
	doc, err := xhtml.Parse(strings.NewReader(sanitize.HTML(htmlBody)))
	if err != nil {
		return nil
	}
	var (
		out  []string
		seen = map[string]bool{}
	)
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		// Any element the sanitizer withheld an image on (today: img).
		if n.Type == xhtml.ElementNode && hasAttrNode(n, "data-blocked-src") {
			for _, a := range n.Attr {
				if a.Key == "data-blocked-src" && a.Val != "" && !seen[a.Val] {
					seen[a.Val] = true
					out = append(out, a.Val)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return out
}
