package ui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"strings"

	// Register the common image formats for the dimension probe below.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	gv2 "github.com/diamondburned/gotk4/pkg/glib/v2"

	"btcx/internal/contract"
	"btcx/internal/model"
	"btcx/internal/webview"
)

// thumbnailHeight is the display height of inline/remote images in the reader.
const thumbnailHeight = 64

// readerPane shows one message: header, remote-content bar, body (Pango
// label for plain text, hardened WebKit view for HTML), images and
// attachments. The web view is created once and reused across messages.
type readerPane struct {
	win    *MainWindow
	root   *gtk.Box // scroll (header + plain body) over the persistent web view
	scroll *gtk.ScrolledWindow
	web    *webview.View
	view   contract.MessageView
}

func newReaderPane(w *MainWindow) *readerPane {
	r := &readerPane{win: w}
	r.root = gtk.NewBox(gtk.OrientationVertical, 0)
	r.scroll = gtk.NewScrolledWindow()
	r.scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	r.scroll.SetVExpand(true)
	r.root.Append(r.scroll)
	r.clear()
	return r
}

// newBox is the vertical container of one reader page. Pages are replaced
// wholesale on show, so nothing has to be removed child by child.
func (r *readerPane) newBox() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(12)
	box.SetMarginBottom(12)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)
	return box
}

func (r *readerPane) widget() *gtk.Box { return r.root }

func (r *readerPane) clear() {
	r.view = contract.MessageView{}
	r.hideWeb()
	r.scroll.SetVExpand(true)
	box := r.newBox()
	hint := gtk.NewLabel("Select a message")
	hint.AddCSSClass("dim-label")
	box.Append(hint)
	r.scroll.SetChild(box)
}

// showWeb displays an HTML document in the persistent hardened web view.
func (r *readerPane) showWeb(html string) {
	if r.web == nil {
		r.web = webview.New(r.win.app.openURL)
		r.web.Attach(r.root.Object.Native())
	}
	r.web.Load(html)
	r.web.SetVisible(true)
}

func (r *readerPane) hideWeb() {
	if r.web != nil {
		r.web.SetVisible(false)
	}
}

// show rebuilds the pane for a message.
func (r *readerPane) show(view contract.MessageView) {
	r.view = view
	box := r.newBox()

	subject := gtk.NewLabel("")
	subject.SetUseMarkup(true)
	subject.SetMarkup("<b>" + gv2.MarkupEscapeText(view.Subject) + "</b>")
	subject.SetXAlign(0)
	subject.SetWrap(true)
	subject.SetSelectable(true)
	subject.AddCSSClass("title-3")
	box.Append(subject)

	box.Append(r.meta("From", view.From))
	if len(view.To) > 0 {
		box.Append(r.meta("To", view.To))
	}
	if len(view.Cc) > 0 {
		box.Append(r.meta("Cc", view.Cc))
	}
	date := gtk.NewLabel(view.Date.Format("Mon, 2 Jan 2006 15:04"))
	date.SetXAlign(0)
	date.AddCSSClass("dim-label")
	box.Append(date)

	if view.ShowRemoteContentBar {
		box.Append(r.remoteBar(view))
	}

	body := gtk.NewLabel("")
	body.SetUseMarkup(true) // already sanitized and rendered by the app
	body.SetMarkup(view.PangoBody)
	body.SetXAlign(0)
	body.SetYAlign(0)
	body.SetWrap(true)
	body.SetWrapMode(pango.WrapWordChar)
	body.SetSelectable(true)
	body.SetJustify(gtk.JustifyLeft)
	body.SetMaxWidthChars(100)
	if view.HTMLBody != "" {
		body.SetVisible(false) // the web view renders HTML mail below
	}
	box.Append(body)

	// Inline images render inside the web view for HTML mail; the strip is
	// the fallback for plain text.
	if images := r.images(view); images != nil && view.HTMLBody == "" {
		box.Append(images)
	}
	if bar := r.attachments(view); bar != nil {
		box.Append(bar)
	}
	r.scroll.SetChild(box)
	r.scroll.VAdjustment().SetValue(0)

	if view.HTMLBody != "" {
		r.showWeb(view.HTMLBody)
		r.scroll.SetVExpand(false)
	} else {
		r.hideWeb()
		r.scroll.SetVExpand(true)
	}
}

