// Command mipcheck is issue #200's acceptance as a check: a render target with
// a full mip chain, filled level by level by application compute dispatches
// that each read level n and write level n+1 of the same image, sampled with
// hardware LOD and through a view restricted to one level -- with validation
// on, and through a resize, history rotation, a disabled and re-enabled
// reduction and a destroyed and recreated chain.
//
// renderer/targetmips_test.go checks what reaches the driver with no GPU: the
// image's level count, every view's range, the sampler's LOD range, the
// barriers' ranges. What it cannot check is what the shaders read back: a
// reduction reading the wrong level, a sampled view that stops at level 0, or
// a barrier that lets a reduction read a level before the one above it has
// landed would all pass every unit assertion and produce wrong numbers here.
//
// The chain is 64x32 RGBA16F with all seven levels, linear and repeating. A
// seed dispatch writes level 0 (a 4x4 checker in red, a ramp in green, a
// quadratic in blue); six reductions write levels 1..6 as 2x2 box means; a
// probe dispatch writes a 7x4 chart that a display pass stretches over the
// frame. The expected values come from the same reduction on the CPU in
// float64 and a CPU bilinear sampler over the levels it produces.
package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/shaders"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

//go:embed seed.comp.spv
var seedComp []byte

//go:embed reduce.comp.spv
var reduceComp []byte

//go:embed probe.comp.spv
var probeComp []byte

//go:embed display.frag.spv
var displayFrag []byte

const (
	width, height = 64, 32
	levels        = 7
	columns, rows = 7, 4

	// The readout's tolerance. The chain holds half-floats (a relative error
	// of 2^-11, carried through six reductions), the hardware's linear filter
	// weights have eight fractional bits, and the capture is an 8-bit sRGB
	// round trip of the chart that cmd/apppasscheck -filter measured at
	// 0.37/255; none of the expected values is chosen to land on a multiple of
	// 1/255, so half a step of rounding is in here too.
	tolerance = 2.0 / 255

	// Must match probe.comp.
	edgeV = 0.3
)

var lodPoints = [columns][2]float64{{0.3, 0.6}, {0.0, 0.0}, {0.71, 0.13}, {0.5, 0.5}, {0.93, 0.87}, {0.17, 0.41}, {0.62, 0.99}}
var rangeTexels = [columns][2]int{{0, 0}, {1, 0}, {3, 1}, {5, 2}, {7, 3}, {2, 3}, {6, 1}}
var rowNames = [rows]string{"level mean", "LOD 1.5", "repeat edge u=0", "level 3 by range"}

func main() {
	runtime.LockOSThread()
	var (
		frames   = flag.Int("frames", 8, "frames per phase; each phase's capture is its last")
		validate = flag.Bool("validate", false, "enable the Vulkan validation layer and require silence")
		out      = flag.String("out", filepath.Join(".task", "mips"), "directory for the captures")
	)
	flag.Parse()
	var messages bytes.Buffer
	log.SetOutput(io.MultiWriter(os.Stderr, &messages))
	status, err := run(*frames, *validate, *out)
	log.SetOutput(os.Stderr)
	if err != nil {
		log.Fatalf("mipcheck: %v", err)
	}
	if n := bytes.Count(messages.Bytes(), []byte("VULKAN ERROR")) + bytes.Count(messages.Bytes(), []byte("VULKAN WARNING")); n > 0 {
		// Before the pixels are trusted: a frame whose barriers the layer
		// reported is a failed run even when every probe happened to read
		// back right.
		log.Fatalf("mipcheck: FAIL: Vulkan validation reported %d messages", n)
	}
	if *validate && !bytes.Contains(messages.Bytes(), []byte("Vulkan validation layer enabled")) {
		log.Fatal("mipcheck: validation layer did not run")
	}
	if status == 0 {
		fmt.Println("mipcheck: PASS")
	}
	os.Exit(status)
}

type rgb = [3]float64

// model is the chain on the CPU: the seed's level 0 and the same clamped
// 2x2 reduction the shader runs, in float64.
type model struct {
	w, h []int
	tex  [][]rgb
}

