// Command quadcheck establishes what this device's FRAGMENT_SHADER_INVOCATIONS
// counter counts, from geometry whose true quad factor is known.
//
// Forward rendering shades in 2x2 quads: a triangle that covers one pixel still
// occupies a quad, and the three lanes outside it run as HELPER invocations so
// that derivatives exist. Whether the pipeline statistic counts those helpers is
// implementation-defined -- Vulkan permits either -- so a ratio of invocations
// to covered samples is uninterpretable until the device has answered. A counter
// that excludes helpers CANNOT measure quad overshading at all: it would read 1.0
// on a field of pixel-sized triangles, which is the exact number a perfect
// per-pixel shading model would read.
//
// Two arms, and the pair is the proof:
//
//   - fullscreen: one triangle covering every pixel. Every quad is fully covered,
//     so there are no helper lanes and the ratio must be 1.0 whatever the counter
//     includes. This arm says the counter is per covered sample and nothing is
//     double counted; at MSAA 4 it also says shading is per PIXEL rather than per
//     sample, because per-sample shading would read 4.0 here.
//   - pixels: a field of triangles each sized to one pixel, spaced so that no two
//     share a 2x2 quad and no triangle covers a second pixel's samples. Each one
//     occupies exactly one quad and covers exactly one sample, so the ratio is
//     4.0 if helpers are counted and 1.0 if they are not. Nothing else is close
//     to either.
//
// Covered samples come from a readback rather than from the counter, deliberately:
// deriving the denominator from the same number as the numerator would make the
// ratio a tautology. The scene has no sky dome (glyph.StaticSource, black clear)
// and bloom off, so every non-black pixel in the captured frame is geometry and
// nothing spreads to a neighbour. Each arm asserts its own covered count against
// what its geometry predicts, so a ratio is only ever reported for a frame that
// covered what it meant to.
//
//	go run ./cmd/quadcheck                     # both arms at MSAA 1 and 4, with the verdict
//	go run ./cmd/quadcheck -arm fullscreen     # one arm, as a subprocess runs it
//	task quads
//
// A ratio near neither 1.0 nor 4.0 on the pixels arm is a failure, not a result:
// it means the geometry is not one triangle per quad per pixel, and the first
// thing to look at is -size against this device's MSAA sample positions.
package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"math"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

const (
	// The requested frame. Small, and both axes even so the fullscreen arm has no
	// partially covered quad at the screen edge -- which would put helper lanes in
	// the arm whose whole job is to have none. What the swapchain actually granted
	// is read back from Renderer.Extent and used for every figure, because a
	// window manager that rounded the size would otherwise shift every triangle
	// off its pixel centre.
	frameW = 640
	frameH = 480

	// planeDist is how far in front of the camera the geometry sits, and fov the
	// vertical field of view. Only their product with the frame size matters: they
	// fix how many world units one pixel is.
	planeDist = 10.0
	fov       = 50.0
	near      = 0.1
	far       = 100.0

	// stride is the spacing of the pixel field, in pixels. 4 rather than 2 so
	// that a triangle slightly larger than its pixel still cannot reach the next
	// occupied quad: at 2 the quads would be adjacent and one misjudged size
	// would merge two of them into one quad's worth of invocations, which reads
	// as a quad factor of 2 and looks like a result.
	stride = 4

	// visible is how far from black a pixel has to be to count as covered. The
	// background is an exact zero clear through a per-pixel tonemap, so anything
	// above the darkest dither is geometry; the arms' own coverage assertions are
	// what would catch a threshold that admitted or missed a pixel.
	visible = 8
)

// armResult is one arm's measurement, printed as one line so the driver mode can
// read it back out of a subprocess.
type armResult struct {
	arm       string
	msaa      int
	prepass   string
	pixels    int
	triangles int
	invoc     uint64
	clipped   uint64
	covered   int
	ratio     float64
}

