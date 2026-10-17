// Command tex3dcheck is issue #190's acceptance as a check: a small known
// volume, sampled by an application graphics pass and by an application
// compute pass, interpolates along Z the way it does along X and Y, repeats
// continuously on every axis, holds its edge slice where Z clamps, and reads
// the right level out of both a supplied chain and a generated one -- while the
// volumes are replaced with a different extent, destroyed with frames in
// flight, resized under, and fed every unsupported input, with validation on.
//
// renderer/texture3d_test.go checks what reaches the driver with no GPU: the
// image type, the extents, the per-level copies and blits, the sampler's three
// axes. What it cannot check is what a sampler3D reads back, and that is the
// whole claim: a 3D view with the right extent whose Z slices were uploaded in
// the wrong order, or whose chain was blitted without filtering in Z, would
// pass every unit assertion and sample wrong.
//
// Three arms, each a volume with a distinct value per texel: 8x8x8 RGBA8,
// 16x8x4 RGBA8 (the chain floors to one in Z before X) and 8x4x4 R8. Each
// arm uploads the volume three ways -- a supplied two-level chain repeating on
// every axis, a generated chain repeating on every axis, and the supplied chain
// clamping in Z -- and a graphics pass and a compute dispatch each probe eleven
// points of each into a chart that a display pass stretches over the frame.
// The expected values are a CPU trilinear sampler over the same bytes; every
// chosen point lands on a multiple of 1/255, so an 8-bit capture is exact to
// its own precision and the tolerance is the readout's, not the claim's.
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

//go:embed probe.frag.spv
var probeFrag []byte

//go:embed probe.comp.spv
var probeComp []byte

//go:embed display.frag.spv
var displayFrag []byte

const (
	columns = 11
	rows    = 3

	// The readout's tolerance. Every expected value is a multiple of 1/255 and
	// the chart holds half-floats, so what is left is the swapchain's sRGB
	// round trip, which cmd/apppasscheck -filter measured at 0.0015 (0.37/255).
	tolerance = 1.5 / 255
)

var rowNames = [rows]string{"supplied chain, repeat XYZ", "generated chain, repeat XYZ", "supplied chain, clamp Z"}

var columnNames = [columns]string{
	"texel (1,2,3)", "Z midpoint", "X midpoint", "Y midpoint",
	"X edge", "Y edge", "Z edge", "far Z edge", "Z -0.25 texel",
	"level 1 (0,1,1)", "level 2 (0,0,0)",
}

func main() {
	runtime.LockOSThread()
	var (
		frames   = flag.Int("frames", 12, "frames per arm; the capture is the arm's last")
		validate = flag.Bool("validate", false, "enable the Vulkan validation layer and require silence")
		bind2D   = flag.Bool("bind2d", false, "the gate's own break: bind a 2D texture where the shader declares sampler3D; creation must refuse it, and if that refusal is disabled the sampled values must be wrong")
		out      = flag.String("out", filepath.Join(".task", "tex3d"), "directory for the captures")
	)
	flag.Parse()
	var messages bytes.Buffer
	log.SetOutput(io.MultiWriter(os.Stderr, &messages))
	status, err := run(*frames, *validate, *bind2D, *out)
	log.SetOutput(os.Stderr)
	if err != nil {
		log.Fatalf("tex3dcheck: %v", err)
	}
	if bytes.Contains(messages.Bytes(), []byte("VULKAN ERROR")) || bytes.Contains(messages.Bytes(), []byte("VULKAN WARNING")) {
		log.Fatal("Vulkan validation was not silent")
	}
	if *validate && !bytes.Contains(messages.Bytes(), []byte("Vulkan validation layer enabled")) {
		log.Fatal("validation layer did not run")
	}
	if status == 0 {
		fmt.Println("tex3dcheck: PASS")
	}
	os.Exit(status)
}

