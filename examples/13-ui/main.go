// Command 13-ui draws a HUD over a 3D scene: panels, bars, and MSDF labels.
//
// The UI is immediate mode. Nothing is retained between frames except the
// widget structs holding their own values — every frame rebuilds the quads,
// uploads them to one dynamic mesh, and hands the renderer a single overlay
// object. There is no layout tree, no invalidation, and no widget lifetime to
// manage, which is the right trade for a HUD that changes every frame anyway.
//
// Three layers stack up, and they are drawn in this order for a reason:
//
//	panels   flat quads, drawn first so everything sits on top of them
//	bars     ui.ProgressBar, two quads each: background then fill
//	labels   MSDF text, drawn last so it is never occluded
//
// The UI pipeline is ortho-projected with depth testing off, so screen
// position is the only thing that decides what covers what.
//
//	go run ./13-ui                    # windowed
//	go run ./13-ui -frames 120        # render 120 frames, then exit
//	go run ./13-ui -glow on           # a second panel whose elements emit light
//	go run ./13-ui -yamlui cooldown   # a second HUD, built from YAML
//
// -glow selects one of four modes, and the reason there are four is that each
// pair of them isolates exactly one thing:
//
//	off     no layer and no second panel: the frame this example always drew
//	direct  the second panel, drawn straight onto the swapchain as usual
//	layer   the same panel through the UI glow layer, nothing asking to glow
//	on      the same panel again, with three of its elements emitting
//
// direct against layer is the layer's own cost, over geometry that is identical
// in both -- it has to come out the same picture, because the layer is not
// supposed to change what a colour at or below 1 means. layer against on is the
// glow and nothing else. `task uiglow` differences exactly those two pairs.
//
// What to look at in `on`: the teal button and the amber label light up, and the
// dark panel AROUND them lifts with it. That bleed across elements is the thing
// only the engine can add -- a game can already replace UIFrag for a halo on one
// element, but not make one element light the next.
//
// -time, -bloom, -exposure and -curve move the SCENE, and they are here so that
// the UI's independence from it is measurable from outside rather than merely
// asserted: the glow has to be the same at midnight as at noon, and the same
// under ACES as under identity, because it does not go through either.
//
// -yamlui names a file under assets/ui and draws a second HUD from it through
// ui/yamlui, which is the only place in this repository that package is driven
// by a clock. `cooldown` is the one to watch: a sweep unwinding on one icon, a
// downward wipe on the next, and a tint on a toggle that flips on its own. A
// still frame cannot tell a sweep that unwinds the wrong way from one that does
// not, so this exists to be looked at. `indicators` is the fixed grid
// `task indicator` reads pixels out of.
//
// It is off by default and allocates nothing when off, so every capture this
// example already produces is unchanged.
//
// The bars animate on their own. Escape quits.
package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"math"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/ui"
	"github.com/derekmwright/glyphengine/ui/yamlui"
)

//go:embed assets
var assetsFS embed.FS

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

// uiMaxQuads bounds the dynamic mesh. Every panel and every bar segment is one
// quad, so this is generous for a HUD.
const uiMaxQuads = 512

// autoOrbitRate circles the camera slowly, in radians per second.
//
// No mouse look: this example is about building a HUD, and camera
// plumbing in front of that is just noise to read past. Orbiting on its own
// also makes -frames screenshots repeatable.
const autoOrbitRate = 0.15

