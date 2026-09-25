package main

import (
	"fmt"
	"math"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/ui"
	"github.com/derekmwright/glyphengine/ui/yamlui"
)

// The -yamlui HUD: a second, declarative HUD built from a YAML file instead of
// from Go, so the indicator block can be seen driven by a clock rather than
// only asserted about in a unit test.
//
// It is off by default and allocates nothing when off. That is not tidiness:
// `task screenshots` renders this example with stock flags and compares the
// image byte for byte, so anything this file does unconditionally is a changed
// capture.
//
// Everything here is the game's side of the yamlui seam -- the package renders
// no textures and owns no meshes, it hands back geometry and asks three
// callbacks to turn it into draws. PanelFn is not among them because this HUD
// has no nine-slices; flat `bg_color` panels go through the vertex stream
// instead, which is the other half of what BuildAt returns.

// yamlHUDOrigin is where the grid is pinned, at scale 1.
//
// Fixed rather than anchored, and at scale 1 rather than ui.UIScale, because
// `task indicator` reads pixels out of boxes written down in the Taskfile: the
// icons land at exactly the coordinates the YAML's widths and paddings say they
// do. An anchored HUD would move with the window size and every box with it.
const (
	yamlHUDOriginX = 420
	yamlHUDOriginY = 190
)

// yamlPoolVerts and yamlPoolIndices bound one pooled mesh. The largest thing
// either callback asks for is a full-turn square sweep: a hub plus six rim
// points, five triangles.
const (
	yamlPoolVerts   = 16
	yamlPoolIndices = 48
)

// runeIconSize is the side of the procedural icon the YAML's `sprite: rune`
// resolves to. Generated rather than loaded so this example still loads no art
// from disk, and opaque so a measurement of "the indicator darkened this" is
// about the overlay rather than about what was behind the icon.
const runeIconSize = 32

// meshPool hands out dynamic meshes for one frame's worth of callback results
// and reuses them on the next.
//
// The callbacks are called during Update with geometry the widget tree has just
// computed, and a UIRenderObject needs a mesh; creating one per call per frame
// would allocate on the GPU every frame forever. The pool stabilises after the
// first frame and is destroyed with the rest of the example's meshes.
type meshPool struct {
	r     *renderer.Renderer
	items []*renderer.Mesh
	next  int
}

func (p *meshPool) reset() { p.next = 0 }

func (p *meshPool) take(verts []renderer.Vertex, idxs []uint16) *renderer.Mesh {
	if len(verts) > yamlPoolVerts || len(idxs) > yamlPoolIndices {
		return nil
	}
	if p.next == len(p.items) {
		m, err := p.r.CreateDynamicIndexedMesh(yamlPoolVerts, yamlPoolIndices)
		if err != nil {
			return nil
		}
		p.items = append(p.items, m)
	}
	m := p.items[p.next]
	p.next++
	p.r.UpdateMeshData(m, verts, idxs)
	return m
}

func (p *meshPool) destroy() {
	for _, m := range p.items {
		p.r.DestroyMesh(m)
	}
	p.items = nil
}

// initYamlHUD loads the named YAML and wires the two callbacks it needs.
func (g *game) initYamlHUD(e *glyph.Engine) error {
	r := e.Renderer()

	tex, err := r.CreateTexture(buildRuneIcon(), runeIconSize, runeIconSize)
	if err != nil {
		return err
	}
	g.yamlIcon = tex

	if g.yamlQuads, err = r.CreateDynamicIndexedMesh(uiMaxQuads*4, uiMaxQuads*6); err != nil {
		return err
	}
	g.yamlPool = &meshPool{r: r}

	tree, err := yamlui.Load(assetsFS, "assets/ui/"+g.yamlName+".yaml")
	if err != nil {
		return err
	}

	// A sprite: one textured quad, straight texture * colour. The colour is
	// whatever the widget resolved -- which is where `state` and a `tint`
	// indicator have already been folded in.
	tree.IconFn = func(name string, color [3]float32, opacity float32, x, y, w, h, sw, sh float32) []renderer.UIRenderObject {
		var verts []renderer.Vertex
		var idxs []uint16
		verts, idxs = ui.AppendQuad(verts, idxs, x, y, w, h, color)
		m := g.yamlPool.take(verts, idxs)
		if m == nil {
			return nil
		}
		return []renderer.UIRenderObject{{
			RenderObject: renderer.RenderObject{Mesh: m, Texture: g.yamlIcon, MVP: g.yamlProj},
			Opacity:      opacity,
			TextureMode:  true,
		}}
	}

	// An indicator: a flat-coloured triangle fan with no texture, so the
	// fallback 1x1 white is bound and the fragment is the vertex colour at the
	// object's opacity. This is the callback that makes the alpha possible at
	// all -- renderer.Vertex has no alpha channel, so the interpolated value
	// can only ride on the render object.
	tree.ShapeFn = func(verts []renderer.Vertex, idxs []uint16, opacity float32) []renderer.UIRenderObject {
		m := g.yamlPool.take(verts, idxs)
		if m == nil {
			return nil
		}
		return []renderer.UIRenderObject{{
			RenderObject: renderer.RenderObject{Mesh: m, MVP: g.yamlProj},
			Opacity:      opacity,
		}}
	}

	g.yamlTree = tree
	g.yamlAssets = &yamlui.AssetProvider{Font: g.font}
	return nil
}

