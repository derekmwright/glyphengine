// Command lutskycheck is x/sky/lut's gate.
//
// The scene is the smallest one that can say anything about a dome: a camera at
// the origin, pitched up so the whole frame is sky, and nothing else in the
// world at all. No terrain, no geometry, no lights. Every pixel in every capture
// here is the dome, which is what makes a band of rows a reading of the sky at an
// elevation rather than a reading of whatever was in front of it, and what makes
// the sky pass's GPU bracket the cost of shading a full frame of sky rather than
// the cost of the leftovers around a hill.
//
//	-sky lut -time 0.25 -facing away -screenshot dawn.png
//	-sky none ...                      the same frame with the slot empty
//	-sky xsky / -sky xskydome ...      x/sky, whole and dome-only
//	-sky lut -flat ...                 the LUT bound to one constant texel
//	-gradient                          the three-hour shape check
//	-cost                              the interleaved cost comparison
//	-measure a.png                     print the bands and the box of a capture
//
// Every rendering mode needs GLYPHENGINE_FIXED_FRAME_TIME; without it two runs of
// the same build differ by more than the thing being measured (AGENTS.md rule
// 13), and -gradient and -cost refuse to run without it rather than reporting a
// coin toss.
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
	"github.com/derekmwright/glyphengine/renderer"
	xsky "github.com/derekmwright/glyphengine/x/sky"
	skylut "github.com/derekmwright/glyphengine/x/sky/lut"
)

func init() { runtime.LockOSThread() }

const (
	// 640x480 and a 60-degree vertical field of view, pitched up 0.6 radians.
	// That puts the frame between view elevations of 4.4 and 64.4 degrees: all
	// sky, none of it below the horizon, and none of it at the zenith.
	//
	// Staying off the zenith is deliberate and it is why noon is not one of the
	// hours below. The gradient check reads the half of the sky away from the
	// sun, and with the sun overhead there is no such half -- its halo reaches
	// the top of any frame that contains the zenith, and the check would be
	// measuring the halo while claiming to measure the gradient.
	width  = 640
	height = 480
	fovY   = 60.0

	// defaultPitch keeps the horizon out of the frame. -pitch 0 puts it across
	// the middle, which is the one camera that samples the view axis where its
	// derivative is unbounded; see the comment on textureLod in skylut.frag and
	// the reading in lut.md.
	defaultPitch = 0.6
)

// The three hours. Sunrise with the sun exactly on the horizon, mid-morning with
// it well up, and the blue hour with it below. Together they cover the twilight
// lobe at its peak, full daylight, and the night palette with a trace of
// twilight left in it -- which is the whole span the sun-elevation axis of the
// table has to get right.
//
// Dusk is at 0.78 rather than 0.75 so that it is genuinely past sunset (sun
// elevation -0.183) instead of the mirror image of sunrise.
var hours = []struct {
	name string
	tod  float64
}{
	{"dawn", 0.25},
	{"morning", 0.35},
	{"dusk", 0.78},
}

// bands is how many horizontal strips the frame is read in, and measureX is the
// central column range they are averaged over.
//
// Bands rather than rows because the tonemap output is eight bits and one row of
// a smooth gradient can be the same byte as the row above it; 12 bands of 40 rows
// each is coarse enough that every step is a real step. The central half of the
// width, because the frame's left and right edges are a few degrees round in
// azimuth from its centre and the sun's wash is not azimuth-symmetric.
const (
	bands        = 12
	measureXFrom = width / 4
	measureXTo   = width - width/4
)

// measureBox is where the sun-side comparison is read: the middle of the frame,
// around a view elevation of 34 degrees, well clear of both the horizon band the
// two sides share and the frame edges.
var measureBox = image.Rect(measureXFrom, 180, measureXTo, 300)

// ── the scene ──

