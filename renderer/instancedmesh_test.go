package renderer

import (
	"math"
	"testing"
)

// The set's bound is the only thing standing between a field of props and the
// frustum, and it fails in a way no still frame shows: too small, and the whole
// colony vanishes when the camera looks slightly away from its centre. These
// need no device -- recomputeBounds is arithmetic over the instance matrices.

func translate(x, y, z float32) [16]float32 {
	return [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, x, y, z, 1}
}

func scaled(s, x, y, z float32) [16]float32 {
	return [16]float32{s, 0, 0, 0, 0, s, 0, 0, 0, 0, s, 0, x, y, z, 1}
}

// contains reports whether every instance's own bounding sphere sits inside the
// set's. That is the property the frustum test depends on.
func contains(t *testing.T, s *InstanceSet, instances []MeshInstance) {
	t.Helper()
	mc := s.Mesh.BoundCenter
	for i := range instances {
		m := &instances[i].Model
		cx := m[0]*mc[0] + m[4]*mc[1] + m[8]*mc[2] + m[12]
		cy := m[1]*mc[0] + m[5]*mc[1] + m[9]*mc[2] + m[13]
		cz := m[2]*mc[0] + m[6]*mc[1] + m[10]*mc[2] + m[14]
		sx := float32(math.Sqrt(float64(m[0]*m[0] + m[1]*m[1] + m[2]*m[2])))
		r := s.Mesh.BoundRadius * sx

		dx, dy, dz := cx-s.boundCenter[0], cy-s.boundCenter[1], cz-s.boundCenter[2]
		d := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
		if d+r > s.boundRadius+1e-3 {
			t.Errorf("instance %d reaches %.3f from the set centre but the set radius is %.3f",
				i, d+r, s.boundRadius)
		}
	}
}

func TestInstanceSetBoundContainsEveryInstance(t *testing.T) {
	s := &InstanceSet{Mesh: &Mesh{BoundRadius: 1}}
	instances := []MeshInstance{
		{Model: translate(0, 0, 0)},
		{Model: translate(50, 0, 0)},
		{Model: translate(-50, 0, 30)},
		{Model: translate(0, 12, -80)},
	}
	s.recomputeBounds(instances)

	if s.boundRadius <= 0 {
		t.Fatal("radius is zero, so the containment check below proves nothing")
	}
	contains(t, s, instances)
}

// Scale has to reach the bound too. A set of props scaled up 10x inside a
// radius computed from the unscaled mesh is the quiet version of this bug.
func TestInstanceSetBoundAccountsForScale(t *testing.T) {
	s := &InstanceSet{Mesh: &Mesh{BoundRadius: 1}}
	instances := []MeshInstance{
		{Model: scaled(1, 0, 0, 0)},
		{Model: scaled(10, 40, 0, 0)},
	}
	s.recomputeBounds(instances)
	contains(t, s, instances)

	// The far instance's own sphere reaches 40 + 10 = 50 from the origin, and
	// the set centre sits at x=20, so nothing smaller than 30 can hold it.
	// Ignoring scale would give 21.
	if s.boundRadius < 30 {
		t.Errorf("radius %.3f is too small to hold a 10x-scaled instance; scale was not applied", s.boundRadius)
	}
}

func TestInstanceSetBoundIsEmptyWithNoInstances(t *testing.T) {
	s := &InstanceSet{Mesh: &Mesh{BoundRadius: 1}}
	s.recomputeBounds(nil)
	if s.boundRadius != 0 {
		t.Errorf("an empty set has radius %.3f, want 0 so the frustum test skips it", s.boundRadius)
	}
}

// A mat4 vertex attribute occupies four consecutive locations, which is the
// detail that makes the instanced layout non-obvious: the tint lands at 8, not
// 5. Getting it wrong gives geometry that draws, which is why this is worth
// pinning rather than eyeballing.
func TestInstanceAttributeLayout(t *testing.T) {
	attrs := instanceAttributeDescriptions()

	want := []struct {
		location uint32
		binding  int
		offset   int
	}{
		{4, 1, 0}, {5, 1, 16}, {6, 1, 32}, {7, 1, 48}, // model, four vec4 rows
		{8, 1, 64}, // tint
	}

	perVertex := len(vertexAttributeDescriptions())
	if len(attrs) != perVertex+len(want) {
		t.Fatalf("got %d attributes, want %d per-vertex plus %d per-instance",
			len(attrs), perVertex, len(want))
	}

	for i, w := range want {
		got := attrs[perVertex+i]
		if int(got.Location) != int(w.location) || got.Binding != w.binding || got.Offset != w.offset {
			t.Errorf("instance attribute %d: location %d binding %d offset %d, want %d/%d/%d",
				i, got.Location, got.Binding, got.Offset, w.location, w.binding, w.offset)
		}
	}

	// The stride has to cover the last attribute, or the driver reads the next
	// instance's model matrix as this one's tint.
	last := want[len(want)-1]
	if meshInstanceSize < last.offset+16 {
		t.Errorf("stride %d does not cover the tint at offset %d", meshInstanceSize, last.offset)
	}
	if got := instanceBindingDescription().Stride; got != meshInstanceSize {
		t.Errorf("binding stride %d does not match MeshInstance size %d", got, meshInstanceSize)
	}
}