// buildYamlHUD updates the bindings, lays the tree out and returns the draws.
// The quad stream comes back first because everything in it is a background:
// the panel fills the icons and indicators sit on.
func (g *game) buildYamlHUD(e *glyph.Engine, sw, sh float32) ([]renderer.UIRenderObject, []renderer.TextLine) {
	if g.yamlTree == nil {
		return nil, nil
	}
	g.yamlProj = mgl32.Ortho(0, sw, 0, sh, -1, 1)
	g.yamlPool.reset()
	g.bindYamlHUD()

	panels, verts, idxs, text := g.yamlTree.BuildAt(
		e.Renderer(), g.yamlAssets, yamlHUDOriginX, yamlHUDOriginY, 1, sw, sh)

	var objs []renderer.UIRenderObject
	if len(verts) > 0 {
		e.Renderer().UpdateMeshData(g.yamlQuads, verts, idxs)
		objs = append(objs, renderer.UIRenderObject{
			RenderObject: renderer.RenderObject{Mesh: g.yamlQuads, MVP: g.yamlProj},
			Opacity:      1,
		})
	}
	return append(objs, panels...), text
}

// bindYamlHUD supplies both sets of bindings every frame: the animated ones the
// motion HUD reads and the fixed ones the measured grid reads. A YAML that does
// not mention a key simply never resolves it, so one routine serves both files
// and neither can drift from what the example binds.
func (g *game) bindYamlHUD() {
	t := g.yamlTree
	t.BindFloat("cd_max", cooldownMax)
	t.BindFloat("v_max", cooldownMax)

	// The measured grid: a quarter, a half and a full value, or zero when the
	// gate is asking what a finished cooldown leaves behind.
	values := [3]float32{25, 50, 100}
	if g.yamlZero {
		values = [3]float32{}
	}
	t.BindFloat("v_25", values[0])
	t.BindFloat("v_50", values[1])
	t.BindFloat("v_100", values[2])

	label := "READY"
	if !g.yamlLabels {
		// The same frame without the text, so the gate can difference the two
		// and recover how much glyph coverage survived over the overlay.
		label = ""
	}
	t.Bind("cell_label", label)

	// The motion HUD: three cooldowns running down at different rates, and a
	// toggle that flips independently of any of them.
	sweep := countdown(g.t, 3.0)
	roll := countdown(g.t, 4.5)
	tint := countdown(g.t, 2.0)
	t.BindFloat("cd_sweep", sweep)
	t.BindFloat("cd_roll", roll)
	t.BindFloat("cd_tint", tint)
	t.Bind("cd_sweep_text", fmt.Sprintf("%.0f", math.Ceil(float64(sweep)/10)))
	t.Bind("cd_roll_text", fmt.Sprintf("%.0f", math.Ceil(float64(roll)/10)))

	active := int(g.t/1.5)%2 == 0
	t.Bind("ability_active", fmt.Sprintf("%t", active))
	if active {
		t.Bind("ability_state_text", "ON")
	} else {
		t.Bind("ability_state_text", "OFF")
	}
}

// cooldownMax is the full value every indicator in these files is bound
// against, so a bound value is also a percentage.
const cooldownMax = 100

// countdown runs from cooldownMax down to 0 over period seconds, then restarts.
func countdown(t, period float32) float32 {
	phase := float32(math.Mod(float64(t), float64(period))) / period
	return cooldownMax * (1 - phase)
}

// buildRuneIcon draws the 32x32 icon the YAML's sprite name resolves to: an
// amber tile with a lighter diamond and a dark border.
//
// Opaque everywhere, and deliberately not flat. Opaque so the gate's "this
// region got darker" is a statement about the indicator and not about what
// shows through; not flat so the diamond gives the eye something to watch a
// sweep uncover.
func buildRuneIcon() []byte {
	px := make([]byte, runeIconSize*runeIconSize*4)
	const c = (runeIconSize - 1) / 2.0
	for y := 0; y < runeIconSize; y++ {
		for x := 0; x < runeIconSize; x++ {
			var r, g, b byte
			switch {
			case x < 2 || y < 2 || x >= runeIconSize-2 || y >= runeIconSize-2:
				r, g, b = 60, 45, 25
			case math.Abs(float64(x)-c)+math.Abs(float64(y)-c) < 9:
				r, g, b = 245, 225, 180
			default:
				r, g, b = 200, 150, 70
			}
			i := (y*runeIconSize + x) * 4
			px[i], px[i+1], px[i+2], px[i+3] = r, g, b, 255
		}
	}
	return px
}