type game struct {
	mode   string // lut, none, xsky, xskydome
	tod    float32
	facing float32 // +1 toward the sun, -1 away
	pitch  float64
	flat   bool

	// Cost measurement. GPU timings are reset after warmupFrames so the mean is
	// the steady state rather than the first frames, where pipelines are still
	// being compiled and caches are cold.
	measure bool
	timings renderer.GPUTimings

	sky     *skylut.Sky
	flatTex *renderer.Texture
}

// warmupFrames is how many frames are discarded before the GPU timing mean
// starts. The timer's own doc says a single frame's reading swings by several
// percent; the first dozen swing by much more than that.
const warmupFrames = 60

func (g *game) Init(e *glyph.Engine) error {
	if g.mode == "lut" || g.mode == "none" {
		opts := skylut.DefaultOptions()
		opts.TimeOfDay = g.tod
		// No fog: there is no geometry for it to fade, and a fog density in the
		// state is one more thing differing between the four configurations the
		// cost comparison brackets.
		opts.Fog = nil
		sky, err := skylut.New(e.Renderer(), opts)
		if err != nil {
			return err
		}
		g.sky = sky
		e.Scene.Env = sky

		if g.flat {
			// The break: one constant texel over the whole table, bound to the
			// same slot the sky was bound to, from outside the package. This is
			// "sample a constant texel" done without touching the shader or
			// needing a Vulkan SDK, so the gate's own break is re-runnable by
			// anyone. 0x51 is sqrt(0.3/3) encoded, so the frame comes out a
			// plausible mid sky rather than black -- a break that produced an
			// obviously empty frame would prove less.
			pix := make([]byte, 2*2*4)
			for i := range pix {
				pix[i] = 0x51
				if i%4 == 3 {
					pix[i] = 0xFF
				}
			}
			tex, err := e.Renderer().CreateDataTexture(pix, 2, 2)
			if err != nil {
				return err
			}
			g.flatTex = tex
			if err := e.Renderer().SetShaderTexture(skylut.ShaderTextureSlot, tex); err != nil {
				return err
			}
		}
		return nil
	}

	// x/sky, for the comparison. DayCycleSource rather than Environment because
	// the hour is what matters and nothing here wants a composite.
	sky := xsky.DefaultSky()
	if g.mode == "xskydome" {
		// The dome on its own, so the sky pass holds one draw on both sides of
		// the comparison: no cloud march to composite, no star pass, no
		// billboards, no shafts.
		sky = &xsky.Sky{}
	}
	e.Scene.Env = &xsky.DayCycleSource{Cycle: xsky.DayNight{TimeOfDay: g.tod}, Sky: sky}
	return nil
}

func (g *game) Update(e *glyph.Engine, _ float32) {
	// The camera is aimed from the sun the environment resolved, not from the
	// clock, so the two skies are aimed by their own answer to the same question
	// and a disagreement about where the sun is cannot hide in the framing.
	sun := e.Scene.Environment().RealSunDir
	h := mgl32.Vec2{sun[0], sun[2]}
	if h.Len() > 0 {
		h = h.Normalize()
	} else {
		h = mgl32.Vec2{1, 0}
	}
	c := float32(math.Cos(g.pitch))
	dir := mgl32.Vec3{h.X() * c * g.facing, float32(math.Sin(g.pitch)), h.Y() * c * g.facing}
	eye := mgl32.Vec3{0, 0, 0}
	e.SetCamera(eye, eye.Add(dir), mgl32.Vec3{0, 1, 0})

	if g.measure && e.FrameCount() == warmupFrames {
		e.Renderer().ResetGPUTimings()
	}
	if g.measure {
		if t := e.Renderer().MeanGPUTimings(); t.Valid {
			g.timings = t
		}
	}
}

func (g *game) FixedUpdate(*glyph.Engine, float32) {}

