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
		fx.instancedPipeline, fx.instancedDoubleSidedPipeline, fx.overlayPipeline, fx.skyPipeline,
		fx.starsPipeline, fx.celestialPipeline, fx.uiPipeline, fx.msdfPipeline, fx.skinnedPipeline,
		fx.grassPipeline, fx.waterPipeline, fx.godRayPipeline, fx.waterRenderPass, fx.waterFramebuffer,
		fx.sceneColor, fx.sceneImage, noClouds, fx.cloudSet, fx.bloom, fx.tonemap, fx.particlePipeline,
		fx.terrainPipeline, fx.mat, &fx.stats, fx.pipelineLayout, fx.litPipelineLayout,
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
