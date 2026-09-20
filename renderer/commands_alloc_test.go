package renderer

import (
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// noClouds stands in for the real cloud pass, which lives in a different file
// and is not part of this issue's scope: recordCommandBuffer takes it as an
// opaque closure precisely so the recorder does not need to know what is
// inside it.
func noClouds(core1_0.CommandBuffer) error { return nil }

// record drives recordCommandBuffer with this frame's fixture data, in the
// exact parameter order the function declares. frameIndex selects which of
// the two in-flight slots (joints, shadow descriptor sets, particle buffers)
// the call reads, matching how the real Renderer cycles it.
func (fx *frame) record(d core1_0.DeviceDriver, frameIndex int) error {
	return recordCommandBuffer(
		d, fx.cmdBuf, fx.renderPass, fx.framebuffer, fx.pipeline, fx.litDoubleSidedPipeline,
		fx.translucentPipeline, fx.translucentDoubleSidedPipeline, fx.skinnedTranslucentPipel,
		fx.instancedPipeline, fx.instancedDoubleSidedPipeline, fx.overlayPipeline, fx.skyPipeline, fx.skyVolumetricPipeline,
		fx.starsPipeline, fx.celestialPipeline, fx.uiPipeline, fx.msdfPipeline, fx.skinnedPipeline,
		fx.grassPipeline, fx.waterPipeline, fx.godRayPipeline, fx.waterRenderPass, fx.waterFramebuffer,
		fx.sceneColor, fx.sceneImage, noClouds, fx.cloudSet, fx.bloom, fx.tonemap, fx.particlePipeline,
		fx.terrainPipeline, fx.mat, &fx.stats, fx.pipelineLayout, fx.skyPipelineLayout, fx.litPipelineLayout,
		fx.skinnedPipelineLayout, fx.terrainPipeLayout, fx.extent, fx.draws, fx.overlays, fx.celestials,
		fx.uiOverlays, fx.msdfOverlays, fx.lighting, fx.split, fx.fallbackTexture, fx.milkyWayTex,
		fx.shadow, fx.grass, fx.grassLOD, fx.impostor, fx.grassImpostorPipeline, fx.particles,
		frameIndex, true, fx.timer, nil, &fx.scratch,
	)
}

// TestRecordCommandBufferAllocsAreConstant is the reported bug's second half:
// recordCommandBuffer was 77MB / 7% of a consumer game's total allocation,
// right behind MSDFText.SetText (fixed in #49). Building N and 4N draws and
// requiring the SAME allocs/op is what tells "zero per draw" apart from "zero
// because the fixture only had a few draws" -- a fixed per-frame cost would
// still show up as equal here, but anything that scales with the draw count
// would not.
//
// Verified to fail: putting back the opaque loop's per-draw vertex-buffer
// slice literal (deviceDriver.CmdBindVertexBuffers(cmdBuf, 0,
// []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0}) in place of
// scratch.bindVertexBuffers) reports 84 allocs/op at 61 draws and 324 at 228
// -- scaling with draw count, which is exactly the shape this test exists to
// catch.
func TestRecordCommandBufferAllocsAreConstant(t *testing.T) {
	small := buildFrame(40)
	big := buildFrame(160)

	// One untimed call each first: grass's own retained scratch
	// (visibleScratch/impostorScratch/impostorVariants) is allowed to grow
	// from nil on its first use, same as lightcluster's Builder.
	warm := &fakeDriver{}
	if err := small.record(warm, 0); err != nil {
		t.Fatalf("warm-up record (small): %v", err)
	}
	if err := big.record(warm, 0); err != nil {
		t.Fatalf("warm-up record (big): %v", err)
	}

	d := &fakeDriver{}
	allocsSmall := testing.AllocsPerRun(20, func() {
		if err := small.record(d, 0); err != nil {
			t.Fatal(err)
		}
	})
	allocsBig := testing.AllocsPerRun(20, func() {
		if err := big.record(d, 0); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("allocs/op: %v draws=%d, %v draws=%d", allocsSmall, len(small.draws), allocsBig, len(big.draws))
	if allocsSmall != allocsBig {
		t.Errorf("allocations scale with draw count: %v allocs/op at %d draws, %v at %d draws",
			allocsSmall, len(small.draws), allocsBig, len(big.draws))
	}
}

// BenchmarkRecordCommandBuffer reports ns/op, allocs/op and bytes/op for the
// recording path over a representative frame, and again at 4x the draw count
// so the before/after numbers in the commit message can be read per-draw as
// well as per-frame.
func BenchmarkRecordCommandBuffer(b *testing.B) {
	for _, n := range []int{40, 160} {
		fx := buildFrame(n)
		d := &fakeDriver{}
		if err := fx.record(d, 0); err != nil {
			b.Fatalf("warm-up record: %v", err)
		}
		b.Run(benchName(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := fx.record(d, i%maxFramesInFlight); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchName(n int) string {
	switch n {
	case 40:
		return "N=40draws"
	case 160:
		return "N=160draws"
	default:
		return "N"
	}
}

// goldenStreamHash is recordCommandBuffer's driver-call hash over buildFrame(97)
// on the code as it stood before the per-draw scratch refactor (issue #48's
// second half, after #49 fixed MSDFText.SetText). Captured by temporarily
// logging d.h from TestRecordCommandBufferStreamIsUnchanged and copying the
// value here -- see the test for how to reproduce it.
//
// It has to be a value recorded once and pinned, not something recomputed by
// both "before" and "after" in the same test run: the whole point is to catch
// a change in what gets sent to the driver, and a test that only compares the
// current code to itself would not.
//
// Recomputed once, after the scratch refactor landed: buildFrame's camera was
// an identity VP, which ExtractFrustum reads as the unit cube, so every grass
// tile -- placed tens of units out along +X -- was being culled and the
// impostor and mesh draw paths were never reached. Fixing the camera (a real
// reverse-Z perspective looking down +X) changed how many calls the SAME
// verified-unchanged recording code makes, which is why this moved from
// 0xfe4146bf2977758c to 0xfc9618c8db98fa0f; it did not move because of anything
// in commands.go, which TestRecordCommandBufferAllocsAreConstant and a manual
// before/after diff of this constant both still agreed on at the time.
//
// Recomputed a second time, for #50: the light-shaft draw. godRayPipeline had
// been built and handed to this recorder since 2026-07-29 and never bound, and
// recordWaterPass now binds it, pushes the sun's screen position and draws a
// fullscreen triangle. buildFrame also turns LightShafts on, because a draw the
// fixture never asks for is a draw this hash cannot pin.
//
// SIX driver calls, and nothing else: bind pipeline, set viewport, set scissor,
// bind descriptor sets, push constants, draw. Verified by flipping only the
// fixture's LightShafts back to 0 and re-running this test -- 3293 calls and
// 0xfc9618c8db98fa0f, the old value exactly -- then back to 0.35: 3299 calls
// and the value below. Nothing else in the stream moved, and in particular the
// PassShafts timestamps did not, because the fixture's gpuTimer is unsupported
// and writes none.
//
// And a third time in the same change, when the shafts' shape became data
// (LightShaftShape): the brightness window moved out of godray.frag and into
// two floats of the shaft draw's push block that used to be zero. Same 3299
// calls. Verified by leaving pc[38] and pc[39] unpacked and re-running this
// test -- 0x08525eb349c579bc, the value above exactly -- so those two floats
// are the whole of the difference.
//
// NOT recomputed for #47, and that is the point of saying so. Volumetric
// in-scattering added a pipeline and a draw -- the one that fronts the sky --
// and this hash did not move by a bit: 3299 driver calls with the same
// arguments, 0x7f81990a07a357c6, the value it has held since the shaft draw
// landed. The new draw is recorded only when a light asks to scatter and
// buildFrame's lights do not, so "a scene that does not use volumetrics pays
// nothing" is pinned here at the level of driver calls, the same way the G1
// captures pin it at the level of pixels.
//
// It did move, twice, on the way to that -- and both times for a reason worth
// leaving written down:
//
//   - The first arrangement put the march at the end of sky.frag, which meant
//     the sky draw binding a second descriptor set and pushing the fog. Same
//     3299 calls, different arguments. It also moved three PIXELS of a scene
//     with no volumetric light in it, which is what sent the march into a
//     draw of its own; see shaders/skyvolumetric.frag.
//   - buildFrame briefly gained FogDensity/FogHeight/FogBaseHeight, which is
//     not a sky change at all: fog rides in pc.cameraPos.w and pc.fog.xy for
//     every lit draw, so it moved most of the push blocks in the frame
//     (0x58d24cfbaf980c5d). Removed again once the sky stopped needing it.
//
// One trap worth recording, because it cost an hour and would cost it again.
// fakeHandles hands out a monotonic counter, so allocating the new
// skyPipelineLayout in the middle of buildFrame's struct literal renumbered
// every handle after it and moved this hash by itself, with nothing else
// changed. Both new handles are allocated after the literal, and that is why.
const goldenStreamHash = Hasher(0x7f81990a07a357c6)

// TestRecordCommandBufferStreamIsUnchanged is the GPU-free half of "nothing
// changed": every driver call the recorder makes, folded in order with its
// argument VALUES (not addresses -- see fakeDriver), must hash the same after
// the scratch refactor as it did before. A reused scratch slice that a nested
// call clobbers before the outer caller reads it changes what value reaches
// the driver without changing the number of calls, which is exactly what this
// catches and TestRecordCommandBufferAllocsAreConstant cannot.
//
// Verified to fail: inserting a spurious scratch.resetPC() in the grass
// impostor loop, right before it sets pcImpostorCell -- simulating some other
// call reusing scratch.pc before the outer grass block's VP/lighting/tint
// fill (set once, before the tile loop, and relied on for every impostor
// draw) was read -- changes the hash from 0xfc9618c8db98fa0f to
// 0x82448aa6078d119f. AllocsPerRun does not move: resetPC allocates nothing,
// so this is a case the allocation test genuinely cannot see.
func TestRecordCommandBufferStreamIsUnchanged(t *testing.T) {
	fx := buildFrame(97)
	d := &fakeDriver{hashing: true}
	if err := fx.record(d, 1); err != nil {
		t.Fatalf("record: %v", err)
	}
	t.Logf("stream hash: %#x over %d driver calls", uint64(d.h), d.calls)
	if d.h != goldenStreamHash {
		t.Fatalf("command stream hash = %#x, want %#x: the driver calls (or their argument values) changed", d.h, goldenStreamHash)
	}
}

// withVolumetricLight makes a fixture frame ask for in-scattering: one spot
// with a non-zero Params.x, the header flag that says so, and the fog it
// scatters off.
//
// A separate helper rather than a change to buildFrame, exactly as
// withUILayer is and for the same reason. The feature has to be FREE when
// nothing asks for it, so the fixture that pins the recorder's stream has to
// be able to stay exactly as it was -- and goldenStreamHash being UNCHANGED
// across this whole change is the evidence for that rather than a claim about
// it.
func withVolumetricLight(fx *frame) *frame {
	fx.lighting.LightFlags |= LightFlagVolumetric
	fx.lighting.FogDensity = 0.006
	fx.lighting.FogHeight = 6
	fx.lighting.FogBaseHeight = 0.5
	fx.lighting.Lights = []GpuLight{{
		PosRange: [4]float32{1, 3, -2, 10},
		Color:    [4]float32{1, 0.58, 0.28, 0.93},
		DirCone:  [4]float32{0, -1, 0, 0.68},
		Params:   [4]float32{1, 0, 0, 0},
	}}
	return fx
}

// goldenVolumetricStreamHash pins the stream with the sky in-scattering draw
// recorded, the way goldenUILayerStreamHash pins the UI glow layer's.
//
// A SECOND pinned value beside goldenStreamHash rather than a replacement,
// because the two say different things and the first says the more important
// one. Recomputing that constant to accommodate this feature would have
// thrown away the only call-level evidence that a scene asking for no
// volumetrics records the frame it always did.
//
// Captured once by logging d.h from the test below; reproduce it the same way
// if a deliberate change to the draw moves it. SIX driver calls more than the
// volumetrics-off stream -- 3305 against 3299 -- which is bind pipeline, set
// viewport, set scissor, bind descriptor sets, push constants, draw, and
// nothing else.
const goldenVolumetricStreamHash = Hasher(0x01e4b081132b7d26)

// TestVolumetricSkyDrawIsRecorded is the volumetrics-on half of "nothing
// changed": the extra draw reaches the driver, with the argument values it
// reaches it with today, and it costs exactly six calls.
//
// The direction of the count matters as much as the hash. A volumetrics-on
// frame recording the SAME number of calls as a volumetrics-off one would
// mean the draw was never entered, which no comparison against a single
// pinned value can see -- and "the pipeline was built, handed to this
// recorder and never bound" is not hypothetical here. That is exactly what
// happened to godRayPipeline from 2026-07-29 to 2026-09-19, with every gate
// in the repository green; see cmd/shaftcheck.
func TestVolumetricSkyDrawIsRecorded(t *testing.T) {
	off := &fakeDriver{hashing: true}
	if err := buildFrame(97).record(off, 1); err != nil {
		t.Fatalf("record (volumetrics off): %v", err)
	}
	on := &fakeDriver{hashing: true}
	if err := withVolumetricLight(buildFrame(97)).record(on, 1); err != nil {
		t.Fatalf("record (volumetrics on): %v", err)
	}
	t.Logf("off: %d calls, hash %#x; on: %d calls, hash %#x",
		off.calls, uint64(off.h), on.calls, uint64(on.h))

	if off.h != goldenStreamHash {
		t.Errorf("volumetrics-off hash = %#x, want the unchanged %#x: the march is not free when nothing asks for it",
			off.h, goldenStreamHash)
	}
	if got, want := on.calls-off.calls, 6; got != want {
		t.Errorf("the sky in-scattering draw records %d driver calls, want %d "+
			"(bind pipeline, viewport, scissor, descriptor sets, push constants, draw)", got, want)
	}
	if on.h != goldenVolumetricStreamHash {
		t.Errorf("volumetrics-on command stream hash = %#x, want %#x: the draw (or its argument values) changed",
			on.h, goldenVolumetricStreamHash)
	}
}