func (g *game) destroy(e *glyph.Engine) {
	if g.flatTex != nil {
		e.Renderer().DestroyTexture(g.flatTex)
		g.flatTex = nil
	}
	if g.sky != nil {
		g.sky.Destroy(e.Renderer())
		g.sky = nil
	}
}

// ── main ──

func main() {
	mode := flag.String("sky", "lut", "which sky: lut, none (the empty slot), xsky (the whole thing) or xskydome (x/sky's dome alone)")
	tod := flag.Float64("time", 0.25, "time of day in [0,1)")
	facing := flag.String("facing", "away", "aim the camera toward the sun's azimuth (sun) or away from it (away)")
	pitchFlag := flag.Float64("pitch", defaultPitch, "camera pitch in radians; 0 puts the horizon across the middle of the frame")
	flat := flag.Bool("flat", false, "with -sky lut: bind one constant texel over the table, which is this gate's own break")
	frames := flag.Int("frames", 120, "render N frames then exit")
	shot := flag.String("screenshot", "", "write the last frame to this PNG")
	measure := flag.Bool("timings", false, "print the sky pass and whole-frame GPU means after a warm-up")
	gradient := flag.Bool("gradient", false, "run the three-hour shape check and report pass or fail")
	cost := flag.Bool("cost", false, "run the interleaved cost comparison and report pass or fail")
	show := flag.String("measure", "", "comma-separated PNGs: print each one's bands and box and exit")
	out := flag.String("out", ".task/xskylut", "directory the orchestrating modes write captures to")
	flag.Parse()

	if *show != "" {
		for _, p := range strings.Split(*show, ",") {
			p = strings.TrimSpace(p)
			b, err := readBands(p)
			if err != nil {
				log.Fatal(err)
			}
			box, err := readBox(p)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("%s box %.3f bands %s\n", p, box, format(b))
		}
		return
	}
	if *gradient || *cost {
		if os.Getenv("GLYPHENGINE_FIXED_FRAME_TIME") == "" {
			log.Fatal("GLYPHENGINE_FIXED_FRAME_TIME is unset: two runs of the same build differ by more than this package does, so these numbers would be a coin toss (AGENTS.md rule 13)")
		}
		if err := os.MkdirAll(*out, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	if *gradient {
		// -flat composes with -gradient on purpose, so this gate's own break is
		// one command rather than a procedure: `-gradient -flat` binds the
		// constant texel for every LUT capture the shape check takes.
		if err := runGradient(*out, *flat); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *cost {
		if err := runCost(*out); err != nil {
			log.Fatal(err)
		}
		return
	}

	switch *mode {
	case "lut", "none", "xsky", "xskydome":
	default:
		log.Fatalf("-sky must be lut, none, xsky or xskydome, not %q", *mode)
	}
	sign := float32(-1)
	if *facing == "sun" {
		sign = 1
	} else if *facing != "away" {
		log.Fatalf("-facing must be sun or away, not %q", *facing)
	}

	g := &game{mode: *mode, tod: float32(*tod), facing: sign, pitch: *pitchFlag, flat: *flat, measure: *measure}
	opts := []glyph.Option{
		glyph.WithTitle("x/sky/lut check"),
		glyph.WithWindowSize(width, height),
		glyph.WithProjection(fovY, 0.1, 1000),
		// MSAA off. There is no geometry to antialias -- every pixel is a
		// fullscreen-triangle fragment -- and a multisampled colour target plus
		// its resolve is cost that belongs to neither sky.
		glyph.WithMSAA(1),
		glyph.WithMaxFrames(*frames),
		glyph.WithScreenshot(*shot),
	}
	switch *mode {
	case "lut":
		opts = append(opts, glyph.WithShaders(skylut.Shaders()))
	case "xsky":
		opts = append(opts, glyph.WithShaders(xsky.Shaders()))
	case "xskydome":
		s := xsky.Shaders()
		s.StarsFrag, s.CloudsFrag = nil, nil
		opts = append(opts, glyph.WithShaders(s))
	case "none":
		// No WithShaders at all: renderer.DefaultShaders leaves the sky slot
		// empty, so the source asks for a dome and the renderer draws none.
	}

	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer e.Destroy()
	if *measure && !e.Renderer().Capabilities().GPUTimestamps {
		log.Fatal("this device cannot timestamp graphics work, so there is no cost to compare; a silent skip here would be a gate that proves nothing")
	}
	e.Run()
	g.destroy(e)
	if *measure {
		t := g.timings
		if !t.Valid {
			log.Fatal("no GPU timings were collected")
		}
		fmt.Printf("timings sky=%.4f clouds=%.4f total=%.4f\n",
			t.Pass[renderer.PassSky], t.Pass[renderer.PassClouds], t.Total)
	}
}

// render re-executes this same binary, so every capture and every timing is made
// by exactly the build that is checking them.
func render(args ...string) (string, error) {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stderr = os.Stderr
	outBytes, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("render %v: %w", args, err)
	}
	return string(outBytes), nil
}

func capture(path string, args ...string) error {
	if _, err := render(append(args, "-screenshot", path)...); err != nil {
		return err
	}
	// A gate whose captures are empty files has shipped in this repository
	// before.
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return fmt.Errorf("%s is empty; this gate would prove nothing", path)
	}
	return nil
}

// ── the shape check ──

// The floors, all of them set from this gate's own readings. Measured 2026-10-03,
// 640x480, frame 120, MSAA off, RX 7900 XTX, under the fixed clock:
//
//	            bottom band   top band   drop    sun side / away   ratio
//	dawn           165.432     82.926    82.51   144.261 / 101.361  1.42
//	morning        198.471    144.571    53.90
//	dusk           104.028     56.627    47.40
//
// Every band of all three is darker than the one below it, and the worst rise
// anywhere in the 33 steps is exactly 0.000: the top three bands of each hour are
// bit-identical, because the gradient's smoothstep saturates at an elevation of
// 0.75 and the dome is then literally one colour.
const (
	// bandTolerance is how far a band may be brighter than the one below it, in
	// counts of mean sRGB luma. Measured worst rise 0.000, so this is slack for a
	// driver that rounds the other way and nothing else -- a quarter of one count,
	// against band means taken over 12,800 pixels each, where a single pixel
	// moving by one count moves the mean by 0.00008.
	bandTolerance = 0.25

	// gradientFloor is how much darker the top band has to be than the bottom
	// one, in counts. 30 against a measured minimum of 47.40 at dusk, which is
	// the darkest of the three hours and therefore the one with the least
	// gradient to show. It is the check the constant-texel break fails, at
	// exactly zero.
	gradientFloor = 30.0

	// sunSideFloor and sunSideRatio are how much brighter the sun's half of the
	// sky has to be than the half opposite, at dawn. Measured 42.90 counts and
	// 1.42; the ratio is the tighter of the two, so it is the one to watch.
	//
	// Both are far below the linear-light ratio of 3.73 the unit check reads out
	// of the same table, and the difference is the tonemap: 1.0465 and 0.2806 of
	// linear luminance arrive as 144 and 101 of 255. That is the reason the two
	// checks are not redundant and the reason this one's floors cannot be
	// borrowed from that one's.
	sunSideFloor = 25.0
	sunSideRatio = 1.25
)

func runGradient(dir string, flat bool) error {
	var problems []string
	note := func(format string, a ...any) {
		problems = append(problems, fmt.Sprintf(format, a...))
	}
	// The break, threaded through every capture of the dome. The empty-slot
	// control is deliberately NOT given it: the control's job is to say the dome
	// is drawing at all, and a flat dome is still a dome.
	lutArgs := func(a ...string) []string {
		a = append([]string{"-sky", "lut"}, a...)
		if flat {
			a = append(a, "-flat")
		}
		return a
	}
	if flat {
		fmt.Println("BREAK: one constant texel bound over the table; the gradient checks must fail")
	}

	for _, h := range hours {
		away := fmt.Sprintf("%s/%s-away.png", dir, h.name)
		if err := capture(away, lutArgs("-time", fmt.Sprint(h.tod), "-facing", "away")...); err != nil {
			return err
		}
		b, err := readBands(away)
		if err != nil {
			return err
		}
		fmt.Printf("%-8s away  bands %s\n", h.name, format(b))

		// Monotonic from the bottom band (nearest the horizon) to the top.
		for i := 0; i+1 < len(b); i++ {
			if b[i+1] > b[i]+bandTolerance {
				note("%s: band %d is %.2f and band %d above it is %.2f; the sky brightens toward the zenith",
					h.name, i, b[i], i+1, b[i+1])
			}
		}
		if drop := b[0] - b[len(b)-1]; drop < gradientFloor {
			note("%s: the horizon-to-zenith drop is %.2f counts, want at least %.0f", h.name, drop, gradientFloor)
		} else {
			fmt.Printf("%-8s away  horizon-to-zenith drop %.2f counts\n", h.name, drop)
		}
	}

	// The sun's side, at dawn.
	dawn := hours[0]
	sunward := fmt.Sprintf("%s/%s-sun.png", dir, dawn.name)
	if err := capture(sunward, lutArgs("-time", fmt.Sprint(dawn.tod), "-facing", "sun")...); err != nil {
		return err
	}
	near, err := readBox(sunward)
	if err != nil {
		return err
	}
	far, err := readBox(fmt.Sprintf("%s/%s-away.png", dir, dawn.name))
	if err != nil {
		return err
	}
	fmt.Printf("%-8s sun %.3f, away %.3f, ratio %.2f\n", dawn.name, near, far, near/far)
	if near < far+sunSideFloor || near/far < sunSideRatio {
		note("%s: the sun's side reads %.2f and the far side %.2f, a ratio of %.2f; want at least %.0f counts and %.2fx",
			dawn.name, near, far, near/far, sunSideFloor, sunSideRatio)
	}

	// The control. With the slot empty the same scene has no dome at all, so if
	// these two captures agree then nothing above was reading the table.
	empty := fmt.Sprintf("%s/%s-empty.png", dir, dawn.name)
	if err := capture(empty, "-sky", "none", "-time", fmt.Sprint(dawn.tod), "-facing", "away"); err != nil {
		return err
	}
	if err := differs(fmt.Sprintf("%s/%s-away.png", dir, dawn.name), empty); err != nil {
		note("the empty slot renders the same frame as the LUT dome: %v", err)
	} else {
		fmt.Println("control: the empty slot renders a different frame, so the dome is being drawn")
	}

	// Repeatability, which is what makes every number above worth printing.
	repeat := fmt.Sprintf("%s/%s-away-repeat.png", dir, dawn.name)
	if err := capture(repeat, lutArgs("-time", fmt.Sprint(dawn.tod), "-facing", "away")...); err != nil {
		return err
	}
	if err := samePixels(repeat, fmt.Sprintf("%s/%s-away.png", dir, dawn.name)); err != nil {
		note("two runs of the same capture differ: %v", err)
	} else {
		fmt.Println("repeat capture: identical under the fixed clock")
	}

	return report(problems, dir)
}

// ── the cost comparison ──

// The cost thresholds. Measured 2026-10-03, RX 7900 XTX, 640x480, MSAA off, three
// interleaved trials of 300 frames each with the first 60 discarded. THREE
// independent runs of the whole comparison, each line the mean of that run's three
// trials:
//
//	           sky pass                  frame                       frame spread
//	none       0.0001  0.0001  0.0001    0.0504  0.0505  0.0494 ms   2.6%  5.9%  0.6%
//	lut        0.0082  0.0082  0.0083    0.0557  0.0554  0.0559 ms   1.6%  0.4%  2.5%
//	xskydome   0.0099  0.0101  0.0101    0.0571  0.0575  0.0575 ms   0.7%  0.7%  2.1%
//	xsky       0.0091  0.0090  0.0091    0.6235  0.6218  0.6218 ms   0.4%  0.5%  1.1%
//
// The cloud pass is 0.0001 ms for every configuration but xsky, where it is
// 0.4849 to 0.4853: with CloudsFrag nil the engine builds no cloud pipeline and
// recordClouds returns before the barrier, so that half-millisecond is the single
// largest thing this package does not spend.
//
// The three ratios the checks below read, per run: 10.5% / 9.6% / 13.0% added to
// the frame, 82.6% / 81.5% / 82.1% of x/sky's dome pass, 8.9% / 8.9% / 9.0% of
// x/sky's whole frame.
//
// Across all nine trials the sky-pass samples are 0.0081 to 0.0085 for lut and
// 0.0099 to 0.0102 for xskydome: the two ranges do not overlap, which is what
// makes 82% a reading rather than a coin toss at this scale.
//
// Two things in that table are worth knowing before reading the checks.
//
// The dome brackets are 8 to 10 MICROseconds, which is small enough that the
// spread matters: the lut pass read 0.0082/0.0083/0.0081 and xskydome's
// 0.0100/0.0099/0.0099, so the 0.0017 gap between them is about eight times
// either one's own spread. That is what makes it a measurement rather than a
// coin toss, and it is why the comparison is made against xskydome -- a
// configuration whose FRAME is the same size as the LUT's -- and not against
// xsky.
//
// Because xsky's sky pass reads 0.0091, LOWER than xskydome's 0.0099, while
// drawing strictly more (the dome plus a sun billboard). Reproducibly: 0.0090,
// 0.0090, 0.0093. The explanation that fits is clock state -- xsky's frame is
// 0.62 ms of work against xskydome's 0.057, so the card is in a higher clock
// state for the whole of it and every pass in that frame is faster. It is
// recorded rather than explained away because it is exactly the trap a
// pass-bracket comparison across two different frame loads falls into, and the
// whole-frame comparison below crosses the same boundary in the direction that
// UNDERSTATES this package's advantage.
const (
	// costBudget is how much of the empty slot's whole-frame cost the LUT dome is
	// allowed to add. The frame is nothing but sky, so this is the worst case for
	// it: every pixel is shaded by the dome with no geometry covering any of them.
	//
	// 0.20 against 10.5%, 9.6% and 13.0% over three runs. It is not tighter
	// because the frames being divided are 50 microseconds: the empty slot's own
	// frame-to-frame spread reached 5.9% in one run, so this ratio carries a
	// 3.4-point swing of its own, and a 15% budget would have 2 points of margin
	// against it.
	costBudget = 0.20

	// domeBudget is the LUT sky pass as a share of x/sky's dome pass. 0.95
	// against a measured 0.826, 0.815 and 0.821: a bare `<` between two ranges that
	// do not overlap would pass just as well, but it would also pass on the day the
	// gap closes to nothing, and the claim on the page is that this dome is cheaper
	// rather than not more expensive.
	domeBudget = 0.95

	// wholeSkyBudget is the LUT frame as a share of x/sky's whole sky. 0.50
	// against a measured 0.089, 0.089 and 0.090 -- eleven times cheaper, nearly all
	// of it the cloud march at 0.485 ms. Loose on purpose: this one is a claim about which
	// package a game should pick on a slow card, and it has an order of magnitude
	// in hand.
	wholeSkyBudget = 0.50
)

type timing struct{ sky, clouds, total float32 }

func runCost(dir string) error {
	configs := []string{"none", "lut", "xskydome", "xsky"}
	const trials = 3
	got := map[string][]timing{}

	// Interleaved: all four configurations in each trial, rather than three runs
	// of one and then three of the next. A warm card reads several percent faster
	// than a cold one, and grouping the runs by configuration charges that
	// difference to whichever one went first.
	for trial := 0; trial < trials; trial++ {
		for _, cfg := range configs {
			out, err := render("-sky", cfg, "-time", "0.35", "-facing", "sun", "-frames", "300", "-timings")
			if err != nil {
				return err
			}
			t, err := parseTimings(out)
			if err != nil {
				return fmt.Errorf("%s trial %d: %w", cfg, trial+1, err)
			}
			got[cfg] = append(got[cfg], t)
			fmt.Printf("trial %d  %-9s sky %.4f  clouds %.4f  frame %.4f ms\n", trial+1, cfg, t.sky, t.clouds, t.total)
		}
	}

	mean := map[string]timing{}
	for _, cfg := range configs {
		var sum timing
		for _, t := range got[cfg] {
			sum.sky += t.sky
			sum.clouds += t.clouds
			sum.total += t.total
		}
		n := float32(trials)
		mean[cfg] = timing{sum.sky / n, sum.clouds / n, sum.total / n}
		fmt.Printf("mean     %-9s sky %.4f  clouds %.4f  frame %.4f ms\n", cfg, mean[cfg].sky, mean[cfg].clouds, mean[cfg].total)
	}

	var problems []string
	note := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	// The dome against the empty slot: the whole frame, because the sky pass's
	// own bracket with an empty slot is two timestamps and nothing between them,
	// and a ratio against nearly zero is not a measurement.
	if over := mean["lut"].total/mean["none"].total - 1; over > costBudget {
		note("the LUT dome adds %.1f%% to the frame over the empty slot, want at most %.0f%%", over*100, costBudget*100)
	} else {
		fmt.Printf("the LUT dome adds %.1f%% to the frame over the empty slot (budget %.0f%%)\n", over*100, costBudget*100)
	}

	// The dome against x/sky's dome, pass bracket to pass bracket: one draw each,
	// the same depth state, the same fullscreen triangle, the same number of
	// pixels.
	if share := mean["lut"].sky / mean["xskydome"].sky; share > domeBudget {
		note("the LUT sky pass is %.4f ms against x/sky's dome at %.4f ms, %.1f%% of it; want at most %.0f%%",
			mean["lut"].sky, mean["xskydome"].sky, share*100, domeBudget*100)
	} else {
		fmt.Printf("the LUT sky pass is %.4f ms against x/sky's dome at %.4f ms, %.1f%% of it\n",
			mean["lut"].sky, mean["xskydome"].sky, share*100)
	}

	// And the whole sky against the whole sky, which is the comparison a game
	// choosing between the two packages is actually making.
	if share := mean["lut"].total / mean["xsky"].total; share > wholeSkyBudget {
		note("the LUT frame is %.4f ms against x/sky's %.4f ms, %.1f%% of it; want at most %.0f%%",
			mean["lut"].total, mean["xsky"].total, share*100, wholeSkyBudget*100)
	} else {
		fmt.Printf("the LUT frame is %.4f ms against x/sky's %.4f ms, %.1f%% of it\n",
			mean["lut"].total, mean["xsky"].total, share*100)
	}

	// The spread within each configuration, printed rather than asserted: three
	// trials that disagree with each other by more than the configurations
	// disagree would make everything above noise, and the reader needs to see
	// that it does not.
	for _, cfg := range configs {
		lo, hi := got[cfg][0].total, got[cfg][0].total
		for _, t := range got[cfg] {
			lo, hi = min32(lo, t.total), max32(hi, t.total)
		}
		fmt.Printf("spread   %-9s frame %.4f to %.4f ms (%.1f%%)\n", cfg, lo, hi, 100*(hi-lo)/lo)
	}

	return report(problems, dir)
}

func parseTimings(out string) (timing, error) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "timings ") {
			continue
		}
		var t timing
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "timings sky=%f clouds=%f total=%f", &t.sky, &t.clouds, &t.total); err != nil {
			return timing{}, fmt.Errorf("unreadable timing line %q: %w", line, err)
		}
		if t.total <= 0 {
			return timing{}, fmt.Errorf("the whole-frame time is %g; nothing was measured", t.total)
		}
		return t, nil
	}
	return timing{}, fmt.Errorf("no timing line in the run's output")
}

