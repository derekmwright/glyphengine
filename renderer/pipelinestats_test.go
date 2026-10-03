package renderer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
	"github.com/vkngwrapper/extensions/v3/khr_dynamic_rendering"
)

// statsPoolDriver is the two calls newPipelineStats makes, with the create info
// kept so a test can say what the pool was asked for rather than only that one
// was asked for.
type statsPoolDriver struct {
	core1_0.DeviceDriver
	created   []core1_0.QueryPoolCreateInfo
	destroyed int
}

func (d *statsPoolDriver) CreateQueryPool(_ *loader.AllocationCallbacks, o core1_0.QueryPoolCreateInfo) (core1_0.QueryPool, common.VkResult, error) {
	d.created = append(d.created, o)
	return core1_0.InternalQueryPool(0, loader.VkQueryPool(uintptr(len(d.created))), 0), core1_0.VKSuccess, nil
}

func (d *statsPoolDriver) DestroyQueryPool(core1_0.QueryPool, *loader.AllocationCallbacks) {
	d.destroyed++
}

// A pool exists only when a build asked for one AND the device can grant it, and
// the three outcomes are distinguishable afterwards from the accessor's error
// rather than from a zero that could mean anything.
//
// Verified to fail twice. Returning nil from pipelineStats.err when nobody asked
// reports "PipelineStats: err = <nil>, want pipeline statistics not enabled: see
// WithPipelineStatistics". Creating the pool before the capability check reports
// "created 1 pools, want 0" on the asked-for-but-unsupported device, and four more
// lines with it -- including "PipelineStatsSupported() = true with 0 pools", which
// is the shape a game would act on.
func TestPipelineStatisticsPoolExistsOnlyWhenAskedAndGranted(t *testing.T) {
	granted := Capabilities{PipelineStatistics: true}
	withheld := Capabilities{}

	for _, tc := range []struct {
		name      string
		caps      Capabilities
		requested bool
		wantPools int
		wantErr   error
	}{
		{"nobody asked", granted, false, 0, ErrStatisticsNotEnabled},
		{"nobody asked, no feature", withheld, false, 0, ErrStatisticsNotEnabled},
		{"asked, no feature", withheld, true, 0, ErrCapabilityUnavailable},
		{"asked and granted", granted, true, 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &statsPoolDriver{}
			s, err := newPipelineStats(d, tc.caps, tc.requested)
			if err != nil {
				t.Fatalf("newPipelineStats: %v", err)
			}
			if len(d.created) != tc.wantPools {
				t.Errorf("created %d pools, want %d", len(d.created), tc.wantPools)
			}
			r := &Renderer{pipelineStats: s}
			got, err := r.PipelineStats()
			switch {
			case tc.wantErr == nil && err != nil:
				t.Errorf("PipelineStats: %v, want no error", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Errorf("PipelineStats: err = %v, want %v", err, tc.wantErr)
			}
			if got.Valid {
				t.Error("a reading before any frame is Valid")
			}
			// A refused accessor has to read zero, not stale or garbage: a caller
			// that ignores the error must see nothing rather than something.
			if got.FragmentInvocations[PassOpaque] != 0 || got.Frames != 0 {
				t.Errorf("refused reading is not zero: %+v", got)
			}
			if _, err := r.MeanPipelineStats(); (err == nil) != (tc.wantErr == nil) {
				t.Errorf("MeanPipelineStats err = %v, want agreement with PipelineStats", err)
			}
			if got := r.PipelineStatsSupported(); got != (tc.wantPools == 1) {
				t.Errorf("PipelineStatsSupported() = %v with %d pools", got, tc.wantPools)
			}
			// Destroying gives back exactly what was made, and never a pool that
			// was not.
			s.destroy(d)
			if d.destroyed != tc.wantPools {
				t.Errorf("destroyed %d pools, want %d", d.destroyed, tc.wantPools)
			}
		})
	}

	// What the pool was asked for, since a pool of the wrong type or with the
	// wrong counters would create successfully and count nothing useful.
	d := &statsPoolDriver{}
	if _, err := newPipelineStats(d, granted, true); err != nil {
		t.Fatal(err)
	}
	o := d.created[0]
	if o.QueryType != core1_0.QueryTypePipelineStatistics {
		t.Errorf("query type %v, want pipeline statistics", o.QueryType)
	}
	wantFlags := core1_0.QueryPipelineStatisticFragmentShaderInvocations | core1_0.QueryPipelineStatisticClippingPrimitives
	if o.PipelineStatistics != wantFlags {
		t.Errorf("statistics flags %v, want fragment invocations and clipping primitives", o.PipelineStatistics)
	}
	if want := statsQueriesPerFrame * maxFramesInFlight; o.QueryCount != want {
		t.Errorf("query count %d, want %d (%d bracketed passes x %d frame slots)",
			o.QueryCount, want, statsQueriesPerFrame, maxFramesInFlight)
	}
}

