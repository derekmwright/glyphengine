package renderer

import (
	"github.com/go-gl/mathgl/mgl32"
)

const (
	msdfMaxChars   = 2048
	msdfMaxVerts   = msdfMaxChars * 4
	msdfMaxIndices = msdfMaxChars * 6
)

// MSDFText renders text using an MSDF font atlas as textured quads.
type MSDFText struct {
	mesh *Mesh
	font *Font

	// verts and idx are the geometry scratch, kept between calls and reset to
	// length zero rather than rebuilt from nil. A text overlay is redrawn
	// every frame by definition, and growing two slices from nothing each
	// time was 73% of everything a consumer game allocated -- about 390 KB a
	// frame, a garbage collection every fifteen frames in a game that
	// otherwise allocated almost nothing. UpdateMeshData copies out of them
	// into its own staging buffer, so keeping them aliases nothing.
	verts []Vertex
	idx   []uint16
}

// NewMSDFText allocates a dynamic indexed mesh for MSDF text rendering.
func NewMSDFText(r *Renderer, font *Font) (*MSDFText, error) {
	m, err := r.CreateDynamicIndexedMesh(msdfMaxVerts, msdfMaxIndices)
	if err != nil {
		return nil, err
	}
	return &MSDFText{mesh: m, font: font}, nil
}

// SetText rebuilds the mesh with textured quads for each character in the given lines.
func (t *MSDFText) SetText(r *Renderer, lines []TextLine, screenW, screenH float32) {
	t.rebuild(lines, screenW)
	r.UpdateMeshData(t.mesh, t.verts, t.idx)
}

// rebuild regenerates the geometry into the retained buffers. It is the part of
// SetText that does not need a device, split out so a test can hold the
// retention itself to zero allocations -- testing only the builder below would
// pass just as happily if SetText handed it nil every frame.
func (t *MSDFText) rebuild(lines []TextLine, screenW float32) {
	t.verts, t.idx = appendMSDFGeometry(t.verts[:0], t.idx[:0], t.font, lines, screenW)
}

// appendMSDFGeometry appends one quad per visible glyph to vertices and
// indices and returns them. It is a function of its arguments and nothing
// else so that it can be tested, and held to zero allocations, without a
// device: SetText itself needs a Renderer.
//
// It stops at the mesh's capacity instead of building everything and
// truncating afterwards. The output is the same first msdfMaxChars glyphs
// either way, but a retained buffer that grew to fit one absurdly long string
// would stay that size for the life of the overlay, and the uint16 index base
// would have wrapped long before the truncation threw the evidence away.
func appendMSDFGeometry(vertices []Vertex, indices []uint16, font *Font, lines []TextLine, screenW float32) ([]Vertex, []uint16) {
	for _, line := range lines {
		fontSize := line.Scale // Scale = pixel height of 1 EM
		cellW := fontSize      // used for rough width estimate (right-align)

		// Estimate text width for right-alignment
		textW := float32(0)
		for _, ch := range line.Text {
			if g, ok := font.Glyphs[ch]; ok {
				textW += g.Advance * fontSize
			} else {
				textW += cellW * 0.5
			}
		}

		cursorX := line.X
		if cursorX < 0 {
			// Right-aligned: |X| is right margin
			cursorX = screenW - textW + cursorX
		}
		cursorY := line.Y

		for _, ch := range line.Text {
			g, ok := font.Glyphs[ch]
			if !ok {
				cursorX += cellW * 0.5
				continue
			}

			// Only emit a quad if the glyph has visible bounds (skip space, etc.)
			if g.PlaneBounds != [4]float32{} {
				if len(vertices)+4 > msdfMaxVerts {
					return vertices, indices
				}

				// Screen-space quad corners from planeBounds * fontSize
				// planeBounds: left, bottom, right, top (in EM units)
				// In our screen space: Y=0 is top, Y increases downward
				x0 := cursorX + g.PlaneBounds[0]*fontSize
				y0 := cursorY + (font.Ascender-g.PlaneBounds[3])*fontSize
				x1 := cursorX + g.PlaneBounds[2]*fontSize
				y1 := cursorY + (font.Ascender-g.PlaneBounds[1])*fontSize

				// Atlas UVs: left, bottom, right, top (normalized)
				// MSDF atlas has Y=0 at top in PNG, but atlasBounds.bottom < atlasBounds.top
				// in pixel space from the bottom. We need to flip V.
				uLeft := g.AtlasBounds[0]
				vBottom := 1.0 - g.AtlasBounds[1]
				uRight := g.AtlasBounds[2]
				vTop := 1.0 - g.AtlasBounds[3]

				// Alpha: 0 means fully opaque (default zero-value), otherwise use as-is.
				alpha := line.Alpha
				if alpha == 0 {
					alpha = 1.0
				}
				// Per-glyph screenPxRange so mixed font sizes render at correct weight.
				spr := fontSize / font.PxPerEM * font.PxRange
				norm := [3]float32{alpha, spr, font.BoldBias}

				base := uint16(len(vertices))
				vertices = append(vertices,
					Vertex{Pos: [3]float32{x0, y0, 0}, Color: line.Color, Normal: norm, UV: [2]float32{uLeft, vTop}},
					Vertex{Pos: [3]float32{x1, y0, 0}, Color: line.Color, Normal: norm, UV: [2]float32{uRight, vTop}},
					Vertex{Pos: [3]float32{x1, y1, 0}, Color: line.Color, Normal: norm, UV: [2]float32{uRight, vBottom}},
					Vertex{Pos: [3]float32{x0, y1, 0}, Color: line.Color, Normal: norm, UV: [2]float32{uLeft, vBottom}},
				)
				indices = append(indices, base, base+1, base+2, base+2, base+3, base)
			}

			cursorX += g.Advance * fontSize
		}
	}

	return vertices, indices
}

// RenderObject returns a RenderObject for drawing this MSDF text overlay.
func (t *MSDFText) RenderObject(screenW, screenH, fontSize float32) RenderObject {
	proj := mgl32.Ortho(0, screenW, 0, screenH, -1, 1)
	screenPxRange := fontSize / t.font.PxPerEM * t.font.PxRange
	return RenderObject{
		Mesh:    t.mesh,
		Texture: t.font.Atlas,
		MVP:     proj,
		Color:   [3]float32{screenPxRange, screenPxRange, screenPxRange}, // packed into tint
	}
}

// ScreenPxRange computes the screenPxRange for a given font size.
func (t *MSDFText) ScreenPxRange(fontSize float32) float32 {
	return fontSize / t.font.PxPerEM * t.font.PxRange
}

// Destroy frees the overlay's GPU resources.
func (t *MSDFText) Destroy(r *Renderer) {
	r.DestroyMesh(t.mesh)
}
