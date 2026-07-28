package msdf

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// ASCII is the default charset: printable ASCII, which covers most UI text and
// keeps the atlas small. Pass a wider set in Options.Charset when a game needs
// one, remembering that atlas area grows with the glyph count.
func ASCII() []rune {
	out := make([]rune, 0, 95)
	for r := rune(0x20); r <= 0x7E; r++ {
		out = append(out, r)
	}
	return out
}

// Options controls atlas generation. The zero value is valid and matches what
// msdf-atlas-gen is normally invoked with for this engine.
type Options struct {
	// Size is the em size in pixels each glyph is rendered at. Larger costs
	// atlas area; it does not improve sharpness, because a distance field is
	// resolution-independent by construction. 48 is plenty for UI text.
	Size int

	// Range is the total width of the distance field in pixels, matching
	// msdf-atlas-gen's -pxrange. It sets how far the gradient extends either
	// side of the outline, which bounds how much the shader can thicken, thin,
	// or outline text before the field runs out.
	//
	// It also sets the smallest size the atlas renders cleanly. The shader
	// needs roughly two screen pixels of gradient to resolve an edge, so with
	// an em size of E and a range of R the practical floor is
	//
	//	minimum legible size = 2 * E / R  pixels
	//
	// The default 48/4 is therefore good down to about 24px, which covers
	// ordinary UI text. Below that, edges start to break up -- speckles in the
	// counters and stray marks in the gaps between glyphs. Raising Range fixes
	// it at the cost of atlas area, since every cell grows by the padding.
	// Raising Size instead does not: the field is resolution-independent, and
	// a bigger em with the same range has the same ratio.
	//
	// Defaults to 4.
	Range float64

	// Charset defaults to ASCII.
	Charset []rune

	// Padding is the gap in pixels between glyph cells. Each cell already
	// contains its own distance range, so this only has to stop bilinear
	// filtering from sampling across the seam — 1 or 2 pixels. Larger values
	// waste atlas area without improving anything.
	Padding int

	// CornerAngle is the angle in degrees above which a direction change is
	// treated as a corner to be preserved. Lower values find more corners.
	CornerAngle float64
}

func (o *Options) applyDefaults() {
	if o.Size <= 0 {
		o.Size = 48
	}
	if o.Range <= 0 {
		o.Range = 4
	}
	if len(o.Charset) == 0 {
		o.Charset = ASCII()
	}
	if o.Padding <= 0 {
		o.Padding = 2
	}
	if o.CornerAngle <= 0 {
		o.CornerAngle = 30
	}
}

// Bounds is a rectangle in the JSON layout msdf-atlas-gen emits.
type Bounds struct {
	Left   float64 `json:"left"`
	Bottom float64 `json:"bottom"`
	Right  float64 `json:"right"`
	Top    float64 `json:"top"`
}

// GlyphJSON describes one glyph's placement and metrics.
type GlyphJSON struct {
	Unicode     int     `json:"unicode"`
	Advance     float64 `json:"advance"`
	PlaneBounds *Bounds `json:"planeBounds,omitempty"`
	AtlasBounds *Bounds `json:"atlasBounds,omitempty"`
}

// Metadata is the JSON side of an atlas, matching msdf-atlas-gen's schema so
// atlases from either generator are interchangeable.
type Metadata struct {
	Atlas struct {
		Type          string  `json:"type"`
		DistanceRange float64 `json:"distanceRange"`
		Size          float64 `json:"size"`
		Width         int     `json:"width"`
		Height        int     `json:"height"`
		YOrigin       string  `json:"yOrigin"`
	} `json:"atlas"`
	Metrics struct {
		EmSize     float64 `json:"emSize"`
		LineHeight float64 `json:"lineHeight"`
		Ascender   float64 `json:"ascender"`
		Descender  float64 `json:"descender"`
	} `json:"metrics"`
	Glyphs []GlyphJSON `json:"glyphs"`
}

// Atlas is a generated font atlas: the distance field image and the metrics
// describing where each glyph sits in it.
type Atlas struct {
	Image *image.RGBA
	Meta  *Metadata
}

// WritePNG writes the atlas image.
func (a *Atlas) WritePNG(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return errf("create %q: %w", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, a.Image); err != nil {
		return errf("encode %q: %w", path, err)
	}
	return nil
}