// The counters come back in the flags' BIT order, not in the order they are
// named, and reading them the other way round reports triangle counts as
// invocations -- a number that is wrong by a factor of three to five and
// perfectly plausible.
//
// Verified to fail: swapping statsClipping and statsFragment reports
// "fragment invocations = 1200, want 7000".
func TestPipelineStatisticsResultOrderIsClippingThenFragment(t *testing.T) {
	if core1_0.QueryPipelineStatisticClippingPrimitives >= core1_0.QueryPipelineStatisticFragmentShaderInvocations {
		t.Fatal("the bit order this file assumes is not the bit order Vulkan defines")
	}
	s := &pipelineStats{requested: true, supported: true, scratch: make([]byte, statsQueriesPerFrame*statsResultBytes)}
	s.recorded[0] = true
	at := statsQueryOf[PassOpaque] * statsResultBytes
	d := &statsResultDriver{results: make([]byte, len(s.scratch))}
	binary.LittleEndian.PutUint64(d.results[at:], 1200)   // clipping primitives, bit 6
	binary.LittleEndian.PutUint64(d.results[at+8:], 7000) // fragment invocations, bit 7
	s.collect(d, 0)
	if !s.latest.Valid {
		t.Fatal("collect produced no reading")
	}
	if got := s.latest.FragmentInvocations[PassOpaque]; got != 7000 {
		t.Errorf("fragment invocations = %d, want 7000", got)
	}
	if got := s.latest.ClippingPrimitives[PassOpaque]; got != 1200 {
		t.Errorf("clipping primitives = %d, want 1200", got)
	}
	if s.latest.Frames != 1 {
		t.Errorf("Frames = %d on a single reading, want 1", s.latest.Frames)
	}
}

// statsResultDriver answers a readback with fixed bytes, and refuses to answer
// for a slot the test says is still in flight.
type statsResultDriver struct {
	core1_0.DeviceDriver

	results []byte

	// inFlight marks slots whose submission has not been waited on. A read of
	// one is the mistake this driver exists to catch: on a real device the
	// result is whatever the GPU has written so far, which is a plausible number
	// from a half-finished frame rather than an error.
	inFlight  map[int]bool
	violation string
	reads     int
	resets    int
	begins    int
	ends      int
}

func (d *statsResultDriver) slotOf(firstQuery int) int { return firstQuery / statsQueriesPerFrame }

func (d *statsResultDriver) GetQueryPoolResults(_ core1_0.QueryPool, firstQuery, queryCount int, results []byte, stride int, flags core1_0.QueryResultFlags) (common.VkResult, error) {
	d.reads++
	if d.inFlight[d.slotOf(firstQuery)] {
		d.violation = fmt.Sprintf("read slot %d while its submission was still in flight", d.slotOf(firstQuery))
		return core1_0.VKNotReady, nil
	}
	if stride != statsResultBytes {
		d.violation = fmt.Sprintf("read with stride %d, want %d", stride, statsResultBytes)
	}
	if flags&core1_0.QueryResult64Bit == 0 {
		d.violation = "read without the 64-bit flag, so the counters would be truncated"
	}
	if queryCount != statsQueriesPerFrame {
		d.violation = fmt.Sprintf("read %d queries, want the slot's %d", queryCount, statsQueriesPerFrame)
	}
	if d.results != nil {
		copy(results, d.results)
	}
	return core1_0.VKSuccess, nil
}

func (d *statsResultDriver) CmdResetQueryPool(core1_0.CommandBuffer, core1_0.QueryPool, int, int) {
	d.resets++
}

func (d *statsResultDriver) CmdBeginQuery(core1_0.CommandBuffer, core1_0.QueryPool, int, core1_0.QueryControlFlags) {
	d.begins++
}