func newModel() model {
	m := model{}
	w, h := width, height
	base := make([]rgb, w*h)
	for y := range h {
		for x := range w {
			checker := 0.25
			if (x/4+y/4)%2 == 0 {
				checker = 0.75
			}
			q := float64(y) / float64(h-1)
			base[y*w+x] = rgb{checker, float64(x) / float64(w-1) * 0.8, q * q}
		}
	}
	m.w, m.h, m.tex = append(m.w, w), append(m.h, h), append(m.tex, base)
	for l := 1; l < levels; l++ {
		pw, ph, src := w, h, m.tex[l-1]
		w, h = max(w/2, 1), max(h/2, 1)
		dst := make([]rgb, w*h)
		for y := range h {
			for x := range w {
				var s rgb
				for _, o := range [4][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
					v := src[min(2*y+o[1], ph-1)*pw+min(2*x+o[0], pw-1)]
					for c := range 3 {
						s[c] += v[c] / 4
					}
				}
				dst[y*w+x] = s
			}
		}
		m.w, m.h, m.tex = append(m.w, w), append(m.h, h), append(m.tex, dst)
	}
	return m
}

func (m model) mean(l int) rgb {
	var s rgb
	for _, v := range m.tex[l] {
		for c := range 3 {
			s[c] += v[c] / float64(len(m.tex[l]))
		}
	}
	return s
}

// bilinear samples level l at (u, v) with repeat on both axes: texel i is
// centred at i+0.5, and the four neighbours blend by their distances.
func (m model) bilinear(l int, u, v float64) rgb {
	w, h := m.w[l], m.h[l]
	fx, fy := u*float64(w)-0.5, v*float64(h)-0.5
	x0, y0 := math.Floor(fx), math.Floor(fy)
	tx, ty := fx-x0, fy-y0
	wrap := func(i, n int) int { return ((i % n) + n) % n }
	var out rgb
	for dy, wy := range []float64{1 - ty, ty} {
		for dx, wx := range []float64{1 - tx, tx} {
			t := m.tex[l][wrap(int(y0)+dy, h)*w+wrap(int(x0)+dx, w)]
			for c := range 3 {
				out[c] += t[c] * wx * wy
			}
		}
	}
	return out
}

func (m model) expected(col, row int) rgb {
	switch row {
	case 0:
		return m.mean(col)
	case 1:
		a, b := m.bilinear(1, lodPoints[col][0], lodPoints[col][1]), m.bilinear(2, lodPoints[col][0], lodPoints[col][1])
		return rgb{(a[0] + b[0]) / 2, (a[1] + b[1]) / 2, (a[2] + b[2]) / 2}
	case 2:
		return m.bilinear(col, 0, edgeV)
	default:
		p := rangeTexels[col]
		return m.tex[3][p[1]*m.w[3]+p[0]]
	}
}

// chainSet is everything one chain owns: the target, its seed, its
// reductions and the probe that reads it.
type chainSet struct {
	target  *renderer.RenderTarget
	seed    *renderer.AppCompute
	reduce  []*renderer.AppCompute
	probe   *renderer.AppCompute
	history bool
}

func groups(n uint32) uint32 { return (n + 7) / 8 }

// newChain creates a chain and its passes. With chart nil it is the churn
// chain: swapchain-relative, with History, and no probe.
func newChain(r *renderer.Renderer, name string, desc renderer.RenderTargetDesc, chart *renderer.RenderTarget) (*chainSet, error) {
	t, err := r.CreateRenderTarget(desc)
	if err != nil {
		return nil, err
	}
	c := &chainSet{target: t, history: desc.History}
	c.seed, err = r.CreateAppCompute(renderer.AppComputeDesc{Name: name + " seed", Stage: renderer.StageBeforeScene, Comp: seedComp,
		Writes: []*renderer.RenderTarget{t}})
	if err != nil {
		return nil, err
	}
	for n := range t.Levels() - 1 {
		p, err := r.CreateAppCompute(renderer.AppComputeDesc{Name: fmt.Sprintf("%s reduce %d", name, n), Stage: renderer.StageBeforeScene, Comp: reduceComp,
			Reads: []*renderer.Texture{t.Texture()}, ReadRanges: []renderer.LevelRange{{Base: n, Count: 1}},
			Writes: []*renderer.RenderTarget{t}, WriteLevels: []int{n + 1}})
		if err != nil {
			return nil, err
		}
		c.reduce = append(c.reduce, p)
	}
	if chart != nil {
		c.probe, err = r.CreateAppCompute(renderer.AppComputeDesc{Name: name + " probe", Stage: renderer.StageBeforeScene, Comp: probeComp,
			Reads: []*renderer.Texture{t.Texture(), t.Texture()}, ReadRanges: []renderer.LevelRange{{}, {Base: 3, Count: 1}},
			Writes: []*renderer.RenderTarget{chart}})
		if err != nil {
			return nil, err
		}
		c.probe.SetDispatch(1, 1, 1)
	}
	c.size()
	return c, nil
}

