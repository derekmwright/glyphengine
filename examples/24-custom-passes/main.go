// Command 24-custom-passes adds a light field and distance fog to a plain scene.
// The game owns the effects; the renderer owns their images and scheduling.
//
//	go run ./24-custom-passes -frames 120 -screenshot passes.png
//	go run ./24-custom-passes -passes off  # identical scene, no application work
//	go run ./24-custom-passes -timings     # print each application's GPU bracket
//
// Escape quits. GLYPHENGINE_FIXED_FRAME_TIME makes the moving field repeatable;
// GLYPHENGINE_BACKGROUND opens the same hidden window as the other examples.
package main

import (
	_ "embed"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math"
	"runtime"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/shaders"
	"github.com/go-gl/mathgl/mgl32"
)

//go:embed pattern.vert.spv
var patternVert []byte

//go:embed pattern.frag.spv
var patternFrag []byte

//go:embed smooth.comp.spv
var smoothComp []byte

//go:embed lit.frag.spv
var litFrag []byte

//go:embed fog.frag.spv
var fogFrag []byte

//go:embed composite.frag.spv
var compositeFrag []byte

var lightColor = [3]float32{1.0, 0.78, 0.48}

type game struct {
	passes, timings bool
	t               float32
	pattern, fog    *renderer.AppPass
	smooth          *renderer.AppCompute
	field           *renderer.RenderTarget
	draws           []renderer.RenderObject
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()
	e.Env = &glyph.Environment{
		Sun:     &glyph.DirectionalLight{Direction: [3]float32{0.6, 0.8, 0.3}, Color: lightColor},
		Ambient: &glyph.AmbientLight{Color: [3]float32{0.10, 0.12, 0.16}},
		Sky:     &glyph.Sky{FixedSunElevation: 0.4},
	}
	plane, err := r.CreatePlane(160, 160)
	if err != nil {
		return err
	}
	spawn := func(mesh *renderer.Mesh, pos, scale mgl32.Vec3, color glyph.Color) {
		id := e.Spawn()
		e.C.Transform.Set(id, &glyph.Transform{Position: pos, Scale: scale})
		e.C.MeshRef.Set(id, &glyph.MeshRef{Mesh: mesh, Roughness: 0.95})
		e.C.Color.Set(id, &color)
		e.C.Static.Set(id, &glyph.Static{})
	}
	spawn(plane, mgl32.Vec3{}, mgl32.Vec3{1, 1, 1}, glyph.Color{R: 0.22, G: 0.28, B: 0.32})
	cube, err := r.CreateCube(1)
	if err != nil {
		return err
	}
	for i, pos := range []mgl32.Vec3{{-7, 1.5, -3}, {0, 2.5, -7}, {7, 2, -12}} {
		spawn(cube, pos, mgl32.Vec3{3, pos.Y() * 2, 3}, glyph.Color{R: 0.52 + float32(i)*0.1, G: 0.34, B: 0.21})
	}
	e.RebuildStatics()
	e.SetCamera(mgl32.Vec3{0, 8, 24}, mgl32.Vec3{0, 2, -8}, mgl32.Vec3{0, 1, 0})
	if !g.passes {
		return nil
	}

	// 1. A world-XZ light map, not a screen overlay. Three quads contribute
	// to one half-resolution R16F image; additive blending keeps their overlap.
	pattern, err := r.CreateRenderTarget(renderer.RenderTargetDesc{
		Name: "light pattern", Format: renderer.TargetR16F, Scale: 0.5,
	})
	if err != nil {
		return err
	}
	g.pattern, err = r.CreateAppPass(renderer.AppPassDesc{
		Name: "light pattern", Stage: renderer.StageBeforeScene, Target: pattern,
		Vert: patternVert, Frag: patternFrag, Blend: renderer.BlendAdditive, Timed: true,
	})
	if err != nil {
		return err
	}
	quad, err := r.CreateIndexedMesh([]renderer.Vertex{
		{Pos: [3]float32{-1, -1, 0}, UV: [2]float32{0, 0}},
		{Pos: [3]float32{1, -1, 0}, UV: [2]float32{1, 0}},
		{Pos: [3]float32{1, 1, 0}, UV: [2]float32{1, 1}},
		{Pos: [3]float32{-1, 1, 0}, UV: [2]float32{0, 1}},
	}, []uint16{0, 1, 2, 0, 2, 3})
	if err != nil {
		return err
	}
	g.draws = make([]renderer.RenderObject, 3)
	for i := range g.draws {
		g.draws[i].Mesh = quad
	}
	g.pattern.SetDraws(g.draws) // retained through DrawFrame; update Models in place

	// 2. Creation order puts compute after the mesh pass at the same stage.
	// History samples the previous image while storage writes the other one.
	g.field, err = r.CreateRenderTarget(renderer.RenderTargetDesc{
		Name: "smoothed light", Format: renderer.TargetR32F, Scale: 0.5, Storage: true, History: true,
	})
	if err != nil {
		return err
	}
	g.smooth, err = r.CreateAppCompute(renderer.AppComputeDesc{
		Name: "temporal smoothing", Stage: renderer.StageBeforeScene, Comp: smoothComp,
		Reads:  []*renderer.Texture{pattern.Texture(), g.field.Texture()},
		Writes: []*renderer.RenderTarget{g.field}, Timed: true,
	})
	if err != nil {
		return err
	}
	// 3. The custom LitFrag uses light-set binding 7 (slot 0). History slots
	// read the previous write too, so this presentation is one frame behind.
	if err := r.SetShaderTarget(0, g.field); err != nil {
		return err
	}

	// 4. Read resolved HDR/depth into an own target, then add it to loaded
	// HDR. Reading SceneColor while writing HDR itself would be a feedback loop.
	fog, err := r.CreateRenderTarget(renderer.RenderTargetDesc{
		Name: "half-resolution fog", Format: renderer.TargetRGBA16F, Scale: 0.5,
	})
	if err != nil {
		return err
	}
	depth := r.SceneDepth() // reverse-Z R32F, including the MSAA depth resolve
	g.fog, err = r.CreateAppPass(renderer.AppPassDesc{
		Name: "distance fog", Stage: renderer.StageBeforeBloom, Target: fog,
		Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: fogFrag,
		Reads: []*renderer.Texture{r.SceneColor(), depth}, Timed: true,
	})
	if err != nil {
		return err
	}
	_, err = r.CreateAppPass(renderer.AppPassDesc{
		Name: "depth-aware composite", Stage: renderer.StageBeforeBloom,
		Target: nil, Load: true, Blend: renderer.BlendAdditive,
		Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: compositeFrag,
		Reads: []*renderer.Texture{fog.Texture(), depth}, Timed: true,
	})
	// Renderer.Destroy releases these resources. For effects with shorter
	// lifetimes, destroy their passes/compute first, then their targets.
	return err
}