func (d *statsResultDriver) CmdEndQuery(core1_0.CommandBuffer, core1_0.QueryPool, int) { d.ends++ }

// The readback reads the slot whose fence has signalled, never one still being
// recorded or submitted, and never a slot whose queries have never been reset.
//
// Both halves are real failures seen in this engine's instruments. Reading a
// never-reset query is undefined rather than "not ready", and the result comes
// back NotReady either way, so an early-out for NotReady looks like it handles
// the case and does not. Reading a slot still in flight returns a count from a
// half-finished frame, which is a plausible number rather than an error.
//
// Verified to fail twice. Dropping the !s.recorded[frame] guard from collect
// reports "read a slot whose queries were never reset (2 reads)". Moving collect
// after reset in the loop below -- the order DrawFrame must not use -- reports
// "slot discipline: read slot 1 while its submission was still in flight".
func TestPipelineStatisticsReadsOnlyTheRetiredSlot(t *testing.T) {
	s := &pipelineStats{requested: true, supported: true, scratch: make([]byte, statsQueriesPerFrame*statsResultBytes)}
	d := &statsResultDriver{inFlight: map[int]bool{}}

	// Nothing has been recorded, so there is nothing legal to read: every slot
	// hits this on its first time round, before its first command buffer.
	for f := 0; f < maxFramesInFlight; f++ {
		s.collect(d, f)
	}
	if d.reads != 0 {
		t.Fatalf("read a slot whose queries were never reset (%d reads)", d.reads)
	}

	// DrawFrame's order, for enough frames that every slot is read after having
	// been written: wait on the slot's fence, collect it, then reset and record
	// it again. A slot is in flight from the moment it is reset until its fence
	// is waited on.
	for frame := 0; frame < maxFramesInFlight*3; frame++ {
		f := frame % maxFramesInFlight
		d.inFlight[f] = false // the fence wait
		s.collect(d, f)
		s.reset(d, core1_0.CommandBuffer{}, f)
		d.inFlight[f] = true // submitted; the GPU is writing it now
	}
	if d.violation != "" {
		t.Fatalf("slot discipline: %s", d.violation)
	}
	// Reads actually happened, or the discipline above is a statement about
	// nothing. The first pass over the slots has nothing to collect.
	if want := maxFramesInFlight * 2; d.reads != want {
		t.Errorf("%d readbacks over %d frames, want %d", d.reads, maxFramesInFlight*3, want)
	}
	if s.frames != maxFramesInFlight*2 {
		t.Errorf("accumulated %d frames, want %d", s.frames, maxFramesInFlight*2)
	}
}

// statsBracketDriver logs the query begins and ends beside the timestamps, so a
// test can say that the counted bracket is the timed bracket rather than merely
// that both exist.
type statsBracketDriver struct {
	*timingDriver
	statsPool core1_0.QueryPool
	resets    []string
}

func (d *statsBracketDriver) CmdResetQueryPool(_ core1_0.CommandBuffer, pool core1_0.QueryPool, first, count int) {
	which := "timer"
	if pool.Handle() == d.statsPool.Handle() {
		which = "stats"
	}
	d.resets = append(d.resets, fmt.Sprintf("%s:%d+%d", which, first, count))
}

func (d *statsBracketDriver) CmdBeginRendering(cb core1_0.CommandBuffer, o khr_dynamic_rendering.RenderingInfo) error {
	d.events = append(d.events, "beginpass")
	return d.timingDriver.CmdBeginRendering(cb, o)
}

func (d *statsBracketDriver) CmdBeginQuery(_ core1_0.CommandBuffer, _ core1_0.QueryPool, query int, _ core1_0.QueryControlFlags) {
	d.events = append(d.events, fmt.Sprintf("%s:qbegin", statsPassOf(query)))
}

func (d *statsBracketDriver) CmdEndQuery(_ core1_0.CommandBuffer, _ core1_0.QueryPool, query int) {
	d.events = append(d.events, fmt.Sprintf("%s:qend", statsPassOf(query)))
}

// statsPassOf inverts statsQueryOf for the driver above.
func statsPassOf(query int) Pass {
	q := query % statsQueriesPerFrame
	for p := Pass(0); p < passCount; p++ {
		if statsQueryOf[p] == q {
			return p
		}
	}
	return Pass(-1)
}

