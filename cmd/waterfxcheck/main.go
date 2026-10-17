// Command waterfxcheck asserts the two water options -- whitecap foam and the
// subsurface crest term -- against what a real render of 09-water drew, by
// predicting both on the CPU from the same wave function.
//
//	task waterfx
//
// Every prediction goes through glyphengine.WaterOptions.SurfaceAt, the Go
// mirror of the shader's wave sum, at the grid points 09-water -fxdump reports,
// with the camera it reports. Nothing here reads a box off a screenshot by eye:
// a grid point is projected through the frame's own view-projection to the
// pixel it landed on, so the check asks about the water it computed rather than
// about a rectangle somebody once chose.
//
// Foam (-foam, -foamoff, -foamon, -foamoffpng):
//
//   - with the option off the surface has no foam target at all;
//   - the target the GPU accumulated matches a CPU simulation of the same
//     frames, step for step under the fixed clock, texel for texel;
//   - foam is only where the simulation put it, which is only where the
//     Jacobian has been under the threshold within the last Decay seconds;
//   - texels whose crest moved on about a second before the last frame hold
//     less than the foam they were given and more than nothing -- the decayed
//     value, which is the persistence that makes foam read as foam;
//   - on the screen, water the simulation left foam-free is unchanged by the
//     option, and water it foamed is changed.
//
// Subsurface (-sssbehind, -sssfront, -ssshalf, each a dump and an on/off pair):
//
//   - sun low and behind the crests from the camera: crest pixels brighten
//     with the option on, and trough pixels do not move;
//   - sun in front of the camera, behind the eye: the term is zero, so the
//     water is unchanged;
//   - halving WaveAmplitude keeps the term's share of the crest's light, which
//     is what normalising the height by the amplitude is for.
//
// Repeatability is the task's: it captures each foam frame twice and compares
// the pixels and the read-back targets.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"sort"

	glyph "github.com/derekmwright/glyphengine"
)

// dump is what 09-water -fxdump writes; see examples/09-water/fxdump.go.
type dump struct {
	Frames  int                `json:"frames"`
	Elapsed float32            `json:"elapsed"`
	Width   int                `json:"width"`
	Height  int                `json:"height"`
	VP      [16]float32        `json:"vp"`
	SunDir  [3]float32         `json:"sunDir"`
	Options glyph.WaterOptions `json:"options"`
	Grid    glyph.WaterGrid    `json:"grid"`
	Foam    []float32          `json:"foam"`
}

var failed bool

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	failed = true
}

func main() {
	foamOn := flag.String("foam", "", "-fxdump of the foam run")
	foamOff := flag.String("foamoff", "", "-fxdump of the same run with foam off")
	foamRepeat := flag.String("foamrepeat", "", "-fxdump of the foam run repeated; its target must match -foam exactly")
	foamOnPNG := flag.String("foamon", "", "capture of the foam run")
	foamOffPNG := flag.String("foamoffpng", "", "capture of the run with foam off")
	frameTime := flag.Float64("frametime", 0.016667, "GLYPHENGINE_FIXED_FRAME_TIME in seconds, which the simulation replays")
	foamTol := flag.Float64("foamtol", 0.02, "largest coverage difference allowed between the GPU's target and the simulation's")

	sssBehind := flag.String("sssbehind", "", "dump,on.png,off.png with the sun low behind the crests")
	sssFront := flag.String("sssfront", "", "dump,on.png,off.png with the sun behind the camera")
	sssHalf := flag.String("ssshalf", "", "dump,on.png,off.png as -sssbehind with WaveAmplitude halved")
	minGain := flag.Float64("mingain", 0.5, "crest pixels must gain at least this much mean luminance with the subsurface term, in 8-bit steps")
	troughTol := flag.Float64("troughtol", 0.25, "trough pixels may move at most this much mean luminance")
	shareTol := flag.Float64("sharetol", 0.25, "the half-amplitude crest share may differ from the full one by at most this fraction of it")

	measure := flag.String("measure", "", "print the Jacobian distribution and foam episode lengths behind the defaults, over this dump's grid, and exit")
	flag.Parse()

	if *measure != "" {
		measureDefaults(load(*measure))
		return
	}
	ran := false
	if *foamOn != "" {
		ran = true
		checkFoam(load(*foamOn), *foamOff, *foamRepeat, *foamOnPNG, *foamOffPNG, float32(*frameTime), float32(*foamTol))
	}
	if *sssBehind != "" || *sssFront != "" || *sssHalf != "" {
		ran = true
		checkSubsurface(*sssBehind, *sssFront, *sssHalf, *minGain, *troughTol, *shareTol)
	}
	if !ran {
		fmt.Fprintln(os.Stderr, "waterfxcheck: nothing to check; see -h")
		os.Exit(2)
	}
	if failed {
		os.Exit(1)
	}
	fmt.Println("waterfx: every check held")
}

