package renderer

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// appStatsFrame is a GPU-free frame whose application passes are TIMED, with a
// statistics recorder bound to the same pass list the timer uses.
//
// withAppFrame's passes are untimed, because the fixture's own timer is
// unsupported and the pinned stream hashes depend on it staying that way. This
// builds the other half: a supported timer, a supported recorder, and
// gpuTimer.apps assembled the way replaceAppGraph assembles it -- the
// application's passes followed by the engine's streamed-upload node. The
// upload node is timed here on purpose, because a transfer node that reads "not
// bracketed" is the second half of rule 2 and compute alone would not show it.
func appStatsFrame(n int, compute bool) (*frame, *pipelineStats) {
	fx := withAppFrame(buildFrame(n), true, compute)
	apps := append(append([]*AppPass{}, fx.graph.apps...), fx.graph.uploadTimer)
	for _, p := range apps {
		p.desc.Timed = true
	}
	s := &pipelineStats{requested: true, supported: true,
		scratch: make([]byte, statsQueriesPerFrame*statsResultBytes)}
	s.bindApps(apps, fx.graph)
	fx.timer = &gpuTimer{supported: true, stats: s, apps: apps,
		appSums: make(map[*AppPass]appTimingSum, len(apps))}
	return fx, s
}

// appEdgeDriver labels the application timestamps as well as the queries, which
// timingDriver cannot: it reduces every timestamp index modulo queriesPerFrame
// and so reports an application timestamp as some engine pass's.
type appEdgeDriver struct {
	*statsBracketDriver
}

func (d *appEdgeDriver) CmdWriteTimestamp(cb core1_0.CommandBuffer, stage core1_0.PipelineStageFlags, pool core1_0.QueryPool, query int) {
	if query < queriesPerFrame*maxFramesInFlight {
		d.statsBracketDriver.CmdWriteTimestamp(cb, stage, pool, query)
		return
	}
	q := (query - queriesPerFrame*maxFramesInFlight) % appQueriesPerFrame
	edge := "tsbegin"
	if q%2 == 1 {
		edge = "tsend"
	}
	d.events = append(d.events, fmt.Sprintf("app%d:%s", q/2, edge))
}

// indexOnce is the position of an event that must appear exactly once, or -1 for
// absent and -2 for repeated -- a doubled begin or end being as wrong as none.
func indexOnce(events []string, want string) int {
	at := -1
	for i, ev := range events {
		if ev != want {
			continue
		}
		if at >= 0 {
			return -2
		}
		at = i
	}
	return at
}