// arm is one extent and format, with the value every texel holds.
type arm struct {
	name    string
	w, h, d int
	format  renderer.Texture3DFormat
	ch      int // bytes per texel
	value   func(x, y, z int) [4]byte
}

var arms = []arm{
	{"8x8x8 RGBA8", 8, 8, 8, renderer.Texture3DRGBA8, 4, func(x, y, z int) [4]byte {
		return [4]byte{byte(x*32 + 16), byte(y*32 + 16), byte(z*32 + 16), 255}
	}},
	{"16x8x4 RGBA8", 16, 8, 4, renderer.Texture3DRGBA8, 4, func(x, y, z int) [4]byte {
		return [4]byte{byte(x*16 + 8), byte(y*32 + 16), byte(z*64 + 32), 255}
	}},
	// R8 holds 256 values and 8x4x4 is 128 texels, so every texel is distinct
	// here too, stepping by two.
	{"8x4x4 R8", 8, 4, 4, renderer.Texture3DR8, 1, func(x, y, z int) [4]byte {
		return [4]byte{byte(2 * (x + 8*y + 32*z)), 0, 0, 255}
	}},
}

// base is the arm's level 0, x fastest.
func (a arm) base() []byte {
	pix := make([]byte, 0, a.w*a.h*a.d*a.ch)
	for z := range a.d {
		for y := range a.h {
			for x := range a.w {
				v := a.value(x, y, z)
				pix = append(pix, v[:a.ch]...)
			}
		}
	}
	return pix
}

// supplied is the arm's level 1 as the caller supplies it: the inverse of the
// base pattern, so that it is nothing a box filter would have produced and a
// read that lands on it cannot be mistaken for a generated level.
func (a arm) supplied() []byte {
	w, h, d := max(a.w/2, 1), max(a.h/2, 1), max(a.d/2, 1)
	pix := make([]byte, 0, w*h*d*a.ch)
	for z := range d {
		for y := range h {
			for x := range w {
				v := a.value(x, y, z)
				for c := range a.ch {
					if c < 3 {
						v[c] = 255 - v[c]
					}
				}
				pix = append(pix, v[:a.ch]...)
			}
		}
	}
	return pix
}

// texel is the base value as the sampler sees it, 0..1 per channel, with the
// channels the format does not hold reading zero and alpha one.
func (a arm) texel(x, y, z int) [3]float64 {
	v := a.value(x, y, z)
	var out [3]float64
	for c := range min(a.ch, 3) {
		out[c] = float64(v[c]) / 255
	}
	return out
}

// sample is the CPU trilinear sampler the GPU is measured against: texel i is
// centred at i+0.5, each axis wraps or clamps on its own, and the eight
// neighbours blend by their distances.
func (a arm) sample(p [3]float64, wrap [3]bool) [3]float64 {
	n := [3]int{a.w, a.h, a.d}
	var i0, i1 [3]int
	var t [3]float64
	for ax := range 3 {
		f := p[ax] - 0.5
		fl := math.Floor(f)
		t[ax] = f - fl
		i0[ax], i1[ax] = int(fl), int(fl)+1
		if wrap[ax] {
			i0[ax] = ((i0[ax] % n[ax]) + n[ax]) % n[ax]
			i1[ax] = ((i1[ax] % n[ax]) + n[ax]) % n[ax]
		} else {
			i0[ax] = min(max(i0[ax], 0), n[ax]-1)
			i1[ax] = min(max(i1[ax], 0), n[ax]-1)
		}
	}
	var out [3]float64
	for dz, wz := range []float64{1 - t[2], t[2]} {
		for dy, wy := range []float64{1 - t[1], t[1]} {
			for dx, wx := range []float64{1 - t[0], t[0]} {
				x, y, z := [2]int{i0[0], i1[0]}[dx], [2]int{i0[1], i1[1]}[dy], [2]int{i0[2], i1[2]}[dz]
				v := a.texel(x, y, z)
				for c := range 3 {
					out[c] += v[c] * wx * wy * wz
				}
			}
		}
	}
	return out
}

