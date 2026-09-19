package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"
)

// readPNGHeights decodes a greyscale heightmap PNG into row-major uint16
// samples (image row 0 first) plus its dimensions.
//
// 16-bit greyscale is the format this tool wants: Go's png decoder returns
// *image.Gray16 for it, and each pixel is used directly as a 0-65535 height
// sample. An 8-bit greyscale PNG (*image.Gray) is also accepted, replicated
// into the same 0-65535 range (v*257, since 255*257 = 65535 exactly) so the
// rest of the pipeline never has to know which it got -- but only 256
// distinct heights exist in the source data, so the result terraces
// visibly on anything but a very flat, very low-relief terrain. warn is
// non-empty exactly when that happened, for the caller to print.
//
// Anything else (RGBA, paletted, a 16-bit image that isn't greyscale) is
// rejected rather than guessed at with a luminance formula: a heightmap
// PNG's channels are not colour, and silently averaging RGB into a height
// would produce a plausible-looking but fabricated surface.
func readPNGHeights(path string) (samples []uint16, w, h int, warn string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, "", err
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		return nil, 0, 0, "", fmt.Errorf("decode %s: %w", path, err)
	}

	switch g := img.(type) {
	case *image.Gray16:
		w, h = g.Rect.Dx(), g.Rect.Dy()
		samples = make([]uint16, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				o := g.PixOffset(g.Rect.Min.X+x, g.Rect.Min.Y+y)
				samples[y*w+x] = uint16(g.Pix[o])<<8 | uint16(g.Pix[o+1])
			}
		}
		return samples, w, h, "", nil
	case *image.Gray:
		w, h = g.Rect.Dx(), g.Rect.Dy()
		samples = make([]uint16, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				o := g.PixOffset(g.Rect.Min.X+x, g.Rect.Min.Y+y)
				samples[y*w+x] = uint16(g.Pix[o]) * 257
			}
		}
		warn = fmt.Sprintf("%s is 8-bit greyscale (256 distinct heights) -- the terrain will terrace; re-export as 16-bit for smooth height data", path)
		return samples, w, h, warn, nil
	default:
		return nil, 0, 0, "", fmt.Errorf("%s: unsupported PNG format %T -- want 16-bit or 8-bit greyscale", path, img)
	}
}

// readRawHeights reads a headerless WxH grid of uint16 samples, row-major
// with row 0 first -- the World Machine / Gaea / Unity .r16 convention.
// Little-endian by default; bigEndian selects the other byte order.
func readRawHeights(path string, w, h int, bigEndian bool) ([]uint16, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	want := w * h * 2
	if len(data) != want {
		return nil, fmt.Errorf("%s is %d bytes, want %d for a %dx%d uint16 raw grid", path, len(data), want, w, h)
	}
	samples := make([]uint16, w*h)
	order := binary.ByteOrder(binary.LittleEndian)
	if bigEndian {
		order = binary.BigEndian
	}
	for i := range samples {
		samples[i] = order.Uint16(data[i*2:])
	}
	return samples, nil
}

// heightsFromSamples maps row-major uint16 samples (0-65535, row 0 first as
// readPNGHeights/readRawHeights produce) onto a Heightmap's row-major
// [z*gridW+x] float32 layout, applying this tool's image orientation
// convention -- see docs/agents/terrain-heightmap.md's "Orientation" section
// for the diagram this implements:
//
//   - image column 0 (leftmost) -> grid x=0 -> world OriginX (no flip: left
//     in the image is -X/west in the world, the ordinary left-to-right
//     reading of a map).
//   - image row 0 (top, first row in the file) -> grid z=GridH-1 -> world
//     OriginZ+WorldD, the FAR edge. Image viewers show row 0 at the top,
//     and a top-down map's "top" is conventionally the far/away direction
//     (north) from a viewer standing at the near edge -- so row 0 maps to
//     the far edge, not the near one. This is a flip against the grid's
//     own z index, which is why it is called out by name rather than left
//     implicit in a loop bound.
//
// rangeMin/rangeMax is the world height sample 0 and sample 65535 map to.
func heightsFromSamples(samples []uint16, w, h int, rangeMin, rangeMax float32) []float32 {
	heights := make([]float32, w*h)
	span := rangeMax - rangeMin
	for row := 0; row < h; row++ {
		iz := h - 1 - row // row 0 (top of image) -> far edge (max z)
		for col := 0; col < w; col++ {
			v := float32(samples[row*w+col]) / 65535
			heights[iz*w+col] = rangeMin + v*span
		}
	}
	return heights
}
