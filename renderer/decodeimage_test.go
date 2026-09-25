package renderer

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"testing"
)

// The generic conversion every image used to take; the fast paths must
// produce exactly its bytes.
func genericStraightRGBA(img image.Image) []byte {
	b := img.Bounds()
	n := image.NewNRGBA(b)
	draw.Draw(n, b, img, b.Min, draw.Src)
	return n.Pix
}

func fillTest(set func(x, y int, c color.NRGBA), w, h int, alpha bool) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha {
				a = uint8((x * 7) % 256)
			}
			set(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), a})
		}
	}
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestStraightRGBABorrowsPackedBuffers is the fast path: an RGB PNG decodes to
// an opaque *image.RGBA and an RGBA PNG to an *image.NRGBA, and both are
// handed back without a copy. Forcing the generic path (deleting the switch)
// fails the "same backing array" checks below with "copied"; the bytes still
// match, which is why the alias check is the one that matters.
func TestStraightRGBABorrowsPackedBuffers(t *testing.T) {
	rgb := image.NewRGBA(image.Rect(0, 0, 37, 23))
	fillTest(func(x, y int, c color.NRGBA) { rgb.SetRGBA(x, y, color.RGBA{c.R, c.G, c.B, 255}) }, 37, 23, false)
	nrgba := image.NewNRGBA(image.Rect(0, 0, 37, 23))
	fillTest(func(x, y int, c color.NRGBA) { nrgba.SetNRGBA(x, y, c) }, 37, 23, true)

	for _, tc := range []struct {
		name string
		src  image.Image
	}{{"rgb png", rgb}, {"rgba png", nrgba}} {
		img, _, err := image.Decode(bytes.NewReader(encodePNG(t, tc.src)))
		if err != nil {
			t.Fatal(err)
		}
		var pix []byte
		switch m := img.(type) {
		case *image.RGBA:
			pix = m.Pix
		case *image.NRGBA:
			pix = m.Pix
		default:
			t.Fatalf("%s: png decoded to %T, the fast paths never run", tc.name, img)
		}
		got := straightRGBA(img)
		if &got[0] != &pix[0] {
			t.Errorf("%s: copied; the decoder's buffer was not borrowed", tc.name)
		}
		if want := genericStraightRGBA(img); !bytes.Equal(got, want) {
			t.Errorf("%s: bytes differ from the generic conversion", tc.name)
		}
	}
}

// TestStraightRGBAConvertsWhatItCannotBorrow: an RGBA image with real alpha
// is premultiplied, so its bytes are not straight alpha; a sub-image has a
// stride wider than its row; a paletted image is not 4 bytes per pixel. All
// three must go through the conversion and equal it.
func TestStraightRGBAConvertsWhatItCannotBorrow(t *testing.T) {
	translucent := image.NewRGBA(image.Rect(0, 0, 16, 16))
	fillTest(func(x, y int, c color.NRGBA) { translucent.Set(x, y, c) }, 16, 16, true)
	big := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	fillTest(func(x, y int, c color.NRGBA) { big.SetNRGBA(x, y, c) }, 32, 32, true)
	sub := big.SubImage(image.Rect(4, 4, 20, 20))
	pal := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.NRGBA{1, 2, 3, 255}, color.NRGBA{9, 8, 7, 255}})
	for i := range pal.Pix {
		pal.Pix[i] = uint8(i % 2)
	}
	for _, tc := range []struct {
		name string
		img  image.Image
		pix  []byte
	}{
		{"premultiplied alpha", translucent, translucent.Pix},
		{"sub-image", sub, sub.(*image.NRGBA).Pix},
		{"paletted", pal, nil},
	} {
		got := straightRGBA(tc.img)
		if tc.pix != nil && &got[0] == &tc.pix[0] {
			t.Errorf("%s: borrowed a buffer that is not straight packed RGBA", tc.name)
		}
		if want := genericStraightRGBA(tc.img); !bytes.Equal(got, want) {
			t.Errorf("%s: bytes differ from the generic conversion", tc.name)
		}
		if b := tc.img.Bounds(); len(got) != 4*b.Dx()*b.Dy() {
			t.Errorf("%s: %d bytes for %dx%d", tc.name, len(got), b.Dx(), b.Dy())
		}
	}
}

// BenchmarkDecodeImageRGB2048 is the case the profile in issue #135 was
// about: a 2048x2048 RGB PNG. Allocated bytes per op are the number to watch.
func BenchmarkDecodeImageRGB2048(b *testing.B) {
	rgb := image.NewRGBA(image.Rect(0, 0, 2048, 2048))
	fillTest(func(x, y int, c color.NRGBA) { rgb.SetRGBA(x, y, color.RGBA{c.R, c.G, c.B, 255}) }, 2048, 2048, false)
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgb); err != nil {
		b.Fatal(err)
	}
	data := buf.Bytes()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := decodeImage(data); err != nil {
			b.Fatal(err)
		}
	}
}