func main() {
	var (
		arm      = flag.String("arm", "", "run one arm: fullscreen or pixels. Empty runs every arm as a subprocess and prints the verdict")
		msaa     = flag.Int("msaa", 1, "samples per pixel for this arm")
		prepass  = flag.String("prepass", "off", "depth prepass for this arm: off or on")
		size     = flag.Float64("size", 0.9, "circumradius of a pixel-field triangle, in pixels. Tuned so one triangle covers exactly one pixel's samples and no neighbour's")
		frames   = flag.Int("frames", 30, "frames to render before reading the counters")
		tol      = flag.Float64("tol", 0.02, "relative tolerance on a ratio against 1.0 or 4.0")
		validate = flag.Bool("validate", false, "enable the Vulkan validation layer in the subprocesses")
	)
	flag.Parse()

	if *arm == "" {
		if err := drive(*validate, *tol); err != nil {
			log.Fatalf("quadcheck: %v", err)
		}
		return
	}
	res, err := measure(*arm, *msaa, *prepass, *size, *frames)
	if err != nil {
		log.Fatalf("quadcheck: %v", err)
	}
	if err := res.assert(*tol); err != nil {
		log.Printf("%s", res)
		log.Fatalf("quadcheck: %v", err)
	}
	fmt.Printf("%s\n", res)
}

// drive runs every arm in a subprocess of its own and combines them into the
// verdict.
//
// A subprocess per arm rather than several engines in one process, which is what
// every other multi-arm check here does: one Vulkan device, one window and one
// teardown per run is what makes a run validation-clean, and the arms have
// different MSAA counts, which is a construction-time decision.
func drive(validate bool, tol float64) error {
	type cell struct {
		arm     string
		msaa    int
		prepass string
	}
	cells := []cell{
		{"fullscreen", 1, "off"},
		// 4x MSAA on the fullscreen arm and not on the pixel field, which is the
		// one asymmetry here and it is measured rather than chosen. The fullscreen
		// arm is what the sample count has something to say about: per-sample
		// shading would read 4.0 there, and 1.0 says a fragment runs once per
		// covered PIXEL.
		//
		// The pixel field cannot be run at 4x and mean anything. Its samples are
		// not at the pixel centre -- the standard 4x pattern puts all four 0.395
		// pixels from it, and the nearest sample of the next pixel at 0.605 -- so a
		// triangle that covers one of its own and none of its neighbour's has to
		// have a circumradius between 0.591 and 0.605, a 2.4 % window, and the arm
		// would be measuring the sample pattern rather than the quad factor.
		// Measured at the default 0.9: 19,200 triangles covered 38,400 pixels, two
		// each, and the arm failed its own coverage assertion rather than reporting
		// a ratio -- which is what that assertion is for. -arm pixels -msaa 4 is
		// still there for anyone who wants to see it.
		{"fullscreen", 4, "off"},
		{"pixels", 1, "off"},
		// The configuration the measurement is actually taken in: the prepass on,
		// so the opaque pass compares EQUAL against depth it already wrote. The
		// proof has to hold there too, or the measurement is proved in one
		// configuration and taken in another.
		{"fullscreen", 1, "on"},
		{"fullscreen", 4, "on"},
		{"pixels", 1, "on"},
	}
	results := make([]armResult, 0, len(cells))
	for _, c := range cells {
		args := []string{"-arm", c.arm, "-msaa", strconv.Itoa(c.msaa), "-prepass", c.prepass,
			"-tol", strconv.FormatFloat(tol, 'g', -1, 64)}
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), "GLYPHENGINE_BACKGROUND=1", "GLYPHENGINE_FIXED_FRAME_TIME=16.667ms")
		if validate {
			cmd.Env = append(cmd.Env, "GLYPHENGINE_VALIDATION=1")
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s msaa=%d prepass=%s: %w\n%s", c.arm, c.msaa, c.prepass, err, out)
		}
		if strings.Contains(string(out), "VULKAN ERROR") || strings.Contains(string(out), "VULKAN WARNING") {
			return fmt.Errorf("%s msaa=%d prepass=%s: validation messages\n%s", c.arm, c.msaa, c.prepass, out)
		}
		r, err := parse(string(out))
		if err != nil {
			return fmt.Errorf("%s msaa=%d prepass=%s: %w\n%s", c.arm, c.msaa, c.prepass, err, out)
		}
		fmt.Printf("%s\n", r)
		results = append(results, r)
	}

	// The verdict. Every pixels arm has to agree, because "helpers are counted"
	// is a property of the device and not of a sample count or a depth mode -- if
	// two of them disagreed, the counter would be measuring something else as
	// well and no single answer would be honest.
	helpers, decided := false, false
	for _, r := range results {
		if r.arm != "pixels" {
			continue
		}
		this := math.Abs(r.ratio-4) < math.Abs(r.ratio-1)
		if decided && this != helpers {
			return fmt.Errorf("the pixel-field arms disagree about helper lanes; the counter is not measuring one thing")
		}
		helpers, decided = this, true
	}
	if !decided {
		return fmt.Errorf("no pixel-field arm ran, so nothing was established")
	}
	if helpers {
		fmt.Println("QUADCHECK VERDICT helpers_counted=true -- FRAGMENT_SHADER_INVOCATIONS includes helper lanes on this device, so invocations per covered sample IS the quad factor")
		return nil
	}
	fmt.Println("QUADCHECK VERDICT helpers_counted=false -- FRAGMENT_SHADER_INVOCATIONS excludes helper lanes on this device, so it CANNOT measure quad overshading: a gl_HelperInvocation counter in a storage buffer is the only route left")
	return nil
}