type game struct {
	camera *glyph.Camera
	cube   glyph.Entity

	// One dynamic mesh for the whole HUD. Separate meshes per widget would
	// mean a draw call per widget for geometry that is a few hundred vertices
	// in total.
	uiMesh *renderer.Mesh

	font *renderer.Font
	text *renderer.MSDFText

	health, stamina, mana ui.ProgressBar

	// The glow demo. mode >= glowDirect draws the second panel; mode == glowOn
	// is the only one where anything emits. The meshes are nil in glowOff: an
	// element that emits needs its own UIRenderObject, because Glow is per
	// object, and allocating for objects nothing will draw is the sort of thing
	// that makes a default run stop being the frame it was.
	mode     glowMode
	btnMesh  *renderer.Mesh
	warnMesh *renderer.Mesh

	// The YAML-driven HUD (-yamlui). Everything here stays nil when the flag
	// is not given; see yamlhud.go.
	yamlName   string
	yamlZero   bool
	yamlLabels bool
	yamlTree   *yamlui.WidgetTree
	yamlAssets *yamlui.AssetProvider
	// The icon texture is not destroyed here: the renderer sweeps its own
	// texture registry at Destroy, which is how 16-materials and
	// 21-streetlights leave theirs too. The meshes are not in that sweep.
	yamlIcon  *renderer.Texture
	yamlQuads *renderer.Mesh
	yamlPool  *meshPool
	yamlProj  [16]float32

	// The scene's look, so the UI's independence from it can be MEASURED
	// rather than argued. The whole reason the UI has a layer of its own is
	// that its brightness must not move with the time of day or with the
	// scene's exposure curve, and a claim like that is worth nothing unless
	// someone outside the engine can render both and difference them.
	timeOfDay               float32
	sceneBloom, sceneThres  float32
	exposure, curve         float32
	glowStrength, glowThres float32

	t float32
}

// glowMode is which of the four demo configurations to draw; see the package
// comment for what each pair of them isolates.
type glowMode int

const (
	glowOff glowMode = iota
	glowDirect
	glowLayer
	glowOn
)

func parseGlowMode(s string) (glowMode, error) {
	switch s {
	case "off":
		return glowOff, nil
	case "direct":
		return glowDirect, nil
	case "layer":
		return glowLayer, nil
	case "on":
		return glowOn, nil
	}
	return glowOff, fmt.Errorf("unknown -glow mode %q: want off, direct, layer or on", s)
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// ── scene behind the HUD ──
	ground, err := r.CreatePlane(40, 40)
	if err != nil {
		return err
	}
	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: ground, Roughness: 0.95})
	e.C.Color.Set(ent, &glyph.Color{R: 0.26, G: 0.31, B: 0.27})
	e.C.Static.Set(ent, &glyph.Static{})
	e.C.NoCastShadow.Set(ent, &glyph.NoCastShadow{})

	cube, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	g.cube = e.Spawn()
	e.C.Transform.Set(g.cube, &glyph.Transform{
		Position: mgl32.Vec3{0, 1.2, 0},
		Scale:    mgl32.Vec3{2.2, 2.2, 2.2},
	})
	e.C.MeshRef.Set(g.cube, &glyph.MeshRef{Mesh: cube, Metallic: 0.1, Roughness: 0.45})
	e.C.Color.Set(g.cube, &glyph.Color{R: 0.72, G: 0.44, B: 0.30})

	// ── HUD ──
	g.uiMesh, err = r.CreateDynamicIndexedMesh(uiMaxQuads*4, uiMaxQuads*6)
	if err != nil {
		return err
	}

	font, err := renderer.LoadFont(r, assetsFS,
		"assets/fonts/goregular.json", "assets/fonts/goregular.png")
	if err != nil {
		return err
	}
	g.font = font
	if g.text, err = renderer.NewMSDFText(r, font); err != nil {
		return err
	}

	// Widgets hold their own state; the example only updates Value.
	g.health = ui.ProgressBar{
		Width: 240, Height: 18, Max: 100, Value: 72,
		FgColor: [3]float32{0.80, 0.22, 0.24}, BgColor: [3]float32{0.16, 0.07, 0.08},
	}
	g.stamina = ui.ProgressBar{
		Width: 240, Height: 12, Max: 100, Value: 45,
		FgColor: [3]float32{0.35, 0.75, 0.32}, BgColor: [3]float32{0.09, 0.16, 0.09},
	}
	g.mana = ui.ProgressBar{
		Width: 240, Height: 12, Max: 100, Value: 88,
		FgColor: [3]float32{0.32, 0.52, 0.90}, BgColor: [3]float32{0.08, 0.11, 0.18},
	}

	if r.UIGlowLayer() {
		// Flags rather than constants, because they are the game's numbers to
		// tune and not the engine's opinion. The defaults are the engine's own
		// starting point: 1.2 / 0.2 puts the foot of the ramp at exactly 1.0,
		// which is the most an ordinary UI colour can reach once srgbToLinear
		// and the premultiply have run, so nothing glows by accident.
		//
		// -glowstrength 0 is the row worth benchmarking against: it switches
		// the UI's bloom chain off and skips recording it, leaving the layer
		// itself and the composite.
		r.SetUIGlow(g.glowStrength, g.glowThres, 0.2, 1.0)
		r.SetUIExposure(1.0)
	}
	if g.mode >= glowDirect {
		if g.btnMesh, err = r.CreateDynamicIndexedMesh(4, 6); err != nil {
			return err
		}
		if g.warnMesh, err = r.CreateDynamicIndexedMesh(4, 6); err != nil {
			return err
		}
	}

	e.SetTimeOfDay(g.timeOfDay)
	e.SetDayCycleSpeed(0)
	if g.sceneBloom > 0 {
		// The SCENE's bloom, which the UI must never feed and must never be fed
		// by. The threshold is a flag because the default 1.2 selects nothing in
		// this scene -- measured: -bloom 0.7 at 1.2 renders a byte-identical
		// frame -- and a bloom that does nothing proves nothing about what it
		// does not pick up. Drop it to 0.5 and the sky clears it, which is the
		// configuration `task hud` uses for the same reason.
		r.SetBloom(g.sceneBloom, g.sceneThres, 0.2, 1.0)
	}
	if g.exposure > 0 || g.curve > 0 {
		r.SetTonemap(g.exposure, g.curve, 6)
	}

	if g.yamlName != "" {
		if err := g.initYamlHUD(e); err != nil {
			return err
		}
	}

	g.camera = glyph.NewCamera(9)
	g.camera.Target = mgl32.Vec3{0, 1.2, 0}
	g.camera.Pitch = 0.20

	log.Println("13-ui running. Escape quits.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	g.t += dt

	if t, ok := e.C.Transform.Get(g.cube); ok {
		t.Rotation[1] += 0.4 * dt
	}
	g.camera.Yaw += autoOrbitRate * dt
	e.SetCamera(g.camera.ViewVectors())

	// Drift the values so the bars visibly track something.
	g.health.Value = 55 + float32(math.Sin(float64(g.t)*0.6))*35
	g.stamina.Value = 50 + float32(math.Sin(float64(g.t)*1.7+1))*45
	g.mana.Value = 60 + float32(math.Sin(float64(g.t)*0.9+2))*35

	w, h := e.Renderer().Extent()
	sw, sh := float32(w), float32(h)
	g.buildHUD(e, sw, sh)
}

