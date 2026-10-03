package renderer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// PipelineStats is one frame's fragment-shader invocations and post-clip
// primitives, per pass.
//
// It answers the question the timer cannot: whether a pass got slower because it
// shades more fragments or because it shades the same ones more slowly. The two
// counters are the numerator and the denominator of a shading-cost ratio --
// FragmentInvocations over the samples a frame actually covers is how much of
// the fragment work is real, and ClippingPrimitives over those samples is how
// finely the geometry is diced.
//
// FragmentInvocations is the counter whose definition is implementation-defined
// in the one way that matters here: whether it includes the HELPER lanes a 2x2
// quad runs for a triangle that does not fill the quad. Vulkan permits either,
// so the number means nothing until that is established on the device in hand.
// cmd/quadcheck establishes it from geometry whose true quad factor is known,
// and docs/agents/profiling.md records what this machine answered. Do not read a
// ratio off this struct without reading that.
//
// Counts are per frame, not per second, and they are exact rather than sampled:
// two runs of the same scene under the fixed clock produce the same numbers,
// which is what makes a difference in them a change in work rather than noise.
type PipelineStats struct {
	// FragmentInvocations is VK_QUERY_PIPELINE_STATISTIC_FRAGMENT_SHADER_INVOCATIONS,
	// per pass: how many times the fragment stage ran inside that bracket.
	FragmentInvocations [passCount]uint64

	// ClippingPrimitives is VK_QUERY_PIPELINE_STATISTIC_CLIPPING_PRIMITIVES, per
	// pass: the primitives that came out of the clipping stage, which is the
	// triangle count a triangles-per-pixel figure needs. It is NOT
	// RenderStats.Triangles -- that one counts what the vertex stage was asked
	// for, before clipping and before any of it left the frustum, and it is
	// recorded on the CPU rather than measured on the GPU. Face culling happens
	// after clipping, so a back-facing triangle is in here and shades nothing.
	ClippingPrimitives [passCount]uint64

	// Frames is how many frames these numbers cover: 1 for the latest reading
	// and the averaged count for a mean. Reported because a mean over three
	// frames and a mean over two hundred are not the same evidence.
	Frames int

	// Valid is false until a frame slot's results have come back, and on a
	// renderer that is not recording statistics at all. A zero counter on a
	// Valid reading is a pass that shaded nothing; a zero counter on an invalid
	// one has not been measured. See StatisticsBracketed for the third case --
	// a pass that is measured on the clock and deliberately not here.
	Valid bool
}

// StatisticsBracketed reports whether p's GPU-timing bracket also carries
// pipeline statistics.
//
// Exported because a zero in PipelineStats has to be readable as "shaded
// nothing" or "not counted here", and nothing else can tell those apart. Four of
// the twenty brackets are not counted, and the reason is a Vulkan rule rather
// than a choice:
//
//   - A pipeline-statistics query is begun and ended with commands, not written
//     at a point like a timestamp, and a query begun inside a render pass
//     instance must END inside the same one. PassWater opens at the scene-copy
//     node and closes inside the water pass; PassSceneResolve, PassWaterResolve
//     and PassTonemap each deliberately straddle a vkCmdEndRendering, which is
//     the whole point of those three brackets. None of them can be a query.
//   - The whole-frame bracket is out for a second reason: it encloses every pass
//     bracket, and two queries of the same type cannot be active in one command
//     buffer at once (VUID-vkCmdBeginQuery-queryPool-01922). There is therefore
//     no whole-frame statistic to check the per-pass sum against, which is a
//     real loss -- the timer's equivalent check is what caught its
//     TopOfPipe/BottomOfPipe mismatch.
//
// Moving a bracket so that it straddles a rendering instance would make the
// query illegal rather than wrong, and the validation layer says so loudly;
// TestPipelineStatisticsBracketsMatchTheTimerBrackets is the GPU-free half.
func StatisticsBracketed(p Pass) bool {
	switch p {
	case PassWater, PassSceneResolve, PassWaterResolve, PassTonemap:
		return false
	}
	return p >= 0 && p < passCount
}

// statsQueries is the counters a single query holds, in the order the driver
// writes them: the flags' bit order, not the order they are named in.
// CLIPPING_PRIMITIVES is bit 6 and FRAGMENT_SHADER_INVOCATIONS bit 7, so
// clipping comes first in every result. Reading them the other way round
// reports triangle counts as invocations and is plausible enough to survive a
// glance, which is why the order is spelled out here and pinned by a test.
const (
	statsClipping    = 0
	statsFragment    = 1
	statsPerQuery    = 2
	statsResultBytes = statsPerQuery * 8
)

// statsQueryOf maps a pass to its query index inside a frame slot, or -1.
//
// Compact rather than one query per Pass, because an allocated query that is
// never begun comes back "unavailable" and makes the WHOLE readback return
// NotReady -- the same trap the timer records at its water arm, except that a
// timestamp can be written unconditionally and a begin/end query over a bracket
// that cannot hold one cannot. Compacting them means every query in the range
// read is one that was begun and ended this frame.
var statsQueryOf, statsQueriesPerFrame = func() ([passCount]int, int) {
	var idx [passCount]int
	n := 0
	for p := Pass(0); p < passCount; p++ {
		if !StatisticsBracketed(p) {
			idx[p] = -1
			continue
		}
		idx[p] = n
		n++
	}
	return idx, n
}()