func load(path string) *dump {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "waterfxcheck: %v\n", err)
		os.Exit(2)
	}
	d := &dump{}
	if err := json.Unmarshal(b, d); err != nil {
		fmt.Fprintf(os.Stderr, "waterfxcheck: %s: %v\n", path, err)
		os.Exit(2)
	}
	if d.Grid.Width*d.Grid.Height != len(d.Grid.Depth) || d.Grid.Width < 2 {
		fmt.Fprintf(os.Stderr, "waterfxcheck: %s: the grid is malformed -- this check proves nothing\n", path)
		os.Exit(2)
	}
	return d
}

func loadPNG(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "waterfxcheck: %v\n", err)
		os.Exit(2)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "waterfxcheck: %s: %v\n", path, err)
		os.Exit(2)
	}
	return img
}

// times replays the engine's fixed clock: Elapsed accumulates the frame time in
// float32, so the sequence the GPU saw is reproducible exactly, and the last
// value has to be the Elapsed the run reported or the replay is of some other
// run.
func times(d *dump, frameTime float32) []float32 {
	var all []float32
	var t float32
	for len(all) < d.Frames+4 {
		t += frameTime
		all = append(all, t)
		if t == d.Elapsed {
			break
		}
	}
	if all[len(all)-1] != d.Elapsed || len(all) < d.Frames {
		fmt.Fprintf(os.Stderr, "waterfxcheck: cannot replay the clock: %d frames of %v end at %v, the run reported %v\n",
			d.Frames, frameTime, all[len(all)-1], d.Elapsed)
		os.Exit(2)
	}
	return all[len(all)-d.Frames:]
}

// simulateFoam is water_foam.comp on the CPU: one step per frame, in the same
// float32 order, quantized on every store exactly as the shader quantizes.
//
// It also records, per texel, which frame's foam is the one still showing at
// the end: the frame k maximising fresh(k) - (t_end - t_k)/Decay, which is the
// crest whose foam has decayed least. That frame and its fresh value are what
// the persistence check asks about -- not the last frame with any foam, which
// is the faint tail of a crest leaving rather than the crest.
func simulateFoam(d *dump, ts []float32) (cov []float32, source []int, sourceFresh []float32) {
	g := d.Grid
	o := d.Options
	// The noise moves the height and not the horizontal map, so it cannot
	// change the Jacobian; leaving it out only skips work, as the shader does.
	o.WaveNoise = 0
	n := g.Width * g.Height
	cov = make([]float32, n)
	source = make([]int, n)
	sourceFresh = make([]float32, n)
	best := make([]float64, n)
	for i := range source {
		source[i] = -1
	}
	spacing := g.Spacing()
	var prev float32
	for k, t := range ts {
		var decay float32
		if k > 0 {
			decay = max(t-prev, 0) / o.Foam.Decay
		}
		prev = t
		for iz := 0; iz < g.Height; iz++ {
			for ix := 0; ix < g.Width; ix++ {
				i := iz*g.Width + ix
				x, z, depth := g.Point(ix, iz)
				fresh := float32(0)
				if depth > 0 {
					j := o.SurfaceAt(x, z, depth, spacing, t).Jacobian
					fresh = min(max((o.Foam.Threshold-j)/0.1, 0), 1)
				}
				if fresh > 0 {
					if score := float64(fresh) + float64(t)/float64(o.Foam.Decay); source[i] < 0 || score > best[i] {
						best[i], source[i], sourceFresh[i] = score, k, fresh
					}
				}
				cov[i] = roundHalf(max(cov[i]-decay, fresh))
			}
		}
	}
	return cov, source, sourceFresh
}