// buildHUD rebuilds every quad in the HUD and uploads them as one mesh.
//
// Rebuilding wholesale each frame is the immediate-mode bargain: it costs a
// few hundred vertices of upload and buys the absence of any retained state to
// keep in sync with the game.
func (g *game) buildHUD(e *glyph.Engine, sw, sh float32) {
	var verts []renderer.Vertex
	var idxs []uint16

	// Backing panel. ui.AppendQuad is the primitive everything here is built
	// from; the widgets are conveniences over it, not a separate system.
	verts, idxs = ui.AppendQuad(verts, idxs, 24, 24, 372, 118, [3]float32{0.05, 0.06, 0.08})
	verts, idxs = ui.AppendQuad(verts, idxs, 24, 24, 372, 3, [3]float32{0.78, 0.65, 0.31})

	// Bars, positioned explicitly. BuildAt takes screen coordinates; Build
	// resolves an Anchor instead, for widgets that should follow a corner.
	appendBar := func(b *ui.ProgressBar, x, y float32) {
		bv, bi := b.BuildAt(x, y, b.Width, b.Height)
		base := uint16(len(verts))
		verts = append(verts, bv...)
		for _, i := range bi {
			idxs = append(idxs, base+i)
		}
	}
	appendBar(&g.health, 48, 54)
	appendBar(&g.stamina, 48, 84)
	appendBar(&g.mana, 48, 106)

	if g.mode >= glowDirect {
		// The second panel's backdrop, in the SAME mesh and the same overlay as
		// the HUD above -- so it carries Glow 0 and cannot emit whatever mode
		// this is. It is here to be lit BY the elements on it, which is the half
		// of the feature a game cannot reach on its own, and it is dark on
		// purpose: the sky behind it is already near white, and added light on
		// something already at 255 is invisible to a measurement as well as to
		// the eye.
		verts, idxs = ui.AppendQuad(verts, idxs, 420, 40, 340, 152, [3]float32{0.05, 0.06, 0.08})
		verts, idxs = ui.AppendQuad(verts, idxs, 420, 40, 340, 3, [3]float32{0.78, 0.65, 0.31})
	}

	e.Renderer().UpdateMeshData(g.uiMesh, verts, idxs)

	// The UI pipeline is ortho and depth-test free, so this matrix is the only
	// thing placing the HUD on screen.
	proj := mgl32.Ortho(0, sw, 0, sh, -1, 1)
	overlays := []renderer.UIRenderObject{{
		RenderObject: renderer.RenderObject{Mesh: g.uiMesh, MVP: proj},
		Opacity:      0.92,
	}}

	if g.mode >= glowDirect {
		// A button, and a warning that pulses. Both are ordinary panels with an
		// ordinary sRGB colour, drawn in every mode from `direct` up; the only
		// thing `on` changes is Glow, a LINEAR multiple of that colour. The
		// button's 2.0 means it emits three times its own teal, which puts it at
		// 2.06 in linear -- clear of the 1.2 threshold, so the chain over the
		// layer picks it up and spreads it onto the backdrop.
		var bv []renderer.Vertex
		var bi []uint16
		bv, bi = ui.AppendQuad(bv, bi, 444, 60, 140, 30, [3]float32{0.20, 0.85, 0.80})
		e.Renderer().UpdateMeshData(g.btnMesh, bv, bi)

		var wv []renderer.Vertex
		var wi []uint16
		wv, wi = ui.AppendQuad(wv, wi, 444, 104, 140, 18, [3]float32{0.90, 0.30, 0.18})
		e.Renderer().UpdateMeshData(g.warnMesh, wv, wi)

		// 0 at the trough rather than a floor above it, so the pulse passes
		// through "no glow at all" every cycle. A warning that never stops
		// glowing is a lamp; one that crosses the threshold is a warning.
		pulse := 0.5 + 0.5*float32(math.Sin(float64(g.t)*3.0))

		var buttonGlow, warnGlow float32
		if g.mode == glowOn {
			buttonGlow, warnGlow = 2.0, 3.0*pulse
		}

		overlays = append(overlays,
			renderer.UIRenderObject{
				RenderObject: renderer.RenderObject{Mesh: g.btnMesh, MVP: proj},
				Opacity:      1.0,
				Glow:         buttonGlow,
			},
			renderer.UIRenderObject{
				RenderObject: renderer.RenderObject{Mesh: g.warnMesh, MVP: proj},
				Opacity:      1.0,
				Glow:         warnGlow,
			},
		)
	}

	// The YAML HUD's draws go after the immediate-mode ones. Within them the
	// order is the one buildYamlHUD returns: the flat-quad mesh first, because
	// everything in it is a background, then the sprites and the indicators
	// over them in the order the widget tree emitted them.
	yamlObjs, yamlText := g.buildYamlHUD(e, sw, sh)
	overlays = append(overlays, yamlObjs...)

	e.SetUIOverlays(overlays)

	// Labels last, so nothing covers them.
	lines := []renderer.TextLine{
		{Text: "STATUS", X: 48, Y: 32, Scale: 15, Color: [3]float32{0.78, 0.65, 0.31}},
		{Text: fmt.Sprintf("HP  %.0f", g.health.Value), X: 300, Y: 52, Scale: 15,
			Color: [3]float32{0.92, 0.72, 0.72}},
		{Text: fmt.Sprintf("ST  %.0f", g.stamina.Value), X: 300, Y: 80, Scale: 13,
			Color: [3]float32{0.76, 0.92, 0.74}},
		{Text: fmt.Sprintf("MP  %.0f", g.mana.Value), X: 300, Y: 102, Scale: 13,
			Color: [3]float32{0.74, 0.82, 0.96}},
		{Text: fmt.Sprintf("%.0f fps", e.FPS()), X: -32, Y: 28, Scale: 18,
			Color: [3]float32{0.6, 1.0, 0.6}},
		{Text: "immediate-mode HUD: rebuilt every frame into one mesh",
			X: 32, Y: sh - 44, Scale: 16, Color: [3]float32{0.85, 0.85, 0.85}},
	}
	if g.mode >= glowDirect {
		// Two lines of the same block, in the same draw as every other line.
		// Glow is per TextLine precisely so this works: MSDFText builds one mesh
		// from every line it is given, so a per-overlay value could not light
		// the first of these and leave the second alone.
		var labelGlow float32
		if g.mode == glowOn {
			labelGlow = 2.2
		}
		lines = append(lines,
			renderer.TextLine{
				Text: "REACTOR CRITICAL", X: 444, Y: 134, Scale: 20,
				Color: [3]float32{1.00, 0.75, 0.35}, Glow: labelGlow,
			},
			renderer.TextLine{
				Text: "coolant nominal", X: 444, Y: 162, Scale: 14,
				Color: [3]float32{0.62, 0.66, 0.70},
			},
		)
	}
	// The YAML HUD's labels join the same MSDF draw, which is composited after
	// every UI panel -- so a label over an indicator is over it in the frame as
	// well as in the widget tree.
	lines = append(lines, yamlText...)

	g.text.SetText(e.Renderer(), lines, sw, sh)
	e.SetMSDFOverlays([]renderer.RenderObject{g.text.RenderObject(sw, sh, 48)})
}