// size sets every dispatch from the extent of the level it writes, which is
// the point of LevelExtent: a reduction sized from Extent would run 64x32
// invocations into a 2x1 level.
func (c *chainSet) size() {
	w, h := c.target.LevelExtent(0)
	c.seed.SetDispatch(groups(w), groups(h), 1)
	for n, p := range c.reduce {
		w, h := c.target.LevelExtent(n + 1)
		p.SetDispatch(groups(w), groups(h), 1)
	}
}

func (c *chainSet) destroy(r *renderer.Renderer) {
	// No idle: the frames that used these are still in flight, which is the
	// release contract under test. Destroying the target destroys its writers.
	if c.probe != nil {
		r.DestroyAppCompute(c.probe)
	}
	r.DestroyRenderTarget(c.target)
}

func run(frames int, validate bool, out string) (int, error) {
	if frames < 4 {
		return 1, fmt.Errorf("-frames must be at least 4: the descriptors are written per frame slot")
	}
	w, err := window.New(350, 240, "Mip chain probe")
	if err != nil {
		return 1, err
	}
	defer w.Destroy()
	// MSAA off: a fullscreen pass into a multisampled target is resolved by
	// averaging, and an averaged cell is a blend of two probes.
	r, err := renderer.New(w, renderer.WithValidation(validate), renderer.WithMSAASamples(1))
	if err != nil {
		return 1, err
	}
	defer r.Destroy()
	fmt.Printf("device: %s\n", r.Capabilities().GPUName)

	chart, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "probes", Format: renderer.TargetRGBA16F, Width: columns, Height: rows, Storage: true})
	if err != nil {
		return 1, err
	}
	display, err := r.CreateAppPass(renderer.AppPassDesc{
		Name: "display probes", Stage: renderer.StageBeforeTonemap, Load: true, Fullscreen: true,
		Reads: []*renderer.Texture{chart.Texture()}, Vert: shaders.DepthResolveVertSpv, Frag: displayFrag,
	})
	if err != nil {
		return 1, err
	}
	mainDesc := renderer.RenderTargetDesc{Name: "chain", Format: renderer.TargetRGBA16F, Width: width, Height: height, MipLevels: levels,
		Storage: true, Filter: renderer.FilterLinear, Wrap: renderer.WrapRepeat}
	chain, err := newChain(r, "chain", mainDesc, chart)
	if err != nil {
		return 1, err
	}
	fmt.Printf("chain: %d levels:", chain.target.Levels())
	for l := range chain.target.Levels() {
		lw, lh := chain.target.LevelExtent(l)
		fmt.Printf(" %dx%d", lw, lh)
	}
	fmt.Println()
	// The churn chain is swapchain-relative with History, so a resize
	// recreates its whole chain and every frame rotates which copy is read.
	churnDesc := renderer.RenderTargetDesc{Name: "churn chain", Format: renderer.TargetRGBA16F, Scale: 0.25, MipLevels: 5,
		Storage: true, History: true, Filter: renderer.FilterLinear}
	churn, err := newChain(r, "churn", churnDesc, nil)
	if err != nil {
		return 1, err
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return 1, err
	}
	m := newModel()
	light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4()}
	size := [2]int{350, 240}
	draw := func(n int, each func(f int)) error {
		for f := range n {
			if each != nil {
				each(f)
			}
			w.PollEvents()
			if w.WasResized() {
				r.NotifyResize()
			}
			var push [16]byte
			binary.LittleEndian.PutUint32(push[0:], math.Float32bits(float32(size[0])))
			binary.LittleEndian.PutUint32(push[4:], math.Float32bits(float32(size[1])))
			if err := display.SetPushConstants(push[:]); err != nil {
				return err
			}
			if err := r.DrawFrame(nil, nil, nil, nil, nil, light); err != nil {
				return err
			}
		}
		return nil
	}
	status := 0
	capture := func(phase string) error {
		img, err := r.CaptureFrame()
		if err != nil {
			return err
		}
		if img.Bounds().Dx() != size[0] || img.Bounds().Dy() != size[1] {
			return fmt.Errorf("%s: captured %dx%d, want %dx%d", phase, img.Bounds().Dx(), img.Bounds().Dy(), size[0], size[1])
		}
		if err := r.SaveScreenshot(filepath.Join(out, phase+".png")); err != nil {
			return err
		}
		if !check(phase, m, img) {
			status = 1
		}
		return nil
	}

	if err := draw(frames, nil); err != nil {
		return 1, err
	}
	if err := capture("first"); err != nil {
		return 1, err
	}

	// Resize mid-run: the churn chain is rebuilt at a quarter of the new
	// extent, every pass's sets are reallocated, and the probes must not move.
	if err := draw(frames, func(f int) {
		if f == frames/2 {
			size = [2]int{440, 300}
			w.Handle().SetSize(size[0], size[1])
			r.NotifyResize()
		}
	}); err != nil {
		return 1, err
	}
	churn.size()
	if err := draw(frames, nil); err != nil {
		return 1, err
	}
	fmt.Printf("churn chain after resize: %d levels, base %dx%d\n", churn.target.Levels(), first(churn.target.LevelExtent(0)), second(churn.target.LevelExtent(0)))
	if err := capture("resized"); err != nil {
		return 1, err
	}

	// Disable and re-enable a reduction in each chain. While level 3 of the
	// main chain is not rewritten it keeps last frame's identical values, so
	// the capture after re-enabling is the same chart.
	if err := draw(frames, func(f int) {
		on := f >= frames/2
		chain.reduce[2].SetEnabled(on)
		churn.reduce[1].SetEnabled(on)
	}); err != nil {
		return 1, err
	}
	if err := capture("reenabled"); err != nil {
		return 1, err
	}

	// Destroy both chains with frames in flight and build them again.
	chain.destroy(r)
	churn.destroy(r)
	if err := draw(3, nil); err != nil {
		return 1, err
	}
	if chain, err = newChain(r, "chain", mainDesc, chart); err != nil {
		return 1, err
	}
	if churn, err = newChain(r, "churn", churnDesc, nil); err != nil {
		return 1, err
	}
	if err := draw(frames, nil); err != nil {
		return 1, err
	}
	if err := capture("recreated"); err != nil {
		return 1, err
	}

	if !refusals(r, chain.target) {
		status = 1
	}
	chain.destroy(r)
	churn.destroy(r)
	if err := draw(3, nil); err != nil {
		return 1, err
	}
	return status, nil
}

