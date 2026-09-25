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

	// built is what this layer's mesh currently holds, and hasBuilt says
	// whether it holds anything yet. Rebuild compares the two against the
	// values it is about to draw from; see quadInputs.
	built    quadInputs
	hasBuilt bool
}

// quadInputs is every value that reaches a layer's vertices, and it is the
// whole of Rebuild's skip rule: a value AppendQuads reads and this does not
// record is a panel that goes on drawing its old shape after the game moved,
// resized or recoloured it, with nothing in a log and nothing in a validation
// report to say so.
//
// The nine-slice is held by pointer as well as by the three numbers read out
// of it, so swapping a layer's artwork for a different slice with the same
// metrics still rebuilds. Texture and Fill are deliberately absent: neither
// reaches a vertex, and UIRenderObjects reads both live every frame.
type quadInputs struct {
	slice                *NineSlice
	x, y, w, h           float32
	scale                float32
	color                [3]float32
	texSize, texH, inset int
}

// Panel is a multi-layer 9-slice UI element.
type Panel struct {
	X, Y, Width, Height float32
	Scale               float32 // texture-pixel to screen-pixel ratio
	Layers              []*PanelLayer
	Visible             bool

	// Scratch geometry and render objects, reused across rebuilds. A HUD
	// rebuilds its panels every frame and the quads are the same 36 vertices
	// and 54 indices every time, so growing a fresh pair of slices per layer
	// per frame was the engine's largest steady-state allocator.
	scratchV []Vertex
	scratchI []uint16
	objs     []UIRenderObject
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

// Rebuild regenerates the mesh data for any layer whose geometry inputs have
// changed since its last build, and leaves the rest alone.
//
// Skipping is safe because an unupdated dynamic mesh keeps pointing at the
// buffer it was last flushed into, and nothing writes that buffer again until
// the next UpdateMeshData -- so the panel keeps drawing the same vertices
// without the CPU touching them. What it does mean is that several frames in
// flight end up reading that one buffer, which is why flushDynamicMeshes
// chooses the slot an update is copied into rather than writing the frame's
// own; read that comment before changing either side.
func (p *Panel) Rebuild(r *Renderer) {
	for _, layer := range p.Layers {
		ns := layer.NineSlice
		in := quadInputs{
			slice: ns,
			x:     p.X, y: p.Y, w: p.Width, h: p.Height,
			scale: p.Scale, color: layer.Color,
			texSize: ns.TexSize, texH: ns.TexH, inset: ns.Inset,
		}
		if layer.hasBuilt && layer.built == in {
			continue
		}
		p.scratchV, p.scratchI = ns.AppendQuads(
			p.scratchV[:0], p.scratchI[:0],
			p.X, p.Y, p.Width, p.Height, p.Scale, layer.Color,
		)
		r.UpdateMeshData(layer.mesh, p.scratchV, p.scratchI)
		layer.built, layer.hasBuilt = in, true
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
	// It does nothing without the UI glow layer (renderer.WithUIGlowLayer), and
	// that is enforced rather than merely likely: the recorder does not push it
	// on the direct path at all. The swapchain is 8 bits per channel, so there
	// is nowhere above 1 for emission to live, and honouring it anyway would
	// clamp -- turning a mid-grey panel that asked for glow into a white one,
	// which is not "no glow", it is a different colour. A game that sets this
	// and sees nothing should check UIGlowLayer first.
	Glow float32

	// Fill overrides the panel interior; nil derives it from the tint. Panels
	// built through NineSlice carry theirs from there, and a game assembling
	// UIRenderObjects itself sets this directly.
	Fill *PanelFill
}

// UIRenderObjects returns UIRenderObjects with per-layer opacity for the UI pipeline.
//
// The slice is the panel's own and is overwritten by the next call on this
// panel, which is what keeps a per-frame Build from allocating. Copy out of it
// -- which is what append(dst, objs...) at every call site already does -- if
// it has to outlive the frame.
func (p *Panel) UIRenderObjects(screenW, screenH float32) []UIRenderObject {
	if !p.Visible {
		return nil
	}
	proj := mgl32.Ortho(0, screenW, 0, screenH, -1, 1)
	objs := p.objs[:0]
	for _, layer := range p.Layers {
		// A layer with no mesh draws nothing. Layers and PanelLayer are both
		// exported, so a layer assembled by hand rather than through AddLayer
		// reaches here, and dereferencing it was a nil-pointer panic inside
		// the renderer rather than an empty panel the caller could see.
		if layer.mesh == nil {
			continue
		}
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
	p.objs = objs
	return objs
}

// Destroy frees GPU resources for all panel layers.
func (p *Panel) Destroy(r *Renderer) {
	for _, layer := range p.Layers {
		r.DestroyMesh(layer.mesh)
	}
}