// An application pass's statistics query is begun and ended at its own timer
// edges: one begin immediately before the opening timestamp, one end immediately
// after the closing one, for every bracketable pass and for no other.
//
// This is the application half of what
// TestPipelineStatisticsBracketsMatchTheTimerBrackets holds for the engine, and
// it matters for the same reason: the number a game reads is invocations inside
// ONE pass against that pass's gpu time, so a counted interval that is not the
// timed interval is a number nobody can use. A compute dispatch and the engine's
// transfer node get no query at all, which is rule 2 -- fragment invocations and
// clipping primitives do not exist for a dispatch, so a zero for one would be a
// measurement that was never taken.
//
// Verified to fail twice. Driving stats.beginApp after appEdge in
// gpuTimer.beginApp reports "fixture mesh: query begins at event 319, want 317
// (immediately before its opening timestamp)" and "fixture fullscreen: query
// begins at event 519, want 517". Returning true from frameGraph.statsBracketed
// whatever the node is reports "3 application queries over the frame, want 2
// (the two graphics passes)" with "fixture compute is reported as bracketable;
// it is not a rendering instance" and the same for "upload" -- and the legality
// walk reports "19 queries over the frame, want 18" on the same break, which is
// the pair of tests doing their separate jobs.
func TestAppPipelineStatsBracketsAreTheTimerEdges(t *testing.T) {
	fx, s := appStatsFrame(61, true)
	d := &appEdgeDriver{statsBracketDriver: &statsBracketDriver{
		timingDriver: &timingDriver{fakeDriver: &fakeDriver{}}}}
	if err := fx.record(d, 0); err != nil {
		t.Fatalf("record: %v", err)
	}

	rows := s.appRowN[0]
	if rows == 0 {
		t.Fatal("the slot recorded no application rows, so this test says nothing")
	}
	queries, bracketed := 0, 0
	for i := 0; i < rows; i++ {
		p := s.appRows[0][i]
		ts, te := indexOnce(d.events, fmt.Sprintf("app%d:tsbegin", i)), indexOnce(d.events, fmt.Sprintf("app%d:tsend", i))
		if ts < 0 || te < 0 {
			t.Fatalf("%s: timestamps not written exactly once (begin %d, end %d)", p.desc.Name, ts, te)
		}
		q := s.appQuery[0][i]
		if q < 0 {
			if s.bracketed[p] && p.enabled {
				t.Errorf("%s is bracketable and enabled but carries no query", p.desc.Name)
			}
			continue
		}
		bracketed++
		qb, qe := indexOnce(d.events, fmt.Sprintf("app%d:qbegin", q)), indexOnce(d.events, fmt.Sprintf("app%d:qend", q))
		if qb != ts-1 {
			t.Errorf("%s: query begins at event %d, want %d (immediately before its opening timestamp)", p.desc.Name, qb, ts-1)
		}
		if qe != te+1 {
			t.Errorf("%s: query ends at event %d, want %d (immediately after its closing timestamp)", p.desc.Name, qe, te+1)
		}
	}
	for _, ev := range d.events {
		if len(ev) > 7 && ev[len(ev)-7:] == ":qbegin" && len(ev) > 3 && ev[:3] == "app" {
			queries++
		}
	}
	if queries != bracketed || queries != 2 {
		t.Errorf("%d application queries over the frame, want 2 (the two graphics passes)", queries)
	}
	// The dispatch and the transfer node are rows with no query, named so a
	// failure says which one gained or lost one.
	for _, name := range []string{"fixture compute", "upload"} {
		p := appPassByName(t, s, name)
		if s.bracketed[p] {
			t.Errorf("%s is reported as bracketable; it is not a rendering instance", name)
		}
	}
}

// appPassByName finds a pass in the slot-zero table.
func appPassByName(t *testing.T, s *pipelineStats, name string) *AppPass {
	t.Helper()
	for i := 0; i < s.appRowN[0]; i++ {
		if s.appRows[0][i].desc.Name == name {
			return s.appRows[0][i]
		}
	}
	t.Fatalf("no application row named %q", name)
	return nil
}

// appStatsDriver answers both regions of the statistics pool from bytes the test
// put there, by pool query index, so two passes cannot be given the same answer
// by accident.
type appStatsDriver struct {
	core1_0.DeviceDriver
	results map[int][2]uint64 // query index -> {clipping, fragment}
	// failApps makes reads of the application region come back NotReady while
	// the engine region still answers. The asymmetry is the case worth testing:
	// a whole slot coming back NotReady leaves the previous reading in place,
	// which is what PipelineStats has always documented, while the application
	// rows have to be rebuilt from THIS slot's table or a row keeps a number
	// that no longer belongs to it.
	failApps bool
	reads    []int
	begins   []int
	ends     []int
}

func (d *appStatsDriver) GetQueryPoolResults(_ core1_0.QueryPool, firstQuery, queryCount int, results []byte, stride int, flags core1_0.QueryResultFlags) (common.VkResult, error) {
	d.reads = append(d.reads, firstQuery)
	if d.failApps && firstQuery >= appStatsBase(0) {
		return core1_0.VKNotReady, nil
	}
	if flags&core1_0.QueryResult64Bit == 0 {
		return core1_0.VKNotReady, fmt.Errorf("read without the 64-bit flag")
	}
	for i := 0; i < queryCount; i++ {
		v := d.results[firstQuery+i]
		binary.LittleEndian.PutUint64(results[i*stride:], v[0])
		binary.LittleEndian.PutUint64(results[i*stride+8:], v[1])
	}
	return core1_0.VKSuccess, nil
}

