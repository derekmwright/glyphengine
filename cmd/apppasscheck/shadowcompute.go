package main

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/shaders"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

//go:embed shadowprobe.comp.spv
var shadowProbeComp []byte

//go:embed shadowprobe.frag.spv
var shadowProbeFrag []byte

//go:embed shadowcompare.frag.spv
var shadowCompare []byte

// The LUT texel the dispatch reads times the application uniform it reads:
// target-values.frag writes 0.125 at texel (0,0) and SetShaderParameters supplies
// 0.5. The white fallback texture is 1.0, so a lost LUT binding reports 0.5 and a
// lost uniform reports 0 -- both far outside the tolerance.
const (
	shadowProbeLUTValue  = 0.125
	shadowProbeUniform   = 0.5
	shadowProbeShapeWant = shadowProbeLUTValue * shadowProbeUniform

	// shadowProbeAllowed is how many probe texels may disagree between the two
	// halves, and it is zero because that is what was measured, not because
	// exactness sounded likely. Both halves run the same arithmetic from the same
	// uniform block on the same inputs and sample the same descriptor, so the
	// comparison is bit-identical rather than merely close.
	//
	// 640x360, 40 frames, fixed clock, RX 7900 XTX: 0 of 225280 probe texels
	// disagree in all three phases, worst 0/65025 -- and the blue channel reports
	// the difference at 255x, so a single byte there would be 1/65025 of a shadow
	// value. Raise this only with a measurement beside it, and only for texels on
	// a cascade boundary, which is the one place the bilinear comparison blend
	// could legitimately land differently.
	shadowProbeAllowed = 0

	// Fully occluded reads 0 and fully lit reads 1; the band between is the
	// bilinear comparison blend on a cascade texel boundary, which is exactly
	// where the two halves are allowed to be interesting rather than equal.
	shadowProbeDark  = 64
	shadowProbeLight = 192

	// The patch has to be substantially both, or two shaders agreeing on a
	// constant would pass. Measured over 225280 texels: sun A 56352 shadowed and
	// 168688 lit, sun B 66032 and 158664. The floors are a half and a third of
	// the smaller measured count on each side, which leaves room for the sun
	// angle to be retuned without making the gate a tripwire.
	shadowProbeMinDarkDivisor  = 8 // 28160 texels against 56352 measured
	shadowProbeMinLightDivisor = 4 // 56320 texels against 158664 measured

	// The sun-moved control: measured 103168 of 225280 texels moving by at least
	// 16/255, maximum 255/255. A third of that count, and the maximum has to
	// reach half scale -- a dispatch reading a stale or constant map moves
	// nothing at all, so this only has to be clear of noise.
	shadowProbeMinMovedDivisor = 3 // 75093 texels against 103168 measured
	shadowProbeMinMovedDelta   = 128
)

// shadowProbePhase is one capture: a sun direction and whether the cascades are
// drawn at all.
type shadowProbePhase struct {
	name    string
	sun     [3]float32
	shadows bool
}

