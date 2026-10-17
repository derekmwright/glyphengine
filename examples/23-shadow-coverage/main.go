// Command 23-shadow-coverage puts an off-camera caster one kilometre toward
// the light. -coverage=false restores the original volumes; -caster=false is
// the negative control. Nothing here relies on game assets or planet geometry.
//
// -cascades 3 is the planet-scale arm: three directional cascades at 90 m,
// 3 km and 120 km, each with 200 km of caster reach toward the light and
// 120 km away, over an 8 km ground plane, with a 4 km mountain behind the
// camera that shades the near and middle receivers. -caster=false removes the
// mountain; -receivers=false drops the two finer tiers' ReceiverDepth so the
// depth test alone decides which tier a receiver gets. -planet without
// -cascades is the same scene at two cascades (90 m and 3 km), the control for
// what the third tier costs. -probe N looks straight down at a shadow edge
// served by cascade N, for its texel size. -pan moves the camera every frame
// and rebases the world to the origin every 25 frames.
package main

import (
	"flag"
	"log"
	"runtime"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/go-gl/mathgl/mgl32"
)

type game struct {
	coverage, caster, receivers bool
	cascades                    int
	planet                      bool
	// pan moves the camera and the cascade centre every frame and rebases the
	// world toward the origin every 25 frames, the way a camera-relative game
	// does; see Update.
	pan    bool
	centre mgl32.Vec3
	// probe >= 0 is the texel-size probe: see initPlanet.
	probe int
}

func (g *game) Init(e *glyph.Engine) error {
	if g.planet {
		return g.initPlanet(e)
	}
	if g.coverage {
		c := renderer.DefaultShadowCoverage()
		// Depth reaches the ridge without sacrificing the nearby map's texels.
		c.Cascades[0].TowardLight = 1500
		c.Cascades[1] = renderer.ShadowCascadeCoverage{Radius: 2000, TowardLight: 3000, AwayFromLight: 3000}
		if err := e.SetShadowCoverage(c); err != nil {
			return err
		}
	}
	e.Env = &glyph.StaticSource{Sun: &glyph.DirectionalLight{Direction: [3]float32{0.8, 0.6, 0}, Color: [3]float32{1, 1, 1}}, Ambient: &glyph.AmbientLight{Color: [3]float32{0.05, 0.05, 0.05}}}
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

// initPlanet is the -cascades 3 scene. The sun is behind the camera, so the
// mountain that shades the view is behind it too: its shadow reaches the
// ground in front of the camera only through caster reach, which each tier
// keeps at 200 km. The cascades are centred on the camera target at the
// origin.
//
// Along the view, a ground point at z = -115 is 92 m from the centre along the
// light and 69 m off its axis: inside the 90 m map's XY bounds and past its
// 90 m ReceiverDepth, so the 3 km tier shadows it. Further out, z = -300 to
// -1600 is middle-tier ground, and the 2 m prop at (-5, 1, 0) stands on 90 m
// ground for contact detail.
func (g *game) initPlanet(e *glyph.Engine) error {
	reach := func(radius, receivers float32) renderer.ShadowCascadeCoverage {
		c := renderer.ShadowCascadeCoverage{Radius: radius, TowardLight: 200000, AwayFromLight: 120000}
		if g.receivers {
			c.ReceiverDepth = receivers
		}
		return c
	}
	// At two cascades (-planet without -cascades) the scene keeps the 90 m and
	// 3 km tiers, so the two runs differ by exactly the 120 km tier: the cost
	// measurement's control.
	coverage := renderer.ShadowCoverage{Count: g.cascades}
	coverage.Cascades[0], coverage.Cascades[1] = reach(90, 90), reach(3000, 3000)
	if g.cascades > 2 {
		coverage.Cascades[2] = reach(120000, 0)
	}
	if g.cascades == renderer.MaxShadowCascades {
		// The fourth tier only exists so the four-layer path renders; it
		// covers what the third does at a coarser size.
		coverage.Cascades[3] = reach(240000, 0)
	}
	for tier := range max(g.probe, 0) {
		coverage.Cascades[tier].ReceiverDepth = 1e-4
	}
	if err := e.SetShadowCoverage(coverage); err != nil {
		return err
	}
	e.Env = &glyph.StaticSource{Sun: &glyph.DirectionalLight{Direction: [3]float32{0, 0.6, 0.8}, Color: [3]float32{1, 1, 1}}, Ambient: &glyph.AmbientLight{Color: [3]float32{0.05, 0.05, 0.05}}}
	r := e.Renderer()
	plane, err := r.CreatePlane(8000, 8000)
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
		// 4 km wide and tall, 4 km behind the camera. At the sun's 37 degree
		// elevation its top edge throws shadow to z = 3700 - 4000/0.75, about
		// -1630, which covers the near tier and most of the middle one.
		positions = append(positions, mgl32.Vec3{0, 2000, 4000})
		scales = append(scales, mgl32.Vec3{4000, 4000, 600})
	}
	for i, p := range positions {
		id := e.Spawn()
		e.C.Transform.Set(id, &glyph.Transform{Position: p, Scale: scales[i]})
		e.C.MeshRef.Set(id, &glyph.MeshRef{Mesh: cube, Roughness: 0.98})
		e.C.Color.Set(id, &glyph.Color{R: 0.7, G: 0.4, B: 0.2})
	}
	if g.probe >= 0 {
		// Straight down onto the mountain shadow's side edge at x = 2000,
		// which runs along z and lines up with the shadow maps' X axis. Each
		// tier below the probed one gets a 0.1 mm ReceiverDepth, so every
		// receiver in view falls through to the probed tier, and the height
		// puts its filter ramp at a few dozen pixels.
		height := [renderer.MaxShadowCascades]float32{4, 100, 1500, 1500}[g.probe]
		e.SetCamera(mgl32.Vec3{2000, height, 0}, mgl32.Vec3{2000, 0, 0}, mgl32.Vec3{0, 0, -1})
		return nil
	}
	e.SetCamera(mgl32.Vec3{0, 50, 140}, mgl32.Vec3{}, mgl32.Vec3{0, 1, 0})
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	if e.FrameCount() == 30 {
		e.Renderer().ResetGPUTimings()
	}
	if !g.pan {
		return
	}
	// 0.37 units a frame is about four 90 m-map texels, so the snapped fit
	// moves by whole texels most frames and by a different count on others.
	g.centre = g.centre.Add(mgl32.Vec3{0.37, 0, -0.21})
	if e.FrameCount()%25 == 0 {
		// Rebase: move the world, not the camera, so the centre is the origin
		// again. Static geometry has no previous transform, so interpolation
		// does not smear it across the jump.
		shift := g.centre
		e.C.Transform.Each(func(_ ecs.Entity, t *glyph.Transform) {
			t.Position = t.Position.Sub(shift)
		})
		g.centre = mgl32.Vec3{}
	}
	e.SetCamera(g.centre.Add(mgl32.Vec3{0, 50, 140}), g.centre, mgl32.Vec3{0, 1, 0})
}

