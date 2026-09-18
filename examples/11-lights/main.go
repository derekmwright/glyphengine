// Command 11-lights shows the two kinds of point light and the shadows one of
// them casts.
//
// The engine has a deliberate asymmetry:
//
//   - Scene.SetPointLight is the single shadow-casting light. It renders the
//     scene six more times into a cube map, so there is exactly one, and it is
//     the one you spend on the lantern the player is carrying.
//   - Scene.SetPointLights are unshadowed fill lights, up to
//     renderer.MaxPointLights of them. They cost a loop in the fragment
//     shader and nothing else, so they are what you scatter around a level.
//
// Telling them apart is the point of this example: watch the orbiting white
// light throw pillar shadows across the floor while the coloured lights pool
// on the walls without casting anything.
//
// The scene is set at night with no cycle, so the lights are the only thing
// lighting it — see the Environment below.
//
//	go run ./11-lights              # windowed
//	go run ./11-lights -frames 200  # render 200 frames, then exit
//	go run ./11-lights -static      # stop the lights moving
//	go run ./11-lights -count 200   # raise the fill lights past the old 32-light ceiling
//	go run ./11-lights -spots 6     # add downward-aimed warm spotlights over the ground
//	go run ./11-lights -spots 6 -spothardedge  # same, with a crisp cone edge instead of a soft one
//	go run ./11-lights -lamps 400   # a floor of small static lamps instead of the orbiting ring
//	go run ./11-lights -camdist 0.8 -campitch 0.6 -camtarget 0.05 -camlook 0   # down among them
//	go run ./11-lights -lightdebug heatmap -lightstats   # what the froxel grid holds
//
// The camera orbits on its own. Escape quits.
package main

