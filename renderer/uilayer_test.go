package renderer

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// pcDriver is fakeDriver that keeps the push-constant blocks pushed while one
// of a named set of pipelines is bound, as the 64 floats the shaders read
// rather than as bytes.
//
// Filtered by pipeline because the block is shared: pc[44] and pc[45] are the
// UI's emission and premultiply flag, and the same two floats are the lit
// block's pointPos.xy on every other draw in the frame. A test that read every
// block in a frame would be asserting about a point light's position.
//
// It records the VALUES at the moment of the call, which is the only way to see
// them: commandScratch hands the driver its own array and refills it for the
// next draw, so a driver that kept the slice would end up with N views of the
// last block.
type pcDriver struct {
	*fakeDriver
	watch  map[uint64]bool
	bound  uint64
	blocks [][64]float32
}

func watchPipelines(ps ...core1_0.Pipeline) map[uint64]bool {
	m := make(map[uint64]bool, len(ps))
	for _, p := range ps {
		m[uint64(p.Handle())] = true
	}
	return m
}

func (d *pcDriver) CmdBindPipeline(cb core1_0.CommandBuffer, bp core1_0.PipelineBindPoint, p core1_0.Pipeline) {
	d.fakeDriver.CmdBindPipeline(cb, bp, p)
	d.bound = uint64(p.Handle())
}

func (d *pcDriver) CmdPushConstants(cb core1_0.CommandBuffer, layout core1_0.PipelineLayout, stages core1_0.ShaderStageFlags, offset int, valueBytes []byte) {
	d.fakeDriver.CmdPushConstants(cb, layout, stages, offset, valueBytes)
	if !d.watch[d.bound] || len(valueBytes) != pushConstantSize {
		return
	}
	var b [64]float32
	for i := range b {
		b[i] = math.Float32frombits(binary.LittleEndian.Uint32(valueBytes[i*4 : i*4+4]))
	}
	d.blocks = append(d.blocks, b)
}

// withUILayer attaches a UI glow layer to a fixture frame, built from fake
// handles the same way buildFrame builds everything else.
//
// A separate helper in a separate file rather than a change to buildFrame, and
// that is the whole point: the layer is opt-in and has to be FREE when it is
// off, so the fixture that pins the recorder's command stream has to be able to
// stay exactly as it was. tonemapPass.ui is a nil field in a struct buildFrame
// already fills, so a layer-off frame needs no edit to
// TestRecordCommandBufferStreamIsUnchanged -- and that test passing unmodified
// is the evidence for "off is free", not a claim about it.
func withUILayer(fx *frame, glow bool) *frame {
	h := &fakeHandles{next: 10000}
	var strength float32
	if glow {
		strength = 0.7
	}
	sets := make([]core1_0.DescriptorSet, bloomLevels)
	downFB := make([]core1_0.Framebuffer, bloomLevels)
	upFB := make([]core1_0.Framebuffer, bloomLevels)
	for i := 0; i < bloomLevels; i++ {
		sets[i] = h.descSet()
		downFB[i] = h.framebuffer()
		upFB[i] = h.framebuffer()
	}
	fx.tonemap.ui = &uiLayerPass{
		renderPass:   h.renderPass(),
		framebuffer:  h.framebuffer(),
		uiPipeline:   h.pipeline(),
		msdfPipeline: h.pipeline(),
		layout:       h.layout(),
		bloom: bloomPass{
			enabled:        strength > 0,
			downRenderPass: h.renderPass(),
			upRenderPass:   h.renderPass(),
			prefilter:      h.pipeline(),
			down:           h.pipeline(),
			up:             h.pipeline(),
			layout:         h.layout(),
			sceneSet:       h.descSet(),
			sets:           sets,
			downFB:         downFB,
			upFB:           upFB,
			extents:        bloomExtents(fx.extent),
			sceneExtent:    fx.extent,
			threshold:      1.2,
			knee:           0.2,
			radius:         1.0,
		},
		resolve:       h.pipeline(),
		resolveSet:    h.descSet(),
		resolveLayout: h.layout(),
		exposure:      1,
		strength:      strength,
	}
	return fx
}

// goldenUILayerStreamHash pins recordCommandBuffer's driver-call stream over
// buildFrame(97) with the UI glow layer ON, the way goldenStreamHash pins it
// with the layer off.
//
// It is a SECOND pinned value beside that one rather than a replacement for it,
// because the two say different things and the first says the more important
// one: with the layer off, nothing about the recorded frame moved. Recomputing
// that constant to accommodate this feature would have thrown away the only
// evidence that "off is free" is true rather than intended.
//
// Captured once by logging d.h from the test below. Reproduce it the same way
// if a deliberate change to the layer's recording moves it -- and if an
// UNINTENDED change moves it, that is what this is for.
// #98 changes the regular sky's descriptor/layout arguments; UI work and
// the 3379 calls are unchanged. Previous hash: 0xab0f60e975b26193.
const goldenUILayerStreamHash = Hasher(0xdf477ab29db846b8)