// mean is the box mean of the base over [0,x1)x[0,y1)x[0,z1), clipped to the
// extent, which is what a generated level's first texel holds.
func (a arm) mean(x1, y1, z1 int) [3]float64 {
	var out [3]float64
	n := 0
	for z := range min(z1, a.d) {
		for y := range min(y1, a.h) {
			for x := range min(x1, a.w) {
				v := a.texel(x, y, z)
				for c := range 3 {
					out[c] += v[c]
				}
				n++
			}
		}
	}
	for c := range 3 {
		out[c] /= float64(n)
	}
	return out
}

// expected is the value the chart should hold at (col, row), or ok false where
// the column makes no claim about that row.
func (a arm) expected(col, row int) (want [3]float64, ok bool) {
	repeat := [3]bool{true, true, true}
	clampZ := [3]bool{true, true, false}
	wrap := repeat
	if row == 2 {
		wrap = clampZ
	}
	D := float64(a.d)
	points := [...][3]float64{
		{1.5, 2.5, 3.5}, {1.5, 2.5, 3.0}, {2.0, 2.5, 3.5}, {1.5, 2.0, 3.5},
		{0.0, 2.5, 3.5}, {1.5, 0.0, 3.5}, {1.5, 2.5, 0.0}, {1.5, 2.5, D}, {1.5, 2.5, -0.25},
	}
	switch {
	case col < len(points):
		return a.sample(points[col], wrap), true
	case col == 9:
		if row == 1 {
			// Generated: the 2x2x2 box mean under level-1 texel (0,1,1).
			var out [3]float64
			for z := 2; z < 4; z++ {
				for y := 2; y < 4; y++ {
					for x := 0; x < 2; x++ {
						v := a.texel(x, y, z)
						for c := range 3 {
							out[c] += v[c] / 8
						}
					}
				}
			}
			return out, true
		}
		// Supplied: the inverse pattern at level-1 texel (0,1,1).
		v := a.value(0, 1, 1)
		for c := range min(a.ch, 3) {
			want[c] = float64(255-v[c]) / 255
		}
		return want, true
	case col == 10 && row == 1:
		return a.mean(4, 4, 4), true
	}
	return want, false
}

// resources is everything one arm creates, destroyed together with frames in
// flight when the next arm replaces it.
type resources struct {
	volumes []*renderer.Texture
	pass    *renderer.AppPass
	compute *renderer.AppCompute
}

