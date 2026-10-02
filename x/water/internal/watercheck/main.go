// Command watercheck is x/water's gate.
//
// It builds the smallest scene that can say anything about the package: a
// heightmap bowl, the *engine's* water surface filling it, and one broad flat
// wall standing on the bed at a fixed distance from a camera whose position is
// a flag rather than a walk. That last part is why this is not a flag on
// 09-water: the whole measurement is "the same wall, the same distance, from
// three different depths", and no amount of pitch and yaw gets a player to an
// exact depth repeatably.
//
// Nothing here is lit by shadows and nothing moves, so the wall reads the same
// at every depth with the package off. That is the control: it is what makes
// "the water darkened it" distinguishable from "the wall was lit differently".
//
//	-water on -depth 10 -screenshot on10.png     the package, 10 units under
//	-water off -depth 10 -screenshot off10.png   the same frame without it
//	-water on -disabled -depth 10 ...            created, never enabled
//	-measure a.png,b.png                         mean RGB of the wall box in each
//	-depths                                      run the whole depth comparison
//	-balance                                     create/destroy cycles, pass count
//	-allocs                                      bytes allocated per frame by Update
//	-sweep                                       descend through the surface mid-run
//
// Every rendering mode needs GLYPHENGINE_FIXED_FRAME_TIME; without it two runs
// of the same build differ by more than the package does (AGENTS.md rule 13),
// and -depths refuses to run without it rather than reporting a coin toss.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/x/water"
)

func init() { runtime.LockOSThread() }

const (
	gridSize  = 81
	worldSize = 200.0
	// The surface sits at zero so a depth flag reads as a depth.
	waterLevel = 0.0
	// Deep enough that the deepest camera the gate uses still has bed below it
	// *at the wall's distance*, not only directly underneath -- the bowl slopes,
	// so the usable depth is set by the bed twenty units ahead rather than by
	// the centre. The rim is dry land, so CreateWaterMesh has a shoreline.
	bedDepth = 120.0
	// The wall: a broad flat face square to the camera, at a fixed distance, so
	// every measured pixel has the same length of water in front of it.
	wallDistance = 20.0
	// Sized to fill a little under half the frame at that distance, so the bed,
	// the surface overhead and the open water around it are all still in the
	// picture. A wall that covered the frame would measure just as well and
	// would show nothing, and a gate nobody can look at is how a wrong image
	// stays wrong.
	wallWidth  = 18.0
	wallHeight = 11.0
)

// measureBox is the wall region every number in this gate comes from, in pixels
// of the 640x360 window: inside the wall's silhouette by a wide margin, and
// below the height at which the surface meets the wall at the shallowest depth
// the gate uses, so the waterline never enters it.
var measureBox = image.Rect(255, 150, 385, 235)

type game struct {
	depth    float64
	mode     string // "on", "off"
	disabled bool
	balance  bool
	allocs   bool
	noShafts bool
	sweep    bool
	frame    int

	w            *water.Water
	baseline     int
	haveBaseline bool
	failures     []string
}