func (g *game) Shutdown(e *glyph.Engine) {
	if g.text != nil {
		g.text.Destroy(e.Renderer())
	}
	if g.uiMesh != nil {
		e.Renderer().DestroyMesh(g.uiMesh)
	}
	if g.btnMesh != nil {
		e.Renderer().DestroyMesh(g.btnMesh)
	}
	if g.warnMesh != nil {
		e.Renderer().DestroyMesh(g.warnMesh)
	}
	if g.yamlQuads != nil {
		e.Renderer().DestroyMesh(g.yamlQuads)
	}
	if g.yamlPool != nil {
		g.yamlPool.destroy()
	}
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	glow := flag.String("glow", "off", "glow demo: off, direct, layer or on (see the package comment)")
	timeOfDay := flag.Float64("time", 0.33, "time of day, 0 = midnight, 0.5 = noon")
	sceneBloom := flag.Float64("bloom", 0, "scene bloom intensity (0 = off)")
	sceneThres := flag.Float64("bloomthreshold", 1.2, "scene bloom threshold")
	glowStrength := flag.Float64("glowstrength", 0.7, "UI glow strength; 0 switches the UI's bloom chain off and skips recording it")
	glowThres := flag.Float64("glowthreshold", 1.2, "UI glow threshold, in linear light; keep it at or above 1 + the knee")
	exposure := flag.Float64("exposure", 0, "scene tonemap exposure (0 = unchanged)")
	curve := flag.Float64("curve", 0, "scene tonemap curve: 0 identity, 1 Reinhard, 2 ACES")
	yamlName := flag.String("yamlui", "", "draw a second HUD from assets/ui/<name>.yaml (empty = off)")
	yamlZero := flag.Bool("yamluizero", false, "bind every indicator value to zero: what a finished cooldown leaves behind")
	yamlLabels := flag.Bool("yamluilabels", true, "draw the YAML HUD's labels")
	flag.Parse()

	mode, err := parseGlowMode(*glow)
	if err != nil {
		log.Fatal(err)
	}

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 13 UI"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		glyph.WithQuitKey(input.KeyEscape),
	}
	if mode >= glowLayer {
		opts = append(opts, glyph.WithUIGlow())
	}
	if *fullscreen {
		opts = append(opts, glyph.WithFullscreen())
	}
	if *frames > 0 {
		opts = append(opts, glyph.WithMaxFrames(*frames))
	}
	if *shot != "" {
		opts = append(opts, glyph.WithScreenshot(*shot))
	}

	e, err := glyph.New(&game{
		mode:         mode,
		timeOfDay:    float32(*timeOfDay),
		sceneBloom:   float32(*sceneBloom),
		sceneThres:   float32(*sceneThres),
		glowStrength: float32(*glowStrength),
		glowThres:    float32(*glowThres),
		exposure:     float32(*exposure),
		curve:        float32(*curve),
		yamlName:     *yamlName,
		yamlZero:     *yamlZero,
		yamlLabels:   *yamlLabels,
	}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
