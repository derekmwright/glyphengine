package renderer

// PanelFill is what panel mode draws inside the frame.
//
// Colour is sRGB, the same as every other UI colour: what you write is what
// reaches the display.
//
// Opacity is absolute rather than a fraction of the border's, which is what
// makes an opaque panel reachable at all. The derived default caps the interior
// at 0.7 of the border opacity, so a modal dialog or a title card drawn through
// the panel path always showed the scene behind it.
type PanelFill struct {
	Color   [3]float32
	Opacity float32
}

// edgeSkirt is how far a panel's outer boundary is grown past itself, in
// pixels, to give ui.frag's coverage ramp an outside half. It matches
// ui.edgeSkirt, which cannot be shared because renderer does not import ui.
const edgeSkirt = 0.5

// NineSlice generates 9 textured quads from a texture with uniform insets.
type NineSlice struct {
	Texture *Texture
	TexSize int // texture width in pixels (e.g. 48)
	TexH    int // texture height; 0 means same as TexSize (square)
	Inset   int // pixels from each edge for corner regions

	// Fill overrides the panel's interior. Nil keeps the derived look: the
	// border tint at 0.2, with 0.7 of its opacity.
	//
	// The split itself is what makes nine-slice artwork cheap -- the texture's
	// alpha separates frame from fill, so the art only has to be an opaque
	// border around a transparent middle. What the derived version could not
	// express is any interior that is not a fixed fraction of the frame:
	// darkening the fill darkened the bezel with it, and no tint produces a
	// near-black interior under artwork that should stay light.
	Fill *PanelFill
}

// NewNineSlice creates a NineSlice definition.
func NewNineSlice(tex *Texture, texSize, inset int) *NineSlice {
	return &NineSlice{Texture: tex, TexSize: texSize, Inset: inset}
}

// GenerateQuads builds 9 textured quads for a panel at (x,y) with size (w,h).
// scale is screen pixels per texture pixel (e.g. 2.0 means 16px inset = 32 screen px).
// color tints the vertex color (white texture * color = colored panel).
//
// It allocates a fresh pair of slices on every call. A caller that rebuilds
// every frame should keep its own buffers and use AppendQuads.
func (ns *NineSlice) GenerateQuads(x, y, w, h, scale float32, color [3]float32) ([]Vertex, []uint16) {
	return ns.AppendQuads(nil, nil, x, y, w, h, scale, color)
}

// AppendQuads appends the nine quads GenerateQuads describes to caller-owned
// slices and returns them grown, so a panel rebuilt every frame can reuse one
// pair of buffers reset with [:0] and allocate nothing in the steady state.
//
// Indices are positions within the whole of verts rather than within the nine
// quads, so several panels can be appended into one buffer and drawn as one
// mesh. Appending into an empty pair therefore produces exactly what
// GenerateQuads returns.
func (ns *NineSlice) AppendQuads(verts []Vertex, idx []uint16, x, y, w, h, scale float32, color [3]float32) ([]Vertex, []uint16) {
	// Corner size in screen pixels
	cornerX := float32(ns.Inset) * scale
	texH := ns.TexH
	if texH == 0 {
		texH = ns.TexSize
	}
	cornerY := float32(ns.Inset) * scale

	// UV breakpoints (separate for non-square textures)
	uBreak := float32(ns.Inset) / float32(ns.TexSize)
	vBreak := float32(ns.Inset) / float32(texH)

	// Clamp corner to half the panel size so panels smaller than 2*corner still work
	if cornerX > w/2 {
		cornerX = w / 2
	}
	if cornerY > h/2 {
		cornerY = h / 2
	}

	// Screen-space X breakpoints: left edge, left+corner, right-corner, right
	sx := [4]float32{x, x + cornerX, x + w - cornerX, x + w}
	// Screen-space Y breakpoints
	sy := [4]float32{y, y + cornerY, y + h - cornerY, y + h}
	// UV breakpoints
	u := [4]float32{0, uBreak, 1 - uBreak, 1}
	v := [4]float32{0, vBreak, 1 - vBreak, 1}

	// Grow the panel's outer boundary half a pixel outward, so ui.frag's
	// coverage ramp has an outside half; see edgeCoverage there and edgeSkirt
	// in the ui package.
	//
	// Only the outer breakpoints move. The inner two are where the nine quads
	// abut each other, and shifting those would either overlap or gap the
	// seams, which is exactly the visible line the UV-distance formulation was
	// chosen to avoid.
	//
	// The UVs move with them, by the same fraction of each edge quad's own
	// extent, so UV 0 and 1 stay on the requested boundary rather than on the
	// grown one. That is what makes the shader's distance zero at the real edge.
	if cornerX > 0 {
		du := edgeSkirt * uBreak / cornerX
		sx[0] -= edgeSkirt
		sx[3] += edgeSkirt
		u[0] -= du
		u[3] += du
	}
	if cornerY > 0 {
		dv := edgeSkirt * vBreak / cornerY
		sy[0] -= edgeSkirt
		sy[3] += edgeSkirt
		v[0] -= dv
		v[3] += dv
	}

	for row := 0; row < 3; row++ {
		for col := 0; col < 3; col++ {
			x0, x1 := sx[col], sx[col+1]
			y0, y1 := sy[row], sy[row+1]
			u0, u1 := u[col], u[col+1]
			v0, v1 := v[row], v[row+1]

			// Skip degenerate quads
			if x1 <= x0 || y1 <= y0 {
				continue
			}

			base := uint16(len(verts))
			verts = append(verts,
				Vertex{Pos: [3]float32{x0, y0, 0}, Color: color, UV: [2]float32{u0, v0}},
				Vertex{Pos: [3]float32{x1, y0, 0}, Color: color, UV: [2]float32{u1, v0}},
				Vertex{Pos: [3]float32{x1, y1, 0}, Color: color, UV: [2]float32{u1, v1}},
				Vertex{Pos: [3]float32{x0, y1, 0}, Color: color, UV: [2]float32{u0, v1}},
			)
			idx = append(idx, base, base+1, base+2, base+2, base+3, base)
		}
	}

	return verts, idx
}