func report(problems []string, dir string) error {
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

// ── reading captures ──

// readBands returns the mean sRGB luma of each horizontal band, bottom band
// first, so index 0 is nearest the horizon.
//
// sRGB luma rather than linear: these are tonemapped display values, and
// undoing the transfer would add a curve of this gate's own between the frame
// and the claim. Both are monotonic in the other, so the direction the gradient
// runs is the same either way; only the sizes differ, and the sizes here are
// quoted in counts of 255 for exactly that reason.
func readBands(path string) ([]float64, error) {
	img, err := loadPNG(path)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() < measureXTo || b.Dy() < bands {
		return nil, fmt.Errorf("%s is %v, too small to read %d bands of the central columns", path, b, bands)
	}
	rows := b.Dy() / bands
	out := make([]float64, bands)
	for i := 0; i < bands; i++ {
		// Band 0 is the BOTTOM of the image, which is the lowest elevation.
		yTo := b.Max.Y - i*rows
		yFrom := yTo - rows
		var sum float64
		for y := yFrom; y < yTo; y++ {
			for x := b.Min.X + measureXFrom; x < b.Min.X+measureXTo; x++ {
				sum += lumaAt(img, x, y)
			}
		}
		out[i] = sum / float64(rows*(measureXTo-measureXFrom))
	}
	return out, nil
}

func readBox(path string) (float64, error) {
	img, err := loadPNG(path)
	if err != nil {
		return 0, err
	}
	box := measureBox.Intersect(img.Bounds())
	if box.Empty() {
		return 0, fmt.Errorf("%s: the measure box falls outside the image %v", path, img.Bounds())
	}
	var sum float64
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			sum += lumaAt(img, x, y)
		}
	}
	return sum / float64(box.Dx()*box.Dy()), nil
}