// pipelineStats records the per-pass statistics with one query per bracketed
// pass per frame slot.
//
// It is read back a frame late through the same slot discipline the timer uses,
// and for the same reason: the one moment a slot's results are both complete and
// not yet overwritten is after its fence has signalled and before its queries
// are reset, and asking for them anywhere else costs a stall that would change
// what was being measured.
//
// It is driven from the gpuTimer's own begin/end/reset/collect rather than from
// call sites of its own. That is deliberate and it is the single most important
// decision in this file: the measurement this instrument exists for is
// invocations per covered sample inside ONE pass, read against that pass's
// gpu time, and two independent sets of bracket call sites is exactly how an
// instrument's bracket drifts away from the one it is being compared against.
type pipelineStats struct {
	// requested is WithPipelineStatistics. supported is that AND the device
	// feature; both are kept because "nobody asked" and "the device cannot" are
	// different answers to a caller and only one of them is a capability
	// problem.
	requested bool
	supported bool

	pool core1_0.QueryPool

	// scratch is reused for readback, so collecting does not allocate inside the
	// frame loop of the thing being measured.
	scratch []byte

	latest PipelineStats

	// Running sums since the last ResetPipelineStats. Counts rather than times,
	// so these do not drift the way the timer's do -- but a mean is still the
	// right comparator, because a scene with a moving camera covers a different
	// number of pixels on every frame.
	sumFrag [passCount]uint64
	sumClip [passCount]uint64
	frames  int

	// recorded marks a frame slot whose queries have been reset at least once.
	// Reading a query that was never reset is undefined rather than "not
	// ready", and it comes back NotReady either way -- see the same field on
	// gpuTimer.
	recorded [maxFramesInFlight]bool
}

// newPipelineStats creates the query pool, or returns an inert recorder.
//
// Three outcomes, and they are distinguishable afterwards on purpose: nobody
// asked (no pool, no feature needed, the accessors say so), somebody asked and
// the device has no pipelineStatisticsQuery (no pool, the accessors wrap
// ErrCapabilityUnavailable), or the pool exists. The middle one logs, because a
// game that asked for a measurement and silently never got one is the failure
// mode this engine's pages keep warning about.
//
// Zero cost when nobody asks: no pool, no reset, no begin, no end, no readback,
// and therefore not one extra driver call in a recorded frame.
// TestRecordCommandBufferStreamIsUnchanged holds that at the level of call
// arguments.
func newPipelineStats(deviceDriver core1_0.DeviceDriver, caps Capabilities, requested bool) (*pipelineStats, error) {
	s := &pipelineStats{requested: requested}
	if !requested {
		return s, nil
	}
	if !caps.PipelineStatistics {
		log.Println("Pipeline statistics unavailable: the device does not support pipelineStatisticsQuery, " +
			"so PipelineStats stays invalid and its accessors return ErrCapabilityUnavailable")
		return s, nil
	}

	pool, _, err := deviceDriver.CreateQueryPool(nil, core1_0.QueryPoolCreateInfo{
		QueryType:  core1_0.QueryTypePipelineStatistics,
		QueryCount: statsQueriesPerFrame * maxFramesInFlight,
		PipelineStatistics: core1_0.QueryPipelineStatisticFragmentShaderInvocations |
			core1_0.QueryPipelineStatisticClippingPrimitives,
	})
	if err != nil {
		return nil, fmt.Errorf("create pipeline statistics query pool: %w", err)
	}
	s.pool = pool
	s.supported = true
	s.scratch = make([]byte, statsQueriesPerFrame*statsResultBytes)
	return s, nil
}

func (s *pipelineStats) destroy(deviceDriver core1_0.DeviceDriver) {
	if s == nil || !s.supported {
		return
	}
	deviceDriver.DestroyQueryPool(s.pool, nil)
}

// base is the first query index belonging to a frame slot.
func (s *pipelineStats) base(frame int) int { return frame * statsQueriesPerFrame }

// reset discards the frame slot's previous queries. Outside any render pass,
// which vkCmdResetQueryPool requires, and before the first query of the slot is
// begun.
func (s *pipelineStats) reset(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, frame int) {
	if s == nil || !s.supported {
		return
	}
	deviceDriver.CmdResetQueryPool(cmdBuf, s.pool, s.base(frame), statsQueriesPerFrame)
	s.recorded[frame] = true
}

func (s *pipelineStats) begin(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, frame int, p Pass) {
	if s == nil || !s.supported || p < 0 || p >= passCount || statsQueryOf[p] < 0 {
		return
	}
	deviceDriver.CmdBeginQuery(cmdBuf, s.pool, s.base(frame)+statsQueryOf[p], 0)
}