// WriteJSON writes the atlas metrics.
func (a *Atlas) WriteJSON(path string) error {
	b, err := json.MarshalIndent(a.Meta, "", "  ")
	if err != nil {
		return errf("marshal metrics: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return errf("write %q: %w", path, err)
	}
	return nil
}

// glyphWork is one glyph's flattened outline and where it lands in the atlas.
type glyphWork struct {
	r        rune
	contours []contour
	edges    []edge
	advance  float64

	// Outline bounds in pixels, expanded by the range so the field has room.
	x0, y0, x1, y1 float64
	cellW, cellH   int
	atlasX, atlasY int
	blank          bool
}

// Generate builds an MSDF atlas from TTF or OTF font data.
func Generate(fontData []byte, opt Options) (*Atlas, error) {
	opt.applyDefaults()

	f, err := sfnt.Parse(fontData)
	if err != nil {
		return nil, errf("parse font: %w", err)
	}

	var buf sfnt.Buffer
	ppem := fixed.I(opt.Size)

	metrics, err := f.Metrics(&buf, ppem, 0)
	if err != nil {
		return nil, errf("font metrics: %w", err)
	}

	cornerCos := cosDegrees(opt.CornerAngle)
	runes := sortedRunes(opt.Charset)

	works := make([]*glyphWork, 0, len(runes))
	for _, r := range runes {
		idx, err := f.GlyphIndex(&buf, r)
		if err != nil || idx == 0 {
			continue // not in the font; skip rather than emit a blank box
		}

		adv, err := f.GlyphAdvance(&buf, idx, ppem, 0)
		if err != nil {
			continue
		}

		segs, err := f.LoadGlyph(&buf, idx, ppem, nil)
		if err != nil {
			continue
		}

		w := &glyphWork{r: r, advance: float64(adv) / 64}
		w.contours = flatten(segs)

		minX, minY, maxX, maxY, ok := bounds(w.contours)
		if !ok {
			// Whitespace: real advance, nothing to draw.
			w.blank = true
			works = append(works, w)
			continue
		}

		// Range is the TOTAL width of the distance field, matching
		// msdf-atlas-gen's -pxrange, so the outline needs half of it on each
		// side. Expanding by the full range on both sides would double the
		// field and make text render softer than the same nominal range does
		// elsewhere.
		pad := opt.Range / 2

		// Snap the cell to whole pixels and derive planeBounds from the cell,
		// not the other way round. If the two disagree even by a pixel, every
		// glyph is sampled at a slightly wrong scale — a systematic stretch
		// that looks plausible on screen and is wrong everywhere.
		w.x0 = math.Floor(minX - pad)
		w.y0 = math.Floor(minY - pad)
		w.x1 = math.Ceil(maxX + pad)
		w.y1 = math.Ceil(maxY + pad)
		w.cellW = int(w.x1 - w.x0)
		w.cellH = int(w.y1 - w.y0)

		for _, c := range w.contours {
			w.edges = append(w.edges, colorEdges(c, cornerCos)...)
		}
		works = append(works, w)
	}

	width, height := packShelves(works, opt.Padding)

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// Fill with "fully outside" rather than transparent black: an unwritten
	// texel sampled at a glyph edge would otherwise read as a distance of zero
	// and put a dark fringe around text.
	outside := color.RGBA{0, 0, 0, 255}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = outside.R
		img.Pix[i+1] = outside.G
		img.Pix[i+2] = outside.B
		img.Pix[i+3] = 255
	}

	rasterizeAll(img, works, opt.Range)

	meta := &Metadata{}
	meta.Atlas.Type = "msdf"
	meta.Atlas.DistanceRange = opt.Range
	meta.Atlas.Size = float64(opt.Size)
	meta.Atlas.Width = width
	meta.Atlas.Height = height
	meta.Atlas.YOrigin = "bottom"
	meta.Metrics.EmSize = 1
	em := float64(opt.Size)
	meta.Metrics.LineHeight = float64(metrics.Height) / 64 / em
	meta.Metrics.Ascender = float64(metrics.Ascent) / 64 / em
	meta.Metrics.Descender = -float64(metrics.Descent) / 64 / em

	for _, w := range works {
		g := GlyphJSON{Unicode: int(w.r), Advance: w.advance / em}
		if !w.blank {
			// planeBounds is Y *up* from the baseline, the opposite of
			// sfnt's convention, so the vertical extents swap and negate.
			g.PlaneBounds = &Bounds{
				Left: w.x0 / em, Bottom: -w.y1 / em,
				Right: w.x1 / em, Top: -w.y0 / em,
			}
			// Atlas bounds are in pixels with Y measured from the bottom,
			// which is what yOrigin: bottom declares.
			g.AtlasBounds = &Bounds{
				Left:   float64(w.atlasX),
				Bottom: float64(height - (w.atlasY + w.cellH)),
				Right:  float64(w.atlasX + w.cellW),
				Top:    float64(height - w.atlasY),
			}
		}
		meta.Glyphs = append(meta.Glyphs, g)
	}

	return &Atlas{Image: img, Meta: meta}, nil
}