// fresh is the foam the Jacobian makes at texel i at time t.
func fresh(d *dump, i int, t float32) float32 {
	g := d.Grid
	x, z, depth := g.Point(i%g.Width, i/g.Width)
	if depth <= 0 {
		return 0
	}
	o := d.Options
	o.WaveNoise = 0
	j := o.SurfaceAt(x, z, depth, g.Spacing(), t).Jacobian
	return min(max((o.Foam.Threshold-j)/0.1, 0), 1)
}

// roundHalf is water_foam.comp's quantize: the nearest binary16 value, ties to
// even, with anything under the smallest normal flushed to zero.
func roundHalf(v float32) float32 {
	if v < 6.103515625e-05 {
		return 0
	}
	exp := math.Floor(math.Log2(math.Abs(float64(v))))
	exp = max(exp, -14)
	ulp := math.Ldexp(1, int(exp)-10)
	return float32(math.RoundToEven(float64(v)/ulp) * ulp)
}

func checkFoam(on *dump, offPath, repeatPath, onPNG, offPNG string, frameTime, tol float32) {
	fmt.Printf("foam: %d frames to t=%.4f, threshold %.3f, decay %.2f s, WaveAmplitude %.2f, grid %dx%d\n",
		on.Frames, on.Elapsed, on.Options.Foam.Threshold, on.Options.Foam.Decay, on.Options.WaveAmplitude, on.Grid.Width, on.Grid.Height)
	if !(on.Options.Foam.Decay > 0) {
		fail("the foam run has the option off; this check proves nothing")
		return
	}
	if len(on.Foam) != on.Grid.Width*on.Grid.Height {
		fail("the foam run read back %d texels for a %dx%d grid", len(on.Foam), on.Grid.Width, on.Grid.Height)
		return
	}

	if offPath != "" {
		off := load(offPath)
		if off.Foam != nil {
			fail("with the option off the surface still has a foam target (%d texels)", len(off.Foam))
		} else {
			fmt.Println("off: the surface has no foam target, so there is no coverage to have")
		}
	}

	ts := times(on, frameTime)
	sim, last, lastFresh := simulateFoam(on, ts)

	// Texel agreement.
	var worst float32
	worstAt, over, covered, simCovered, stray := 0, 0, 0, 0, 0
	for i := range sim {
		diff := float32(math.Abs(float64(on.Foam[i] - sim[i])))
		if diff > worst {
			worst, worstAt = diff, i
		}
		if diff > tol {
			over++
		}
		if on.Foam[i] > 0 {
			covered++
		}
		if sim[i] > 0 {
			simCovered++
		}
		// Coverage only where the Jacobian put it: foam on a texel the
		// simulation never foamed within the decay window.
		if on.Foam[i] > tol && last[i] < 0 {
			stray++
		}
	}
	fmt.Printf("target: %d texels covered on the GPU, %d in the simulation; largest difference %.4f at texel %d; %d over %.3f; %d foamed where the Jacobian never went under\n",
		covered, simCovered, worst, worstAt, over, tol, stray)
	if simCovered == 0 {
		fail("the simulation foamed nothing at this sea state; this check proves nothing")
	}
	// A Jacobian a hair either side of the threshold on the GPU and the CPU can
	// put one texel in or out; a handful of those is precision, more is a
	// different wave function.
	if allowed := len(sim) / 1000; over > allowed {
		fail("%d texels differ from the simulation by more than %.3f (allowed %d)", over, tol, allowed)
	}
	if stray > 0 {
		fail("%d texels hold foam the Jacobian never made", stray)
	}

	// Persistence: texels whose showing foam came from a crest about a second
	// before the last frame, with nothing made there since.
	lastFrame := len(ts) - 1
	var checked, decayed int
	var worstPersist float32
	for i := range sim {
		k := last[i]
		if k < 0 || lastFresh[i] < 0.5 || fresh(on, i, ts[lastFrame]) > 0 {
			continue
		}
		age := ts[lastFrame] - ts[k]
		if age < 0.9 || age > 1.1 {
			continue
		}
		checked++
		expect := max(lastFresh[i]-age/on.Options.Foam.Decay, 0)
		got := on.Foam[i]
		if d := float32(math.Abs(float64(got - expect))); d > worstPersist {
			worstPersist = d
		}
		// Between the decayed and the fresh value, and at the simulation's:
		// held at the fresh value is foam that never fades, and nothing at all
		// is foam that never persisted. Compared against the simulation rather
		// than against expect, which is the same decay without the per-frame
		// quantization the target really does.
		if got < lastFresh[i]-0.5*age/on.Options.Foam.Decay && got > 0 && math.Abs(float64(got-sim[i])) <= float64(tol) {
			decayed++
		}
	}
	fmt.Printf("persistence: %d texels whose foam came from a crest 0.9-1.1 s before the last frame; %d hold the decayed value (largest distance from fresh - age/Decay %.4f)\n", checked, decayed, worstPersist)
	if checked < 10 {
		fail("only %d texels had a crest leave about a second ago; this check proves nothing", checked)
	} else if decayed != checked {
		fail("%d of %d texels a second past their crest are not between the decayed and the fresh value", checked-decayed, checked)
	}

	if repeatPath != "" {
		rep := load(repeatPath)
		same := len(rep.Foam) == len(on.Foam)
		for i := 0; same && i < len(on.Foam); i++ {
			same = rep.Foam[i] == on.Foam[i]
		}
		if !same {
			fail("the repeated run's foam target differs from the first")
		} else {
			fmt.Println("repeat: the foam target is identical texel for texel")
		}
	}

	if onPNG != "" && offPNG != "" {
		checkFoamPixels(on, sim, loadPNG(onPNG), loadPNG(offPNG))
	}
}

