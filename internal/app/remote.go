package app

import (
	"context"
	"fmt"
	"strings"

	"btcx/internal/contract"
	"btcx/internal/model"
	"btcx/internal/remotecontent"
	"btcx/internal/render"
)

// LoadRemoteContent opts in to remote content for one message: the images the
// sanitizer blocked are fetched (bounded, public addresses only) and the view
// is re-delivered with RemoteImages populated and the opt-in bar hidden.
// Inline cid: resources are resolved from the message itself.
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

// TrustImageDomain remembers a sender domain whose remote images always
// load, persisting the change immediately (remote_images.toml, 0600).
func (c *Controller) TrustImageDomain(domain string) error {
	if err := c.trust.Add(domain); err != nil {
		return fmt.Errorf("app: trust image domain: %w", err)
	}
	c.log.Debug("app: trusted image domain", "domain", domain)
	return nil
}

// trustedImagesAllowed reports whether remote images load without asking
// for this message: either the global policy is off, or a From sender
// domain is on the trusted list.
func (c *Controller) trustedImagesAllowed(msg *model.Message) bool {
	if !c.cfg.BlockRemoteContent {
		return true
	}
	var domains []string
	for _, from := range msg.From() {
		if at := strings.LastIndexByte(from.Addr, '@'); at >= 0 {
			domains = append(domains, from.Addr[at+1:])
		}
	}
	return c.trust.Allowed(domains)
}

// remoteImages fetches the blocked remote images and resolves inline cid:
// parts.
func (c *Controller) remoteImages(ctx context.Context, msg *model.Message) []contract.InlineImage {
	fetched := c.remote.Fetch(ctx, remotecontent.BlockedImages(msg.HTML))
	images := make([]contract.InlineImage, 0, len(fetched))
	for _, img := range fetched {
		images = append(images, contract.InlineImage{
			URL:         img.URL,
			ContentType: img.ContentType,
			Data:        img.Data,
			Failed:      img.Failed,
		})
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
