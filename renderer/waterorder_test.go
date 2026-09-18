package renderer

import "testing"

// The split's rule is one line and every interesting case is a sign comparison,
// which makes it exactly the kind of thing a capture is a poor instrument for:
// an underwater camera needs someone to walk into the lake, two lakes at
// different heights need a scene nothing ships, and neither failure is loud —
// a draw on the wrong side of the surface renders, it is just refracted when it
// should not be or painted over when it should not be.
//
// So the rule is tested here instead, and the captures are left to prove that
// the rule is wired to the recorder at all.
func TestBlendSplitSeparatesEyeFromDraw(t *testing.T) {
	// One lake: surface at y=3, 50 units of reach around the origin.
	lake := waterPlane{level: 3, cx: 0, cz: 0, radius: 50}

	cases := []struct {
		name       string
		eyeY       float32
		x, y, z    float32
		wantBehind bool
	}{
		// The reported bug: standing on the shore, a flame above the water.
		{"above the surface, eye above", 6, 0, 5, 10, false},
		// The other half: a slab on the lake bed, which must stay refracted.
		{"below the surface, eye above", 6, 0, 1, 10, true},

		// Underwater, which the rule is meant to handle without a case of its
		// own. From below, the things BEHIND the water are the ones above it.
		{"above the surface, eye below", 1, 0, 5, 10, true},
		{"below the surface, eye below", 1, 0, 2, 10, false},

		// Outside the lake's reach in XZ, nothing separates anything.
		{"below the level but off the lake", 6, 200, 1, 0, false},

		// Exactly at the level counts as above, on both sides of the test, so
		// a draw sitting on the waterline with the eye above is in front.
		{"draw exactly at the level", 6, 0, 3, 10, false},
		{"eye exactly at the level", 3, 0, 1, 10, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := blendSplit{planes: []waterPlane{lake}, eyeY: c.eyeY}
			if got := s.behind(c.x, c.y, c.z); got != c.wantBehind {
				t.Errorf("behind(%g,%g,%g) with eye at %g = %v, want %v",
					c.x, c.y, c.z, c.eyeY, got, c.wantBehind)
			}
			// keep is behind for the pre-copy half and its negation for the
			// half after the water. Nothing may land in both or in neither.
			before := s.keep(false, c.x, c.y, c.z)
			after := s.keep(true, c.x, c.y, c.z)
			if before == after {
				t.Errorf("draw is recorded %v times, want exactly once", map[bool]int{true: 2, false: 0}[before])
			}
			if before != c.wantBehind {
				t.Errorf("keep(before) = %v, want %v", before, c.wantBehind)
			}
		})
	}
}

// Two bodies at different heights, which the rule handles by being deliberately
// conservative: a draw behind ANY surface goes before the copy.
//
// Note what "behind" needs, because the first version of this test got it
// wrong and so did the comment it was written from. A surface only separates
// when the EYE and the draw are on opposite sides of it. An eye between the
// two levels is under the tarn along with everything else down there, and the
// tarn separates nothing.
func TestBlendSplitSeveralBodies(t *testing.T) {
	lake := waterPlane{level: 3, cx: 0, cz: 0, radius: 50}
	tarn := waterPlane{level: 20, cx: 0, cz: 0, radius: 10} // higher and smaller
	planes := []waterPlane{lake, tarn}

	// Eye above both. A draw between the levels is above the lake — nothing
	// there — but under the tarn, which does separate it. It goes before the
	// copy and is tinted by a lake it is not in: the deliberate trade, because
	// the other way round loses it behind the surface entirely.
	above := blendSplit{planes: planes, eyeY: 30}
	if !above.behind(0, 10, 0) {
		t.Error("a draw under the higher surface, seen from above it, must go before the copy")
	}
	// Same draw, same eye, outside the tarn's reach: only the lake applies and
	// the draw is above it.
	if above.behind(30, 10, 0) {
		t.Error("outside the higher surface's footprint only the lake applies")
	}
	// Eye between the two levels. Nothing separates a draw at the same height:
	// the tarn is above both of them.
	between := blendSplit{planes: planes, eyeY: 6}
	if between.behind(0, 10, 0) {
		t.Error("a surface above both the eye and the draw separates nothing")
	}
	// The lake still does, from here.
	if !between.behind(0, 1, 0) {
		t.Error("the lake must still separate a draw under it")
	}
}