// checkFoamPixels projects grid points to the screen and asks whether the
// option changed the pixel each one landed on. Foam-free water, judged by the
// simulation over the point and its neighbours, must be untouched; well-foamed
// water must be changed. Points hidden behind a nearer crest are left out (see
// visible), and both are fractions rather than all-or-nothing because that test
// is an approximation.
func checkFoamPixels(d *dump, sim []float32, on, off image.Image) {
	g := d.Grid
	eye := cameraEye(d)
	clearN, clearSame, foamN, foamChanged := 0, 0, 0, 0
	for iz := 1; iz < g.Height-1; iz++ {
		for ix := 1; ix < g.Width-1; ix++ {
			x, z, depth := g.Point(ix, iz)
			if depth < 0.5 {
				continue
			}
			s := d.Options.SurfaceAt(x, z, depth, g.Spacing(), d.Elapsed)
			if !nearEye(eye, s.Position) || !visible(d, eye, s.Position) {
				continue
			}
			px, py, ok := project(d, s.Position)
			if !ok {
				continue
			}
			near := float32(0)
			for dz := -1; dz <= 1; dz++ {
				for dx := -1; dx <= 1; dx++ {
					near = max(near, sim[(iz+dz)*g.Width+ix+dx])
				}
			}
			changed := luma(on, px, py) != luma(off, px, py)
			switch {
			case near == 0:
				clearN++
				if !changed {
					clearSame++
				}
			case sim[iz*g.Width+ix] >= 0.5:
				foamN++
				if changed {
					foamChanged++
				}
			}
		}
	}
	fmt.Printf("screen: %d foam-free points on screen, %d unchanged; %d foamed points, %d changed\n", clearN, clearSame, foamN, foamChanged)
	if clearN < 50 || foamN < 20 {
		fail("too few points on screen (%d clear, %d foamed); this check proves nothing", clearN, foamN)
		return
	}
	// 5 percent rather than none: the visibility march approximates the
	// surface, so a point just behind a foamed crest can still project onto it.
	if float64(clearSame) < 0.95*float64(clearN) {
		fail("the option changed %.1f%% of the foam-free water, want under 5%%", 100*float64(clearN-clearSame)/float64(clearN))
	}
	if float64(foamChanged) < 0.8*float64(foamN) {
		fail("only %.1f%% of the foamed water changed on screen, want 80%%", 100*float64(foamChanged)/float64(foamN))
	}
}