func first(a, _ uint32) uint32  { return a }
func second(_, b uint32) uint32 { return b }

// check reads every probe from the capture and compares it with the model.
func check(phase string, m model, img *image.RGBA) bool {
	ok := true
	wd, ht := img.Bounds().Dx(), img.Bounds().Dy()
	worst := 0.0
	base := m.mean(0)
	for row := range rows {
		fmt.Printf("%-10s %-17s", phase, rowNames[row])
		for col := range columns {
			px := img.RGBAAt((2*col+1)*wd/(2*columns), (2*row+1)*ht/(2*rows))
			got := rgb{float64(px.R) / 255, float64(px.G) / 255, float64(px.B) / 255}
			want := m.expected(col, row)
			delta := 0.0
			for c := range 3 {
				delta = math.Max(delta, math.Abs(got[c]-want[c]))
			}
			worst = math.Max(worst, delta)
			fmt.Printf(" (%3d %3d %3d)", px.R, px.G, px.B)
			if delta > tolerance {
				ok = false
				fmt.Printf("\nFAIL %s %s column %d: got (%.4f %.4f %.4f), want (%.4f %.4f %.4f), off by %.4f (tolerance %.4f)\n%-10s %-17s",
					phase, rowNames[row], col, got[0], got[1], got[2], want[0], want[1], want[2], delta, tolerance, phase, rowNames[row])
			}
			// Constant preservation: every level's mean is the base's mean,
			// measured against the model's base rather than the model's level.
			if row == 0 {
				for c := range 3 {
					if math.Abs(got[c]-base[c]) > tolerance {
						ok = false
						fmt.Printf("\nFAIL %s: level %d mean %.4f in channel %d, base mean %.4f\n%-10s %-17s", phase, col, got[c], c, base[c], phase, rowNames[row])
					}
				}
			}
		}
		fmt.Println()
	}
	fmt.Printf("%s: base mean (%.4f %.4f %.4f), worst probe error %.6f (tolerance %.6f), %dx%d capture\n",
		phase, base[0], base[1], base[2], worst, tolerance, wd, ht)
	return ok
}