func (r *readerPane) meta(name string, list []model.Address) *gtk.Label {
	label := gtk.NewLabel(name + ": " + addresses(list))
	label.SetXAlign(0)
	label.SetWrap(true)
	label.AddCSSClass("dim-label")
	return label
}

func (r *readerPane) remoteBar(view contract.MessageView) gtk.Widgetter {
	bar := gtk.NewBox(gtk.OrientationHorizontal, 8)
	bar.AddCSSClass("toolbar")
	bar.SetMarginTop(4)
	bar.SetMarginBottom(4)
	bar.SetMarginStart(4)
	bar.SetMarginEnd(4)

	lock := gtk.NewImageFromIconName("channel-secure-symbolic")
	bar.Append(lock)

	text := gtk.NewLabel("Remote content blocked (privacy)")
	text.SetXAlign(0)
	text.SetHExpand(true)
	bar.Append(text)

	button := gtk.NewButtonWithLabel("Load remote content")
	button.AddCSSClass("suggested-action")
	button.ConnectClicked(func() {
		r.win.controller.LoadRemoteContent(view.Account, view.Ref)
	})
	bar.Append(button)
	return bar
}

// images builds the thumbnail strip for remote/inline images.
func (r *readerPane) images(view contract.MessageView) gtk.Widgetter {
	if len(view.RemoteImages) == 0 {
		return nil
	}
	flow := gtk.NewFlowBox()
	flow.SetSelectionMode(gtk.SelectionNone)
	flow.SetMaxChildrenPerLine(16)
	for _, img := range view.RemoteImages {
		flow.Insert(r.thumbnail(img), -1)
	}
	return flow
}

func (r *readerPane) thumbnail(img contract.InlineImage) gtk.Widgetter {
	if img.Failed {
		return r.brokenImage(img.URL)
	}
	pb := loadPixbuf(img.Data, maxDecodedDim)
	if pb == nil {
		return r.brokenImage(img.URL)
	}
	// gtk.Picture scales the pixbuf at draw time; gotk4 v0.4.1 does not wrap
	// gdk_pixbuf_scale_simple, so no resampled copy is kept in memory.
	pic := gtk.NewPictureForPaintable(gdk.NewTextureForPixbuf(pb)) //nolint:staticcheck // glycin is not a dependency; gdkpixbuf decodes under our size cap
	pic.SetCanShrink(true)
	pic.SetContentFit(gtk.ContentFitContain)
	pic.SetSizeRequest(thumbnailHeight*4, thumbnailHeight)
	pic.SetCursorFromName("pointer")

	button := gtk.NewButton()
	button.AddCSSClass("flat")
	button.SetTooltipText(img.URL)
	button.SetChild(pic)
	button.ConnectClicked(func() { r.showImage(img, pb) })
	return button
}

func (r *readerPane) brokenImage(url string) gtk.Widgetter {
	icon := gtk.NewImageFromIconName("image-missing-symbolic")
	icon.SetPixelSize(thumbnailHeight)
	icon.SetTooltipText(url)
	return icon
}

// showImage opens one image at full size in a separate window.
func (r *readerPane) showImage(img contract.InlineImage, pb *gdkpixbuf.Pixbuf) {
	win := gtk.NewWindow()
	win.SetTitle(img.URL)
	win.SetModal(true)
	win.SetTransientFor(&r.win.Window)
	win.SetDefaultSize(800, 600)

	pic := gtk.NewPictureForPaintable(gdk.NewTextureForPixbuf(pb)) //nolint:staticcheck // glycin is not a dependency; gdkpixbuf decodes under our size cap
	pic.SetCanShrink(true)
	pic.SetContentFit(gtk.ContentFitContain)
	win.SetChild(pic)
	win.Present()
}