func (d *appStatsDriver) CmdResetQueryPool(core1_0.CommandBuffer, core1_0.QueryPool, int, int) {}
func (d *appStatsDriver) CmdBeginQuery(_ core1_0.CommandBuffer, _ core1_0.QueryPool, query int, _ core1_0.QueryControlFlags) {
	d.begins = append(d.begins, query)
}
func (d *appStatsDriver) CmdEndQuery(_ core1_0.CommandBuffer, _ core1_0.QueryPool, query int) {
	d.ends = append(d.ends, query)
}

// appStatsRecorder is a recorder over hand-built passes: a pair of graphics
// passes, a dispatch and an engine node, without a graph behind them.
//
// Hand-built on purpose. frameGraph.statsBracketed is what decides bracketing in
// the real renderer and TestAppPipelineStatsBracketsAreTheTimerEdges is what
// holds it; these tests are about what the recorder DOES with that answer, and
// building the answer by hand is what lets a disabled pass, a destroyed pass and
// an unbracketable one all appear in one table.
func appStatsRecorder() (*pipelineStats, []*AppPass) {
	wide := &AppPass{desc: AppPassDesc{Name: "wide", Timed: true}, enabled: true}
	tall := &AppPass{desc: AppPassDesc{Name: "tall", Timed: true}, enabled: true}
	dispatch := &AppPass{desc: AppPassDesc{Name: "dispatch", Timed: true}, enabled: true}
	apps := []*AppPass{wide, tall, dispatch}
	s := &pipelineStats{requested: true, supported: true,
		scratch:   make([]byte, statsQueriesPerFrame*statsResultBytes),
		bracketed: map[*AppPass]bool{wide: true, tall: true},
		appSums:   map[*AppPass]appStatsSum{wide: {}, tall: {}},
	}
	return s, apps
}

// appStatsFrameRound drives one frame of the recorder's slot discipline:
// collect the retired slot, reset it, then record both edges of every pass.
func appStatsFrameRound(s *pipelineStats, d core1_0.DeviceDriver, frame int, apps []*AppPass) {
	cmd := core1_0.CommandBuffer{}
	s.collect(d, frame)
	s.recorded[frame] = true
	s.resetApps(d, cmd, frame, apps)
	for _, p := range apps {
		s.beginApp(d, cmd, frame, p)
		s.endApp(d, cmd, frame, p)
	}
}

// Two application passes read independently, and the value a caller gets does
// not alias the recorder's storage.
//
// Independently is not a formality: the rows and the queries are two parallel
// tables, and an off-by-one between them would hand each pass its neighbour's
// count -- plausible numbers, attributed to the wrong effect, which is the exact
// failure the hardware mode breaks by swapping the two query indices. Distinct
// values per query are what make a swap visible here.
//
// The aliasing half is the issue's explicit ask. App is a slice into storage the
// next collected frame refills in place, so an accessor that handed it over
// as-is would give a caller a view that changes under it two frames later.
//
// Verified to fail twice. Returning s.latest without the clone from
// Renderer.PipelineStats reports "the returned rows alias the recorder: wide
// reads 1 after the copy was mutated" and "a retained reading changed under the
// caller: wide reads 2 after two more frames" -- both halves, which is what says
// the two directions are separate failures. Reading the fragment counter from
// the clipping half of the result reports "wide: {Name:wide
// FragmentInvocations:5000 ...}, want {... FragmentInvocations:11000 ...}".
func TestAppPipelineStatsReadIndependentlyAndDoNotAlias(t *testing.T) {
	s, apps := appStatsRecorder()
	d := &appStatsDriver{results: map[int][2]uint64{
		appStatsBase(0):     {5000, 11000}, // wide
		appStatsBase(0) + 1: {700, 1300},   // tall
	}}
	appStatsFrameRound(s, d, 0, apps)
	s.collect(d, 0)

	r := &Renderer{pipelineStats: s}
	got, err := r.PipelineStats()
	if err != nil {
		t.Fatalf("PipelineStats: %v", err)
	}
	for _, want := range []AppPipelineStats{
		{Name: "wide", FragmentInvocations: 11000, ClippingPrimitives: 5000, Frames: 1, Bracketed: true, Valid: true},
		{Name: "tall", FragmentInvocations: 1300, ClippingPrimitives: 700, Frames: 1, Bracketed: true, Valid: true},
		{Name: "dispatch"},
	} {
		row, ok := got.Pass(want.Name)
		if !ok {
			t.Fatalf("no row named %q", want.Name)
		}
		if row != want {
			t.Errorf("%s: %+v, want %+v", want.Name, row, want)
		}
	}
	if _, ok := got.Pass("no such pass"); ok {
		t.Error("an unknown name was reported as a pass")
	}

	// Mutating the copy must not reach the recorder, and the recorder must not
	// reach the copy. Both directions, because a clone in one place only is
	// still a shared view from the other side.
	got.App[0].FragmentInvocations = 1
	again, err := r.PipelineStats()
	if err != nil {
		t.Fatal(err)
	}
	if row, _ := again.Pass("wide"); row.FragmentInvocations != 11000 {
		t.Errorf("the returned rows alias the recorder: wide reads %d after the copy was mutated", row.FragmentInvocations)
	}
	d.results[appStatsBase(0)] = [2]uint64{1, 2}
	appStatsFrameRound(s, d, 0, apps)
	s.collect(d, 0)
	if row, _ := again.Pass("wide"); row.FragmentInvocations != 11000 {
		t.Errorf("a retained reading changed under the caller: wide reads %d after two more frames", row.FragmentInvocations)
	}
}