func run(frames int, validate, bind2D bool, out string) (int, error) {
	if frames < 4 {
		return 1, fmt.Errorf("-frames must be at least 4: the descriptor is written per frame slot")
	}
	w, err := window.New(352, 240, "Volume texture probe")
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
	caps := r.Capabilities()
	fmt.Printf("device: %s, 3D extent limit %d, volume mip generation %v\n", caps.GPUName, caps.MaxTexture3DExtent, caps.VolumeMipGeneration)
	if !caps.VolumeMipGeneration {
		return 1, fmt.Errorf("this device cannot generate a volume chain; the generated arm has nothing to measure")
	}

	// The two charts outlive every arm: fixed 11x3, so the display pass that
	// reads them is created once.
	graphicsChart, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "graphics probes", Format: renderer.TargetRGBA16F, Width: columns, Height: rows})
	if err != nil {
		return 1, err
	}
	computeChart, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "compute probes", Format: renderer.TargetRGBA16F, Width: columns, Height: rows, Storage: true})
	if err != nil {
		return 1, err
	}
	display, err := r.CreateAppPass(renderer.AppPassDesc{
		Name: "display probes", Stage: renderer.StageBeforeTonemap, Load: true, Fullscreen: true,
		Reads: []*renderer.Texture{graphicsChart.Texture(), computeChart.Texture()},
		Vert:  shaders.DepthResolveVertSpv, Frag: displayFrag,
	})
	if err != nil {
		return 1, err
	}

	// A 2D texture for the break and for the refusals below.
	flat, err := r.CreateDataTexture([]byte{255, 0, 255, 255}, 1, 1)
	if err != nil {
		return 1, err
	}

	create := func(a arm, firstFlat bool) (*resources, error) {
		res := &resources{}
		// One volume per chart row, in rowNames' order.
		for _, opts := range []renderer.Texture3DOptions{
			{Format: a.format, Filter: renderer.FilterLinear, WrapX: renderer.WrapRepeat, WrapY: renderer.WrapRepeat, WrapZ: renderer.WrapRepeat, Mips: [][]byte{a.supplied()}},
			{Format: a.format, Filter: renderer.FilterLinear, WrapX: renderer.WrapRepeat, WrapY: renderer.WrapRepeat, WrapZ: renderer.WrapRepeat, Mipmap: true},
			{Format: a.format, Filter: renderer.FilterLinear, WrapX: renderer.WrapRepeat, WrapY: renderer.WrapRepeat, WrapZ: renderer.WrapClampToEdge, Mips: [][]byte{a.supplied()}},
		} {
			tex, err := r.CreateTexture3D(a.base(), a.w, a.h, a.d, opts)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", a.name, err)
			}
			res.volumes = append(res.volumes, tex)
		}
		reads := []*renderer.Texture{res.volumes[0], res.volumes[1], res.volumes[2]}
		if firstFlat {
			reads[0] = flat
		}
		reads3D := []int{0, 1, 2}
		res.pass, err = r.CreateAppPass(renderer.AppPassDesc{
			Name: "probe graphics " + a.name, Stage: renderer.StageBeforeScene, Target: graphicsChart, Fullscreen: true,
			Reads: reads, Reads3D: reads3D, Vert: shaders.DepthResolveVertSpv, Frag: probeFrag,
		})
		if err != nil {
			return nil, err
		}
		res.compute, err = r.CreateAppCompute(renderer.AppComputeDesc{
			Name: "probe compute " + a.name, Stage: renderer.StageBeforeScene, Comp: probeComp,
			Reads: reads, Reads3D: reads3D, Writes: []*renderer.RenderTarget{computeChart},
		})
		if err != nil {
			return nil, err
		}
		res.compute.SetDispatch(1, 1, 1)
		var push [16]byte
		binary.LittleEndian.PutUint32(push[0:], math.Float32bits(float32(a.w)))
		binary.LittleEndian.PutUint32(push[4:], math.Float32bits(float32(a.h)))
		binary.LittleEndian.PutUint32(push[8:], math.Float32bits(float32(a.d)))
		if err := res.pass.SetPushConstants(push[:]); err != nil {
			return nil, err
		}
		if err := res.compute.SetPushConstants(push[:]); err != nil {
			return nil, err
		}
		return res, nil
	}
	destroy := func(res *resources) {
		// No idle: the frames that sampled these are still in flight, which is
		// the release contract under test.
		r.DestroyAppPass(res.pass)
		r.DestroyAppCompute(res.compute)
		for _, v := range res.volumes {
			r.DestroyTexture(v)
		}
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return 1, err
	}
	light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4()}
	size := [2]int{352, 240}
	status := 0
	var current *resources
	for i, a := range arms {
		if current != nil {
			destroy(current)
		}
		current, err = create(a, bind2D && i == 0)
		if err != nil {
			if bind2D && i == 0 {
				// The break's expected outcome: refused before the pass exists.
				fmt.Printf("-bind2d: refused at creation, as it must be: %v\n", err)
				return 0, nil
			}
			return 1, err
		}
		if bind2D && i == 0 {
			fmt.Println("-bind2d: a 2D texture where sampler3D is declared was NOT refused; the probes below are what it samples")
		}
		for f := range frames {
			if i == 1 && f == frames/2 {
				// Resize under the middle arm: the charts are fixed-size, the
				// display pass's push constants follow the window, and every
				// pass's input sets are reallocated and rewritten.
				size = [2]int{440, 300}
				w.Handle().SetSize(size[0], size[1])
				r.NotifyResize()
			}
			w.PollEvents()
			if w.WasResized() {
				r.NotifyResize()
			}
			var push [16]byte
			binary.LittleEndian.PutUint32(push[0:], math.Float32bits(float32(size[0])))
			binary.LittleEndian.PutUint32(push[4:], math.Float32bits(float32(size[1])))
			if err := display.SetPushConstants(push[:]); err != nil {
				return 1, err
			}
			if err := r.DrawFrame(nil, nil, nil, nil, nil, light); err != nil {
				return 1, err
			}
		}
		img, err := r.CaptureFrame()
		if err != nil {
			return 1, err
		}
		if img.Bounds().Dx() != size[0] || img.Bounds().Dy() != size[1] {
			return 1, fmt.Errorf("%s: captured %dx%d, want %dx%d", a.name, img.Bounds().Dx(), img.Bounds().Dy(), size[0], size[1])
		}
		if err := r.SaveScreenshot(filepath.Join(out, strings.ReplaceAll(a.name, " ", "-")+".png")); err != nil {
			return 1, err
		}
		if !check(a, img) {
			status = 1
		}
	}
	if bind2D {
		fmt.Println("FAIL -bind2d: the refusal is disabled and the sampled values above are wrong")
		return 1, nil
	}

	// Destroyed with frames in flight, then enough frames for the deferred
	// retirement to run under the layer.
	destroy(current)
	for range 3 {
		w.PollEvents()
		if err := r.DrawFrame(nil, nil, nil, nil, nil, light); err != nil {
			return 1, err
		}
	}

	if !refusals(r, caps, flat, graphicsChart, computeChart) {
		status = 1
	}
	return status, nil
}

