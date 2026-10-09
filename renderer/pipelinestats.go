package renderer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"slices"

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

	// App is the same two counters for each timed application pass, under the
	// name GPUTimings reports its timing by and in that same order, so a row
	// here reads beside the row there. For an application pass a zero has three
	// meanings rather than two, and AppPipelineStats carries which one it is.
	//
	// Safe to retain: PipelineStats and MeanPipelineStats hand out a clone, so
	// the slice is the caller's and no later frame writes through it.
	App []AppPipelineStats

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

// AppPipelineStats is one named application pass's fragment invocations and
// post-clip primitives.
//
// Two flags rather than one, because a zero here has three meanings and a game
// that cannot tell them apart will read the wrong one. Bracketed is a property
// of the pass: false for a compute dispatch -- fragment invocations and clipping
// primitives do not exist for a dispatch, and reporting zero for one would be
// inventing a measurement -- and false for the engine's own timed nodes, which
// are a transfer copy and a bracket over compute work. Valid is a property of
// this frame: false for a pass that was disabled or not yet measured when the
// slot was recorded, or whose reading a graph rebuild or a resize has since
// invalidated. A DESTROYED pass has no row at all, because its name is free for
// reuse. So Bracketed && Valid && zero is a pass that really shaded nothing, and
// the other two zeroes are not numbers at all.
type AppPipelineStats struct {
	// Name is the pass's own name, as CreateAppPass or CreateAppCompute was
	// given it and as GPUTimings().App reports it. Unique among live passes:
	// CreateAppPass refuses a duplicate precisely so this is a key.
	Name string

	FragmentInvocations uint64
	ClippingPrimitives  uint64

	// Frames is how many frames this row covers -- 1 for a latest reading, and
	// its own count for a mean, which is NOT PipelineStats.Frames: a pass
	// switched off for half a run has a mean over the half it ran.
	Frames int

	// Bracketed is whether this pass's scope can legally hold a query at all.
	Bracketed bool

	// Valid is whether this frame measured it.
	Valid bool
}

// Pass returns the counters recorded for the application pass with this name,
// and false when no row carries that name.
//
// Keyed by name rather than by index for the reason AppStats.Pass is: the index
// of a pass in graph order moves when another pass is created, and a HUD line
// keyed on it would silently start reading its neighbour.
func (p PipelineStats) Pass(name string) (AppPipelineStats, bool) {
	for _, a := range p.App {
		if a.Name == name {
			return a, true
		}
	}
	return AppPipelineStats{}, false
}