// refusals feeds the declarations every unsupported input and requires each
// to be refused at creation with the message the page promises.
func refusals(r *renderer.Renderer, chain *renderer.RenderTarget) bool {
	ok := true
	flat, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "flat", Format: renderer.TargetRGBA16F, Width: 8, Height: 8, Storage: true})
	if err != nil {
		fmt.Printf("FAIL: a single-level target was refused: %v\n", err)
		return false
	}
	defer r.DestroyRenderTarget(flat)
	before := r.ResourceCounts()
	reduce := func(read renderer.LevelRange, write int, target *renderer.RenderTarget) error {
		_, err := r.CreateAppCompute(renderer.AppComputeDesc{Name: "refused", Stage: renderer.StageBeforeScene, Comp: reduceComp,
			Reads: []*renderer.Texture{chain.Texture()}, ReadRanges: []renderer.LevelRange{read},
			Writes: []*renderer.RenderTarget{target}, WriteLevels: []int{write}})
		return err
	}
	for _, tc := range []struct {
		name, want string
		try        func() error
	}{
		{"an overlapping read and write", `reads levels [2,4) of target "chain", overlapping level 3`, func() error { return reduce(renderer.LevelRange{Base: 2, Count: 2}, 3, chain) }},
		{"the whole chain read while a level is written", `reads levels [0,7) of target "chain", overlapping level 1`, func() error { return reduce(renderer.LevelRange{}, 1, chain) }},
		{"a level past the chain", `selects level 7 of target "chain", which has 7 levels`, func() error { return reduce(renderer.LevelRange{Base: 0, Count: 1}, 7, chain) }},
		{"a range past the chain", `selects levels [5,8) of target "chain", which has 7 levels`, func() error { return reduce(renderer.LevelRange{Base: 5, Count: 3}, 1, chain) }},
		{"a level on a single-level target", `selects level 1 of target "flat", which has one level`, func() error { return reduce(renderer.LevelRange{Base: 0, Count: 1}, 1, flat) }},
		{"a chain longer than its extent", "MipLevels: 8 levels exceed the 7 a 64x32 chain has", func() error {
			_, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "long", Format: renderer.TargetRGBA16F, Width: 64, Height: 32, MipLevels: 8})
			return err
		}},
	} {
		err := tc.try()
		switch {
		case err == nil:
			fmt.Printf("FAIL refusal %s: accepted\n", tc.name)
			ok = false
		case !strings.Contains(err.Error(), tc.want):
			fmt.Printf("FAIL refusal %s: %v does not say %q\n", tc.name, err, tc.want)
			ok = false
		default:
			fmt.Printf("refused %s: %v\n", tc.name, err)
		}
	}
	if after := r.ResourceCounts(); after.Textures != before.Textures || after.DescriptorSets != before.DescriptorSets {
		fmt.Printf("FAIL: the refusals left %d textures and %d descriptor sets, want %d and %d\n",
			after.Textures, after.DescriptorSets, before.Textures, before.DescriptorSets)
		ok = false
	}
	return ok
}