// A dispatch and a disabled pass report the two kinds of "no number" rather than
// a measured zero, and neither of them is given a query.
//
// The distinction is the whole reason AppPipelineStats carries two flags. A
// dispatch has no fragment stage, so Bracketed is false forever; a disabled pass
// could be counted and was not, so Bracketed is true and Valid is false. Both
// read zero, and a game that could not tell them apart would conclude an effect
// it had switched off was free rather than absent -- which is the reported bug
// in #162 in the other direction.
//
// Giving a disabled pass a query instead would be worse than useless: the query
// would be reset and never begun, which makes the WHOLE slot's readback come
// back NotReady and loses every other pass's number with it.
//
// Verified to fail twice. Dropping the p.enabled term from resetApps's
// bracketing condition reports "a disabled pass was given query [32 33], want
// only 32" and with it "tall: Valid true with the pass disabled, reporting a
// measured zero". Reporting every row as bracketed in collectApps reports
// "dispatch: Bracketed true for a pass with no rendering instance".
func TestAppPipelineStatsDisabledAndComputeAreNotMeasuredZero(t *testing.T) {
	s, apps := appStatsRecorder()
	d := &appStatsDriver{results: map[int][2]uint64{
		appStatsBase(0):     {5000, 11000},
		appStatsBase(0) + 1: {700, 1300},
	}}
	appStatsFrameRound(s, d, 0, apps)
	s.collect(d, 0)
	if row, _ := s.latest.Pass("tall"); !row.Valid {
		t.Fatal("tall was never measured, so switching it off proves nothing")
	}

	apps[1].enabled = false
	d.begins, d.ends = nil, nil
	appStatsFrameRound(s, d, 0, apps)
	s.collect(d, 0)

	if len(d.begins) != 1 || d.begins[0] != appStatsBase(0) {
		t.Errorf("a disabled pass was given query %v, want only %d", d.begins, appStatsBase(0))
	}
	if len(d.begins) != len(d.ends) {
		t.Errorf("%d begins and %d ends; a query left open is a recording error", len(d.begins), len(d.ends))
	}
	for _, want := range []AppPipelineStats{
		{Name: "wide", FragmentInvocations: 11000, ClippingPrimitives: 5000, Frames: 1, Bracketed: true, Valid: true},
		{Name: "tall", Bracketed: true},
		{Name: "dispatch"},
	} {
		row, ok := s.latest.Pass(want.Name)
		if !ok {
			t.Fatalf("no row named %q", want.Name)
		}
		if row != want {
			t.Errorf("%s: %+v, want %+v", want.Name, row, want)
		}
	}
	if row, _ := s.latest.Pass("tall"); row.Valid {
		t.Error("tall: Valid true with the pass disabled, reporting a measured zero")
	}
	if row, _ := s.latest.Pass("dispatch"); row.Bracketed {
		t.Error("dispatch: Bracketed true for a pass with no rendering instance")
	}

	// A readback that does not come back leaves every row invalid rather than
	// last frame's numbers under this frame's names.
	apps[1].enabled = true
	appStatsFrameRound(s, d, 0, apps)
	d.failApps = true
	s.collect(d, 0)
	for _, row := range s.latest.App {
		if row.Valid || row.FragmentInvocations != 0 {
			t.Errorf("%s survived a failed readback as %+v", row.Name, row)
		}
	}
}