// TestUILayerStreamIsPinned is the layer-on half of "nothing changed": the
// extra render pass, the two overlay pipelines bound inside it, the bloom chain
// over it and the composite triangle all reach the driver with the argument
// values they reach it with today.
//
// Verified to fail: dropping the premultiply flag (pushing 0 into pc[45]
// regardless of the layer) moves the hash to 0xdb3ffbec1ae05055 -- a change no
// call-count test could see, because the number of driver calls is identical
// and only 4 bytes of one push-constant block differ.
func TestUILayerStreamIsPinned(t *testing.T) {
	fx := withUILayer(buildFrame(97), true)
	d := &fakeDriver{hashing: true}
	if err := fx.record(d, 1); err != nil {
		t.Fatalf("record: %v", err)
	}
	t.Logf("stream hash: %#x over %d driver calls", uint64(d.h), d.calls)
	if d.h != goldenUILayerStreamHash {
		t.Fatalf("layer-on command stream hash = %#x, want %#x: the driver calls (or their argument values) changed",
			d.h, goldenUILayerStreamHash)
	}
}

// TestUILayerOffRecordsNothing is the claim "off is free", stated as a number
// rather than as a design intention: the same fixture with and without the
// layer, and the layer-off recording has to make FEWER driver calls and hash
// differently -- otherwise the layer-on test above is measuring nothing.
//
// The direction matters. A layer-on frame that recorded the same number of
// calls as a layer-off one would mean the layer pass was never entered, which
// is exactly the failure `task uiglow` catches on the GPU side and which no
// hash comparison against a single pinned value can see on its own.
func TestUILayerOffRecordsNothing(t *testing.T) {
	off := &fakeDriver{hashing: true}
	if err := buildFrame(97).record(off, 1); err != nil {
		t.Fatalf("record (layer off): %v", err)
	}
	on := &fakeDriver{hashing: true}
	if err := withUILayer(buildFrame(97), true).record(on, 1); err != nil {
		t.Fatalf("record (layer on): %v", err)
	}
	t.Logf("layer off: %d calls, hash %#x; layer on: %d calls, hash %#x",
		off.calls, uint64(off.h), on.calls, uint64(on.h))
	if off.h != goldenStreamHash {
		t.Errorf("layer-off hash = %#x, want the unchanged %#x: the layer is not free when it is off",
			off.h, goldenStreamHash)
	}
	if on.calls <= off.calls {
		t.Errorf("layer on records %d driver calls against %d with it off: the layer pass was never entered",
			on.calls, off.calls)
	}
}

// TestUILayerBracketHoldsTheLayerWorkAndOnlyIt pins what PassUILayer and
// PassUIGlow measure, the way TestShaftBracketHoldsTheShaftDrawAndOnlyIt pins
// PassShafts -- and for the same reason. A bracket containing nothing reads
// 0.000 ms and looks like a cheap pass rather than like a missing one, so the
// number alone is not evidence; what is in the bracket is.
//
// Both halves matter. With the layer on, PassUILayer must hold every UI and
// MSDF draw the frame has, and exactly one render pass end -- the layer's own.
// With it off both brackets must hold nothing AND still write both timestamps,
// because a query that is reset and never written makes the whole frame's
// readback come back NotReady and every pass loses its number, not just the
// skipped one.
//
// Verified to fail: moving the PassUILayer end timestamp above the
// vkCmdEndRenderPass inside recordUILayer reports "uilayer holds 0 pass ends".
// Verified the other way too: recording the layer unconditionally, ignoring the
// nil check, reports the off case holding draws.
func TestUILayerBracketHoldsTheLayerWorkAndOnlyIt(t *testing.T) {
	uiDraws := func(fx *frame) int { return len(fx.uiOverlays) + len(fx.msdfOverlays) }

	t.Run("on", func(t *testing.T) {
		fx := withUILayer(buildFrame(61), true)
		fx.timer = &gpuTimer{supported: true}
		d := &timingDriver{fakeDriver: &fakeDriver{}}
		if err := fx.record(d, 0); err != nil {
			t.Fatalf("record: %v", err)
		}

		draws, ends := 0, 0
		for _, ev := range between(t, d.events, PassUILayer) {
			switch ev {
			case "draw":
				draws++
			case "endpass":
				ends++
			}
		}
		if want := uiDraws(fx); draws != want {
			t.Errorf("uilayer holds %d draws, want the frame's %d UI and MSDF draws", draws, want)
		}
		if ends != 1 {
			t.Errorf("uilayer holds %d pass ends, want exactly one -- its own", ends)
		}

		// The glow chain: one prefilter, bloomLevels-1 downsamples and
		// bloomLevels-1 upsamples, each a fullscreen triangle in a pass of its
		// own.
		glowDraws, glowEnds := 0, 0
		for _, ev := range between(t, d.events, PassUIGlow) {
			switch ev {
			case "draw":
				glowDraws++
			case "endpass":
				glowEnds++
			}
		}
		if want := 2*bloomLevels - 1; glowDraws != want {
			t.Errorf("uiglow holds %d draws, want %d (prefilter + %d down + %d up)",
				glowDraws, want, bloomLevels-1, bloomLevels-1)
		}
		if glowDraws != glowEnds {
			t.Errorf("uiglow holds %d draws but %d pass ends; each stage is its own pass", glowDraws, glowEnds)
		}
	})

	t.Run("off", func(t *testing.T) {
		fx := buildFrame(61)
		fx.timer = &gpuTimer{supported: true}
		d := &timingDriver{fakeDriver: &fakeDriver{}}
		if err := fx.record(d, 0); err != nil {
			t.Fatalf("record: %v", err)
		}
		for _, p := range []Pass{PassUILayer, PassUIGlow} {
			// between fatals unless BOTH timestamps were written, which is the
			// half of this that matters most: a skipped pass still has to write
			// them or the whole frame's readback comes back NotReady.
			if in := between(t, d.events, p); len(in) != 0 {
				t.Errorf("%s holds %v with the layer off, want nothing", p, in)
			}
		}
	})
}