func lumaAt(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return 0.2126*float64(r>>8) + 0.7152*float64(g>>8) + 0.0722*float64(b>>8)
}

func format(b []float64) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprintf("%.3f", v)
	}
	return strings.Join(parts, " ")
}

// samePixels compares pixels rather than file bytes: Go's PNG encoder has
// changed its output across releases for identical images, so a byte compare is
// the wrong oracle. cmd/pngsame in the engine module makes the same point.
func samePixels(a, b string) error {
	n, worst, err := diffPixels(a, b)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%d pixels differ between %s and %s, worst channel %d/255", n, a, b, worst)
	}
	return nil
}

// differs is samePixels inverted, for the control: two captures that are supposed
// to be different frames.
func differs(a, b string) error {
	n, worst, err := diffPixels(a, b)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s and %s are pixel-identical", a, b)
	}
	// A handful of last-bit pixels is not a dome. The dome covers every pixel of
	// these frames, so a real difference is the whole image.
	total := bands * (height / bands) * width
	if n < total/2 || worst < 8 {
		return fmt.Errorf("only %d of %d pixels differ between %s and %s, worst channel %d/255", n, total, a, b, worst)
	}
	return nil
}

func diffPixels(a, b string) (int, int, error) {
	ia, err := loadPNG(a)
	if err != nil {
		return 0, 0, err
	}
	ib, err := loadPNG(b)
	if err != nil {
		return 0, 0, err
	}
	if ia.Bounds() != ib.Bounds() {
		return 0, 0, fmt.Errorf("%s is %v and %s is %v", a, ia.Bounds(), b, ib.Bounds())
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
	return differing, worst, nil
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
