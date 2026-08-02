package renderer

import (
	"encoding/binary"
	"fmt"
	"log"
	"time"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// Pass names a section of the frame that is timed separately.
//
// The split follows what can actually be changed independently — a number that
// lumps the shadow cascades in with the lit pass cannot tell you which one to
// work on, which is the whole reason for measuring rather than guessing.
type Pass int

const (
	PassShadow    Pass = iota // both sun cascades plus the point-light cube
	PassTerrain               // the splat pipeline
	PassOpaque                // lit and skinned geometry
	PassGrass                 // instanced flora
	PassSky                   // sky dome, volumetric clouds, stars
	PassParticles             // billboard particles
	PassWater                 // scene copy, refraction, god rays
	PassOverlay               // UI panels, MSDF text, unlit overlays

	passCount
)

// String is what shows up in the report; kept short so a per-frame line fits.
func (p Pass) String() string {
	switch p {
	case PassShadow:
		return "shadow"
	case PassTerrain:
		return "terrain"
	case PassOpaque:
		return "opaque"
	case PassGrass:
		return "grass"
	case PassSky:
		return "sky"
	case PassParticles:
		return "particles"
	case PassWater:
		return "water"
	case PassOverlay:
		return "overlay"
	default:
		return fmt.Sprintf("pass%d", int(p))
	}
}

// queriesPerFrame is two timestamps per pass plus a pair bracketing the whole
// frame, so the parts can be checked against the total rather than assumed to
// account for it.
const queriesPerFrame = (int(passCount) + 1) * 2

// frameQuery is the index of the whole-frame pair, sitting just past the passes.
const frameQuery = Pass(passCount)

// GPUTimings is one frame's GPU cost in milliseconds, per pass.
//
// Total is measured directly rather than summed, so the two can be compared.
// A total somewhat above the sum is a gap between passes that belongs to neither.
// A sum above the total is impossible and means the instrument is wrong -- which
// is exactly how the original TopOfPipe/BottomOfPipe mismatch was found, so the
// check is worth keeping.
type GPUTimings struct {
	Pass  [passCount]float32
	Total float32
	Valid bool
}

// gpuTimer measures each pass with timestamp queries.
//
// Timestamps read the GPU's own clock, so they measure GPU work whether or not
// presentation is blocking. That is the point: whole-frame timing has to run with
// vsync off to show anything, which pegs the card, makes it audible, and still
// cannot say which pass the time went to.
//
// Results are read for the frame slot whose fence has just been waited on, so the
// query results are guaranteed available and nothing stalls to collect them. The
// numbers are therefore maxFramesInFlight frames old, which is the right trade —
// a fresh number would cost a pipeline flush to get, and would change what it was
// measuring.
type gpuTimer struct {
	pool      core1_0.QueryPool
	period    float32 // nanoseconds per tick
	supported bool

	// scratch is reused for readback so a per-frame allocation does not show up
	// in the CPU profile of the thing measuring cost.
	scratch []byte

	latest GPUTimings

	// Running mean since the last reset. A single frame's reading swings by
	// several percent -- 7.0 to 7.3 ms on the same workload -- which is enough to
	// hide a change worth making, so a comparison should be made against the mean
	// rather than against whatever the last frame happened to cost.
	sum    GPUTimings
	frames int

	// recorded marks a frame slot whose queries have been reset by a recorded
	// command buffer at least once.
	//
	// Reading a query that has never been reset is not "not ready", it is
	// undefined -- the validation layer calls it out as QueryPool-NotReset. The
	// result comes back as NotReady either way, so the early-out for that looked
	// like it was handling this case and was not.
	recorded [maxFramesInFlight]bool
}

// newGPUTimer creates the query pool, or returns a disabled timer when the device
// cannot timestamp graphics work.
//
// Two separate capabilities have to hold, and a device can have one without the
// other: the limit says timestamps work on graphics and compute queues at all,
// and timestampValidBits says this particular queue family writes meaningful
// bits. Checking only the first is how this silently returns zeros on hardware
// that reports support but not on the queue being used.
func newGPUTimer(instanceDriver core1_0.CoreInstanceDriver, deviceDriver core1_0.DeviceDriver, physicalDevice core1_0.PhysicalDevice, graphicsFamily int) (*gpuTimer, error) {
	t := &gpuTimer{}

	props, err := instanceDriver.GetPhysicalDeviceProperties(physicalDevice)
	if err != nil {
		return t, nil // not fatal; the engine renders fine without timing
	}
	if !props.Limits.TimestampComputeAndGraphics {
		log.Println("GPU timing unavailable: device does not support graphics timestamps")
		return t, nil
	}

	families := instanceDriver.GetPhysicalDeviceQueueFamilyProperties(physicalDevice)
	if graphicsFamily >= len(families) || families[graphicsFamily].TimestampValidBits == 0 {
		log.Println("GPU timing unavailable: graphics queue writes no timestamp bits")
		return t, nil
	}

	pool, _, err := deviceDriver.CreateQueryPool(nil, core1_0.QueryPoolCreateInfo{
		QueryType:  core1_0.QueryTypeTimestamp,
		QueryCount: queriesPerFrame * maxFramesInFlight,
	})
	if err != nil {
		return nil, fmt.Errorf("create timestamp query pool: %w", err)
	}

	t.pool = pool
	t.period = props.Limits.TimestampPeriod
	t.supported = true
	t.scratch = make([]byte, queriesPerFrame*8)
	return t, nil
}

func (t *gpuTimer) destroy(deviceDriver core1_0.DeviceDriver) {
	if t == nil || !t.supported {
		return
	}
	deviceDriver.DestroyQueryPool(t.pool, nil)
}

// base is the first query index belonging to a frame slot.
func (t *gpuTimer) base(frame int) int { return frame * queriesPerFrame }

// reset discards the frame slot's previous queries. It must run outside a render
// pass, which is why it is called at the very top of the command buffer: a
// timestamp read before its query is reset returns the previous frame's value,
// and the resulting numbers look plausible while being one frame stale.
func (t *gpuTimer) reset(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, frame int) {
	if t == nil || !t.supported {
		return
	}
	deviceDriver.CmdResetQueryPool(cmdBuf, t.pool, t.base(frame), queriesPerFrame)
	t.recorded[frame] = true
}

// begin writes the opening timestamp for a pass.
//
// BottomOfPipe, the same stage end uses, and that matters. A TopOfPipe timestamp
// is written as soon as the GPU *reaches* the command, while BottomOfPipe waits
// for everything issued before it to drain. Mixing them makes each pass's
// interval start before its predecessor has finished, so the intervals overlap
// and every pass is charged for the tail of the one before it.
//
// The first version of this file did mix them, and the passes summed to 13.2 ms
// inside a 7.8 ms frame -- impossible, and caught only because the whole-frame
// bracket gave something to check the sum against. With both ends at
// BottomOfPipe each interval is exactly the work issued between the two writes.
func (t *gpuTimer) begin(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, frame int, p Pass) {
	if t == nil || !t.supported {
		return
	}
	deviceDriver.CmdWriteTimestamp(cmdBuf, core1_0.PipelineStageBottomOfPipe, t.pool, t.base(frame)+int(p)*2)
}

// end writes the closing timestamp for a pass, at BottomOfPipe so it waits for
// the pass's work to have finished rather than merely been issued.
func (t *gpuTimer) end(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, frame int, p Pass) {
	if t == nil || !t.supported {
		return
	}
	deviceDriver.CmdWriteTimestamp(cmdBuf, core1_0.PipelineStageBottomOfPipe, t.pool, t.base(frame)+int(p)*2+1)
}

// collect reads back the timings for a frame slot whose fence has signalled.
//
// Called immediately after the fence wait and before the slot's queries are
// reset, which is the one moment the results are both complete and not yet
// overwritten.
func (t *gpuTimer) collect(deviceDriver core1_0.DeviceDriver, frame int) {
	if t == nil || !t.supported {
		return
	}
	// Nothing has ever reset this slot's queries, so there is nothing legal to
	// read. Collect runs before this frame's command buffer is recorded, so every
	// slot hits this on its first time round.
	if !t.recorded[frame] {
		return
	}

	// No Wait flag: the fence has already guaranteed completion, and asking the
	// driver to wait here would turn a measurement into a stall.
	res, err := deviceDriver.GetQueryPoolResults(t.pool, t.base(frame), queriesPerFrame,
		t.scratch, 8, core1_0.QueryResult64Bit)
	if err != nil || res != core1_0.VKSuccess {
		// NotReady on the first frames, before the slot has ever been written.
		return
	}

	var out GPUTimings
	tick := func(i int) uint64 {
		return binary.LittleEndian.Uint64(t.scratch[i*8 : i*8+8])
	}
	// Nanoseconds per tick, to milliseconds.
	scale := t.period / 1e6

	for p := Pass(0); p < passCount; p++ {
		start, stop := tick(int(p)*2), tick(int(p)*2+1)
		if stop > start {
			out.Pass[p] = float32(stop-start) * scale
		}
	}
	if start, stop := tick(int(frameQuery)*2), tick(int(frameQuery)*2+1); stop > start {
		out.Total = float32(stop-start) * scale
	}
	out.Valid = true
	t.latest = out

	for p := range out.Pass {
		t.sum.Pass[p] += out.Pass[p]
	}
	t.sum.Total += out.Total
	t.frames++
}

// mean returns the average of every frame collected since the last ResetGPUTimings.
func (t *gpuTimer) mean() GPUTimings {
	if t == nil || t.frames == 0 {
		return GPUTimings{}
	}
	n := float32(t.frames)
	out := GPUTimings{Valid: true, Total: t.sum.Total / n}
	for p := range t.sum.Pass {
		out.Pass[p] = t.sum.Pass[p] / n
	}
	return out
}

// GPUTimings returns the most recent per-pass GPU timings, in milliseconds.
//
// Valid is false until enough frames have run for a slot's results to come back,
// and stays false on devices without timestamp support — so a caller printing
// these should check it rather than reporting a frame that took zero.
func (r *Renderer) GPUTimings() GPUTimings {
	if r.gpuTimer == nil {
		return GPUTimings{}
	}
	return r.gpuTimer.latest
}

// LastRecord is how long the last DrawFrame spent building the command buffer.
// Genuine CPU work, unlike the waits either side of it.
func (r *Renderer) LastRecord() time.Duration { return r.lastRecord }

// LastPresent is how long the last QueuePresent took. With vsync on it can block
// for most of a frame, which is pacing rather than work.
func (r *Renderer) LastPresent() time.Duration { return r.lastPresent }

// LastFenceWait is how long the last DrawFrame spent blocked waiting for the GPU
// and for a swapchain image.
//
// With vsync on this is mostly the pacing wait and a large value is healthy.
// With it off, or when the frame is long, a small value means the CPU is the
// limit rather than the GPU.
func (r *Renderer) LastFenceWait() time.Duration { return r.lastFenceWait }

// GPUTimingSupported reports whether the device can measure per-pass GPU time.
func (r *Renderer) GPUTimingSupported() bool {
	return r.gpuTimer != nil && r.gpuTimer.supported
}

// MeanGPUTimings averages every frame collected so far. Prefer it over
// GPUTimings for any comparison: one frame's reading moves by several percent on
// an unchanged workload, which is enough to swamp a change worth keeping.
func (r *Renderer) MeanGPUTimings() GPUTimings {
	if r.gpuTimer == nil {
		return GPUTimings{}
	}
	return r.gpuTimer.mean()
}

// ResetGPUTimings discards the accumulated mean, for measuring one phase of a run
// without the startup frames in it.
func (r *Renderer) ResetGPUTimings() {
	if r.gpuTimer == nil {
		return
	}
	r.gpuTimer.sum, r.gpuTimer.frames = GPUTimings{}, 0
}