// A frame with no water must classify nothing, because the recorder's whole
// water-free path is "keep(false, ...) is true for everything".
func TestBlendSplitInactiveKeepsEverythingBeforeTheCopy(t *testing.T) {
	var s blendSplit
	if s.active() {
		t.Fatal("a split with no planes is active")
	}
	for _, y := range []float32{-100, 0, 3, 1000} {
		if !s.keep(false, 0, y, 0) {
			t.Errorf("y=%g dropped from the scene pass", y)
		}
		if s.keep(true, 0, y, 0) {
			t.Errorf("y=%g recorded after the water in a frame with none", y)
		}
	}
}

// A plane with no radius is unbounded rather than empty. A mesh with no bounds
// would otherwise separate nothing from anything, which fails in the direction
// that loses submerged geometry to the water.
func TestBlendSplitUnboundedPlane(t *testing.T) {
	s := blendSplit{planes: []waterPlane{{level: 3}}, eyeY: 6}
	if !s.behind(10000, 1, 10000) {
		t.Error("a plane with no footprint must still separate")
	}
}

func TestSplitAtWaterPartitionsInstances(t *testing.T) {
	// Six instances alternating above and below a surface at y=3, so a partition
	// that was not stable, or that lost one, shows up as a changed order rather
	// than only as a changed count.
	ps := &ParticleSystem{staging: []ParticleInstance{
		{Y: 1, Size: 1}, // below
		{Y: 5, Size: 2}, // above
		{Y: 2, Size: 3}, // below
		{Y: 6, Size: 4}, // above
		{Y: 0, Size: 5}, // below
		{Y: 9, Size: 6}, // above
	}}
	ps.InstanceCount = len(ps.staging)
	s := blendSplit{planes: []waterPlane{{level: 3}}, eyeY: 10}

	ps.splitAtWater(s)

	if ps.behind != 3 {
		t.Fatalf("behind = %d, want 3", ps.behind)
	}
	// Stable: the submerged three keep their order, then the three above keep
	// theirs. Additive blending is order sensitive enough that a sort here
	// would be a silent change to every frame's arithmetic.
	want := []float32{1, 3, 5, 2, 4, 6}
	for i, w := range want {
		if ps.staging[i].Size != w {
			t.Errorf("instance %d is %g, want %g (order: %v)", i, ps.staging[i].Size, w, sizes(ps.staging))
		}
	}
	// Reordering invalidates every frame slot's uploaded copy.
	for i, d := range ps.dirty {
		if !d {
			t.Errorf("frame slot %d not marked for re-upload after a reorder", i)
		}
	}
}

// With no water the buffer must not be touched at all: the recorder issues one
// draw over the whole of it, in the order the game supplied, which is what
// keeps water-free scenes byte for byte what they were.
func TestSplitAtWaterLeavesWaterFreeFramesAlone(t *testing.T) {
	ps := &ParticleSystem{staging: []ParticleInstance{
		{Y: 5, Size: 1}, {Y: 1, Size: 2}, {Y: 9, Size: 3},
	}}
	ps.InstanceCount = len(ps.staging)

	ps.splitAtWater(blendSplit{})

	if ps.behind != 3 {
		t.Errorf("behind = %d, want all 3 before the copy", ps.behind)
	}
	for i, w := range []float32{1, 2, 3} {
		if ps.staging[i].Size != w {
			t.Errorf("instance %d moved: order is %v", i, sizes(ps.staging))
		}
	}
	for i, d := range ps.dirty {
		if d {
			t.Errorf("frame slot %d marked for re-upload though nothing moved", i)
		}
	}
}

// Everything on one side is the common case and must not reorder either, so the
// single draw it produces is the one that always ran.
func TestSplitAtWaterAllOnOneSide(t *testing.T) {
	ps := &ParticleSystem{staging: []ParticleInstance{
		{Y: 5, Size: 1}, {Y: 6, Size: 2}, {Y: 9, Size: 3},
	}}
	ps.InstanceCount = len(ps.staging)

	ps.splitAtWater(blendSplit{planes: []waterPlane{{level: 3}}, eyeY: 10})

	if ps.behind != 0 {
		t.Errorf("behind = %d, want 0 -- every instance is above the surface", ps.behind)
	}
	for i, w := range []float32{1, 2, 3} {
		if ps.staging[i].Size != w {
			t.Errorf("instance %d moved: order is %v", i, sizes(ps.staging))
		}
	}
	for i, d := range ps.dirty {
		if d {
			t.Errorf("frame slot %d marked for re-upload though nothing moved", i)
		}
	}
}

func sizes(in []ParticleInstance) []float32 {
	out := make([]float32, len(in))
	for i := range in {
		out[i] = in[i].Size
	}
	return out
}