// runShadowCompute answers the question the descriptor stage flags cannot: does a
// compute dispatch sampling the directional cascades get the same answer the
// fragment path gets, at the same world positions, in the same frame.
//
// Both halves share shadowprobe.inc, so a disagreement is a synchronization,
// descriptor or layout fault rather than two shaders computing different things.
// The sun-moved phase is the control that the comparison is not vacuous: a pair
// of shaders that both returned a constant would agree perfectly.
//
// Broken on purpose, 2026-10-02, to check this is not decoration: the dispatch's
// half of the lookup compared against a constant reference of 1.0 instead of the
// probe position's own depth -- the degenerate form of dropping the comparison.
// It FAILED with
//
//	sun A: 169236/225280 texels disagree, worst 255/65025; 225280 shadowed, 0 lit
//
// and that number is only there because the scene was fixed first: see the
// caster list below for the version of this break that passed.
func runShadowCompute(o options) error {
	if o.frames < 8 {
		return fmt.Errorf("-shadowcompute requires at least 8 frames")
	}
	w, err := window.New(640, 360, "Shadow sampling from compute")
	if err != nil {
		return err
	}
	defer w.Destroy()
	r, err := renderer.New(w, renderer.WithValidation(o.validate), renderer.WithMSAASamples(o.msaa))
	if err != nil {
		return err
	}
	defer r.Destroy()

	lut, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "sun LUT", Format: renderer.TargetR16F, Width: 4, Height: 4})
	if err != nil {
		return err
	}
	computed, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "compute shadow", Format: renderer.TargetR32F, Scale: 0.5, Storage: true})
	if err != nil {
		return err
	}
	shape, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "compute shape", Format: renderer.TargetR32F, Scale: 0.5, Storage: true})
	if err != nil {
		return err
	}
	drawn, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "fragment shadow", Format: renderer.TargetR32F, Scale: 0.5})
	if err != nil {
		return err
	}
	// The LUT writer precedes the dispatch at the same stage, so its contents are
	// this frame's rather than the previous frame's.
	if _, err = r.CreateAppPass(renderer.AppPassDesc{Name: "sun LUT", Stage: renderer.StageBeforeScene,
		Target: lut, Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: targetValues}); err != nil {
		return err
	}
	fragment, err := r.CreateAppPass(renderer.AppPassDesc{Name: "fragment shadow probe", Stage: renderer.StageBeforeScene,
		Target: drawn, Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: shadowProbeFrag, Timed: true})
	if err != nil {
		return err
	}
	compute, err := r.CreateAppCompute(renderer.AppComputeDesc{Name: "compute shadow probe", Stage: renderer.StageBeforeScene,
		Comp: shadowProbeComp, Reads: []*renderer.Texture{lut.Texture()},
		Writes: []*renderer.RenderTarget{computed, shape}, ReadsShadows: true, Timed: true})
	if err != nil {
		return err
	}
	compare, err := r.CreateAppPass(renderer.AppPassDesc{Name: "shadow comparison", Stage: renderer.StageBeforeTonemap, Load: true,
		Reads: []*renderer.Texture{computed.Texture(), drawn.Texture(), shape.Texture()},
		Vert:  shaders.DepthResolveVertSpv, Frag: shadowCompare, Fullscreen: true})
	if err != nil {
		return err
	}

	ground, err := r.CreateIndexedMesh([]renderer.Vertex{
		{Pos: [3]float32{-20, 0, -20}, Normal: [3]float32{0, 1, 0}},
		{Pos: [3]float32{20, 0, -20}, Normal: [3]float32{0, 1, 0}},
		{Pos: [3]float32{20, 0, 20}, Normal: [3]float32{0, 1, 0}},
		{Pos: [3]float32{-20, 0, 20}, Normal: [3]float32{0, 1, 0}},
	}, []uint16{0, 1, 2, 0, 2, 3})
	if err != nil {
		return err
	}
	caster, err := r.CreateCube(3)
	if err != nil {
		return err
	}
	// A grid of casters so a useful fraction of the probe patch is occluded and
	// the rest is lit. A capture that is all one or the other proves nothing: the
	// two halves would agree on a constant.
	//
	// The ground casts too, and that is load-bearing rather than scene dressing.
	// With the cubes as the only casters, every lit texel reads the cleared depth
	// of 1.0, so a lookup that compared against a constant 1.0 instead of the
	// position's own depth produced the same image -- measured: the deliberate
	// break of exactly that shape passed this gate with identical numbers in all
	// three phases. A receiver that writes the map makes the comparison's
	// reference matter, and the same break then fails. The bias is 0.0015 of a
	// 37.5-unit depth range, about 7x the 0.00021 a cascade texel of this plane
	// spans at this sun elevation, so it does not acne.
	draws := []renderer.RenderObject{{Mesh: ground, Model: mgl32.Ident4(), MVP: mgl32.Ident4()}}
	for x := -1; x <= 1; x++ {
		for z := -1; z <= 1; z++ {
			draws = append(draws, renderer.RenderObject{Mesh: caster,
				Model: mgl32.Translate3D(float32(x)*7, 3, float32(z)*7), ShadowOnly: true})
		}
	}

	rebuilds := provokeRecreateFrames()
	var captures = map[string]*image.RGBA{}
	for _, phase := range []shadowProbePhase{
		{"sun A", [3]float32{0.35, 0.8, 0.25}, true},
		{"sun B", [3]float32{-0.45, 0.72, -0.3}, true},
		{"shadows off", [3]float32{0.35, 0.8, 0.25}, false},
	} {
		light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4(), SunDir: phase.sun,
			SunColor: [3]float32{1, 1, 1}, SkyColor: [4]float32{0.02, 0.03, 0.04, 1}, ShadowEnabled: phase.shadows}
		if phase.shadows {
			light.CascadeVPs = renderer.ComputeCascadeVPs(phase.sun, mgl32.Vec3{})
		}
		parameters := make([]byte, 16)
		binary.LittleEndian.PutUint32(parameters, math.Float32bits(shadowProbeUniform))
		if err = r.SetShaderParameters(parameters); err != nil {
			return err
		}
		for frame := 0; frame < o.frames; frame++ {
			// Both rebuild shapes: a same-size swapchain recreation from the named
			// environment variable, and a real resize, which replaces the
			// half-resolution probe images and their descriptors while the cascade
			// map -- which is not swapchain-sized -- stays where it is.
			if rebuilds[frame+1] {
				r.NotifyResize()
			}
			if o.recreate && frame > 0 && frame+4 < o.frames && frame%7 == 0 {
				if (frame/7)%2 == 0 {
					w.Handle().SetSize(672, 384)
				} else {
					w.Handle().SetSize(640, 360)
				}
				r.NotifyResize()
			}
			w.PollEvents()
			if w.WasResized() {
				r.NotifyResize()
			}
			pw, ph := computed.Extent()
			fw, fh := r.Extent()
			var probe [16]byte
			for i, v := range []float32{float32(pw), float32(ph), float32(fw), float32(fh)} {
				binary.LittleEndian.PutUint32(probe[i*4:], math.Float32bits(v))
			}
			if err = fragment.SetPushConstants(probe[:]); err != nil {
				return err
			}
			if err = compute.SetPushConstants(probe[:]); err != nil {
				return err
			}
			if err = compare.SetPushConstants(probe[:]); err != nil {
				return err
			}
			compute.SetDispatch((uint32(pw)+7)/8, (uint32(ph)+7)/8, 1)
			if err = r.DrawFrame(draws, nil, nil, nil, nil, light); err != nil {
				return err
			}
		}
		captures[phase.name], err = r.CaptureFrame()
		if err != nil {
			return err
		}
		if err = checkShadowProbe(phase, captures[phase.name]); err != nil {
			return err
		}
	}
	if err = checkShadowProbeSunMoved(captures["sun A"], captures["sun B"]); err != nil {
		return err
	}
	path := o.screenshot
	if path == "" {
		path = filepath.Join(".task", "shadowcompute.png")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return r.SaveScreenshot(path)
}

