// Command widetexcheck measures what a CPU-uploaded half-float texture costs in
// precision against an eight-bit one, on the GPU, through the sampler.
//
// The unit tests in renderer/widetexture_test.go check that the right format and
// the right bytes reach the driver. They cannot check what a shader reads back,
// because there is no device behind them -- and reading back is the whole claim:
// a texture created with the right VkFormat and sampled through a view that
// disagreed with it would pass every one of them.
//
// What it does: build a 256-entry ramp spanning 0.001 to 2.0, upload it three
// ways -- R16G16B16A16_SFLOAT, R32_SFLOAT (the reference, exact) and R8G8B8A8
// holding sqrt(v/3) (the control, which is the transfer x/sky/lut used before the
// wide upload existed) -- bind two of them through Renderer.SetShaderTexture, and
// have a fullscreen pass write the relative difference into the frame at three
// gains. The capture is 8-bit, so the gains are what let it resolve a relative
// error down to 4e-6; see probe.frag.
//
// Three decades is the range on purpose. Eight bits cannot hold it however the
// levels are spread, which is the measurement the control produces and the reason
// the wide formats exist.
package main

import (
	_ "embed"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/shaders"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

//go:embed probe.frag.spv
var probeFrag []byte

const (
	// rampLen entries, one texel each, and a frame two pixels per entry wide so
	// every entry is read at a column centre rather than at a boundary where the
	// integer truncation in probe.frag could land either side.
	rampLen = 256
	frameW  = 2 * rampLen
	frameH  = 256

	// The range the ramp spans. 0.001 to 2.0 is the sky table's own: a night dome
	// is thousandths of the daylit horizon, and a sun halo is above 1.
	rampLo = 0.001
	rampHi = 2.0

	// The control's transfer. sqrt(v/eightBitRange), stored in a UNORM texture --
	// what x/sky/lut did before issue #178, and the best eight bits can do over
	// this range.
	eightBitRange = 3.0
)

func main() {
	runtime.LockOSThread()
	var (
		frames   = flag.Int("frames", 12, "frames to render before capturing")
		eightBit = flag.Bool("eightbit", false, "make the asserted claim about the 8-bit texture instead; this is the gate's own break, and it must fail")
		validate = flag.Bool("validate", false, "enable the Vulkan validation layer")
		out      = flag.String("out", filepath.Join(".task", "widetex"), "directory for the captures")
	)
	flag.Parse()
	// run returns the gate's status rather than exiting itself: its deferred
	// Renderer.Destroy and Window.Destroy are what make this check
	// validation-clean, and os.Exit runs no defers.
	status, err := run(*frames, *eightBit, *validate, *out)
	if err != nil {
		log.Fatalf("widetexcheck: %v", err)
	}
	os.Exit(status)
}

// ramp is the values under test, log-spaced so every decade gets the same number
// of entries. Linear spacing would put 99 percent of them in the top decade,
// where eight bits are nearly adequate, and the interesting end is the bottom.
func ramp() []float32 {
	v := make([]float32, rampLen)
	for i := range v {
		v[i] = float32(rampLo * math.Pow(rampHi/rampLo, float64(i)/float64(rampLen-1)))
	}
	return v
}

func run(frames int, eightBit, validate bool, out string) (int, error) {
	values := ramp()

	w, err := window.New(frameW, frameH, "Wide texture precision probe")
	if err != nil {
		return 1, err
	}
	defer w.Destroy()
	// MSAA off: a fullscreen pass into a multisampled target is resolved by
	// averaging, and an averaged probe column is a blend of two ramp entries.
	r, err := renderer.New(w, renderer.WithValidation(validate), renderer.WithMSAASamples(1))
	if err != nil {
		return 1, err
	}
	defer r.Destroy()

	// The reference. R32_SFLOAT holds each float32 exactly, so every number below
	// is the measured texture's error and nothing else -- including none of the
	// error of whatever the expectation was computed with.
	exactPix := make([]float32, rampLen)
	copy(exactPix, values)
	exact, err := r.CreateTextureR32F(exactPix, rampLen, 1, renderer.TextureOptions{})
	if err != nil {
		return 1, fmt.Errorf("upload the R32F reference: %w", err)
	}

	widePix := make([]uint16, rampLen*4)
	for i, v := range values {
		for ch := range 4 {
			widePix[i*4+ch] = renderer.Float16(v)
		}
	}
	wide, err := r.CreateTextureRGBA16F(widePix, rampLen, 1, renderer.TextureOptions{})
	if err != nil {
		return 1, fmt.Errorf("upload the RGBA16F ramp: %w", err)
	}

	narrowPix := make([]byte, rampLen*4)
	for i, v := range values {
		b := byte(math.Round(math.Sqrt(float64(v)/eightBitRange) * 255))
		narrowPix[i*4], narrowPix[i*4+1], narrowPix[i*4+2], narrowPix[i*4+3] = b, b, b, 255
	}
	narrow, err := r.CreateDataTexture(narrowPix, rampLen, 1)
	if err != nil {
		return 1, fmt.Errorf("upload the 8-bit control: %w", err)
	}

	if err := r.SetShaderTexture(1, exact); err != nil {
		return 1, err
	}
	pass, err := r.CreateAppPass(renderer.AppPassDesc{
		Name:  "wide texture probe",
		Stage: renderer.StageBeforeTonemap,
		// Load because a pass with no Target draws into the scene HDR image, and
		// the renderer only lets that be loaded rather than cleared. It costs
		// nothing here: the pass is fullscreen, so every pixel is overwritten.
		Load:       true,
		Fullscreen: true,
		Vert:       shaders.DepthResolveVertSpv,
		Frag:       probeFrag,
	})
	if err != nil {
		return 1, err
	}

	light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4()}
	probe := func(tex *renderer.Texture, decode float32) (*image.RGBA, error) {
		if err := r.SetShaderTexture(0, tex); err != nil {
			return nil, err
		}
		var push [16]byte
		binary.LittleEndian.PutUint32(push[0:], math.Float32bits(frameW))
		binary.LittleEndian.PutUint32(push[4:], math.Float32bits(rampLen))
		binary.LittleEndian.PutUint32(push[8:], math.Float32bits(decode))
		if err := pass.SetPushConstants(push[:]); err != nil {
			return nil, err
		}
		// Several frames rather than one: the shader-texture descriptor is written
		// per frame slot, so a single frame after a rebind would read whichever
		// slot the fence released rather than the texture just bound.
		for range frames {
			w.PollEvents()
			if w.WasResized() {
				r.NotifyResize()
			}
			if err := r.DrawFrame(nil, nil, nil, nil, nil, light); err != nil {
				return nil, err
			}
		}
		return r.CaptureFrame()
	}

	claimTex, claimDecode, claimName := wide, float32(0), "RGBA16F"
	if eightBit {
		claimTex, claimDecode, claimName = narrow, eightBitRange, "RGBA8 sqrt(v/3)"
	}
	claimImg, err := probe(claimTex, claimDecode)
	if err != nil {
		return 1, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return 1, err
	}
	if err := r.SaveScreenshot(filepath.Join(out, "claim.png")); err != nil {
		return 1, err
	}

	claim := read(claimImg, values)
	report(claimName+" (the claim)", claim)

	status := 0
	// The claim: 0.1 percent across the whole range. Half-float's own worst case
	// is half a step of a 10-bit significand, 2^-11 = 0.0488 percent, so this is
	// the format with room for the sampler and the readout and nothing else.
	if claim.worst > 0.001 {
		fmt.Printf("FAIL %s: worst relative error %.6f at %g, want under 0.001\n", claimName, claim.worst, claim.worstAt)
		status = 1
	}

	if eightBit {
		fmt.Println("-eightbit is the break: the claim above is made about the 8-bit control and must fail")
		if status == 0 {
			fmt.Println("FAIL: the break PASSED, so the check is measuring nothing")
			status = 1
		} else {
			// Expected. The break has done its job, so the process succeeds --
			// a break that exited non-zero would look like a broken check.
			fmt.Println("the break failed the claim, as it must")
			status = 0
		}
		return status, nil
	}

	// The control, in the same run, through the same probe. Without it every
	// number above could be a shader reading one texture twice: rel would be zero
	// everywhere and the claim would pass.
	controlImg, err := probe(narrow, eightBitRange)
	if err != nil {
		return 1, err
	}
	if err := r.SaveScreenshot(filepath.Join(out, "control.png")); err != nil {
		return 1, err
	}
	control := read(controlImg, values)
	report("RGBA8 sqrt(v/3) (the control)", control)

	// Floors from x/sky/lut's own measurement of the same transfer: 2.1 percent
	// above a tenth of full scale and 13.4 percent above two thousandths. These
	// are under those, because what they have to catch is a probe that reads zero.
	if control.worstAbove(0.1) < 0.01 {
		fmt.Printf("FAIL control: only %.6f relative error above 0.1; the probe is not measuring the texture\n", control.worstAbove(0.1))
		status = 1
	}
	if control.worst < 0.08 {
		fmt.Printf("FAIL control: worst relative error %.6f; the probe is not measuring the texture\n", control.worst)
		status = 1
	}
	if control.worst < 20*claim.worst {
		fmt.Printf("FAIL: the control is only %.1fx the claim; two configurations this close are one configuration\n", control.worst/claim.worst)
		status = 1
	}

	if status == 0 {
		fmt.Printf("wide texture precision: RGBA16F worst %.6f, 8-bit control worst %.6f, %.0fx\n",
			claim.worst, control.worst, control.worst/claim.worst)
	}
	return status, nil
}