// project returns the pixel a world position lands on through the dump's
// view-projection: Vulkan's clip space, which the engine's projection already
// flips so that +y is down the screen.
func project(d *dump, p [3]float32) (int, int, bool) {
	m := d.VP
	cx := m[0]*p[0] + m[4]*p[1] + m[8]*p[2] + m[12]
	cy := m[1]*p[0] + m[5]*p[1] + m[9]*p[2] + m[13]
	cw := m[3]*p[0] + m[7]*p[1] + m[11]*p[2] + m[15]
	if cw <= 0.1 {
		return 0, 0, false
	}
	x := int((cx/cw + 1) / 2 * float32(d.Width))
	y := int((cy/cw + 1) / 2 * float32(d.Height))
	if x < 0 || y < 0 || x >= d.Width || y >= d.Height {
		return 0, 0, false
	}
	return x, y, true
}

// luma is Rec. 709 weights over the 8-bit sRGB values -- not linear light, but
// monotonic in it, which is all a brighter/unchanged question needs.
func luma(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return 0.2126*float64(r>>8) + 0.7152*float64(g>>8) + 0.0722*float64(b>>8)
}

type sssCase struct {
	d       *dump
	on, off image.Image
}

func loadCase(spec string) *sssCase {
	if spec == "" {
		return nil
	}
	var parts []string
	start := 0
	for i := 0; i <= len(spec); i++ {
		if i == len(spec) || spec[i] == ',' {
			parts = append(parts, spec[start:i])
			start = i + 1
		}
	}
	if len(parts) != 3 {
		fmt.Fprintf(os.Stderr, "waterfxcheck: %q: want dump,on.png,off.png\n", spec)
		os.Exit(2)
	}
	return &sssCase{d: load(parts[0]), on: loadPNG(parts[1]), off: loadPNG(parts[2])}
}

// crestStats projects deep-water grid points near the camera (see nearEye) and
// splits them by how far the CPU says each one rose: crests more than 0.3 of
// the amplitude above still water, troughs more than 0.3 below it.
//
// For the crests it also predicts each point's share of the term from
// everything but the height and the light -- the sun-behind lobe, the grazing
// factor and the Fresnel weight the term sits under, from the vertex normal --
// and returns gain per unit of crest times that prediction. That is what the
// amplitude check compares: halving the sea flattens the faces, which moves
// the lobe, the grazing factor and the reflection, and only the height
// normalisation is the question.
func crestStats(c *sssCase) (crestGain, crestBase, perUnit, troughMove float64, crests, troughs, troughSame int) {
	d := c.d
	g := d.Grid
	eye := cameraEye(d)
	l := normalize3(d.SunDir)
	var cg, cb, cp, tm float64
	for iz := 0; iz < g.Height; iz++ {
		for ix := 0; ix < g.Width; ix++ {
			x, z, depth := g.Point(ix, iz)
			if depth < 1.5 {
				continue
			}
			s := d.Options.SurfaceAt(x, z, depth, g.Spacing(), d.Elapsed)
			if !nearEye(eye, s.Position) || !visible(d, eye, s.Position) {
				continue
			}
			px, py, ok := project(d, s.Position)
			if !ok {
				continue
			}
			on, off := luma(c.on, px, py), luma(c.off, px, py)
			switch {
			case s.Crest > 0.3:
				crests++
				cg += on - off
				cb += off
				v := normalize3([3]float32{eye[0] - s.Position[0], eye[1] - s.Position[1], eye[2] - s.Position[2]})
				nv := max(dot3(s.Normal, v), 0)
				behind := math.Pow(max(-dot3(v, l), 0), 4)
				fresnel := 0.02 + 0.98*math.Pow(1-max(nv, 0.02), 5)
				cp += float64(s.Crest) * behind * (1 - nv) * (1 - fresnel)
			case s.Crest < -0.3:
				troughs++
				tm += math.Abs(on - off)
				if on == off {
					troughSame++
				}
			}
		}
	}
	if crests > 0 {
		crestGain, crestBase = cg/float64(crests), cb/float64(crests)
	}
	if cp > 0 {
		perUnit = cg / cp
	}
	if troughs > 0 {
		troughMove = tm / float64(troughs)
	}
	return
}

func dot3(a, b [3]float32) float64 {
	return float64(a[0])*float64(b[0]) + float64(a[1])*float64(b[1]) + float64(a[2])*float64(b[2])
}

func normalize3(a [3]float32) [3]float32 {
	l := float32(math.Sqrt(dot3(a, a)))
	if l == 0 {
		return a
	}
	return [3]float32{a[0] / l, a[1] / l, a[2] / l}
}