// The counted bracket is the timed bracket: one begin immediately before each
// pass's opening timestamp and one end immediately after its closing one, for
// every pass that can carry a query and for no pass that cannot.
//
// This is the whole argument for hanging the statistics off the timer rather than
// giving them call sites of their own, and it is worth a test because the failure
// is silent in both directions. A bracket that counted more than it timed would
// put another pass's fragments in this pass's ratio; one that counted less would
// report a ratio for part of a pass against the time for all of it.
//
// What it does NOT hold is that each query is legal Vulkan; that is the test
// below, and the two are separate because this one passes with a straddling
// bracket counted. A begin and an end still land either side of a pass's
// timestamps whether or not a vkCmdEndRendering sits between them.
//
// Verified to fail: moving the stats.begin call in gpuTimer.begin to after the
// opening timestamp write reports "shadow: query begins at event 6, want 4
// (immediately before its opening timestamp)", and the same for the other ten
// brackets in the fixture.
func TestPipelineStatisticsBracketsMatchTheTimerBrackets(t *testing.T) {
	fx := buildFrame(61)
	statsPool := core1_0.InternalQueryPool(0, loader.VkQueryPool(0x5747), 0)
	fx.timer = &gpuTimer{supported: true, stats: &pipelineStats{
		requested: true, supported: true, pool: statsPool,
		scratch: make([]byte, statsQueriesPerFrame*statsResultBytes),
	}}
	d := &statsBracketDriver{timingDriver: &timingDriver{fakeDriver: &fakeDriver{}}, statsPool: statsPool}
	if err := fx.record(d, 0); err != nil {
		t.Fatalf("record: %v", err)
	}

	index := func(event string) int {
		at := -1
		for i, ev := range d.events {
			if ev == event {
				if at >= 0 {
					return -2 // written twice, which is a double begin or a double end
				}
				at = i
			}
		}
		return at
	}
	counted := 0
	for p := Pass(0); p < passCount; p++ {
		qb, qe := index(p.String()+":qbegin"), index(p.String()+":qend")
		if !StatisticsBracketed(p) {
			if qb != -1 || qe != -1 {
				t.Errorf("%s carries a query, but its bracket straddles a rendering instance boundary", p)
			}
			continue
		}
		b, e := index(p.String()+":begin"), index(p.String()+":end")
		if b < 0 || e < 0 {
			t.Fatalf("%s: timestamps not written exactly once (begin %d, end %d)", p, b, e)
		}
		if qb != b-1 {
			t.Errorf("%s: query begins at event %d, want %d (immediately before its opening timestamp)", p, qb, b-1)
		}
		if qe != e+1 {
			t.Errorf("%s: query ends at event %d, want %d (immediately after its closing timestamp)", p, qe, e+1)
		}
		counted++
	}
	if counted != statsQueriesPerFrame {
		t.Errorf("%d passes carried a query, want %d", counted, statsQueriesPerFrame)
	}
	// The frame bracket is timed and deliberately not counted: it encloses every
	// other query, and two queries of the same type cannot be active at once.
	if got := index(frameQuery.String() + ":qbegin"); got != -1 {
		t.Errorf("the whole-frame bracket carries a query (event %d); it encloses every other one", got)
	}
	// Both pools are reset, once each, in the one place a recorded frame is
	// outside every render pass.
	want := []string{
		fmt.Sprintf("stats:%d+%d", 0, statsQueriesPerFrame),
		fmt.Sprintf("timer:%d+%d", 0, queriesPerFrame),
	}
	if len(d.resets) != 2 || d.resets[0] != want[0] || d.resets[1] != want[1] {
		t.Errorf("query pool resets %v, want %v", d.resets, want)
	}
}

