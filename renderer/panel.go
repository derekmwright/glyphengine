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
	mesh        *Mesh
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