// clone detaches App from the recorder's own storage.
//
// The engine arrays are arrays and copy with the struct; App is a slice into
// storage the next collected frame refills in place, so handing it over as-is
// would give a caller a view that changes under it two frames later -- and the
// issue asks explicitly for a result that is safe to retain and copy. Cloning in
// the accessor rather than in collect is what keeps the recorded and collected
// frame allocation-free: the cost lands on whoever asks, once per ask.
func (p PipelineStats) clone() PipelineStats {
	p.App = slices.Clone(p.App)
	return p
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

// maxAppStats is how many application queries a frame slot holds.
//
// maxAppTimings and not maxAppTimings+engineTimings: the two engine nodes that
// ride in the application timing budget are the streamed-upload transfer copy
// and the GPU LOD bracket, and neither is a rendering instance, so neither can
// carry one of these (see frameGraph.statsBracketed). validateAppTiming caps
// the application's own timed passes at maxAppTimings, so this cannot be short.
const maxAppStats = maxAppTimings

// appStatsBase is the first query index of a frame slot's application region.
//
// The application regions sit past EVERY slot's engine region, the way the
// timer lays its application timestamps out, so creating an application pass
// cannot renumber an engine query and base(frame) stays what it was.
func appStatsBase(frame int) int {
	return statsQueriesPerFrame*maxFramesInFlight + frame*maxAppStats
}

// appStatsSum accumulates one application pass's counters for the mean. Its own
// frame count, because a pass disabled for part of a run is measured for part of
// a run and a mean over the whole would understate it.
type appStatsSum struct {
	frag, clip uint64
	frames     int
}

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

	// appRows is the slot's application table, rebuilt at every reset for the
	// reason gpuTimer.appRecorded is: it is the only record of what the slot
	// being read back actually recorded, and a table carried over from a
	// previous frame is how a pass gets charged its neighbour's count. It holds
	// every TIMED pass, so a row exists for one that carries no query, and
	// appQuery is that row's index inside the slot's application region or -1.
	//
	// A pass gets -1 for more than one reason -- it is not bracketable at all,
	// or it was disabled or destroyed when the slot was reset -- and the row's
	// Bracketed flag is what keeps those apart for a reader. A disabled pass is
	// deliberately given no query rather
	// than a query that would count zero: "this pass did not run" and "this pass
	// ran and shaded nothing" are different answers.
	appRows   [maxFramesInFlight][maxAppTimings + engineTimings]*AppPass
	appQuery  [maxFramesInFlight][maxAppTimings + engineTimings]int
	appRowN   [maxFramesInFlight]int
	appQueryN [maxFramesInFlight]int

	appScratch [maxAppStats * statsResultBytes]byte

	// appLatest is the row storage latest.App points into, and appOwner the pass
	// each row belongs to -- needed by invalidateApps, which has to drop the row
	// of a destroyed pass and cannot ask a name.
	appLatest [maxAppTimings + engineTimings]AppPipelineStats
	appOwner  [maxAppTimings + engineTimings]*AppPass
	appN      int
	appMean   [maxAppTimings + engineTimings]AppPipelineStats

	// bracketed is the application passes whose graph node is a rendering
	// instance of its own, rebuilt with the frame graph (see bindApps). A map
	// rather than a field on AppPass because it is a property of the live graph
	// and CreateAppPass compiles a throwaway one to read pipeline formats.
	bracketed map[*AppPass]bool

	// appSums is the mean's accumulator. Entries are created at graph rebuild
	// rather than on first collect, so a collected frame assigns to a key that
	// already exists and allocates nothing.
	appSums map[*AppPass]appStatsSum
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

	// One pool for the engine brackets and the application ones, not two.
	//
	// A second pool would buy exactly nothing: two pipeline-statistics queries
	// may not be active in one command buffer whatever pools they come from
	// (VUID-vkCmdBeginQuery-queryPool-01922), so splitting them would not make a
	// nested bracket legal -- it would only hide that it is the same rule. One
	// pool also keeps the capability gate, the single creation failure and the
	// destruction in one place, and both regions want the same two counters.
	pool, _, err := deviceDriver.CreateQueryPool(nil, core1_0.QueryPoolCreateInfo{
		QueryType:  core1_0.QueryTypePipelineStatistics,
		QueryCount: (statsQueriesPerFrame + maxAppStats) * maxFramesInFlight,
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

// resetApps rebuilds the frame slot's application table and discards the
// queries it hands out, in the same place and for the same reasons reset does.
//
// The table is decided here, before the first application edge of the slot, and
// nothing between here and the slot's readback consults the live pass list
// again. That is what makes a disabled or destroyed pass report invalid rather
// than stale: the row it gets is the row this reset gave it.
//
// Only the queries actually handed out are reset, because only those are read.
// A query that is reset and never begun comes back unavailable and makes the
// WHOLE readback NotReady -- the trap statsQueryOf's compaction exists for, and
// the one an application pass hits every time it is switched off.
func (s *pipelineStats) resetApps(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, frame int, apps []*AppPass) {
	if s == nil || !s.supported {
		return
	}
	rows, queries := 0, 0
	for _, p := range apps {
		if !p.desc.Timed || rows == len(s.appRows[frame]) {
			continue
		}
		q := -1
		if s.bracketed[p] && p.enabled && !p.destroyed && queries < maxAppStats {
			q, queries = queries, queries+1
		}
		s.appRows[frame][rows], s.appQuery[frame][rows] = p, q
		rows++
	}
	s.appRowN[frame], s.appQueryN[frame] = rows, queries
	if queries > 0 {
		deviceDriver.CmdResetQueryPool(cmdBuf, s.pool, appStatsBase(frame), queries)
	}
}

// appQueryOf is the slot query p was given this frame, or -1.
//
// A linear scan over at most eighteen entries, which is gpuTimer.appEdge's scan
// and is deliberately the same shape: the two instruments find a pass's slot the
// same way, so they cannot disagree about which pass they are bracketing.
func (s *pipelineStats) appQueryOf(frame int, p *AppPass) int {
	if s == nil || !s.supported {
		return -1
	}
	for i := 0; i < s.appRowN[frame]; i++ {
		if s.appRows[frame][i] == p {
			return s.appQuery[frame][i]
		}
	}
	return -1
}

func (s *pipelineStats) beginApp(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, frame int, p *AppPass) {
	if q := s.appQueryOf(frame, p); q >= 0 {
		deviceDriver.CmdBeginQuery(cmdBuf, s.pool, appStatsBase(frame)+q, 0)
	}
}

func (s *pipelineStats) endApp(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, frame int, p *AppPass) {
	if q := s.appQueryOf(frame, p); q >= 0 {
		deviceDriver.CmdEndQuery(cmdBuf, s.pool, appStatsBase(frame)+q)
	}
}

// bindApps records which application passes can carry a query, from the graph
// that is about to become the live one.
//
// Called where gpuTimer.apps is rebuilt, from the same list, so the statistics
// rows and the timing rows are the same rows. The mean's accumulator gets its
// keys here too, so collect only ever assigns to one that exists.
func (s *pipelineStats) bindApps(apps []*AppPass, f *frameGraph) {
	if s == nil || !s.supported {
		return
	}
	if s.bracketed == nil {
		s.bracketed = make(map[*AppPass]bool, len(apps))
	}
	if s.appSums == nil {
		s.appSums = make(map[*AppPass]appStatsSum, len(apps))
	}
	clear(s.bracketed)
	for _, p := range apps {
		if !f.statsBracketed(p) {
			continue
		}
		s.bracketed[p] = true
		if _, ok := s.appSums[p]; !ok {
			s.appSums[p] = appStatsSum{}
		}
	}
}

// forgetApp drops a destroyed pass from the mean and from the bracketed set.
// Its rows are dropped by invalidateApps, which the rebuild that follows a
// destroy runs anyway.
func (s *pipelineStats) forgetApp(p *AppPass) {
	if s == nil {
		return
	}
	delete(s.bracketed, p)
	delete(s.appSums, p)
}

// invalidateApps drops the application rows' numbers, keeping the rows.
//
// Called where a reading stops describing the frame rather than merely being a
// couple of frames old: a frame-graph rebuild, which is where a pass is created,
// destroyed or re-staged, and a swapchain rebuild, after which every count here
// is a count of covered pixels at an extent that no longer exists. Without it a
// pass that has stopped being measured keeps reporting the last number it had,
// which is the one failure mode the Valid flag exists to prevent.
//
// The row survives with Valid false rather than disappearing, so a HUD line does
// not move when an effect is switched off -- except a destroyed pass's, which
// goes: its name is free for reuse the moment it is destroyed, and a row under a
// name the next CreateAppPass can take is worse than no row. The mean goes with
// it for the same reason it is not carried across a resize: a mean of two frame
// sizes is a mean of nothing.
//
// DrawFrame collects the retired slot before it replaces a dirty graph, so this
// runs after that frame's collect rather than before it. The order is
// load-bearing: reversed, the rebuild's invalidation would be overwritten by the
// same frame's reading and nothing would ever read invalid. Slots still in
// flight were recorded against the old table and come back a frame or two later
// with real numbers, which is right -- they measured frames the passes really
// rendered -- so a rebuild costs one frame of rows and not a whole mean.
func (s *pipelineStats) invalidateApps() {
	if s == nil {
		return
	}
	n := 0
	for i := 0; i < s.appN; i++ {
		p := s.appOwner[i]
		if p == nil || p.destroyed {
			continue
		}
		row := s.appLatest[i]
		row.FragmentInvocations, row.ClippingPrimitives, row.Frames, row.Valid = 0, 0, 0, false
		s.appLatest[n], s.appOwner[n] = row, p
		n++
	}
	s.appN = n
	s.latest.App = s.appLatest[:n]
	for p := range s.appSums {
		s.appSums[p] = appStatsSum{}
	}
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
	s.collectApps(deviceDriver, frame, &out)
	s.latest = out
	for p := range out.FragmentInvocations {
		s.sumFrag[p] += out.FragmentInvocations[p]
		s.sumClip[p] += out.ClippingPrimitives[p]
	}
	s.frames++
}

// collectApps reads the slot's application region and refills the rows.
//
// The rows are rebuilt whole every time, from the table the slot was reset with,
// which is what makes a pass that recorded nothing report Valid false instead of
// its previous number. A readback that does not come back leaves every row
// invalid rather than leaving the previous frame's numbers under this frame's
// names.
//
// A destroyed pass's row is dropped here as collectApps's timing counterpart
// drops it: by the time a slot is read its pass may be gone, and a count under a
// freed name is worse than no count.
func (s *pipelineStats) collectApps(deviceDriver core1_0.DeviceDriver, frame int, out *PipelineStats) {
	read := false
	if n := s.appQueryN[frame]; n > 0 {
		res, err := deviceDriver.GetQueryPoolResults(s.pool, appStatsBase(frame), n,
			s.appScratch[:n*statsResultBytes], statsResultBytes, core1_0.QueryResult64Bit)
		read = err == nil && res == core1_0.VKSuccess
	}
	count := 0
	for i := 0; i < s.appRowN[frame]; i++ {
		p := s.appRows[frame][i]
		if p.destroyed {
			continue
		}
		row := AppPipelineStats{Name: p.desc.Name, Bracketed: s.bracketed[p]}
		if q := s.appQuery[frame][i]; q >= 0 && read {
			at := q * statsResultBytes
			row.ClippingPrimitives = binary.LittleEndian.Uint64(s.appScratch[at+statsClipping*8:])
			row.FragmentInvocations = binary.LittleEndian.Uint64(s.appScratch[at+statsFragment*8:])
			row.Frames, row.Valid = 1, true
			if _, ok := s.appSums[p]; ok {
				v := s.appSums[p]
				v.frag += row.FragmentInvocations
				v.clip += row.ClippingPrimitives
				v.frames++
				s.appSums[p] = v
			}
		}
		s.appLatest[count], s.appOwner[count] = row, p
		count++
	}
	s.appN = count
	out.App = s.appLatest[:count]
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
	// The application rows in the order and under the names the latest reading
	// has them, each averaged over ITS OWN frame count rather than over s.frames:
	// a pass switched on halfway through a run would otherwise read half of what
	// it costs, which is the shape of error that gets an effect called cheap.
	for i := 0; i < s.appN; i++ {
		row := AppPipelineStats{Name: s.appLatest[i].Name, Bracketed: s.appLatest[i].Bracketed}
		if a := s.appSums[s.appOwner[i]]; a.frames > 0 {
			row.FragmentInvocations = a.frag / uint64(a.frames)
			row.ClippingPrimitives = a.clip / uint64(a.frames)
			row.Frames, row.Valid = a.frames, true
		}
		s.appMean[i] = row
	}
	out.App = s.appMean[:s.appN]
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
	return r.pipelineStats.latest.clone(), nil
}

// MeanPipelineStats averages every frame collected since the last
// ResetPipelineStats. Prefer it over PipelineStats for any comparison: a scene
// with a moving camera covers a different number of samples on every frame, so
// one frame's counts are one camera position's counts.
func (r *Renderer) MeanPipelineStats() (PipelineStats, error) {
	if err := r.pipelineStats.err(); err != nil {
		return PipelineStats{}, err
	}
	return r.pipelineStats.mean().clone(), nil
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
	for p := range s.appSums {
		s.appSums[p] = appStatsSum{}
	}
}

// PipelineStatsSupported reports whether this renderer is recording pipeline
// statistics: the option was given AND the device granted the feature.
func (r *Renderer) PipelineStatsSupported() bool {
	return r.pipelineStats != nil && r.pipelineStats.supported
}