// Every statistics query is legal Vulkan: it begins and ends inside the same
// render pass instance, or outside every one of them, and no two are ever active
// at once.
//
// These are not style rules. A query begun inside a render pass instance must end
// inside the same one (VUID-vkCmdEndRenderPass-None-00910 and the dynamic
// rendering equivalents), and two queries of the same type cannot be active in
// one command buffer (VUID-vkCmdBeginQuery-queryPool-01922). Four of the timer's
// twenty brackets break the first rule by design -- PassWater opens at the
// scene-copy node and closes inside the water pass, and the three resolve-shaped
// brackets exist precisely to straddle a vkCmdEndRendering -- and the whole-frame
// bracket breaks the second by enclosing every other one. StatisticsBracketed is
// where that is written down and this is what holds it.
//
// Without this test the exclusions are a comment: the bracket-identity test above
// passes perfectly well with PassSceneResolve counted, because the begin and the
// end still land either side of its timestamps. The failure only exists on a
// device, under the validation layer, which is the most expensive place to find
// it.
//
// Verified to fail three times, once per way a bracket can be illegal. Dropping
// PassSceneResolve from StatisticsBracketed's exclusion list reports "resolve: its
// query ends outside the render pass instance it began in" -- the bracket that
// closes after a vkCmdEndRendering. Dropping PassWater reports "water: its query
// ends 1 render pass instances deeper than it began" -- the bracket that opens
// outside one and closes inside. And a stray begin driven from the pool reset
// reports "opaque and clouds are active at the same time; two queries of one type
// cannot be", which is the rule the whole-frame bracket is excluded under; it has
// no query index at all, so that exclusion is held by statsQueryOf and by the
// query count asserted at the end of this test rather than by a branch here.
func TestPipelineStatisticsQueriesAreLegalVulkan(t *testing.T) {
	fx := buildFrame(61)
	fx.timer = &gpuTimer{supported: true, stats: &pipelineStats{
		requested: true, supported: true,
		scratch: make([]byte, statsQueriesPerFrame*statsResultBytes),
	}}
	d := &statsBracketDriver{timingDriver: &timingDriver{fakeDriver: &fakeDriver{}}}
	if err := fx.record(d, 0); err != nil {
		t.Fatalf("record: %v", err)
	}

	// The fixture has to contain rendering instances, or "no query straddles one"
	// is true of a frame with none.
	instances := 0
	for _, ev := range d.events {
		if ev == "beginpass" {
			instances++
		}
	}
	if instances == 0 {
		t.Fatal("the fixture opened no render pass instance, so this test proves nothing")
	}

	active := Pass(-1)
	depth, queries := 0, 0
	for _, ev := range d.events {
		switch {
		case ev == "beginpass":
			depth++
		case ev == "endpass":
			depth--
			if active >= 0 && depth < 0 {
				t.Errorf("%s: its query ends outside the render pass instance it began in", active)
				depth = 0
			}
		case len(ev) > 7 && ev[len(ev)-7:] == ":qbegin":
			p := Pass(-1)
			for q := Pass(0); q < passCount; q++ {
				if q.String()+":qbegin" == ev {
					p = q
				}
			}
			if active >= 0 {
				t.Errorf("%s and %s are active at the same time; two queries of one type cannot be", active, p)
			}
			active, depth, queries = p, 0, queries+1
		case len(ev) > 5 && ev[len(ev)-5:] == ":qend":
			if active < 0 {
				t.Errorf("%s ends a query that was never begun", ev)
			}
			if depth != 0 {
				t.Errorf("%s: its query ends %d render pass instances deeper than it began", active, depth)
			}
			active = Pass(-1)
		}
	}
	if active >= 0 {
		t.Errorf("%s left a query active at the end of the frame", active)
	}
	if queries != statsQueriesPerFrame {
		t.Errorf("%d queries over the frame, want %d", queries, statsQueriesPerFrame)
	}
	t.Logf("%d queries, each inside one render pass instance or outside all of them, over %d instances", queries, instances)
}