// visibleAttachments lists the parts that get a save button: inline images are
// already visible in the image strip, everything else — including inline
// non-image parts with a Content-ID — must stay saveable.
func visibleAttachments(list []contract.AttachmentInfo) []contract.AttachmentInfo {
	var out []contract.AttachmentInfo
	for _, a := range list {
		if a.Inline && strings.HasPrefix(strings.ToLower(a.ContentType), "image/") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// attachments lists downloadable attachments as save buttons.
func (r *readerPane) attachments(view contract.MessageView) gtk.Widgetter {
	var box *gtk.Box
	for _, a := range visibleAttachments(view.Attachments) {
		if box == nil {
			box = gtk.NewBox(gtk.OrientationVertical, 2)
		}
		attach := a
		button := gtk.NewButtonWithLabel(attachmentLabel(attach))
		button.AddCSSClass("flat")
		button.SetHAlign(gtk.AlignStart)
		button.ConnectClicked(func() { r.saveAttachment(view, attach) })
		box.Append(button)
	}
	if box == nil {
		return nil
	}
	return box
}

// saveAttachment asks for a destination and hands it to the controller.
func (r *readerPane) saveAttachment(view contract.MessageView, a contract.AttachmentInfo) {
	dlg := gtk.NewFileDialog()
	dlg.SetTitle("Save attachment")
	dlg.SetAcceptLabel("Save")
	dlg.SetInitialName(a.Filename)
	dlg.Save(context.Background(), &r.win.Window, func(res gio.AsyncResulter) {
		file, err := dlg.SaveFinish(res)
		if err != nil || file == nil {
			return // the user dismissed the dialog
		}
		path := file.Path()
		if path == "" {
			r.win.showError(errors.New("cannot save attachment: choose a local file"))
			return
		}
		if err := r.win.controller.SaveAttachment(view.Account, view.Ref, a.Index, path); err != nil {
			r.win.app.showDialog("Cannot save attachment", err.Error())
		}
	})
}

// maxDecodedDim bounds the largest edge of any decoded mail image.
// maxDecodedPixels refuses images whose header claims more than this many
// pixels outright. Mail images are hostile input: a tiny file can claim
// enormous dimensions (pixel bomb), so absurd dimensions are refused before
// decoding and everything else is downscaled during decode.
const (
	maxDecodedDim    = 1024
	maxDecodedPixels = 40 << 20
)

// loadPixbuf decodes image bytes with the output bounded to a maxDim box,
// returning nil when the data is not an image or claims absurd dimensions.
func loadPixbuf(data []byte, maxDim int) *gdkpixbuf.Pixbuf {
	if len(data) == 0 || maxDim <= 0 {
		return nil
	}
	// Refuse pixel bombs up front for formats the standard library can
	// probe; unknown formats fall through to the scaling loader below.
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		if cfg.Width <= 0 || cfg.Height <= 0 ||
			cfg.Width > 20000 || cfg.Height > 20000 ||
			int64(cfg.Width)*int64(cfg.Height) > maxDecodedPixels {
			return nil
		}
	}
	loader := gdkpixbuf.NewPixbufLoader()
	loader.ConnectSizePrepared(func(w, h int) {
		// Downscale during decode so the full-resolution bitmap never
		// materialises in memory.
		if w > maxDim || h > maxDim {
			if w >= h {
				h = h * maxDim / w
				w = maxDim
			} else {
				w = w * maxDim / h
				h = maxDim
			}
		}
		if w > 0 && h > 0 {
			loader.SetSize(w, h)
		}
	})
	if err := loader.Write(data); err != nil {
		return nil
	}
	if err := loader.Close(); err != nil {
		return nil
	}
	pb := loader.Pixbuf()
	if pb == nil || pb.Width() <= 0 || pb.Height() <= 0 {
		return nil
	}
	return pb
}