var resultLine = regexp.MustCompile(`QUADCHECK\s+(\S.*)`)

func parse(out string) (armResult, error) {
	m := resultLine.FindStringSubmatch(out)
	if m == nil {
		return armResult{}, fmt.Errorf("no QUADCHECK line in the output")
	}
	var r armResult
	for _, field := range strings.Fields(m[1]) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		var err error
		switch k {
		case "arm":
			r.arm = v
		case "msaa":
			r.msaa, err = strconv.Atoi(v)
		case "prepass":
			r.prepass = v
		case "pixels":
			r.pixels, err = strconv.Atoi(v)
		case "triangles":
			r.triangles, err = strconv.Atoi(v)
		case "invocations":
			r.invoc, err = strconv.ParseUint(v, 10, 64)
		case "clipped":
			r.clipped, err = strconv.ParseUint(v, 10, 64)
		case "covered":
			r.covered, err = strconv.Atoi(v)
		case "ratio":
			r.ratio, err = strconv.ParseFloat(v, 64)
		}
		if err != nil {
			return r, fmt.Errorf("field %q: %w", field, err)
		}
	}
	if r.arm == "" {
		return r, fmt.Errorf("the QUADCHECK line names no arm")
	}
	return r, nil
}

func (r armResult) String() string {
	return fmt.Sprintf("QUADCHECK arm=%s msaa=%d prepass=%s pixels=%d triangles=%d invocations=%d clipped=%d covered=%d ratio=%.4f",
		r.arm, r.msaa, r.prepass, r.pixels, r.triangles, r.invoc, r.clipped, r.covered, r.ratio)
}

// assert is the arm's own claim about itself, checked before the number is
// believed.
func (r armResult) assert(tol float64) error {
	if r.covered == 0 {
		return fmt.Errorf("%s: nothing was covered, so this frame measures nothing", r.arm)
	}
	switch r.arm {
	case "fullscreen":
		// Every pixel, exactly: a triangle that failed to cover the frame would
		// leave helper lanes at its edges and make this arm's 1.0 a coincidence.
		if r.covered != r.pixels {
			return fmt.Errorf("fullscreen: %d pixels covered of %d; the triangle does not cover the frame",
				r.covered, r.pixels)
		}
		if math.Abs(r.ratio-1) > tol {
			return fmt.Errorf("fullscreen: %.4f invocations per covered sample, want 1.0 +- %.3g -- a fully covered frame has no helper lanes, so this says the counter is not per covered sample (or shading is per sample)",
				r.ratio, tol)
		}
	case "pixels":
		// One covered pixel per triangle. Two would mean a triangle reached a
		// second pixel's samples, which puts two covered samples under one quad
		// and halves the ratio -- a quad factor of 2.0 that looks like a result.
		if r.covered != r.triangles {
			return fmt.Errorf("pixels: %d pixels covered by %d triangles; each triangle must cover exactly one pixel (tune -size against this device's sample positions)",
				r.covered, r.triangles)
		}
		near4 := math.Abs(r.ratio-4) <= 4*tol
		near1 := math.Abs(r.ratio-1) <= tol
		if !near4 && !near1 {
			return fmt.Errorf("pixels: %.4f invocations per covered sample, which is neither 1.0 (helpers not counted) nor 4.0 (helpers counted) -- the field is not one triangle per quad",
				r.ratio)
		}
	default:
		return fmt.Errorf("unknown arm %q", r.arm)
	}
	return nil
}