// Off is free, at the level of driver calls: a frame recorded by a renderer that
// never asked for statistics sends the driver exactly what it always did.
//
// goldenStreamHash is the pinned value and it has NOT moved across this change,
// which is the evidence rather than the claim. The second half measures what the
// option costs when it is on -- two calls per bracketed pass plus one pool reset
// -- because a hash comparison cannot tell "nothing was added" from "nothing was
// added and nothing happens when you ask for it either".
//
// Verified to fail: driving stats.begin from gpuTimer.reset as well reports
// "statistics add 34 calls to the frame, want 33".
func TestPipelineStatisticsOffChangesNothingAndOnCostsExactlyTheBrackets(t *testing.T) {
	off := &fakeDriver{hashing: true}
	if err := buildFrame(97).record(off, 1); err != nil {
		t.Fatalf("record (statistics off): %v", err)
	}
	if off.h != goldenStreamHash {
		t.Errorf("statistics-off hash = %#x, want the unchanged %#x: the option is not free when nothing asks for it",
			off.h, goldenStreamHash)
	}

	// The same fixture with timestamps on, against itself with statistics on, so
	// the difference is the statistics and nothing else. The fixture's default
	// timer is unsupported, which is why both arms set one up.
	timed := func(stats *pipelineStats) int {
		fx := buildFrame(97)
		fx.timer = &gpuTimer{supported: true, stats: stats}
		d := &countingQueryDriver{fakeDriver: &fakeDriver{}}
		if err := fx.record(d, 1); err != nil {
			t.Fatalf("record: %v", err)
		}
		return d.calls
	}
	bare := timed(nil)
	with := timed(&pipelineStats{requested: true, supported: true, scratch: make([]byte, statsQueriesPerFrame*statsResultBytes)})
	if got, want := with-bare, statsQueriesPerFrame*2+1; got != want {
		t.Errorf("statistics add %d calls to the frame, want %d (%d bracketed passes x begin+end, plus one pool reset)",
			got, want, statsQueriesPerFrame)
	}
}

// countingQueryDriver counts every driver call including the query ones, which
// fakeDriver does not implement.
type countingQueryDriver struct {
	*fakeDriver
}

func (d *countingQueryDriver) CmdResetQueryPool(core1_0.CommandBuffer, core1_0.QueryPool, int, int) {
	d.calls++
}
func (d *countingQueryDriver) CmdWriteTimestamp(core1_0.CommandBuffer, core1_0.PipelineStageFlags, core1_0.QueryPool, int) {
	d.calls++
}
func (d *countingQueryDriver) CmdBeginQuery(core1_0.CommandBuffer, core1_0.QueryPool, int, core1_0.QueryControlFlags) {
	d.calls++
}
func (d *countingQueryDriver) CmdEndQuery(core1_0.CommandBuffer, core1_0.QueryPool, int) {
	d.calls++
}

// Recording and collecting the statistics allocates nothing per frame.
//
// The readback scratch is allocated once at construction and the result struct is
// a value, so the only way this grows is somebody adding a slice or a map to the
// per-frame path -- which would show up in the CPU profile of the very thing
// being measured.
//
// Verified to fail: allocating the readback buffer inside collect instead of
// reusing s.scratch -- which is the realistic version of this mistake -- reports
// "2 allocations per frame set, want 0".
func TestPipelineStatisticsRecordAllocatesNothing(t *testing.T) {
	s := &pipelineStats{requested: true, supported: true, scratch: make([]byte, statsQueriesPerFrame*statsResultBytes)}
	d := &statsResultDriver{}
	cmd := core1_0.CommandBuffer{}
	allocs := testing.AllocsPerRun(50, func() {
		for f := 0; f < maxFramesInFlight; f++ {
			s.collect(d, f)
			s.reset(d, cmd, f)
			for p := Pass(0); p < passCount; p++ {
				s.begin(d, cmd, f, p)
				s.end(d, cmd, f, p)
			}
		}
		_ = s.mean()
	})
	if allocs != 0 {
		t.Errorf("%g allocations per frame set, want 0", allocs)
	}
	// The loop above has to have reached the driver, or zero allocations is a
	// measurement of an early return. Only the bracketed passes are begun.
	if d.begins == 0 || d.begins != d.ends {
		t.Fatalf("%d begins and %d ends reached the driver; the allocation count is of nothing", d.begins, d.ends)
	}
}