func (g *game) Init(e *glyph.Engine) error {
	heights := make([]float32, gridSize*gridSize)
	for z := 0; z < gridSize; z++ {
		for x := 0; x < gridSize; x++ {
			// A cone bowl: deepest in the middle, crossing the waterline well
			// inside the rim so the basin has a real shore. Analytic rather than
			// noise, because a gate should not also be testing a generator.
			fx := (float64(x)/float64(gridSize-1) - 0.5) * 2
			fz := (float64(z)/float64(gridSize-1) - 0.5) * 2
			r := math.Hypot(fx, fz)
			heights[z*gridSize+x] = float32(bedDepth * (r*1.45 - 1))
		}
	}
	hm, err := glyph.NewHeightmap(gridSize, gridSize, worldSize, worldSize,
		-worldSize/2, -worldSize/2, heights)
	if err != nil {
		return err
	}
	e.SetTerrain(hm)
	// A flat tint rather than a height ramp: the bed is a backdrop here, and a
	// colour that changes with depth would put a second depth signal into the
	// measurement this gate is trying to attribute to the water.
	mesh, err := e.CreateTerrainMesh(hm, &glyph.TerrainOptions{
		Tint: func(float32, [3]float32) [3]float32 { return [3]float32{0.42, 0.38, 0.30} },
	})
	if err != nil {
		return err
	}
	terrain := e.Spawn()
	e.C.Transform.Set(terrain, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(terrain, &glyph.MeshRef{Mesh: mesh, Roughness: 0.95})
	e.C.Static.Set(terrain, &glyph.Static{})

	opts := glyph.DefaultWaterOptions(waterLevel)
	opts.Resolution = 160
	surface, err := e.CreateWaterMesh(hm, opts)
	if err != nil {
		return err
	}
	w := e.Spawn()
	e.C.Transform.Set(w, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(w, &glyph.MeshRef{Mesh: surface})
	e.C.Water.Set(w, &glyph.Water{Options: opts})
	e.C.Static.Set(w, &glyph.Static{})

	cube, err := e.Renderer().CreateCube(1.0)
	if err != nil {
		return err
	}
	wall := e.Spawn()
	// Centre the wall on the camera's own depth band rather than on the bed, so
	// the measured box lands on the same part of the face whether the camera is
	// 2 units down or 40.
	e.C.Transform.Set(wall, &glyph.Transform{
		Position: mgl32.Vec3{0, float32(waterLevel - g.depth), wallDistance + 0.5},
		Scale:    mgl32.Vec3{wallWidth, wallHeight, 1},
	})
	e.C.MeshRef.Set(wall, &glyph.MeshRef{Mesh: cube, Roughness: 0.9})
	// Mid grey: bright enough that absorbing it is a large signal, dark enough
	// that nothing clips and hides the difference.
	e.C.Color.Set(wall, &glyph.Color{R: 0.55, G: 0.55, B: 0.55})
	e.C.Static.Set(wall, &glyph.Static{})

	// A fixed sun well up the sky, and no day cycle: the look must not depend on
	// when the gate ran.
	e.Scene.Env = &glyph.Environment{
		Sun: &glyph.DirectionalLight{
			Direction: mgl32.Vec3{0.25, 0.9, 0.35}.Normalize(),
			Color:     [3]float32{1.0, 0.96, 0.9},
		},
		Ambient:    &glyph.AmbientLight{Color: [3]float32{0.22, 0.24, 0.28}},
		ClearColor: [3]float32{0.05, 0.08, 0.12},
	}

	if g.mode == "on" && !g.balance {
		opts := water.DefaultOptions(waterLevel)
		if g.noShafts {
			// Zero the scattering tint and nothing else. What is left is the
			// absorption and the water's own radiance, which is the only term
			// BodyDepthFalloff reaches -- so a sweep in this mode measures that
			// one number instead of measuring it mixed with the shafts.
			opts.ScatterColor = [3]float32{}
		}
		if g.w, err = water.New(e.Renderer(), opts); err != nil {
			return err
		}
		g.w.SetEnabled(!g.disabled)
	}
	return nil
}

// eye is the camera. With -sweep it descends through the surface during the
// run, which is the case the depth stations cannot reach: the passes switch on
// and off mid-run, and that transition -- not either steady state -- is where a
// descriptor or a barrier would be wrong. It is what `task validate` runs.
func (g *game) eye() mgl32.Vec3 {
	depth := g.depth
	if g.sweep {
		depth = -8 + float64(g.frame)*0.4
	}
	return mgl32.Vec3{0, float32(waterLevel - depth), 0}
}

func (g *game) Update(e *glyph.Engine, _ float32) {
	g.frame = e.FrameCount()
	eye := g.eye()
	e.SetCamera(eye, eye.Add(mgl32.Vec3{0, 0, 1}), mgl32.Vec3{0, 1, 0})
	if g.balance {
		g.balanceStep(e)
	}
}

func (g *game) LateUpdate(e *glyph.Engine, _ float32) {
	if g.w == nil {
		return
	}
	st := e.Scene.Environment()
	if err := g.w.Update(e.ViewProjection().Inv(), g.eye(), st.SunDir, st.SunColor); err != nil {
		log.Fatalf("water update: %v", err)
	}
	if g.allocs && e.FrameCount() == 60 {
		g.measureAllocs(e, st)
	}
}

func (g *game) FixedUpdate(*glyph.Engine, float32) {}

// measureAllocs is the half TestUpdateAllocatesNothing cannot see: Update with
// real passes behind it, counted against the Go heap rather than against a
// mocked packer.
func (g *game) measureAllocs(e *glyph.Engine, st glyph.EnvironmentState) {
	var before, after runtime.MemStats
	inv := e.ViewProjection().Inv()
	// Warm up first: the first call through a code path can allocate for reasons
	// that have nothing to do with the steady state.
	for i := 0; i < 50; i++ {
		_ = g.w.Update(inv, g.eye(), st.SunDir, st.SunColor)
	}
	runtime.GC()
	runtime.ReadMemStats(&before)
	const n = 2000
	for i := 0; i < n; i++ {
		_ = g.w.Update(inv, g.eye(), st.SunDir, st.SunColor)
	}
	runtime.ReadMemStats(&after)
	bytes := after.TotalAlloc - before.TotalAlloc
	mallocs := after.Mallocs - before.Mallocs
	fmt.Printf("allocs: %d calls, %d bytes, %d objects\n", n, bytes, mallocs)
	if mallocs != 0 {
		g.failures = append(g.failures,
			fmt.Sprintf("Update allocates: %d objects / %d bytes over %d calls, want 0", mallocs, bytes, n))
	}
	e.Close()
}

// balanceStep creates and destroys the water three times over, checking that
// the renderer's own application-pass roster returns to where it started. It
// counts the renderer's rows rather than the package's fields, so a pass this
// package forgot to destroy is visible even though the Go object was dropped.
func (g *game) balanceStep(e *glyph.Engine) {
	const period = 40
	f := e.FrameCount()
	if f < 20 {
		return
	}
	live := len(e.Renderer().Stats().App.Passes)
	switch (f - 20) % period {
	case 0:
		if !g.haveBaseline {
			// Zero is the normal baseline here, so this is a flag rather than a
			// sentinel value. It was a sentinel first, and the balance silently
			// re-based itself every cycle.
			g.baseline, g.haveBaseline = live, true
			fmt.Printf("balance: baseline %d application passes\n", live)
		}
		var err error
		if g.w, err = water.New(e.Renderer(), water.DefaultOptions(waterLevel)); err != nil {
			log.Fatalf("water create: %v", err)
		}
		g.w.SetEnabled(true)
	case 10:
		if live != g.baseline+3 {
			g.failures = append(g.failures,
				fmt.Sprintf("after create: %d passes, want baseline %d + 3", live, g.baseline))
		} else {
			fmt.Printf("balance: after create %d passes\n", live)
		}
	case 20:
		if !g.disabled {
			g.w.Destroy(e.Renderer())
			g.w.Destroy(e.Renderer()) // idempotent, and a second call must not panic
		}
		g.w = nil
	case 30:
		if live != g.baseline {
			g.failures = append(g.failures,
				fmt.Sprintf("after destroy: %d passes, want the baseline %d", live, g.baseline))
		} else {
			fmt.Printf("balance: cycle %d returned to %d passes\n", (f-20)/period, live)
		}
		// -disabled is this check's meta-check: it skips the Destroy above, so
		// the count must not come back. One cycle only, because a second create
		// would collide with the first's pass names and fail for a different
		// reason -- which is what the first version of this did, and it reported
		// a duplicate-name error rather than the leak it was supposed to find.
		if g.disabled || (f-20)/period >= 2 {
			e.Close()
		}
	}
}

func main() {
	mode := flag.String("water", "off", "x/water: on or off")
	depth := flag.Float64("depth", 10, "camera depth below the surface in world units; negative is above it")
	disabled := flag.Bool("disabled", false, "with -water on: create the package and never enable it (with -balance: never destroy it, as the balance meta-check)")
	frames := flag.Int("frames", 120, "render N frames then exit")
	shot := flag.String("screenshot", "", "write the last frame to this PNG")
	measure := flag.String("measure", "", "comma-separated PNGs: print the mean RGB of the wall box in each and exit")
	depths := flag.Bool("depths", false, "run the whole depth comparison and report pass or fail")
	balance := flag.Bool("balance", false, "create/destroy cycles, checking the renderer's pass roster returns to its baseline")
	allocs := flag.Bool("allocs", false, "measure Go allocations per Update with real passes behind it")
	sweep := flag.Bool("sweep", false, "descend through the surface during the run, so the passes switch on and off mid-run")
	noShafts := flag.Bool("noshafts", false, "zero ScatterColor, leaving only absorption and the water's own radiance")
	out := flag.String("out", ".task/xwater", "directory -depths writes its captures to")
	flag.Parse()

	if *measure != "" {
		if err := printMeasurements(strings.Split(*measure, ",")); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *depths {
		if err := runDepths(*out); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *mode != "on" && *mode != "off" {
		log.Fatal("-water must be on or off")
	}

	g := &game{depth: *depth, mode: *mode, disabled: *disabled, balance: *balance, allocs: *allocs, noShafts: *noShafts, sweep: *sweep}
	if *balance {
		g.mode = "on"
	}
	e, err := glyph.New(g,
		glyph.WithTitle("x/water check"), glyph.WithWindowSize(640, 360),
		glyph.WithProjection(55, 0.1, 400),
		// MSAA off. The present pass replaces the HDR scene, and with MSAA that
		// means loading the multisample colour and resolving it, so every frame
		// the package is active would differ from one where it is not for a
		// reason that has nothing to do with water.
		glyph.WithMSAA(1),
		glyph.WithMaxFrames(*frames), glyph.WithScreenshot(*shot))
	if err != nil {
		log.Fatal(err)
	}
	defer e.Destroy()
	e.Run()
	if len(g.failures) > 0 {
		for _, f := range g.failures {
			fmt.Printf("FAIL %s\n", f)
		}
		os.Exit(1)
	}
	fmt.Printf("rendered %d frames\n", e.FrameCount())
}

// ── measurement ──

type rgb struct{ r, g, b float64 }

func (c rgb) String() string { return fmt.Sprintf("%.3f/%.3f/%.3f", c.r, c.g, c.b) }

func meanBox(path string) (rgb, error) {
	f, err := os.Open(path)
	if err != nil {
		return rgb{}, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return rgb{}, fmt.Errorf("%s: %w", path, err)
	}
	box := measureBox.Intersect(img.Bounds())
	if box.Empty() {
		return rgb{}, fmt.Errorf("%s: the measure box falls outside the image %v", path, img.Bounds())
	}
	var sum rgb
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			sum.r += float64(r >> 8)
			sum.g += float64(g >> 8)
			sum.b += float64(b >> 8)
		}
	}
	n := float64(box.Dx() * box.Dy())
	return rgb{sum.r / n, sum.g / n, sum.b / n}, nil
}

func printMeasurements(paths []string) error {
	for _, p := range paths {
		m, err := meanBox(strings.TrimSpace(p))
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", p, m)
	}
	return nil
}

// ── the depth comparison ──

// runDepths renders the pairs itself rather than leaving them to a shell script,
// so the camera positions, the window size and the measure box cannot drift
// apart from the numbers checked against them.
//
// Three depths, each captured with and without the package. The assertions:
//
//  1. the three OFF captures agree. This is the control. The wall is the same
//     wall, flat-lit, at the same distance, so if these three disagree the
//     comparisons below are measuring the scene and not the water.
//  2. every channel is DARKER with the package at every depth. Twenty units of
//     water absorbs more of a mid-grey wall than the water's own radiance and
//     its shafts put back.
//  3. red is darkened more than blue at every depth. That is the colour shift,
//     and it is the whole reason the three absorption coefficients are so far
//     apart.
//  4. the darkening deepens monotonically with depth. There is less daylight
//     reaching the water the camera stands in, so there is less of the water's
//     own radiance and fewer shafts to put back.
//  5. with the shafts removed, that is still true. Checks 1 to 4 pass with
//     BodyDepthFalloff set to zero, because the shafts carry a depth dependence
//     of their own; this is the one that does not.
func runDepths(dir string) error {
	if os.Getenv("GLYPHENGINE_FIXED_FRAME_TIME") == "" {
		return fmt.Errorf("GLYPHENGINE_FIXED_FRAME_TIME is unset: two runs of the same build differ by more than this package does, so these numbers would be a coin toss (AGENTS.md rule 13)")
	}
	// Start from an empty directory. The two byte-identity checks below compare
	// against captures made earlier in this same run, and a leftover file from a
	// previous run with a different scene makes them compare two different
	// worlds -- which is exactly what happened the first time the depths moved.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Spread wide on purpose. The output is eight bits, and the body term's
	// depth falloff moves it by about one count per ten units of depth, so
	// depths ten units apart would be measuring quantisation.
	depths := []float64{4, 20, 60}
	type sample struct {
		on, off rgb
	}
	out := make([]sample, len(depths))
	for i, d := range depths {
		onPath := fmt.Sprintf("%s/on%02.0f.png", dir, d)
		offPath := fmt.Sprintf("%s/off%02.0f.png", dir, d)
		if err := render(d, "on", false, false, onPath); err != nil {
			return err
		}
		if err := render(d, "off", false, false, offPath); err != nil {
			return err
		}
		var err error
		if out[i].on, err = meanBox(onPath); err != nil {
			return err
		}
		if out[i].off, err = meanBox(offPath); err != nil {
			return err
		}
		fmt.Printf("depth %4.0f  off %s  on %s\n", d, out[i].off, out[i].on)
	}

	var problems []string
	// 1. the control.
	for i := 1; i < len(out); i++ {
		for _, c := range []struct {
			name string
			a, b float64
		}{{"red", out[0].off.r, out[i].off.r}, {"green", out[0].off.g, out[i].off.g}, {"blue", out[0].off.b, out[i].off.b}} {
			if math.Abs(c.a-c.b) > 2.0 {
				problems = append(problems, fmt.Sprintf(
					"control: the no-package wall %s differs between depth %.0f and depth %.0f (%.3f vs %.3f); the depth comparison below would be measuring the scene",
					c.name, depths[0], depths[i], c.a, c.b))
			}
		}
	}
	// 2 and 3.
	gains := make([]rgb, len(out))
	for i := range out {
		gains[i] = rgb{out[i].on.r - out[i].off.r, out[i].on.g - out[i].off.g, out[i].on.b - out[i].off.b}
		fmt.Printf("depth %4.0f  change %s\n", depths[i], gains[i])
		// An absolute floor as well as a sign: a darkening of 0.4/255 is a
		// rounding difference, not water. 20 units at the default absorption
		// takes red down by two thirds, so 8/255 leaves a wide margin.
		if gains[i].r > -8 {
			problems = append(problems, fmt.Sprintf("depth %.0f: red changed by %.3f, want at least 8 darker", depths[i], gains[i].r))
		}
		if gains[i].g > -2 || gains[i].b > -2 {
			problems = append(problems, fmt.Sprintf("depth %.0f: green %.3f blue %.3f, want both darker", depths[i], gains[i].g, gains[i].b))
		}
		if gains[i].r >= gains[i].b {
			problems = append(problems, fmt.Sprintf("depth %.0f: red fell %.3f and blue fell %.3f; water absorbs red first, so red must fall further", depths[i], gains[i].r, gains[i].b))
		}
	}
	// 4.
	for i := 1; i < len(gains); i++ {
		if gains[i].b >= gains[i-1].b {
			problems = append(problems, fmt.Sprintf(
				"blue change at depth %.0f (%.3f) is not deeper than at depth %.0f (%.3f); the water is supposed to darken with depth",
				depths[i], gains[i].b, depths[i-1], gains[i-1].b))
		}
	}

	// 5. The depth falloff, on its own.
	//
	// The sweep above is monotonic even with BodyDepthFalloff set to zero,
	// because the shafts dim with depth too -- measured 2026-10-02 on the
	// earlier 4/14/34 sweep: zeroing BodyDepthFalloff shrank the blue spread
	// across the three depths from 2.481 to 1.110 and left the ordering intact,
	// so checks 1 to 4 passed a dead falloff outright. This sweep is
	// the same three depths with ScatterColor zeroed, so the only thing left
	// that can vary with depth is the term BodyDepthFalloff controls, and the
	// per-step floor below is what a dead one cannot clear.
	bodyOnly := make([]rgb, len(depths))
	for i, d := range depths {
		onPath := fmt.Sprintf("%s/body%02.0f.png", dir, d)
		if err := render(d, "on", false, true, onPath); err != nil {
			return err
		}
		m, err := meanBox(onPath)
		if err != nil {
			return err
		}
		bodyOnly[i] = rgb{m.r - out[i].off.r, m.g - out[i].off.g, m.b - out[i].off.b}
		fmt.Printf("depth %4.0f  change without shafts %s\n", d, bodyOnly[i])
	}
	// Removing the shafts has to be visible, or this sweep is the one above
	// under another name and proves nothing extra.
	if bodyOnly[0].b >= gains[0].b-0.2 {
		problems = append(problems, fmt.Sprintf(
			"-noshafts changed the shallowest capture's blue by only %.3f; the shafts are not contributing, so the sweep below is not isolating anything",
			gains[0].b-bodyOnly[0].b))
	}
	for i := 1; i < len(bodyOnly); i++ {
		step := bodyOnly[i-1].b - bodyOnly[i].b
		// Measured 2026-10-02 with the falloff intact: blue darkens by 1.000
		// between depth 4 and 20 and by 2.000 between 20 and 60. With
		// BodyDepthFalloff zeroed both steps read 0.000. The floor is well below
		// the real numbers and well above a dead one; it is 0.2 rather than
		// 0.5 because the output is eight bits and a driver that rounds the
		// other way would cost half a count.
		if step < 0.2 {
			problems = append(problems, fmt.Sprintf(
				"without shafts, blue darkened by only %.3f between depth %.0f and %.0f; BodyDepthFalloff is not reaching the image",
				step, depths[i-1], depths[i]))
		}
	}

	// The two byte-for-byte halves.
	above := fmt.Sprintf("%s/above-on.png", dir)
	aboveOff := fmt.Sprintf("%s/above-off.png", dir)
	if err := render(-6, "on", false, false, above); err != nil {
		return err
	}
	if err := render(-6, "off", false, false, aboveOff); err != nil {
		return err
	}
	if err := samePixels(above, aboveOff); err != nil {
		problems = append(problems, "above the surface the package changed the frame: "+err.Error())
	} else {
		fmt.Println("above the surface: identical to the frame without the package")
	}

	reference := depths[1]
	offPath := fmt.Sprintf("%s/off%02.0f.png", dir, reference)
	inert := fmt.Sprintf("%s/disabled%02.0f.png", dir, reference)
	if err := render(reference, "on", true, false, inert); err != nil {
		return err
	}
	if err := samePixels(inert, offPath); err != nil {
		problems = append(problems, "created but never enabled, the package changed the frame: "+err.Error())
	} else {
		fmt.Println("created and left disabled: identical to the frame without the package")
	}

	// Repeatability, which is what makes every number above worth printing.
	repeat := fmt.Sprintf("%s/on%02.0f-repeat.png", dir, reference)
	if err := render(reference, "on", false, false, repeat); err != nil {
		return err
	}
	if err := samePixels(repeat, fmt.Sprintf("%s/on%02.0f.png", dir, reference)); err != nil {
		problems = append(problems, "two runs of the same capture differ: "+err.Error())
	} else {
		fmt.Println("repeat capture: identical under the fixed clock")
	}

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Printf("FAIL %s\n", p)
		}
		fmt.Printf("captures kept in %s\n", dir)
		return fmt.Errorf("%d check(s) failed", len(problems))
	}
	fmt.Printf("PASS -- captures in %s\n", dir)
	return nil
}