// check reads every probe of every row from both halves of the capture and
// compares it with the CPU sampler.
func check(a arm, img *image.RGBA) bool {
	ok := true
	width, height := img.Bounds().Dx(), img.Bounds().Dy()
	worst := 0.0
	for half, stage := range []string{"graphics", "compute"} {
		for row := range rows {
			fmt.Printf("%s %-8s %-28s", a.name, stage, rowNames[row])
			for col := range columns {
				px := img.RGBAAt((2*col+1)*width/(2*columns), (2*(half*rows+row)+1)*height/(2*2*rows))
				got := [3]float64{float64(px.R) / 255, float64(px.G) / 255, float64(px.B) / 255}
				want, claims := a.expected(col, row)
				if !claims {
					fmt.Printf(" %3d/-", px.R)
					continue
				}
				delta := 0.0
				for c := range 3 {
					delta = math.Max(delta, math.Abs(got[c]-want[c]))
				}
				worst = math.Max(worst, delta)
				fmt.Printf(" %3d/%-3.0f", px.R, want[0]*255)
				if delta > tolerance {
					ok = false
					fmt.Printf("\nFAIL %s %s %s, %s: got (%.4f %.4f %.4f), want (%.4f %.4f %.4f), off by %.4f (tolerance %.4f)\n",
						a.name, stage, rowNames[row], columnNames[col], got[0], got[1], got[2], want[0], want[1], want[2], delta, tolerance)
					fmt.Printf("%s %-8s %-28s", a.name, stage, rowNames[row])
				}
			}
			fmt.Println()
		}
	}
	fmt.Printf("%s: worst probe error %.6f (tolerance %.6f), %dx%d capture\n", a.name, worst, tolerance, width, height)
	return ok
}