// A frame-graph rebuild and a resize invalidate the application rows, and a
// destroyed pass's row goes entirely.
//
// Both are cases where the numbers in hand have stopped describing the frame
// rather than merely being a couple of frames old: a rebuild is where a pass is
// created, destroyed, re-staged or re-enabled, and after a resize every count
// here is a count of covered pixels at an extent that no longer exists. A row
// that kept its last number through either would be plausible and wrong.
//
// Verified to fail twice. Removing the invalidateApps call from replaceAppGraph
// reports "fixture mesh survived the rebuild as {Name:fixture mesh
// FragmentInvocations:11000 ClippingPrimitives:5000 Frames:1 Bracketed:true
// Valid:true}". Removing it from NotifyResize reports the same line with "the
// resize" in it.
func TestAppPipelineStatsRebuildAndResizeInvalidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event func(*Renderer)
	}{
		{"the rebuild", func(r *Renderer) {
			if err := r.replaceAppGraph(false); err != nil {
				t.Fatalf("replaceAppGraph: %v", err)
			}
		}},
		{"the resize", (*Renderer).NotifyResize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, s := appStatsFrame(31, true)
			r := fx.graph.apps[0].r
			r.pipelineStats, r.gpuTimer, r.frameGraph = s, fx.timer, fx.graph

			d := &appStatsDriver{results: map[int][2]uint64{
				appStatsBase(0):     {5000, 11000},
				appStatsBase(0) + 1: {700, 1300},
			}}
			appStatsFrameRound(s, d, 0, fx.timer.apps)
			s.collect(d, 0)
			before, err := r.PipelineStats()
			if err != nil {
				t.Fatal(err)
			}
			valid := 0
			for _, row := range before.App {
				if row.Valid {
					valid++
				}
			}
			if valid != 2 {
				t.Fatalf("%d rows measured before the event, want 2; nothing would be invalidated", valid)
			}
			mean, err := r.MeanPipelineStats()
			if err != nil {
				t.Fatal(err)
			}
			if row, _ := mean.Pass("fixture mesh"); !row.Valid {
				t.Fatal("the mean has no row for the mesh pass, so clearing it proves nothing")
			}

			tc.event(r)

			after, err := r.PipelineStats()
			if err != nil {
				t.Fatal(err)
			}
			if len(after.App) != len(before.App) {
				t.Errorf("%s changed the row count from %d to %d; a live pass keeps its row",
					tc.name, len(before.App), len(after.App))
			}
			for _, row := range after.App {
				if row.Valid || row.FragmentInvocations != 0 || row.ClippingPrimitives != 0 {
					t.Errorf("%s survived %s as %+v", row.Name, tc.name, row)
				}
			}
			// The mean goes with it: a mean across a rebuild or a resize is a
			// mean of two different frames.
			meanAfter, err := r.MeanPipelineStats()
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range meanAfter.App {
				if row.Valid {
					t.Errorf("%s kept a mean across %s: %+v", row.Name, tc.name, row)
				}
			}
		})
	}
}