func (g *game) FixedUpdate(_ *glyph.Engine, dt float32) { g.t += dt }

func (g *game) Update(e *glyph.Engine, _ float32) {
	if e.Input().KeyPressed(input.KeyEscape) {
		e.Close()
	}
	// 5. Drop initialization samples just as 23-shadow-coverage does.
	if g.timings && e.FrameCount() == 30 {
		e.Renderer().ResetGPUTimings()
	}
}

func (g *game) LateUpdate(e *glyph.Engine, dt float32) {
	if !g.passes {
		return
	}
	for i := range g.draws {
		phase := g.t*0.65 + float32(i)*1.7
		x := float32(math.Sin(float64(phase))) * 7
		z := float32(math.Cos(float64(phase*0.8))) * 6
		g.draws[i].Model = mgl32.Translate3D(x, z, 0).
			Mul4(mgl32.HomogRotate3DZ(phase * 0.4)).Mul4(mgl32.Scale3D(9, 2.8, 1))
	}
	// Extent follows recreation. Also cover a pending window resize, which
	// DrawFrame handles after this hook; excess invocations are bounds-checked.
	w, h := g.field.Extent()
	fw, fh := e.Window().GetFramebufferSize()
	w, h = max(w, uint32(fw)/2), max(h, uint32(fh)/2)
	g.smooth.SetDispatch((w+7)/8, (h+7)/8, 1)
	// Exponential smoothing has a time constant, not a frame-count constant.
	alpha := float32(1 - math.Exp(-float64(dt)/0.18))
	if err := g.smooth.SetPushConstants(pack(alpha)); err != nil {
		log.Fatal(err)
	}
	inv := e.ViewProjection().Inv()
	data := append(inv[:], lightColor[0], lightColor[1], lightColor[2], 0)
	if err := g.fog.SetPushConstants(pack(data...)); err != nil {
		log.Fatal(err)
	}
}

// Application data begins at push offset 128, after the engine's VP/model.
func pack(values ...float32) []byte {
	data := make([]byte, (len(values)*4+15)/16*16)
	for i, v := range values {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(v))
	}
	return data
}

func main() {
	runtime.LockOSThread()
	frames := flag.Int("frames", 0, "render N frames then exit (0 = until closed)")
	shot := flag.String("screenshot", "", "write the last frame as PNG")
	passes := flag.String("passes", "on", "application passes: on or off")
	timings := flag.Bool("timings", false, "print application GPU timings after 30 warm-up frames")
	flag.Parse()
	if *passes != "on" && *passes != "off" {
		log.Fatal("-passes must be on or off")
	}
	sh := renderer.DefaultShaders()
	if *passes == "on" {
		sh.LitFrag = litFrag
	}
	e, err := glyph.New(&game{passes: *passes == "on", timings: *timings},
		glyph.WithTitle("GlyphEngine - 24 Custom passes"), glyph.WithWindowSize(960, 540),
		glyph.WithProjection(50, 0.1, 300), glyph.WithMSAA(4), glyph.WithShaders(sh),
		glyph.WithMaxFrames(*frames), glyph.WithScreenshot(*shot))
	if err != nil {
		log.Fatal(err)
	}
	defer e.Destroy()
	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
	if *timings && e.FrameCount() > 30 {
		timing := e.MeanGPUTimings()
		fmt.Printf("GPU mean after warmup: valid=%v total=%.5fms\n", timing.Valid, timing.Total)
		for _, pass := range timing.App {
			fmt.Printf("  %s: %.5fms\n", pass.Name, pass.Ms)
		}
	}
}