// refusals feeds the constructor and the binding sites every unsupported input
// the page names, and requires each to be refused with the message it promises.
func refusals(r *renderer.Renderer, caps renderer.Capabilities, flat *renderer.Texture, chart, storageChart *renderer.RenderTarget) bool {
	ok := true
	vol, err := r.CreateTexture3D(make([]byte, 8), 2, 2, 2, renderer.Texture3DOptions{Format: renderer.Texture3DR8})
	if err != nil {
		fmt.Printf("FAIL: a 2x2x2 R8 volume was refused: %v\n", err)
		return false
	}
	defer r.DestroyTexture(vol)
	before := r.ResourceCounts()
	over := caps.MaxTexture3DExtent + 1
	for _, tc := range []struct {
		name string
		want string
		try  func() error
	}{
		{"extent over the limit", "3D image limit", func() error {
			_, err := r.CreateTexture3D(make([]byte, 4), over, 1, 1, renderer.Texture3DOptions{Format: renderer.Texture3DR8})
			return err
		}},
		{"byte count", "want 32 (4 bytes per texel)", func() error {
			_, err := r.CreateTexture3D(make([]byte, 8), 2, 2, 2, renderer.Texture3DOptions{Format: renderer.Texture3DRGBA8})
			return err
		}},
		{"level byte count", "level 1 holds 3 bytes for 1x1x1, want 1", func() error {
			_, err := r.CreateTexture3D(make([]byte, 8), 2, 2, 2, renderer.Texture3DOptions{Format: renderer.Texture3DR8, Mips: [][]byte{{0, 0, 0}}})
			return err
		}},
		{"a level past 1x1x1", "past the 1x1x1", func() error {
			_, err := r.CreateTexture3D(make([]byte, 8), 2, 2, 2, renderer.Texture3DOptions{Format: renderer.Texture3DR8, Mips: [][]byte{{0}, {0}}})
			return err
		}},
		{"Mips and Mipmap", "not both", func() error {
			_, err := r.CreateTexture3D(make([]byte, 8), 2, 2, 2, renderer.Texture3DOptions{Format: renderer.Texture3DR8, Mips: [][]byte{{0}}, Mipmap: true})
			return err
		}},
		{"no format", "not a Texture3DFormat", func() error {
			_, err := r.CreateTexture3D(make([]byte, 8), 2, 2, 2, renderer.Texture3DOptions{})
			return err
		}},
		{"a 2D texture where sampler3D is declared", "index 0 is a 2D texture", func() error {
			_, err := r.CreateAppPass(renderer.AppPassDesc{Name: "refused", Stage: renderer.StageBeforeScene, Target: chart, Fullscreen: true,
				Reads: []*renderer.Texture{flat}, Reads3D: []int{0}, Vert: shaders.DepthResolveVertSpv, Frag: probeFrag})
			return err
		}},
		{"a volume where sampler2D is declared", "index 0 is a 3D texture", func() error {
			_, err := r.CreateAppCompute(renderer.AppComputeDesc{Name: "refused", Stage: renderer.StageBeforeScene, Comp: probeComp,
				Reads: []*renderer.Texture{vol}, Writes: []*renderer.RenderTarget{storageChart}})
			return err
		}},
		{"Reads3D past Reads", "names no read", func() error {
			_, err := r.CreateAppPass(renderer.AppPassDesc{Name: "refused", Stage: renderer.StageBeforeScene, Target: chart, Fullscreen: true,
				Reads: []*renderer.Texture{vol}, Reads3D: []int{1}, Vert: shaders.DepthResolveVertSpv, Frag: probeFrag})
			return err
		}},
		{"a volume in a shared slot", "sampler2D", func() error { return r.SetShaderTexture(0, vol) }},
		{"a volume as a material map", "sampler2D", func() error {
			_, err := r.CreateMaterial(renderer.MaterialOptions{Albedo: vol})
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
	// A refusal creates nothing: the texture, descriptor-set and pass counts
	// are what they were before the first one.
	if after := r.ResourceCounts(); after.Textures != before.Textures || after.DescriptorSets != before.DescriptorSets {
		fmt.Printf("FAIL: the refusals left %d textures and %d descriptor sets, want %d and %d\n",
			after.Textures, after.DescriptorSets, before.Textures, before.DescriptorSets)
		ok = false
	}
	return ok
}