// A destroyed pass loses its row, because its name is free for reuse the moment
// it is destroyed and a later pass taking that name must not inherit its counts.
//
// Verified to fail: keeping destroyed rows in invalidateApps reports "the
// destroyed pass kept its row". Dropping the forgetApp call from DestroyAppPass
// reports "the destroyed pass is still in the mean's accumulator".
func TestAppPipelineStatsDestroyedPassLosesItsRow(t *testing.T) {
	fx, s := appStatsFrame(31, false)
	r := fx.graph.apps[0].r
	r.pipelineStats, r.gpuTimer, r.frameGraph = s, fx.timer, fx.graph

	d := &appStatsDriver{results: map[int][2]uint64{
		appStatsBase(0):     {5000, 11000},
		appStatsBase(0) + 1: {700, 1300},
	}}
	appStatsFrameRound(s, d, 0, fx.timer.apps)
	s.collect(d, 0)

	mesh := fx.graph.apps[0]
	if _, ok := s.latest.Pass(mesh.desc.Name); !ok {
		t.Fatalf("no row for %q before it is destroyed", mesh.desc.Name)
	}
	name := mesh.desc.Name
	r.DestroyAppPass(mesh)
	if err := r.replaceAppGraph(false); err != nil {
		t.Fatalf("replaceAppGraph: %v", err)
	}
	if row, ok := s.latest.Pass(name); ok {
		t.Errorf("the destroyed pass kept its row: %+v", row)
	}
	if _, ok := s.appSums[mesh]; ok {
		t.Error("the destroyed pass is still in the mean's accumulator")
	}
	if s.bracketed[mesh] {
		t.Error("the destroyed pass is still in the bracketed set")
	}
}

// Recording and collecting the application statistics allocates nothing per
// frame.
//
// The rows live in fixed-size storage on the recorder and the mean's map keys are
// created at graph rebuild, so an assignment during collect never grows it. The
// one allocation in this feature is the clone in the accessor, which is on
// whoever asks rather than on the frame being measured, and the second half of
// this test is what says it happens at all -- a clone that did not allocate
// would be a clone that did not copy.
//
// Verified to fail: cloning the rows in collectApps rather than in the accessor
// -- the realistic version of "make the result safe to retain" -- reports "2
// allocations per frame set, want 0", two slots' worth.
//
// A break that did NOT fail is worth recording too: creating the appSums entry
// lazily in collectApps instead of in bindApps still reports zero here, because
// this fixture's accumulator already has both keys and an assignment to an
// existing key allocates nothing. The eager creation in bindApps is what keeps
// that true for a pass created mid-run, and this test is not what holds it.
func TestAppPipelineStatsRecordAllocatesNothing(t *testing.T) {
	s, apps := appStatsRecorder()
	d := &appStatsDriver{results: map[int][2]uint64{
		appStatsBase(0): {5000, 11000}, appStatsBase(0) + 1: {700, 1300},
		appStatsBase(1): {5000, 11000}, appStatsBase(1) + 1: {700, 1300},
	}}
	for f := 0; f < maxFramesInFlight; f++ {
		s.recorded[f] = true
	}
	allocs := testing.AllocsPerRun(50, func() {
		for f := 0; f < maxFramesInFlight; f++ {
			appStatsFrameRound(s, d, f, apps)
		}
		_ = s.mean()
	})
	if allocs != 0 {
		t.Errorf("%g allocations per frame set, want 0", allocs)
	}
	if len(d.begins) == 0 || len(d.begins) != len(d.ends) {
		t.Fatalf("%d begins and %d ends reached the driver; the allocation count is of nothing", len(d.begins), len(d.ends))
	}
	if len(s.latest.App) == 0 {
		t.Fatal("no rows were produced, so the allocation count is of nothing")
	}

	r := &Renderer{pipelineStats: s}
	clones := testing.AllocsPerRun(20, func() {
		got, _ := r.PipelineStats()
		if len(got.App) != len(s.latest.App) {
			t.Fatalf("the clone has %d rows, want %d", len(got.App), len(s.latest.App))
		}
	})
	if clones == 0 {
		t.Error("PipelineStats allocated nothing, so it handed out the recorder's own storage")
	}
}