import (
	"flag"
	"log"
	"math"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

const (
	pillarCount  = 7
	pillarRadius = 6.0
	lanternRange = 22.0
)

// autoOrbitRate circles the camera slowly, in radians per second.
//
// No mouse look: this example is about how the two light types differ, and camera
// plumbing in front of that is just noise to read past. Orbiting on its own
// also makes -frames screenshots repeatable.
const autoOrbitRate = 0.15

type game struct {
	camera  *glyph.Camera
	lantern glyph.Entity
	fills   []glyph.Entity
	static  bool

	// pointCount overrides the number of unshadowed fill lights (-count); 0
	// keeps the built-in four so the default output is unchanged.
	pointCount int
	// spotCount adds this many downward/outward-aimed warm spotlights over
	// the ground (-spots); 0 adds none.
	spotCount int
	// spotHardEdge sets the spotlights' Inner equal to Outer (-spothardedge),
	// for looking at the crisp-edged cone that produces instead of the
	// default soft one. Has no effect when spotCount is 0.
	spotHardEdge bool

	// lamps replaces the orbiting ring with a static grid of short-range
	// lamps over the floor (-lamps). Built once in Init: they never move, so
	// rebuilding the slice every frame would be an allocation per frame for
	// nothing, and the scene is deterministic without depending on the clock.
	//
	// This layout is what a clustered renderer is for, and the ring is not:
	// every one of the ring's lights reaches most of the scene, so every
	// froxel lists all of them and clustering has nothing to remove. Hundreds
	// of range-4 lamps a metre and a half apart is the case where a cell holds
	// a handful and the rest of the grid never hears about them.
	lamps []glyph.PointLight
	// lampCount is -lamps: 0 keeps the ring, so the default output is
	// unchanged.
	lampCount int

	// Camera pose (-camdist, -campitch, -camtarget, -camlook). The defaults
	// are the pose this example has always used; they are flags so a capture
	// can be taken from down among the lamps, where the froxel grid's first
	// slice is doing all the work, without a second example to maintain.
	camDist, camPitch, camTarget, camLook float32

	t float32
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// Night, with no sky and no cycle: the only light in this scene is the
	// light this example places. A day/night cycle would drown it in sun.
	e.Scene.Env = &glyph.Environment{
		Ambient:    &glyph.AmbientLight{Color: [3]float32{0.035, 0.038, 0.055}},
		ClearColor: [3]float32{0.02, 0.02, 0.035},
	}

	floor, err := r.CreatePlane(46, 46)
	if err != nil {
		return err
	}
	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: floor, Roughness: 0.9})
	e.C.Color.Set(ent, &glyph.Color{R: 0.42, G: 0.42, B: 0.46})
	e.C.Static.Set(ent, &glyph.Static{})
	// A flat floor casts nothing; it only receives.
	e.C.NoCastShadow.Set(ent, &glyph.NoCastShadow{})

	// Pillars in a ring. These are what the shadow-casting light throws
	// shadows from, and a point light's shadows fan outward from it, which is
	// the giveaway that it is not the sun.
	pillar, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	for i := 0; i < pillarCount; i++ {
		a := float64(i) / pillarCount * 2 * math.Pi
		p := e.Spawn()
		e.C.Transform.Set(p, &glyph.Transform{
			Position: mgl32.Vec3{float32(math.Cos(a)) * pillarRadius, 2.5, float32(math.Sin(a)) * pillarRadius},
			Scale:    mgl32.Vec3{0.9, 5, 0.9},
		})
		e.C.MeshRef.Set(p, &glyph.MeshRef{Mesh: pillar, Roughness: 0.8})
		e.C.Color.Set(p, &glyph.Color{R: 0.62, G: 0.60, B: 0.57})
		e.C.Static.Set(p, &glyph.Static{})
	}

	// A small emissive marker for each light, so it is obvious where the light
	// is coming from. Emissive bypasses lighting entirely — the marker is not
	// lit by the light it represents.
	bulb, err := r.CreateCube(0.35)
	if err != nil {
		return err
	}
	g.lantern = e.Spawn()
	e.C.Transform.Set(g.lantern, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(g.lantern, &glyph.MeshRef{Mesh: bulb})
	e.C.Color.Set(g.lantern, &glyph.Color{R: 1, G: 0.96, B: 0.88})
	e.C.Emissive.Set(g.lantern, &glyph.Emissive{})

	// A floor of lamps (-lamps) replaces the ring outright, markers included:
	// at this spacing a marker cube sits within arm's reach of the near camera
	// pose and would fill the frame with emissive geometry, which is not what
	// is being looked at.
	if g.lampCount > 0 {
		g.lamps = buildLamps(g.lampCount)
		g.finishInit()
		return nil
	}

	// One marker per fill light: the built-in four by default, or -count of
	// them once that flag raises the fill-light total past the old 32-light
	// ceiling (see renderer.MaxLights). fillCount is also what Update uses to
	// build the light list itself, so the two never disagree about how many
	// there are.
	for i := 0; i < g.fillCount(); i++ {
		c := fillColor(i, g.fillCount())
		f := e.Spawn()
		e.C.Transform.Set(f, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
		e.C.MeshRef.Set(f, &glyph.MeshRef{Mesh: bulb})
		// The marker has to be given a Color here for Update to have one to
		// modify; Store.Get on an entity without the component returns not-ok
		// and the write goes nowhere.
		e.C.Color.Set(f, &glyph.Color{R: c[0], G: c[1], B: c[2]})
		e.C.Emissive.Set(f, &glyph.Emissive{})
		g.fills = append(g.fills, f)
	}

	g.finishInit()
	return nil
}

// finishInit places the camera. NewCamera rather than assignment, because the
// distance it is constructed with is also the collision-clamped distance the
// view is actually built from, and setting only the field leaves the two
// disagreeing.
func (g *game) finishInit() {
	g.camera = glyph.NewCamera(g.camDist)
	g.camera.Pitch = g.camPitch
	g.camera.LookOffset = g.camLook
	g.camera.Target = mgl32.Vec3{0, g.camTarget, 0}

	log.Println("11-lights running. One shadow-casting light, several fill lights. Escape quits.")
}

// lampSpacing, lampY and lampRange lay out the -lamps floor: lamps far enough
// apart that no froxel is asked to hold an unreasonable number of them, low
// enough that a camera among them is inside several at once, and short enough
// ranged that most of the grid lists none of them.
// lampIntensity is dim on purpose. A lamp every 1.6 m with a range of 4 puts
// about twenty of them on any point of the floor, and at full intensity the
// sum clips to white over most of the frame -- which is the worst thing a
// scene used to compare two renderers can do, because a clipped pixel is the
// same white whether or not it was given the right lights.
const (
	lampSpacing   = 1.6
	lampY         = 0.55
	lampRange     = 4.0
	lampIntensity = 0.15
)

// buildLamps returns n lamps on a square grid centred on the origin, sized so
// the grid stays on the 46x46 floor.
func buildLamps(n int) []glyph.PointLight {
	side := 1
	for side*side < n {
		side++
	}
	lamps := make([]glyph.PointLight, 0, n)
	for i := 0; i < n; i++ {
		q := float32(i%side - side/2)
		r := float32(i/side - side/2)
		c := hueColor(float64(i) / float64(n))
		lamps = append(lamps, glyph.PointLight{
			Pos:   mgl32.Vec3{lampSpacing * q, lampY, lampSpacing * r},
			Range: lampRange,
			Color: mgl32.Vec3{c[0], c[1], c[2]}.Mul(lampIntensity),
		})
	}
	return lamps
}

// fillColors are the unshadowed lights: saturated, so it is easy to see which
// pool of light came from which.
var fillColors = [][3]float32{
	{0.95, 0.25, 0.30},
	{0.25, 0.55, 1.00},
	{0.35, 0.95, 0.45},
	{0.95, 0.75, 0.20},
}

// fillCount is how many fill lights this run has: the built-in four unless
// -count raised it. Both Init (markers) and Update (the light list itself)
// call this so they never disagree about how many there are.
func (g *game) fillCount() int {
	if g.pointCount > 0 {
		return g.pointCount
	}
	return len(fillColors)
}

// fillColor returns the i-th of n fill-light colours: fillColors verbatim
// when n matches its length (the default, unmodified by -count), or a colour
// cycled around the hue wheel when -count has raised the total past it --
// fillColors has only four entries, and repeating them would make lights
// past the fourth impossible to tell apart in a -count 200 capture.
func fillColor(i, n int) [3]float32 {
	if n == len(fillColors) {
		return fillColors[i]
	}
	return hueColor(float64(i) / float64(n))
}

// hueColor converts a hue in [0,1) to a fully saturated, full value RGB
// colour. Standard HSV-to-RGB with S=V=1, used only to spread -count lights
// evenly around the colour wheel so a large count stays visually distinct.
func hueColor(hue float64) [3]float32 {
	hue -= math.Floor(hue)
	h6 := hue * 6
	x := 1 - math.Abs(math.Mod(h6, 2)-1)
	var r, g, b float64
	switch int(h6) {
	case 0:
		r, g, b = 1, x, 0
	case 1:
		r, g, b = x, 1, 0
	case 2:
		r, g, b = 0, 1, x
	case 3:
		r, g, b = 0, x, 1
	case 4:
		r, g, b = x, 0, 1
	default:
		r, g, b = 1, 0, x
	}
	return [3]float32{float32(r), float32(g), float32(b)}
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	if !g.static {
		g.t += dt
	}
	g.camera.Yaw += autoOrbitRate * dt
	e.SetCamera(g.camera.ViewVectors())

	// ── the shadow-casting light ──
	// One only. Every frame it costs six more renders of the scene into a cube
	// map, which is why the engine offers exactly one and makes you choose.
	lx := float32(math.Cos(float64(g.t)*0.55)) * 3.4
	lz := float32(math.Sin(float64(g.t)*0.55)) * 3.4
	ly := 3.2 + float32(math.Sin(float64(g.t)*0.9))*0.8
	e.Scene.SetPointLight(
		mgl32.Vec3{lx, ly, lz},
		mgl32.Vec3{1.0, 0.94, 0.82},
		lanternRange,
	)
	if t, ok := e.C.Transform.Get(g.lantern); ok {
		t.Position = mgl32.Vec3{lx, ly, lz}
	}

	// ── the unshadowed fill lights ──
	// These pass through the whole scene without occlusion, so they light the
	// far side of a pillar as happily as the near side. That is the trade:
	// many of them, none of them casting.
	if len(g.lamps) > 0 {
		e.Scene.SetPointLights(g.lamps)
		g.updateSpots(e)
		return
	}

	n := g.fillCount()
	lights := make([]glyph.PointLight, 0, n)
	for i := 0; i < n; i++ {
		c := fillColor(i, n)
		a := float64(i)/float64(n)*2*math.Pi - float64(g.t)*0.3
		pos := mgl32.Vec3{
			float32(math.Cos(a)) * 9.5,
			1.6 + float32(math.Sin(float64(g.t)*1.3+float64(i)))*0.5,
			float32(math.Sin(a)) * 9.5,
		}
		lights = append(lights, glyph.PointLight{
			Pos:   pos,
			Range: 11,
			Color: mgl32.Vec3{c[0], c[1], c[2]},
		})
		if t, ok := e.C.Transform.Get(g.fills[i]); ok {
			t.Position = pos
		}
		if col, ok := e.C.Color.Get(g.fills[i]); ok {
			col.R, col.G, col.B = c[0], c[1], c[2]
		}
	}
	e.Scene.SetPointLights(lights)

	g.updateSpots(e)
}

// updateSpots places the -spots lights. Its own method because both light
// layouts -- the orbiting ring and the -lamps floor -- end with it, and a
// second copy is a second thing to keep in step.
func (g *game) updateSpots(e *glyph.Engine) {
	// ── the unshadowed spot lights (-spots) ──
	// Posts around the same ring the pillars stand on, aimed down and
	// outward so the cone lands on the ground ahead of its post instead of
	// directly beneath it -- straight down would put the whole pool under
	// geometry the camera never sees from outside the ring.
	if g.spotCount > 0 {
		// -spothardedge sets Inner to Outer's angle for looking at the
		// crisp cone that produces, instead of the default soft-edged one.
		outer := mgl32.DegToRad(32)
		inner := mgl32.DegToRad(18)
		if g.spotHardEdge {
			inner = outer
		}
		spots := make([]glyph.SpotLight, 0, g.spotCount)
		for i := 0; i < g.spotCount; i++ {
			a := float64(i) / float64(g.spotCount) * 2 * math.Pi
			outward := mgl32.Vec3{float32(math.Cos(a)), 0, float32(math.Sin(a))}
			pos := outward.Mul(14).Add(mgl32.Vec3{0, 6.5, 0})
			dir := mgl32.Vec3{0, -1, 0}.Add(outward.Mul(0.5))
			spots = append(spots, glyph.SpotLight{
				Pos:   pos,
				Dir:   dir,
				Range: 16,
				Color: mgl32.Vec3{1.0, 0.75, 0.4}, // warm, like a lamp
				Inner: inner,
				Outer: outer,
			})
		}
		e.Scene.SetSpotLights(spots)
	}
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	static := flag.Bool("static", false, "stop the lights moving")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	count := flag.Int("count", 0, "override the number of unshadowed fill lights (0 = the built-in four; raise past renderer.MaxPointLights to exercise the clustered path)")
	spots := flag.Int("spots", 0, "add N downward/outward-aimed warm spotlights over the ground")
	spotHardEdge := flag.Bool("spothardedge", false, "give the -spots cones a hard edge (Inner == Outer) instead of the default soft one")
	lightDebug := flag.String("lightdebug", "", "light debug mode: heatmap or bruteforce (default: off)")
	lightStats := flag.Bool("lightstats", false, "log the light binner's stats for the last frame on exit")
	lamps := flag.Int("lamps", 0, "replace the orbiting fill lights with N short-range lamps on a grid over the floor")
	// The defaults are the pose this example has always used.
	camDist := flag.Float64("camdist", 17, "camera orbit distance")
	camPitch := flag.Float64("campitch", 0.42, "camera pitch in radians")
	camTarget := flag.Float64("camtarget", 2, "height of the point the camera orbits")
	camLook := flag.Float64("camlook", 0.75, "how far above the target the camera looks")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 11 Lights"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		glyph.WithQuitKey(input.KeyEscape),
		glyph.WithDebugKeys(),
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

	g := &game{
		static:       *static,
		pointCount:   *count,
		spotCount:    *spots,
		spotHardEdge: *spotHardEdge,
		lampCount:    *lamps,
		camDist:      float32(*camDist),
		camPitch:     float32(*camPitch),
		camTarget:    float32(*camTarget),
		camLook:      float32(*camLook),
	}
	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	switch *lightDebug {
	case "heatmap":
		e.SetLightDebugMode(glyph.LightDebugHeatmap)
	case "bruteforce":
		e.SetLightDebugMode(glyph.LightDebugBruteForce)
	case "":
		// default: off
	default:
		log.Fatalf("unknown -lightdebug %q, want heatmap or bruteforce", *lightDebug)
	}

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
	if *lightStats {
		// The last frame's binning, which is the frame a -screenshot captured.
		log.Printf("light stats: %+v", e.LightStats())
	}
}