// game is the scene both arms render: one mesh, one draw, nothing else.
type game struct {
	mesh      *renderer.Mesh
	triangles int
	arm       string
	size      float64

	// w and h are the swapchain's own pixel dimensions, read in Init. Every
	// position below is derived from them rather than from the requested size, so
	// a window the platform rounded still puts a triangle on a pixel centre.
	w, h int
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()
	// Bloom off: it is the only thing in this frame that would spread one
	// covered pixel onto its neighbours, and the denominator here is a count of
	// covered pixels.
	r.SetBloom(0, 1, 0, 1)

	g.w, g.h = r.Extent()
	if g.w%2 != 0 || g.h%2 != 0 {
		return fmt.Errorf("the swapchain is %dx%d; an odd axis leaves a half-covered quad at the edge and the fullscreen arm's claim with it", g.w, g.h)
	}
	verts := g.vertices(r.Aspect())
	g.triangles = len(verts) / 3
	mesh, err := r.CreateMesh(verts)
	if err != nil {
		return err
	}
	g.mesh = mesh

	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: mesh, Roughness: 1})
	e.C.Color.Set(ent, &glyph.Color{R: 1, G: 1, B: 1})
	// No shadow casting and no receiving worth the name: the shadow pass stays
	// empty, so the only fragment work in the frame is the one draw being counted.
	e.C.NoCastShadow.Set(ent, &glyph.NoCastShadow{})
	e.C.Static.Set(ent, &glyph.Static{})
	e.RebuildStatics()

	// Flat white ambient and no sun, so the geometry is unmistakably not the
	// background whatever the tonemap does with it, and the shadow cascades have
	// no light to render from. No sky package, so the frame clears to black and
	// every non-black pixel is this mesh.
	e.Scene.Env = &glyph.StaticSource{
		Ambient:    &glyph.AmbientLight{Color: mgl32.Vec3{1, 1, 1}},
		ClearColor: [3]float32{0, 0, 0},
	}

	e.SetCamera(mgl32.Vec3{0, 0, planeDist}, mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 1, 0})
	return nil
}

func (g *game) Update(*glyph.Engine, float32) {}

// vertices builds the arm's geometry on the z = 0 plane, facing the camera.
//
// Clockwise by decreasing angle, which is what Renderer.CreateDisc does for a
// +Z-facing surface and therefore what this engine's back-face culling expects.
// Reversed, the whole field is culled and every count in the run is a count of
// nothing -- which the covered-pixel assertion would report.
func (g *game) vertices(aspect float32) []renderer.Vertex {
	halfH := planeDist * math.Tan(float64(mgl32.DegToRad(fov))/2)
	halfW := halfH * float64(aspect)
	// One pixel in world units on this plane. The two axes agree by construction
	// -- halfW is halfH times the aspect -- and a disagreement would mean the
	// projection is not the one assumed here.
	pxX, pxY := 2*halfW/float64(g.w), 2*halfH/float64(g.h)

	// Everything below is in PIXELS from the centre of the frame, converted on
	// the way into a vertex. Working in pixels is the point: every size here is a
	// statement about the 2x2 quad grid, and a world-unit constant would have to
	// be re-derived every time the frame size or the field of view moved.
	tri := func(cx, cy, r float64) []renderer.Vertex {
		out := make([]renderer.Vertex, 0, 3)
		for _, deg := range []float64{90, -30, -150} {
			a := deg * math.Pi / 180
			out = append(out, renderer.Vertex{
				Pos:    [3]float32{float32((cx + r*math.Cos(a)) * pxX), float32((cy + r*math.Sin(a)) * pxY), 0},
				Color:  [3]float32{1, 1, 1},
				Normal: [3]float32{0, 0, 1},
				UV:     [2]float32{0, 0},
			})
		}
		return out
	}

	if g.arm == "fullscreen" {
		// Circumradius three times the viewport's half-diagonal, so the inradius
		// alone -- half of it, for an equilateral triangle -- covers every corner
		// with room to spare.
		return tri(0, 0, 3*math.Hypot(float64(g.w)/2, float64(g.h)/2))
	}

	// The pixel field: one triangle centred on the centre of every strideth
	// pixel. Centres rather than corners, so each triangle contains the sample
	// the rasterizer tests at MSAA 1 by construction rather than by luck. The
	// projection's Y flip mirrors the field vertically and nothing else -- a
	// mirrored pixel centre is still a pixel centre, so the alignment survives it.
	verts := make([]renderer.Vertex, 0, 3*(g.w/stride)*(g.h/stride))
	for j := stride / 2; j < g.h; j += stride {
		for i := stride / 2; i < g.w; i += stride {
			verts = append(verts, tri(float64(i)+0.5-float64(g.w)/2, float64(j)+0.5-float64(g.h)/2, g.size)...)
		}
	}
	return verts
}