// errors is one configuration's readings: the relative error at every ramp entry,
// read out of the capture.
type errors struct {
	rel    []float64
	values []float32

	worst   float64
	worstAt float32
}

func (e errors) worstAbove(level float32) float64 {
	var worst float64
	for i, v := range e.values {
		if v > level && e.rel[i] > worst {
			worst = e.rel[i]
		}
	}
	return worst
}

// read decodes the three gains back into a relative error per ramp entry, taking
// the finest channel that has not saturated.
//
// 250 rather than 255 as the saturation threshold: a channel at the top of its
// range is a value that was clamped, and one a few levels below it is a value
// close enough to the clamp that the next gain down is the honest reading.
func read(img *image.RGBA, values []float32) errors {
	e := errors{rel: make([]float64, len(values)), values: values}
	width, height := img.Bounds().Dx(), img.Bounds().Dy()
	for i := range values {
		px := img.RGBAAt(int((float64(i)+0.5)/float64(len(values))*float64(width)), height/2)
		switch {
		case px.R < 250:
			e.rel[i] = float64(px.R) / 255 / 1000
		case px.G < 250:
			e.rel[i] = float64(px.G) / 255 / 10
		default:
			e.rel[i] = float64(px.B) / 255
		}
		if e.rel[i] > e.worst {
			e.worst, e.worstAt = e.rel[i], values[i]
		}
	}
	return e
}

// report prints the shape of one configuration's error, not only its worst: the
// claim is about the whole range, and a single figure cannot say whether the
// error is flat (a relative format) or grows as the values fall (a transfer).
func report(name string, e errors) {
	fmt.Printf("%s\n", name)
	for _, level := range []float32{0.001, 0.01, 0.1, 1.0} {
		fmt.Printf("  worst relative error above %-6g %.6f\n", level, e.worstAbove(level))
	}
	fmt.Printf("  worst relative error anywhere %.6f, at %g\n", e.worst, e.worstAt)
}