func (s *pipelineStats) end(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, frame int, p Pass) {
	if s == nil || !s.supported || p < 0 || p >= passCount || statsQueryOf[p] < 0 {
		return
	}
	deviceDriver.CmdEndQuery(cmdBuf, s.pool, s.base(frame)+statsQueryOf[p])
}

// collect reads back the statistics for a frame slot whose fence has signalled.
func (s *pipelineStats) collect(deviceDriver core1_0.DeviceDriver, frame int) {
	if s == nil || !s.supported || !s.recorded[frame] {
		return
	}
	// No Wait flag, for the reason gpuTimer.collect gives: the fence has already
	// guaranteed completion and asking the driver to wait turns a measurement
	// into a stall.
	res, err := deviceDriver.GetQueryPoolResults(s.pool, s.base(frame), statsQueriesPerFrame,
		s.scratch, statsResultBytes, core1_0.QueryResult64Bit)
	if err != nil || res != core1_0.VKSuccess {
		return
	}

	out := PipelineStats{Frames: 1, Valid: true}
	for p := Pass(0); p < passCount; p++ {
		q := statsQueryOf[p]
		if q < 0 {
			continue
		}
		at := q * statsResultBytes
		out.ClippingPrimitives[p] = binary.LittleEndian.Uint64(s.scratch[at+statsClipping*8:])
		out.FragmentInvocations[p] = binary.LittleEndian.Uint64(s.scratch[at+statsFragment*8:])
	}
	s.latest = out
	for p := range out.FragmentInvocations {
		s.sumFrag[p] += out.FragmentInvocations[p]
		s.sumClip[p] += out.ClippingPrimitives[p]
	}
	s.frames++
}

// mean averages every frame collected since the last reset.
//
// Integer division, because the counters are counts: a mean of 4,812,003.6
// invocations says nothing a mean of 4,812,003 does not, and a float32 cannot
// hold eight significant digits anyway -- which is a real trap at these
// magnitudes, not a theoretical one.
func (s *pipelineStats) mean() PipelineStats {
	if s == nil || s.frames == 0 {
		return PipelineStats{}
	}
	n := uint64(s.frames)
	out := PipelineStats{Frames: s.frames, Valid: true}
	for p := range s.sumFrag {
		out.FragmentInvocations[p] = s.sumFrag[p] / n
		out.ClippingPrimitives[p] = s.sumClip[p] / n
	}
	return out
}

// err is what an accessor returns beside a zero reading: nothing when the
// statistics are live, and otherwise the reason there is nothing to read.
func (s *pipelineStats) err() error {
	switch {
	case s != nil && s.supported:
		return nil
	case s == nil || !s.requested:
		return ErrStatisticsNotEnabled
	default:
		return unavailable("pipelineStatisticsQuery")
	}
}

// ErrStatisticsNotEnabled is returned by the pipeline-statistics accessors on a
// renderer that never asked for them.
//
// A separate sentinel from ErrCapabilityUnavailable, and the distinction is the
// point: "this device cannot count fragment invocations" is something a game
// branches on and reports, while "this build did not ask" is a bug in the
// program doing the asking. Returning the capability error for both would send
// someone looking at their driver for a missing WithPipelineStatistics.
var ErrStatisticsNotEnabled = errors.New("pipeline statistics not enabled: see WithPipelineStatistics")

// PipelineStats returns the most recent per-pass fragment-invocation and
// post-clip primitive counts.
//
// The reading is a couple of frames old, for the reason GPUTimings is. The error
// is ErrStatisticsNotEnabled on a renderer built without
// WithPipelineStatistics, and wraps ErrCapabilityUnavailable on a device without
// the pipelineStatisticsQuery feature -- in both cases beside a zero value, so a
// caller that ignores the error reads zeros rather than nonsense, and
// PipelineStats.Valid is false.
func (r *Renderer) PipelineStats() (PipelineStats, error) {
	if err := r.pipelineStats.err(); err != nil {
		return PipelineStats{}, err
	}
	return r.pipelineStats.latest, nil
}

// MeanPipelineStats averages every frame collected since the last
// ResetPipelineStats. Prefer it over PipelineStats for any comparison: a scene
// with a moving camera covers a different number of samples on every frame, so
// one frame's counts are one camera position's counts.
func (r *Renderer) MeanPipelineStats() (PipelineStats, error) {
	if err := r.pipelineStats.err(); err != nil {
		return PipelineStats{}, err
	}
	return r.pipelineStats.mean(), nil
}

// ResetPipelineStats discards the accumulated mean, for measuring one phase of a
// run without the startup frames in it. It is a no-op on a renderer with no
// statistics, so a harness can call it beside ResetGPUTimings unconditionally.
func (r *Renderer) ResetPipelineStats() {
	s := r.pipelineStats
	if s == nil {
		return
	}
	s.sumFrag, s.sumClip, s.frames = [passCount]uint64{}, [passCount]uint64{}, 0
}

// PipelineStatsSupported reports whether this renderer is recording pipeline
// statistics: the option was given AND the device granted the feature.
func (r *Renderer) PipelineStatsSupported() bool {
	return r.pipelineStats != nil && r.pipelineStats.supported
}