// measure runs one arm and returns its counts.
func measure(arm string, msaa int, prepass string, size float64, frames int) (armResult, error) {
	if arm != "fullscreen" && arm != "pixels" {
		return armResult{}, fmt.Errorf("-arm %q is not fullscreen or pixels", arm)
	}
	if prepass != "off" && prepass != "on" {
		return armResult{}, fmt.Errorf("-prepass %q is not off or on", prepass)
	}
	g := &game{arm: arm, size: size}
	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - quadcheck"),
		glyph.WithWindowSize(frameW, frameH),
		glyph.WithMSAA(msaa),
		glyph.WithProjection(fov, near, far),
		glyph.WithMaxFrames(frames),
		glyph.WithPipelineStatistics(),
	}
	if prepass == "on" {
		opts = append(opts, glyph.WithDepthPrepass(renderer.DepthPrepassOn))
	}
	e, err := glyph.New(g, opts...)
	if err != nil {
		return armResult{}, fmt.Errorf("create engine: %w", err)
	}
	defer e.Destroy()

	caps := e.Capabilities()
	if !caps.PipelineStatistics {
		return armResult{}, fmt.Errorf("%s does not support pipelineStatisticsQuery, so there is nothing to establish here", caps.GPUName)
	}
	if caps.MSAASamples != msaa {
		return armResult{}, fmt.Errorf("asked for %dx MSAA and got %dx; the arm would measure a different denominator than it reports", msaa, caps.MSAASamples)
	}
	e.Run()

	st, err := e.MeanPipelineStats()
	if err != nil {
		return armResult{}, fmt.Errorf("pipeline statistics: %w", err)
	}
	if !st.Valid {
		return armResult{}, fmt.Errorf("pipeline statistics never became valid over %d frames", frames)
	}
	res := armResult{
		arm: arm, msaa: msaa, prepass: prepass, triangles: g.triangles, pixels: g.w * g.h,
		invoc:   st.FragmentInvocations[renderer.PassOpaque],
		clipped: st.ClippingPrimitives[renderer.PassOpaque],
	}
	img, err := e.Renderer().CaptureFrame()
	if err != nil {
		return res, fmt.Errorf("capture: %w", err)
	}
	res.covered = coveredPixels(img)
	if res.covered > 0 {
		res.ratio = float64(res.invoc) / float64(res.covered)
	}
	// The prepass's own bracket, for the record rather than for the claim: it
	// rasterises the same geometry through a null fragment stage, so a reader can
	// see that the depth-only pass is counted as well and is not where the opaque
	// pass's invocations came from.
	log.Printf("quadcheck: prepass bracket %d invocations over %d primitives; draws %d (prepass %d)",
		st.FragmentInvocations[renderer.PassDepthPrepass], st.ClippingPrimitives[renderer.PassDepthPrepass],
		e.Renderer().Stats().DrawCalls, e.Renderer().Stats().PrepassDraws)
	return res, nil
}

// coveredPixels counts the pixels the geometry reached.
//
// The frame clears to black and nothing in it spreads, so this is the covered
// sample count at MSAA 1 and the covered PIXEL count at higher sample counts --
// which is the right denominator either way, because this engine does not enable
// sample shading and a fragment therefore runs once per covered pixel per
// primitive. The fullscreen arm at MSAA 4 is what says so rather than this
// comment: per-sample shading would read 4.0 there.
func coveredPixels(img *image.RGBA) int {
	n := 0
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			at := y*img.Stride + x*4
			if img.Pix[at] >= visible || img.Pix[at+1] >= visible || img.Pix[at+2] >= visible {
				n++
			}
		}
	}
	return n
}