// render re-executes this same binary rather than shelling out to `go run`, so
// the capture is made by exactly the build that is checking it and the working
// directory the task chose is the one the captures land in.
func render(depth float64, mode string, disabled, noShafts bool, out string) error {
	args := []string{"-water", mode, "-depth", fmt.Sprintf("%g", depth),
		"-frames", "120", "-screenshot", out}
	if disabled {
		args = append(args, "-disabled")
	}
	if noShafts {
		args = append(args, "-noshafts")
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("render %s depth %g: %w", mode, depth, err)
	}
	// A gate whose captures are empty files has shipped here before.
	st, err := os.Stat(out)
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return fmt.Errorf("%s is empty; this gate would prove nothing", out)
	}
	return nil
}

// samePixels compares pixels rather than file bytes: Go's PNG encoder has
// changed its output across releases for identical images, so a byte compare is
// the wrong oracle. cmd/pngsame in the engine module makes the same point.
func samePixels(a, b string) error {
	ia, err := loadPNG(a)
	if err != nil {
		return err
	}
	ib, err := loadPNG(b)
	if err != nil {
		return err
	}
	if ia.Bounds() != ib.Bounds() {
		return fmt.Errorf("%s is %v and %s is %v", a, ia.Bounds(), b, ib.Bounds())
	}
	differing, worst := 0, 0
	for y := ia.Bounds().Min.Y; y < ia.Bounds().Max.Y; y++ {
		for x := ia.Bounds().Min.X; x < ia.Bounds().Max.X; x++ {
			r1, g1, b1, a1 := ia.At(x, y).RGBA()
			r2, g2, b2, a2 := ib.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				differing++
				for _, d := range []int{int(r1>>8) - int(r2>>8), int(g1>>8) - int(g2>>8), int(b1>>8) - int(b2>>8)} {
					if d < 0 {
						d = -d
					}
					if d > worst {
						worst = d
					}
				}
			}
		}
	}
	if differing > 0 {
		return fmt.Errorf("%d pixels differ between %s and %s, worst channel %d/255", differing, a, b, worst)
	}
	return nil
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}