// visible marches the ray from the eye to a surface point and reports whether
// any water between them rises above it -- a trough behind a crest, seen from
// a camera standing a few units above the lake, projects to a pixel that shows
// the crest. The surface under each sample is evaluated at the sample's own
// X and Z as a rest position, which ignores the few centimetres the Gerstner
// term moves it sideways; the margin absorbs that.
func visible(d *dump, eye, p [3]float32) bool {
	g := d.Grid
	const steps = 64
	for i := 1; i < steps; i++ {
		f := float32(i) / steps
		q := [3]float32{eye[0] + f*(p[0]-eye[0]), eye[1] + f*(p[1]-eye[1]), eye[2] + f*(p[2]-eye[2])}
		// Depth under the sample from the nearest grid point; the waves are
		// shoaled by it.
		ix := int((q[0]-g.Origin[0])/g.Step[0] + 0.5)
		iz := int((q[2]-g.Origin[1])/g.Step[1] + 0.5)
		if ix < 0 || iz < 0 || ix >= g.Width || iz >= g.Height {
			continue
		}
		depth := g.Depth[iz*g.Width+ix]
		if depth <= 0 {
			continue
		}
		if h := d.Options.SurfaceAt(q[0], q[2], depth, g.Spacing(), d.Elapsed).Position[1]; h > q[1]+0.05 {
			return false
		}
	}
	return true
}

// nearEye limits the screen checks to water within 45 units of the camera.
// Past that a low camera sees crests stacked in front of the water between
// them, and a pixel covers several grid points, so the pixel a point projects
// to is as likely to be a nearer crest's as its own.
func nearEye(eye, p [3]float32) bool {
	dx, dz := p[0]-eye[0], p[2]-eye[2]
	return dx*dx+dz*dz <= 45*45
}

// cameraEye recovers the eye from the view-projection: the point clip space
// maps to w = 0 with x = y = 0, which is where every view ray starts.
func cameraEye(d *dump) [3]float32 {
	m := d.VP
	// Rows 0, 1 and 3 of VP times (eye, 1) are zero at the eye.
	a := [3][4]float64{
		{float64(m[0]), float64(m[4]), float64(m[8]), -float64(m[12])},
		{float64(m[1]), float64(m[5]), float64(m[9]), -float64(m[13])},
		{float64(m[3]), float64(m[7]), float64(m[11]), -float64(m[15])},
	}
	for c := 0; c < 3; c++ {
		p := c
		for r := c + 1; r < 3; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		a[c], a[p] = a[p], a[c]
		for r := 0; r < 3; r++ {
			if r != c {
				f := a[r][c] / a[c][c]
				for k := c; k < 4; k++ {
					a[r][k] -= f * a[c][k]
				}
			}
		}
	}
	return [3]float32{float32(a[0][3] / a[0][0]), float32(a[1][3] / a[1][1]), float32(a[2][3] / a[2][2])}
}

func checkSubsurface(behindSpec, frontSpec, halfSpec string, minGain, troughTol, shareTol float64) {
	behind, front, half := loadCase(behindSpec), loadCase(frontSpec), loadCase(halfSpec)
	var unit float64
	if behind != nil {
		gain, base, perUnit, trough, crests, troughs, same := crestStats(behind)
		unit = perUnit
		fmt.Printf("sun behind the crests: %d crest points gain %+.3f on %.2f (%.2f per unit of predicted term); %d trough points move %.3f, %d unchanged\n",
			crests, gain, base, perUnit, troughs, trough, same)
		if crests < 50 || troughs < 50 {
			fail("too few crest (%d) or trough (%d) points on screen; this check proves nothing", crests, troughs)
		}
		if gain < minGain {
			fail("crests gained %.3f with the sun behind them, want at least %.2f", gain, minGain)
		}
		if trough > troughTol {
			fail("troughs moved %.3f, want at most %.2f", trough, troughTol)
		}
	}
	if front != nil {
		gain, _, _, trough, crests, _, _ := crestStats(front)
		fmt.Printf("sun in front of the camera: %d crest points gain %+.4f, troughs move %.4f\n", crests, gain, trough)
		if crests < 50 {
			fail("too few crest points (%d) on screen; this check proves nothing", crests)
		}
		if math.Abs(gain) > troughTol || trough > troughTol {
			fail("with the sun behind the eye the term should be near zero, got crests %+.4f troughs %.4f", gain, trough)
		}
	}
	if half != nil && behind != nil {
		gain, base, perUnit, _, crests, _, _ := crestStats(half)
		ratio := perUnit / unit
		fmt.Printf("WaveAmplitude halved: %d crest points gain %+.3f on %.2f (%.2f per unit, %.3f of the full sea's)\n", crests, gain, base, perUnit, ratio)
		if crests < 50 {
			fail("too few crest points (%d) on screen; this check proves nothing", crests)
		}
		if unit <= 0 || math.Abs(ratio-1) > shareTol {
			fail("the term per unit of normalised crest changed by %.0f%% with the amplitude halved, want within %.0f%%: it is following the waves' size rather than their shape", 100*(ratio-1), 100*shareTol)
		}
	}
}

