package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// encodePNG builds a deterministic PNG of the given dimensions.
func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLoadPixbufBoundsDecoding(t *testing.T) {
	// A wide-but-decent image must decode, bounded by the max-dim box.
	data := encodePNG(t, 2000, 200)
	pb := loadPixbuf(data, 512)
	if pb == nil {
		t.Fatal("loadPixbuf returned nil for a valid image")
	}
	if pb.Width() > 512 || pb.Height() > 512 {
		t.Errorf("decoded size %dx%d exceeds the 512px bound", pb.Width(), pb.Height())
	}
}

func TestLoadPixbufRefusesPixelBomb(t *testing.T) {
	// Header claims 30000x30000 (900M pixels); the compressed data is tiny.
	data := encodePNG(t, 30000, 30000)
	if len(data) > 2<<20 {
		t.Skipf("encoder produced %d bytes; bomb fixture not compact", len(data))
	}
	if pb := loadPixbuf(data, 512); pb != nil {
		t.Error("loadPixbuf decoded a pixel bomb; want refusal")
	}
}

func TestLoadPixbufRejectsJunk(t *testing.T) {
	if pb := loadPixbuf([]byte("not an image"), 512); pb != nil {
		t.Error("loadPixbuf decoded junk")
	}
	if pb := loadPixbuf(nil, 512); pb != nil {
		t.Error("loadPixbuf decoded empty input")
	}
}
