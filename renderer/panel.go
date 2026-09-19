package renderer

import (
	"github.com/go-gl/mathgl/mgl32"
)

const (
	panelMaxVerts   = 36 // 9 quads * 4 verts
	panelMaxIndices = 54 // 9 quads * 6 indices
)

// PanelLayer is a single 9-slice layer (fill or border) within a Panel.
type PanelLayer struct {
	NineSlice   *NineSlice
	Color       [3]float32
	Opacity     float32
	TextureMode bool // true = straight texture blending (pre-colored textures with alpha)

	// Glow is this layer's emission, carried through to UIRenderObject.Glow.
	// A plain field rather than an AddLayer parameter because every existing
	// call site means zero by it and should not have to say so.
	Glow float32

	mesh *Mesh
}

// Panel is a multi-layer 9-slice UI element.
type Panel struct {
	X, Y, Width, Height float32
	Scale               float32 // texture-pixel to screen-pixel ratio
	Layers              []*PanelLayer
	Visible             bool
}

// NewPanel creates an empty panel with default scale and visibility.
func NewPanel() *Panel {
	return &Panel{Scale: 2.0, Visible: true}
}

// AddLayer adds a 9-slice layer to the panel, allocating its GPU mesh.
func (p *Panel) AddLayer(r *Renderer, ns *NineSlice, color [3]float32, opacity float32) error {
	m, err := r.CreateDynamicIndexedMesh(panelMaxVerts, panelMaxIndices)
	if err != nil {
		return err
	}
	p.Layers = append(p.Layers, &PanelLayer{
		NineSlice: ns,
		Color:     color,
		Opacity:   opacity,
		mesh:      m,
	})
	return nil
}

// Rebuild regenerates the mesh data for all layers based on current panel geometry.
func (p *Panel) Rebuild(r *Renderer) {
	for _, layer := range p.Layers {
		verts, inds := layer.NineSlice.GenerateQuads(
			p.X, p.Y, p.Width, p.Height, p.Scale, layer.Color,
		)
		r.UpdateMeshData(layer.mesh, verts, inds)
	}
}

// UIRenderObject is a RenderObject with additional opacity for the UI pipeline.
type UIRenderObject struct {
	RenderObject
	Opacity     float32
	TextureMode bool // true = straight texture*color blending (icons); false = 9-slice panel mode

	// Glow is how much LINEAR light this element emits on top of its own
	// colour: 0 is no glow, 1 doubles it, 3 quadruples it. It is a multiple of
	// Color rather than a colour of its own, so a red warning glows red.
	//
	// It is separate from Color because Color is an sRGB display value and sRGB
	// has no meaning above 1 -- srgbToLinear's curve is only defined on [0,1],
	// so "write 1.4 and it will be bright" is not an answer, it is undefined
	// behaviour that happens to produce a number. Emission is linear by nature,
	// and this is the multiplier applied after the decode.
	//
	// It does nothing without the UI glow layer (renderer.WithUIGlowLayer): the
	// swapchain is 8-bit, so on the direct path there is nothing above 1 to
	// hold. That is why zero is the default and why an existing game is
	// untouched -- and why a game that sets this and sees no glow should check
	// UIGlowLayer first.
	Glow float32

	// Fill overrides the panel interior; nil derives it from the tint. Panels
	// built through NineSlice carry theirs from there, and a game assembling
	// UIRenderObjects itself sets this directly.
	Fill *PanelFill
}

// UIRenderObjects returns UIRenderObjects with per-layer opacity for the UI pipeline.
func (p *Panel) UIRenderObjects(screenW, screenH float32) []UIRenderObject {
	if !p.Visible {
		return nil
	}
	proj := mgl32.Ortho(0, screenW, 0, screenH, -1, 1)
	var objs []UIRenderObject
	for _, layer := range p.Layers {
		if layer.mesh.IndexCount == 0 && layer.mesh.VertexCount == 0 {
			continue
		}
		objs = append(objs, UIRenderObject{
			RenderObject: RenderObject{
				Mesh:    layer.mesh,
				Texture: layer.NineSlice.Texture,
				MVP:     proj,
				Color:   layer.Color,
			},
			Opacity:     layer.Opacity,
			TextureMode: layer.TextureMode,
			Glow:        layer.Glow,
			Fill:        layer.NineSlice.Fill,
		})
	}
	return objs
}

// Destroy frees GPU resources for all panel layers.
func (p *Panel) Destroy(r *Renderer) {
	for _, layer := range p.Layers {
		r.DestroyMesh(layer.mesh)
	}
}
