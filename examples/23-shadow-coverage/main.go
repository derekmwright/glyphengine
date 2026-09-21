// Command 23-shadow-coverage puts an off-camera caster one kilometre toward
// the light. -coverage=false restores the original volumes; -caster=false is
// the negative control. Nothing here relies on game assets or planet geometry.
package main

import (
	"flag"
	"log"
	"runtime"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/go-gl/mathgl/mgl32"
)

type game struct{ coverage, caster bool }

func (g *game) Init(e *glyph.Engine) error {
	if g.coverage {
		c := renderer.DefaultShadowCoverage()
		// Depth reaches the ridge without sacrificing the nearby map's texels.
		c.Cascades[0].TowardLight = 1500
		c.Cascades[1] = renderer.ShadowCascadeCoverage{Radius: 2000, TowardLight: 3000, AwayFromLight: 3000}
		if err := e.SetShadowCoverage(c); err != nil {
			return err
		}
	}
	e.Env = &glyph.Environment{Sun: &glyph.DirectionalLight{Direction: [3]float32{0.8, 0.6, 0}, Color: [3]float32{1, 1, 1}}, Ambient: &glyph.AmbientLight{Color: [3]float32{0.05, 0.05, 0.05}}}
	r := e.Renderer()
	plane, err := r.CreatePlane(40, 40)
	if err != nil {
		return err
	}
	id := e.Spawn()
	e.C.Transform.Set(id, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(id, &glyph.MeshRef{Mesh: plane, Roughness: 0.98})
	e.C.Color.Set(id, &glyph.Color{R: 0.7, G: 0.7, B: 0.7})
	cube, err := r.CreateCube(1)
	if err != nil {
		return err
	}
	positions := []mgl32.Vec3{{-5, 1, 0}}
	scales := []mgl32.Vec3{{2, 2, 2}}
	if g.caster {
		positions = append(positions, mgl32.Vec3{800, 600, 0})
		scales = append(scales, mgl32.Vec3{6, 6, 6})
	}
	for i, p := range positions {
		id := e.Spawn()
		e.C.Transform.Set(id, &glyph.Transform{Position: p, Scale: scales[i]})
		e.C.MeshRef.Set(id, &glyph.MeshRef{Mesh: cube, Roughness: 0.98})
		e.C.Color.Set(id, &glyph.Color{R: 0.7, G: 0.4, B: 0.2})
	}
	e.SetCamera(mgl32.Vec3{0, 12, 18}, mgl32.Vec3{}, mgl32.Vec3{0, 1, 0})
	return nil
}
func (g *game) Update(e *glyph.Engine, dt float32) {
	if e.FrameCount() == 30 {
		e.Renderer().ResetGPUTimings()
	}
}

func main() {
	runtime.LockOSThread()
	coverage := flag.Bool("coverage", true, "extend shadow volumes")
	caster := flag.Bool("caster", true, "include the distant caster")
	frames := flag.Int("frames", 0, "exit after N frames")
	shot := flag.String("screenshot", "", "capture last frame")
	flag.Parse()
	e, err := glyph.New(&game{*coverage, *caster}, glyph.WithTitle("Directional shadow coverage"), glyph.WithWindowSize(800, 600), glyph.WithMaxFrames(*frames), glyph.WithScreenshot(*shot))
	if err != nil {
		log.Fatal(err)
	}
	defer e.Destroy()
	e.Run()
	timing := e.MeanGPUTimings()
	log.Printf("GPU mean after warmup: valid=%v shadow=%.5fms total=%.5fms", timing.Valid, timing.Pass[renderer.PassShadow], timing.Total)
}
