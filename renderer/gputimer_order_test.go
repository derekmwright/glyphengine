package renderer

import (
	"fmt"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// timingDriver is fakeDriver plus the two calls a live gpuTimer makes, logging
// the order of timestamps, pass ends and draws so a test can ask what each
// timed bracket actually contains.
type timingDriver struct {
	*fakeDriver
	events []string
}

func (d *timingDriver) CmdResetQueryPool(core1_0.CommandBuffer, core1_0.QueryPool, int, int) {}

func (d *timingDriver) CmdWriteTimestamp(_ core1_0.CommandBuffer, _ core1_0.PipelineStageFlags, _ core1_0.QueryPool, query int) {
	q := query % queriesPerFrame
	edge := "begin"
	if q%2 == 1 {
		edge = "end"
	}
	d.events = append(d.events, fmt.Sprintf("%s:%s", Pass(q/2), edge))
}

func (d *timingDriver) CmdEndRendering(cb core1_0.CommandBuffer) {
	d.events = append(d.events, "endpass")
	d.fakeDriver.CmdEndRendering(cb)
}

func (d *timingDriver) CmdPipelineBarrier(cb core1_0.CommandBuffer, src, dst core1_0.PipelineStageFlags, flags core1_0.DependencyFlags, memory []core1_0.MemoryBarrier, buffers []core1_0.BufferMemoryBarrier, images []core1_0.ImageMemoryBarrier) error {
	d.events = append(d.events, "barrier")
	return d.fakeDriver.CmdPipelineBarrier(cb, src, dst, flags, memory, buffers, images)
}

func (d *timingDriver) CmdDraw(cb core1_0.CommandBuffer, vertexCount, instanceCount int, firstVertex, firstInstance uint32) {
	d.events = append(d.events, "draw")
	d.fakeDriver.CmdDraw(cb, vertexCount, instanceCount, firstVertex, firstInstance)
}

func (d *timingDriver) CmdDrawIndexed(cb core1_0.CommandBuffer, indexCount, instanceCount int, firstIndex uint32, vertexOffset int, firstInstance uint32) {
	d.events = append(d.events, "draw")
	d.fakeDriver.CmdDrawIndexed(cb, indexCount, instanceCount, firstIndex, vertexOffset, firstInstance)
}

// between returns the events strictly inside a pass's bracket.
func between(t *testing.T, events []string, p Pass) []string {
	t.Helper()
	b, e := -1, -1
	for i, ev := range events {
		switch ev {
		case p.String() + ":begin":
			b = i
		case p.String() + ":end":
			e = i
		}
	}
	if b < 0 || e < 0 || e < b {
		t.Fatalf("%s: bracket not written in order (begin %d, end %d)", p, b, e)
	}
	return events[b+1 : e]
}

// TestAPassEndIsTimedAsAResolveAndNothingElse pins where the two render passes
// that resolve MSAA colour are charged. Ending such a pass is real GPU time that
// belongs to no draw, and it was charged to whichever bracket happened to close
// last: "overlay" read 0.046 ms on a scene with no overlays, and "overwater"
// 0.021 ms on a lake with nothing in front of it. Each end now sits alone in a
// bracket of its own.
//
// Verified to fail: moving PassOverlay's closing timestamp back below the scene
// pass's vkCmdEndRendering reports "overlay holds a pass end".
func TestAPassEndIsTimedAsAResolveAndNothingElse(t *testing.T) {
	fx := buildFrame(61)
	fx.timer = &gpuTimer{supported: true}
	d := &timingDriver{fakeDriver: &fakeDriver{}}
	if err := fx.record(d, 0); err != nil {
		t.Fatalf("record: %v", err)
	}

	for _, p := range []Pass{PassSceneResolve, PassWaterResolve} {
		in := between(t, d.events, p)
		if len(in) != 1 || in[0] != "endpass" {
			t.Errorf("%s brackets %v, want exactly one pass end", p, in)
		}
	}
	for _, p := range []Pass{PassOverlay, PassOverWater, PassWater, PassShafts, PassParticles, PassTranslucent} {
		for _, ev := range between(t, d.events, p) {
			if ev == "endpass" {
				t.Errorf("%s holds a pass end: its number includes a resolve it did not cause", p)
			}
		}
	}

	// The fixture has water and blended draws, so the brackets that should hold
	// work do; otherwise "no pass end inside them" would be true of empty ones.
	if n := len(between(t, d.events, PassOverWater)); n == 0 {
		t.Error("PassOverWater is empty in this fixture, so the check above proved nothing about it")
	}
}

// TestShaftBracketHoldsTheShaftDrawAndOnlyIt pins what PassShafts measures.
//
// It exists because the thing it measures spent seven weeks not existing:
// godRayPipeline was created, threaded through this recorder and never bound,
// and no test, gate or timing report noticed. A bracket that contains nothing
// would have read 0.000 ms and looked like a cheap pass rather than like a
// missing one, so the number alone is not evidence -- what is in the bracket is.
//
// Both halves matter. With shafts on it must hold exactly one draw, so that a
// bind that stops happening fails here. With shafts off it must hold none and
// still write both timestamps, because a query that is reset and never written
// makes the whole frame's readback come back NotReady; see recordCommandBuffer.
//
// Verified to fail: deleting the recordLightShafts call reports "shafts bracket
// holds [] draws, want exactly one draw". Verified the other way too: writing
// the draw unconditionally, ignoring LightShafts, reports the off case holding
// one.
func TestShaftBracketHoldsTheShaftDrawAndOnlyIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strength float32
		want     int
	}{
		{"on", 0.35, 1},
		{"off", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := buildFrame(61)
			fx.timer = &gpuTimer{supported: true}
			fx.lighting.LightShafts = tc.strength
			d := &timingDriver{fakeDriver: &fakeDriver{}}
			if err := fx.record(d, 0); err != nil {
				t.Fatalf("record: %v", err)
			}
			in := between(t, d.events, PassShafts)
			draws := 0
			for _, ev := range in {
				if ev == "draw" {
					draws++
				}
			}
			if draws != tc.want || len(in) != tc.want {
				t.Errorf("shafts bracket holds %v, want exactly %d draw(s)", in, tc.want)
			}
		})
	}
}
