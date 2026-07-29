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
//	go run ./13-ui              # windowed
//	go run ./13-ui -frames 120  # render 120 frames, then exit
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

	t float32
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

	e.SetTimeOfDay(0.33)
	e.SetDayCycleSpeed(0)

	g.camera = glyph.NewCamera(9)
	g.camera.Target = mgl32.Vec3{0, 1.2, 0}
	g.camera.Pitch = 0.20

	log.Println("13-ui running. Escape quits.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}
	g.t += dt

	if t, ok := e.C.Transform.Get(g.cube); ok {
		t.Rotation[1] += 0.4 * dt
	}
	g.camera.Update(in)
	g.camera.Target = mgl32.Vec3{0, 1.2, 0}
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

	e.Renderer().UpdateMeshData(g.uiMesh, verts, idxs)

	// The UI pipeline is ortho and depth-test free, so this matrix is the only
	// thing placing the HUD on screen.
	proj := mgl32.Ortho(0, sw, 0, sh, -1, 1)
	e.SetUIOverlays([]renderer.UIRenderObject{{
		RenderObject: renderer.RenderObject{Mesh: g.uiMesh, MVP: proj},
		Opacity:      0.92,
	}})

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
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 13 UI"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
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

	e, err := glyph.New(&game{}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