// TestGlowIsZeroOnTheDirectPath is the other half of "off is free", at the one
// place a stray value would be invisible: the push-constant block.
//
// pc[44] is the emission multiplier and pc[45] is the premultiply flag, and
// ui.frag reads both. On the direct path both have to be exactly zero, because
// the shader's `colour * (1 + glow)` is only exact -- bit for bit what it wrote
// before the layer existed -- when glow is 0, and because premultiplying on a
// path whose blend factor is SrcAlpha counts coverage twice.
func TestGlowIsZeroOnTheDirectPath(t *testing.T) {
	fx := buildFrame(12)
	// Ask for glow on every panel, which is the case that would leak: a game
	// that set Glow and then ran without the layer must still get its old frame.
	for i := range fx.uiOverlays {
		fx.uiOverlays[i].Glow = 2.5
	}

	d := &pcDriver{fakeDriver: &fakeDriver{}, watch: watchPipelines(fx.uiPipeline, fx.msdfPipeline)}
	if err := fx.record(d, 0); err != nil {
		t.Fatalf("record: %v", err)
	}
	if want := len(fx.uiOverlays) + len(fx.msdfOverlays); len(d.blocks) != want {
		t.Fatalf("captured %d overlay push blocks, want %d -- this test is not looking at the draws it thinks it is",
			len(d.blocks), want)
	}
	for i, b := range d.blocks {
		if b[44] != 0 {
			t.Errorf("push block %d carries emission %v on the direct path, want 0", i, b[44])
		}
		if b[45] != 0 {
			t.Errorf("push block %d asks to premultiply on the direct path (%v), want 0", i, b[45])
		}
	}
}

// TestGlowReachesThePushConstantsInTheLayer is the same block on the layer
// path: the per-object emission has to arrive, and the premultiply flag with it.
//
// Verified to fail: dropping `scratch.pc[44] = d.Glow` reports every block at
// zero, which is the shape of "the glow silently stopped working" that
// `task uiglow` catches on the GPU and this catches without one.
func TestGlowReachesThePushConstantsInTheLayer(t *testing.T) {
	fx := withUILayer(buildFrame(12), true)
	for i := range fx.uiOverlays {
		fx.uiOverlays[i].Glow = 2.5
	}

	d := &pcDriver{fakeDriver: &fakeDriver{},
		watch: watchPipelines(fx.tonemap.ui.uiPipeline, fx.tonemap.ui.msdfPipeline)}
	if err := fx.record(d, 0); err != nil {
		t.Fatalf("record: %v", err)
	}
	if want := len(fx.uiOverlays) + len(fx.msdfOverlays); len(d.blocks) != want {
		t.Fatalf("captured %d overlay push blocks, want %d -- the layer's own pipelines were not the ones bound",
			len(d.blocks), want)
	}
	glowing, premultiplied := 0, 0
	for _, b := range d.blocks {
		if b[44] == 2.5 {
			glowing++
		}
		if b[45] == 1 {
			premultiplied++
		}
	}
	if glowing != len(fx.uiOverlays) {
		t.Errorf("%d push blocks carry the emission, want one per UI overlay (%d)", glowing, len(fx.uiOverlays))
	}
	if want := len(fx.uiOverlays) + len(fx.msdfOverlays); premultiplied != want {
		t.Errorf("%d push blocks ask to premultiply, want one per overlay draw (%d)", premultiplied, want)
	}
}
