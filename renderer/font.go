package renderer

import (
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
)

// Font holds a parsed MSDF font atlas and its glyph metrics.
type Font struct {
	Atlas       *Texture
	Glyphs      map[rune]Glyph
	LineHeight  float32
	Ascender    float32
	Descender   float32
	AtlasWidth  float32
	AtlasHeight float32
	PxPerEM     float32 // atlas.size from JSON
	PxRange     float32 // atlas.distanceRange from JSON
	BoldBias    float32 // positive = thicker strokes (shifts MSDF threshold)
}

// Glyph holds metrics for a single character in the MSDF atlas.
type Glyph struct {
	Advance float32
	// PlaneBounds: left, bottom, right, top in EM units.
	PlaneBounds [4]float32
	// AtlasBounds: left, bottom, right, top as normalized UVs.
	AtlasBounds [4]float32
}

// JSON structures matching msdf-atlas-gen output.
type fontJSON struct {
	Atlas   atlasJSON   `json:"atlas"`
	Metrics metricsJSON `json:"metrics"`
	Glyphs  []glyphJSON `json:"glyphs"`
}

type atlasJSON struct {
	DistanceRange float32 `json:"distanceRange"`
	Size          float32 `json:"size"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
}

type metricsJSON struct {
	LineHeight float32 `json:"lineHeight"`
	Ascender   float32 `json:"ascender"`
	Descender  float32 `json:"descender"`
}

type glyphJSON struct {
	Unicode     int         `json:"unicode"`
	Advance     float32     `json:"advance"`
	PlaneBounds *boundsJSON `json:"planeBounds"`
	AtlasBounds *boundsJSON `json:"atlasBounds"`
}

type boundsJSON struct {
	Left   float32 `json:"left"`
	Bottom float32 `json:"bottom"`
	Right  float32 `json:"right"`
	Top    float32 `json:"top"`
}

// MeasureText returns the width in screen pixels of a string at the given font size.
func (f *Font) MeasureText(text string, fontSize float32) float32 {
	var w float32
	for _, ch := range text {
		if g, ok := f.Glyphs[ch]; ok {
			w += g.Advance * fontSize
		} else {
			w += fontSize * 0.5
		}
	}
	return w
}

// WrapText breaks text into lines that fit within maxWidth screen pixels at the given fontSize.
func (f *Font) WrapText(text string, fontSize, maxWidth float32) []string {
	if f.MeasureText(text, fontSize) <= maxWidth {
		return []string{text}
	}

	var lines []string
	runes := []rune(text)
	start := 0
	for start < len(runes) {
		// Find the longest substring starting at 'start' that fits.
		lastSpace := -1
		var lineW float32
		end := start
		for end < len(runes) {
			ch := runes[end]
			var adv float32
			if g, ok := f.Glyphs[ch]; ok {
				adv = g.Advance * fontSize
			} else {
				adv = fontSize * 0.5
			}
			if lineW+adv > maxWidth && end > start {
				break
			}
			if ch == ' ' {
				lastSpace = end
			}
			lineW += adv
			end++
		}
		if end < len(runes) && lastSpace > start {
			// Break at last space.
			lines = append(lines, string(runes[start:lastSpace]))
			start = lastSpace + 1
		} else {
			lines = append(lines, string(runes[start:end]))
			start = end
		}
	}
	return lines
}

// LoadFont parses an msdf-atlas-gen JSON file and loads the PNG atlas as a linear texture.
func LoadFont(r *Renderer, jsonPath, pngPath string) (*Font, error) {
	// Parse JSON
	jsonData, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("read font json: %w", err)
	}

	var fj fontJSON
	if err := json.Unmarshal(jsonData, &fj); err != nil {
		return nil, fmt.Errorf("parse font json: %w", err)
	}

	// Load PNG
	pngFile, err := os.Open(pngPath)
	if err != nil {
		return nil, fmt.Errorf("open font atlas: %w", err)
	}
	defer pngFile.Close()

	img, err := png.Decode(pngFile)
	if err != nil {
		return nil, fmt.Errorf("decode font atlas: %w", err)
	}

	// Convert to RGBA
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)

	// Upload as linear texture
	atlas, err := r.CreateTextureLinear(rgba.Pix, w, h)
	if err != nil {
		return nil, fmt.Errorf("create font atlas texture: %w", err)
	}

	atlasW := float32(fj.Atlas.Width)
	atlasH := float32(fj.Atlas.Height)

	// Build glyph map
	glyphs := make(map[rune]Glyph, len(fj.Glyphs))
	for _, gj := range fj.Glyphs {
		g := Glyph{Advance: gj.Advance}
		if gj.PlaneBounds != nil {
			g.PlaneBounds = [4]float32{
				gj.PlaneBounds.Left,
				gj.PlaneBounds.Bottom,
				gj.PlaneBounds.Right,
				gj.PlaneBounds.Top,
			}
		}
		if gj.AtlasBounds != nil {
			// Normalize pixel coords to UVs
			g.AtlasBounds = [4]float32{
				gj.AtlasBounds.Left / atlasW,
				gj.AtlasBounds.Bottom / atlasH,
				gj.AtlasBounds.Right / atlasW,
				gj.AtlasBounds.Top / atlasH,
			}
		}
		glyphs[rune(gj.Unicode)] = g
	}

	return &Font{
		Atlas:       atlas,
		Glyphs:      glyphs,
		LineHeight:  fj.Metrics.LineHeight,
		Ascender:    fj.Metrics.Ascender,
		Descender:   fj.Metrics.Descender,
		AtlasWidth:  atlasW,
		AtlasHeight: atlasH,
		PxPerEM:     fj.Atlas.Size,
		PxRange:     fj.Atlas.DistanceRange,
	}, nil
}