// packShelves lays glyph cells out in rows, tallest first, in a square-ish
// power-of-two atlas. Font glyphs are similar heights, so shelf packing wastes
// very little and is far simpler than a full bin packer.
func packShelves(works []*glyphWork, padding int) (int, int) {
	order := make([]*glyphWork, 0, len(works))
	for _, w := range works {
		if !w.blank {
			order = append(order, w)
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].cellH > order[j].cellH })

	// Binary search the smallest square that fits, in multiples of four
	// rather than powers of two. Rounding up to a power of two can waste more
	// than half the texture: 95 glyphs that fit in 332x332 would otherwise
	// occupy 512x512, which is 2.4x the memory for nothing.
	fits := func(size int) bool {
		x, y, shelfH := padding, padding, 0
		for _, w := range order {
			if x+w.cellW+padding > size {
				x = padding
				y += shelfH + padding
				shelfH = 0
			}
			if y+w.cellH+padding > size {
				return false
			}
			x += w.cellW + padding
			if w.cellH > shelfH {
				shelfH = w.cellH
			}
		}
		return true
	}

	lo, hi := 16, 16384
	if !fits(hi) {
		return hi, hi
	}
	for lo < hi {
		mid := ((lo+hi)/2 + 3) &^ 3
		if mid >= hi {
			break
		}
		if fits(mid) {
			hi = mid
		} else {
			lo = mid + 4
		}
	}

	// Re-run the winning layout to record positions.
	x, y, shelfH := padding, padding, 0
	for _, w := range order {
		if x+w.cellW+padding > hi {
			x = padding
			y += shelfH + padding
			shelfH = 0
		}
		w.atlasX, w.atlasY = x, y
		x += w.cellW + padding
		if w.cellH > shelfH {
			shelfH = w.cellH
		}
	}
	return hi, hi
}

// rasterizeAll fills each glyph's cell with its distance field, one goroutine
// per glyph. Glyphs write to disjoint regions, so no synchronisation is needed
// beyond the wait.
func rasterizeAll(img *image.RGBA, works []*glyphWork, rng float64) {
	jobs := make(chan *glyphWork)
	var wg sync.WaitGroup

	workers := runtime.GOMAXPROCS(0)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for w := range jobs {
				rasterize(img, w, rng)
			}
		}()
	}
	for _, w := range works {
		if !w.blank {
			jobs <- w
		}
	}
	close(jobs)
	wg.Wait()
}

func rasterize(img *image.RGBA, w *glyphWork, rng float64) {
	for py := 0; py < w.cellH; py++ {
		for px := 0; px < w.cellW; px++ {
			// sfnt reports outlines with Y increasing downward and the
			// baseline at zero, which is the same direction as image rows.
			// So row 0 is the glyph's top and no flip is needed here.
			p := vec2{
				w.x0 + float64(px) + 0.5,
				w.y0 + float64(py) + 0.5,
			}
			v := sample(p, w.edges, w.contours, rng)

			i := img.PixOffset(w.atlasX+px, w.atlasY+py)
			img.Pix[i+0] = clamp8(v[0])
			img.Pix[i+1] = clamp8(v[1])
			img.Pix[i+2] = clamp8(v[2])
			img.Pix[i+3] = 255
		}
	}
}

func clamp8(v float64) uint8 {
	n := int(v*255 + 0.5)
	if n < 0 {
		return 0
	}
	if n > 255 {
		return 255
	}
	return uint8(n)
}

func cosDegrees(deg float64) float64 { return math.Cos(deg * math.Pi / 180) }