// Every Vulkan object newPipelineStats makes is given back, including when the
// one creation call it makes fails.
//
// One pool is a short unwind, and the test is here for the half of it that is not
// about counting: a teardown closure that destroys the wrong field still balances
// the count, so the kind-by-kind comparison is paired with the meta-check at the
// end that proves the balance can fail at all.
//
// Verified to fail: dropping the r.onInit teardown for the pool reports
// "QueryPool: created 1 destroyed 0" on the control; making the meta-check's own
// decrement a no-op reports "balance meta-check missed QueryPool".
func TestPipelineStatisticsCreationUnwinds(t *testing.T) {
	create := func(d *resizeFakeDriver) (*Renderer, error) {
		r := newResizeFixture(d, 3)
		r.caps.PipelineStatistics = true
		var err error
		r.pipelineStats, err = newPipelineStats(d, r.caps, true)
		if err != nil {
			return r, err
		}
		r.onInit(func() { r.pipelineStats.destroy(d) })
		return r, nil
	}
	balance := func(tb testing.TB, d *resizeFakeDriver) {
		assertBalanced(tb, d)
		if d.created["QueryPool"] != d.destroyed["QueryPool"] {
			tb.Errorf("QueryPool: created %d destroyed %d", d.created["QueryPool"], d.destroyed["QueryPool"])
		}
	}

	control := newResizeFakeDriver()
	r, err := create(control)
	if err != nil {
		t.Fatal(err)
	}
	if control.created["QueryPool"] != 1 {
		t.Fatalf("created %d query pools, want 1", control.created["QueryPool"])
	}
	r.unwindInit()
	balance(t, control)

	d := newResizeFakeDriver()
	d.failCall, d.failAt = "CreateQueryPool", 1
	r, err = create(d)
	if !errors.Is(err, errInjected) {
		t.Fatalf("error: %v, want the injected one", err)
	}
	r.unwindInit()
	balance(t, d)
	if d.created["QueryPool"] != 0 {
		t.Errorf("a failed creation left %d pools made", d.created["QueryPool"])
	}

	// The balance above has to be able to fail, or it is decoration.
	control.destroyed["QueryPool"]--
	captured := &capturingT{TB: t}
	balance(captured, control)
	control.destroyed["QueryPool"]++
	if !captured.failed {
		t.Fatal("balance meta-check missed QueryPool")
	}
	t.Log("balance meta-check detects a missing destroy for the statistics pool")
}

// The option sets the flag New reads, and nothing else: no pool, no capability
// claim.
//
// Verified to fail: making WithPipelineStatistics a no-op reports
// "WithPipelineStatistics left pipelineStatsRequested false".
func TestWithPipelineStatisticsRequestsThePool(t *testing.T) {
	var r Renderer
	WithPipelineStatistics()(&r)
	if !r.pipelineStatsRequested {
		t.Fatal("WithPipelineStatistics left pipelineStatsRequested false")
	}
	if r.PipelineStatsSupported() {
		t.Error("the option alone reports statistics as supported")
	}
	if _, err := r.PipelineStats(); !errors.Is(err, ErrStatisticsNotEnabled) {
		t.Errorf("before New, PipelineStats err = %v; a renderer with no recorder has nothing to give", err)
	}
}

// The environment override parses the way validation's does, and the name is the
// documented one.
//
// It is here because the env var is how the measurement is taken -- an example
// records statistics without growing a flag -- and a typo in the constant would
// make every such run silently record nothing, which is indistinguishable from a
// device that cannot. There was no test over validationSetting's parsing before
// this; both now go through envSetting.
//
// Verified to fail twice: ignoring the variable reports `"1": envSetting() = false,
// want the option's false -> true` on all three parseable values, and taking an
// unparseable value as false rather than ignoring it reports `"nonsense":
// envSetting() = false, want the option's true -> true`.
//
// A third attempt did NOT fail and is worth recording: swapping os.LookupEnv for
// os.Getenv changes nothing, because ParseBool("") errors and the unset case falls
// through to the same `return optValue` either way. The two-value lookup is
// clearer, not load-bearing.
func TestPipelineStatisticsEnvironmentOverride(t *testing.T) {
	const name = pipelineStatsEnvVar
	if name != "GLYPHENGINE_PIPELINE_STATS" {
		t.Fatalf("the override is %q; the docs and the Taskfile name GLYPHENGINE_PIPELINE_STATS", name)
	}
	for _, tc := range []struct {
		raw    string
		set    bool
		option bool
		want   bool
	}{
		{"", false, true, true},   // unset: the option stands
		{"", false, false, false}, //
		{"1", true, false, true},  // on for a build that did not ask
		{"0", true, true, false},  // off, overriding the option
		{"true", true, false, true},
		{"nonsense", true, true, true}, // unparseable: logged and ignored
	} {
		if tc.set {
			t.Setenv(name, tc.raw)
		} else {
			os.Unsetenv(name)
		}
		label := "unset"
		if tc.set {
			label = fmt.Sprintf("%q", tc.raw)
		}
		if got := envSetting(name, tc.option); got != tc.want {
			t.Errorf("%s: envSetting() = %v, want the option's %v -> %v", label, got, tc.option, tc.want)
		}
	}
}
