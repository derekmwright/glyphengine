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
	var vertices []Vertex
	var indices []uint16

	for _, line := range lines {
		fontSize := line.Scale // Scale = pixel height of 1 EM
		cellW := fontSize      // used for rough width estimate (right-align)

		// Estimate text width for right-alignment
		textW := float32(0)
		for _, ch := range line.Text {
			if g, ok := t.font.Glyphs[ch]; ok {
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
			g, ok := t.font.Glyphs[ch]
			if !ok {
				cursorX += cellW * 0.5
				continue
			}

			// Only emit a quad if the glyph has visible bounds (skip space, etc.)
			if g.PlaneBounds != [4]float32{} {
				// Screen-space quad corners from planeBounds * fontSize
				// planeBounds: left, bottom, right, top (in EM units)
				// In our screen space: Y=0 is top, Y increases downward
				x0 := cursorX + g.PlaneBounds[0]*fontSize
				y0 := cursorY + (t.font.Ascender-g.PlaneBounds[3])*fontSize
				x1 := cursorX + g.PlaneBounds[2]*fontSize
				y1 := cursorY + (t.font.Ascender-g.PlaneBounds[1])*fontSize

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
				spr := fontSize / t.font.PxPerEM * t.font.PxRange
				norm := [3]float32{alpha, spr, t.font.BoldBias}

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

	// Clamp to buffer limits
	if len(vertices) > msdfMaxVerts {
		vertices = vertices[:msdfMaxVerts]
		indices = indices[:msdfMaxIndices]
	}

	r.UpdateMeshData(t.mesh, vertices, indices)
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