// provokeRecreateFrames reads the engine's own provocation variable. The
// examples get it through Engine.Run, which this harness does not use, so the
// named gate would otherwise do nothing here.
func provokeRecreateFrames() map[int]bool {
	out := map[int]bool{}
	for _, part := range strings.Split(os.Getenv("GLYPHENGINE_PROVOKE_RECREATE_FRAMES"), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && n > 0 {
			out[n] = true
		}
	}
	return out
}

// checkShadowProbe compares the two halves texel by texel over the whole
// capture, below the shape band. Agreement to within one cascade texel is the
// requirement, and a cascade texel is where the bilinear comparison blend puts
// the only legitimate difference -- so the measurement reported is how many
// texels disagree at all, and by how much.
func checkShadowProbe(phase shadowProbePhase, img *image.RGBA) error {
	box := img.Bounds()
	differing, worst, shadowed, lit, shapeWrong := 0, 0, 0, 0, 0
	for y := 8; y < box.Dy(); y++ {
		for x := 0; x < box.Dx(); x++ {
			p := img.RGBAAt(x, y)
			if p.B != 0 {
				differing++
			}
			worst = max(worst, int(p.B))
			switch {
			case p.R <= shadowProbeDark:
				shadowed++
			case p.R >= shadowProbeLight:
				lit++
			}
		}
	}
	for y := 0; y < 8; y++ {
		for x := 0; x < box.Dx(); x++ {
			if math.Abs(float64(img.RGBAAt(x, y).R)/255-shadowProbeShapeWant) > 1.0/255 {
				shapeWrong++
			}
		}
	}
	total := box.Dx() * (box.Dy() - 8)
	fmt.Printf("shadowcompute %s: %d/%d texels disagree, worst %d/65025; %d shadowed, %d lit; LUT x uniform wrong in %d/%d band pixels\n",
		phase.name, differing, total, worst, shadowed, lit, shapeWrong, box.Dx()*8)
	if shapeWrong != 0 {
		return fmt.Errorf("%s: the dispatch's LUT or application uniform did not reach it (want %.4f)", phase.name, shadowProbeShapeWant)
	}
	if differing > shadowProbeAllowed {
		return fmt.Errorf("%s: compute and fragment shadow lookups disagree in %d texels (at most %d allowed), worst %d/65025",
			phase.name, differing, shadowProbeAllowed, worst)
	}
	if !phase.shadows {
		if shadowed != 0 || lit != total {
			return fmt.Errorf("shadows off must read fully lit everywhere: %d shadowed, %d of %d lit", shadowed, lit, total)
		}
		return nil
	}
	// A tenth of the patch in each state: enough that a constant on either side
	// cannot pass, with margin below the measured fractions.
	if shadowed < total/shadowProbeMinDarkDivisor || lit < total/shadowProbeMinLightDivisor {
		return fmt.Errorf("%s: the probe patch is not both shadowed and lit (%d shadowed, %d lit, %d total)", phase.name, shadowed, lit, total)
	}
	return nil
}

// checkShadowProbeSunMoved is the control. Moving the sun moves the cascade
// matrices and redraws the layers, so the dispatch's answer must move with them;
// a dispatch reading a stale or constant map would not notice.
func checkShadowProbeSunMoved(a, b *image.RGBA) error {
	if a == nil || b == nil || a.Bounds() != b.Bounds() {
		return fmt.Errorf("sun-moved control: captures %v and %v", a.Bounds(), b.Bounds())
	}
	changed, worst := 0, 0
	for y := 8; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			delta := abs(int(a.RGBAAt(x, y).R) - int(b.RGBAAt(x, y).R))
			if delta >= 16 {
				changed++
			}
			worst = max(worst, delta)
		}
	}
	total := a.Bounds().Dx() * (a.Bounds().Dy() - 8)
	fmt.Printf("shadowcompute sun-moved control: %d/%d texels differ by >=16/255, max %d/255\n", changed, total, worst)
	if changed < total/shadowProbeMinMovedDivisor || worst < shadowProbeMinMovedDelta {
		return fmt.Errorf("moving the sun did not change what the dispatch read")
	}
	return nil
}