func main() {
	runtime.LockOSThread()
	coverage := flag.Bool("coverage", true, "extend shadow volumes (two-cascade arm)")
	caster := flag.Bool("caster", true, "include the distant caster")
	cascades := flag.Int("cascades", 2, "directional shadow cascades; 3 or 4 selects the planet-scale arm")
	receivers := flag.Bool("receivers", true, "bound the finer tiers' receivers with ReceiverDepth (planet arm)")
	probe := flag.Int("probe", -1, "planet arm: look straight down at a shadow edge served by this cascade (texel-size probe)")
	planetFlag := flag.Bool("planet", false, "the planet-scale scene; implied by -cascades 3 or 4, and at two cascades keeps the 90 m and 3 km tiers")
	pan := flag.Bool("pan", false, "planet arm: move the camera every frame and rebase the world every 25 frames")
	novsync := flag.Bool("novsync", false, "disable vsync, for measuring the shadow pass with the GPU kept busy")
	frames := flag.Int("frames", 0, "exit after N frames")
	shot := flag.String("screenshot", "", "capture last frame")
	flag.Parse()
	opts := []glyph.Option{glyph.WithTitle("Directional shadow coverage"), glyph.WithWindowSize(800, 600), glyph.WithMaxFrames(*frames), glyph.WithScreenshot(*shot)}
	planet := *planetFlag || *cascades > renderer.ShadowCascades
	if *novsync {
		// With vsync the card idles most of each frame and the pass timings
		// follow its clock state: paced runs of the three-cascade arm read
		// 1.54, 0.51 and 1.56 ms (2026-10-17). Unpaced, the clocks hold.
		opts = append(opts, glyph.WithVSync(false))
	}
	if *cascades > renderer.ShadowCascades {
		opts = append(opts, glyph.WithShadowCascades(*cascades))
	}
	if planet {
		// The planet arm looks a few kilometres out; the default far plane is
		// 500 units.
		opts = append(opts, glyph.WithProjection(45, 0.5, 20000))
	}
	e, err := glyph.New(&game{coverage: *coverage, caster: *caster, receivers: *receivers, cascades: max(*cascades, renderer.ShadowCascades), planet: planet, pan: *pan, probe: *probe}, opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer e.Destroy()
	e.Run()
	timing := e.MeanGPUTimings()
	log.Printf("GPU mean after warmup: valid=%v cascades=%d shadow=%.5fms total=%.5fms shadowmaps=%d bytes",
		timing.Valid, e.Renderer().ShadowCascadeCount(), timing.Pass[renderer.PassShadow], timing.Total, e.Renderer().ResourceCounts().ShadowMapBytes)
}
