package ui

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"

	"btcx/internal/contract"
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

// bombPNG hand-builds a PNG whose IHDR claims w x h without encoding any
// pixels: header, IHDR, IEND. A real 30000x30000 encode would need 3.6 GB.
func bombPNG(t *testing.T, w, h uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(data)))
		buf.Write(n[:])
		raw := append([]byte(typ), data...)
		buf.Write(raw)
		var sum [4]byte
		binary.BigEndian.PutUint32(sum[:], crc32.ChecksumIEEE(raw))
		buf.Write(sum[:])
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // colour type: RGBA
	chunk("IHDR", ihdr)
	chunk("IEND", nil)
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
	// The header claims 30000x30000 = 900M pixels; the fixture is ~50 bytes.
	data := bombPNG(t, 30000, 30000)
	if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
		t.Fatalf("bomb fixture is not a readable PNG header: %v", err)
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

// TestVisibleAttachments pins F10: inline images live in the image strip, but
// every other part must keep a save button — otherwise an inline PDF or any
// other part with a Content-ID is displayed nowhere.
func TestVisibleAttachments(t *testing.T) {
	got := visibleAttachments([]contract.AttachmentInfo{
		{Index: 0, Filename: "logo.png", ContentType: "image/png", Inline: true},
		{Index: 1, Filename: "report.pdf", ContentType: "application/pdf", Inline: true},
		{Index: 2, Filename: "notes.txt", ContentType: "text/plain"},
		{Index: 3, Filename: "photo.JPG", ContentType: "IMAGE/JPEG", Inline: true},
	})
	if len(got) != 2 || got[0].Index != 1 || got[1].Index != 2 {
		t.Fatalf("visible = %+v, want the inline PDF and the normal attachment", got)
	}
}