// The three tests below mirror renderer/modeldestroy_test.go's
// TestDestroyModelIsIdempotentAndNilsHandles/DefersRatherThanFreeingNow and
// TestResourceCountsReportsTheTrackingLists, for DestroyInstanceSet instead of
// DestroyModel. Like those, they need no device: DeferDestroy,
// flushDeferredDestroys and r.instanceSets are plain bookkeeping on the
// Renderer struct, and the fixture sets carry no real buffer/memory handles,
// so the deferred callback's device calls are all gated off by the same
// `.Handle() != 0` / `!= nil` checks DestroyInstanceSet and destroy already
// have to make for a genuinely empty set. The live-resource half -- an actual
// VkBuffer released for real -- is proved under the validation layer by
// examples/22-level -reload -instanced (see docs/agents/instancing.md).

// TestDestroyInstanceSetIsIdempotentAndNilsHandles pins what a caller can
// observe about a single call without a device: the second call does
// nothing, and Mesh plus the buffer/memory/mapped handles are nil/zero
// afterwards so a stale draw fails on a nil pointer in Go (set.Mesh.vertexBuffer
// in recordInstanced/recordInstancedShadow) instead of passing a freed handle
// to the driver. count is deliberately NOT one of the things zeroed -- see
// DestroyInstanceSet's own comment for why zeroing it would turn that loud
// failure back into the silent skip both draw functions already have for
// set.count == 0.
//
// BROKEN: removed `s.destroyed = true` from DestroyInstanceSet. FAILED with:
// "the second DestroyInstanceSet queued another destroy: 2 deferred, want 1".
// Restored with `git checkout -- renderer/instancedmesh.go`.
func TestDestroyInstanceSetIsIdempotentAndNilsHandles(t *testing.T) {
	r := &Renderer{}
	s := &InstanceSet{Mesh: &Mesh{}, count: 4, capacity: 4}

	r.DestroyInstanceSet(s)
	if got := len(r.deferredDestroys); got != 1 {
		t.Fatalf("DestroyInstanceSet queued %d deferred destroys, want 1", got)
	}
	if s.Mesh != nil {
		t.Error("Mesh still set after DestroyInstanceSet")
	}
	if s.buffer.Handle() != 0 || s.memory.Handle() != 0 || s.mapped != nil {
		t.Error("buffer/memory/mapped still set after DestroyInstanceSet")
	}
	if s.count != 4 {
		t.Errorf("count changed to %d, want it left at 4", s.count)
	}

	r.DestroyInstanceSet(s)
	if got := len(r.deferredDestroys); got != 1 {
		t.Errorf("the second DestroyInstanceSet queued another destroy: %d deferred, want 1", got)
	}

	r.DestroyInstanceSet(nil) // must not panic
	if got := len(r.deferredDestroys); got != 1 {
		t.Errorf("DestroyInstanceSet(nil) queued %d deferred destroy, want 1", got)
	}
}

// TestDestroyInstanceSetDefersRatherThanFreeingNow is DestroyModel's
// frames-in-flight property, stated for InstanceSet: a submitted frame can
// still be reading this set's buffer in recordInstanced the moment a game
// gives it back -- exactly the shape of an -instanced -reload swap, where the
// new level's sets are already drawing before the old ones are released, in
// the same tick.
//
// BROKEN: made DestroyInstanceSet run its release closure inline instead of
// passing it to DeferDestroy. FAILED with: "DestroyInstanceSet freed
// immediately: 0 deferred destroys queued, want 1". Restored with
// `git checkout -- renderer/instancedmesh.go`.
func TestDestroyInstanceSetDefersRatherThanFreeingNow(t *testing.T) {
	r := &Renderer{}
	s := &InstanceSet{}
	r.DestroyInstanceSet(s)

	if got := len(r.deferredDestroys); got != 1 {
		t.Fatalf("DestroyInstanceSet freed immediately: %d deferred destroys queued, want 1", got)
	}
	if got := r.deferredDestroys[0].framesLeft; got != maxFramesInFlight {
		t.Errorf("queued with framesLeft = %d, want %d -- one per frame slot whose fence has to be waited on", got, maxFramesInFlight)
	}
}

// TestDestroyInstanceSetDeregistersOnlyWhenTheDeferredFreeRuns is issue #82's
// same-tick assertion, restated for InstanceSet: ResourceCounts().InstanceSets
// must NOT drop at the DestroyInstanceSet call, only once the deferred free
// has actually run, because the set is genuinely still alive for the frames
// still in flight at the moment of the call. This is exactly what
// examples/22-level's -reload loop compares against its steady-state baseline
// for the instanced path.
//
// BROKEN: moved the r.instanceSets deregistration out of the deferred
// closure to run synchronously inside DestroyInstanceSet instead. FAILED
// with: "InstanceSets = 0 immediately after DestroyInstanceSet, want 1 -- the
// set is still alive for 2 more frames". Restored with
// `git checkout -- renderer/instancedmesh.go`.
func TestDestroyInstanceSetDeregistersOnlyWhenTheDeferredFreeRuns(t *testing.T) {
	r := &Renderer{}
	s := &InstanceSet{}
	r.instanceSets = append(r.instanceSets, s)

	r.DestroyInstanceSet(s)
	if got := r.ResourceCounts().InstanceSets; got != 1 {
		t.Fatalf("InstanceSets = %d immediately after DestroyInstanceSet, want 1 -- the set is still alive for %d more frames", got, maxFramesInFlight)
	}

	for i := 0; i < maxFramesInFlight; i++ {
		r.flushDeferredDestroys()
	}
	if got := r.ResourceCounts().InstanceSets; got != 0 {
		t.Errorf("InstanceSets = %d after %d flushes, want 0", got, maxFramesInFlight)
	}
	if got := len(r.instanceSets); got != 0 {
		t.Errorf("%d entries still in r.instanceSets after the deferred free ran, want 0", got)
	}
}