// measureDefaults prints the numbers DefaultWaterFoam's threshold and decay
// were chosen from, over the grid of the dump given: the Jacobian's spread at
// DefaultWaterOptions' sea state and at steeper ones, and how long one crest
// keeps a point under the threshold against how long until the next one does.
func measureDefaults(d *dump) {
	g := d.Grid
	def := glyph.DefaultWaterOptions(d.Options.Level)
	th := glyph.DefaultWaterFoam().Threshold
	for _, sea := range []struct {
		name    string
		amp     float32
		spacing float32
	}{
		{"DefaultWaterOptions, Resolution 160", def.WaveAmplitude, 200.0 / 160},
		{"DefaultWaterOptions on 09-water's Resolution 200", def.WaveAmplitude, g.Spacing()},
		{"WaveAmplitude 0.3", 0.3, g.Spacing()},
		{"WaveAmplitude 0.6 (steep)", 0.6, g.Spacing()},
	} {
		o := def
		o.WaveAmplitude = sea.amp
		var js []float64
		for t := float32(0); t < 60; t += 0.5 {
			for iz := 0; iz < g.Height; iz += 2 {
				for ix := 0; ix < g.Width; ix += 2 {
					x, z, depth := g.Point(ix, iz)
					if depth < 1.5 {
						continue
					}
					js = append(js, float64(o.SurfaceAt(x, z, depth, sea.spacing, t).Jacobian))
				}
			}
		}
		sort.Float64s(js)
		q := func(p float64) float64 { return js[int(p*float64(len(js)-1))] }
		below := float64(sort.SearchFloat64s(js, float64(th))) / float64(len(js))
		full := float64(sort.SearchFloat64s(js, float64(th)-0.1)) / float64(len(js))
		fmt.Printf("%s: %d samples, Jacobian min %.4f, 1st pct %.4f, median %.4f; under the threshold %.3f: %.3f%% (fully foamed %.3f%%)\n",
			sea.name, len(js), js[0], q(0.01), q(0.5), th, 100*below, 100*full)

		var episodes, gaps []float64
		for iz := 0; iz < g.Height; iz += 7 {
			for ix := 0; ix < g.Width; ix += 7 {
				x, z, depth := g.Point(ix, iz)
				if depth < 1.5 {
					continue
				}
				in, start, end := false, float32(0), float32(-1)
				for f := 0; f < 60*40; f++ {
					t := float32(f) / 60
					under := o.SurfaceAt(x, z, depth, sea.spacing, t).Jacobian < th
					switch {
					case under && !in:
						in, start = true, t
						if end >= 0 {
							gaps = append(gaps, float64(t-end))
						}
					case !under && in:
						in, end = false, t
						if start > 0 {
							episodes = append(episodes, float64(t-start))
						}
					}
				}
			}
		}
		if len(episodes) == 0 {
			fmt.Println("    no point went under the threshold in 40 s")
			continue
		}
		sort.Float64s(episodes)
		sort.Float64s(gaps)
		p := func(a []float64, f float64) float64 {
			if len(a) == 0 {
				return math.NaN()
			}
			return a[int(f*float64(len(a)-1))]
		}
		fmt.Printf("    a crest keeps a point under it for %.2f s (median; 10th-90th %.2f-%.2f), and the next arrives %.2f s later (median; 10th %.2f)\n",
			p(episodes, .5), p(episodes, .1), p(episodes, .9), p(gaps, .5), p(gaps, .1))
	}
}
