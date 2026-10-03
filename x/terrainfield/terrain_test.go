package terrainfield

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"testing"
)

// fieldDigest hashes the generated field as little-endian float32 bits, so a
// single changed sample moves it. Hashing a formatted string instead would round
// the values and hide exactly the drift this is here to catch.
func fieldDigest(heights []float32) string {
	h := fnv.New64a()
	var buf [4]byte
	for _, v := range heights {
		binary.LittleEndian.PutUint32(buf[:], math.Float32bits(v))
		_, _ = h.Write(buf[:])
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// TestGeneratedFieldIsPinned is this package's gate: the island it produces for
// a given seed must not change, because 07-terrain and 25-lod-forest have
// committed screenshots of it and `task determinism`, `task smoke`, `task lod`
// and `task screenshots` all run over this terrain.
//
// The two digests below were taken from the generator as it stood at
// examples/internal/terrainfield before the move to this module, by running it
// out of `git show` rather than out of the moved file -- so they are evidence
// that the move changed no bytes, not a restatement of what the moved code does.
// Nothing about the generator is allowed to drift silently afterwards: an edit
// to the octave count, the falloff or heightScale is a visible change to four
// gates' worth of images, and the cheapest place to find out is here rather than
// in a PNG diff.
//
// Verified by breaking it, 2026-10-02: changing heightScale from 14.0 to 14.1
// fails both subtests (seed 1 digests e1e761f9f5003cda rather than
// c295d09a0874c831, seed 7 1720e6169894a93a rather than ffdd668c98d3d9ef);
// changing octaves from 5 to 6 fails both as well.
func TestGeneratedFieldIsPinned(t *testing.T) {
	// Seed 1 is what both examples default to, so it is the island every
	// committed screenshot and every GPU gate renders. Seed 7 is the alternative
	// 07-terrain's usage comment advertises, and it is here so the pin covers
	// more than one lattice.
	cases := []struct {
		seed int64
		want string
	}{
		{seed: 1, want: "c295d09a0874c831"},
		{seed: 7, want: "ffdd668c98d3d9ef"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("seed %d", c.seed), func(t *testing.T) {
			heights := generateHeights(129, 129, c.seed)
			if len(heights) != 129*129 {
				t.Fatalf("got %d samples, want %d", len(heights), 129*129)
			}
			got := fieldDigest(heights)
			if got != c.want {
				t.Errorf("digest %s, want %s", got, c.want)
			}
		})
	}
}

// TestSeedsDiffer is the control for the digest above. Two seeds hashing to the
// same value would make the pin pass no matter what the generator did, and this
// repository has already shipped a gate that compared a value to itself.
func TestSeedsDiffer(t *testing.T) {
	a := fieldDigest(generateHeights(129, 129, 1))
	b := fieldDigest(generateHeights(129, 129, 7))
	if a == b {
		t.Fatalf("seeds 1 and 7 produced the same field (%s); the pin above proves nothing", a)
	}
}

// TestIslandEdgesDropToZero pins the reason the radial falloff exists rather
// than just its numbers: HeightAt stops returning ground past the heightmap
// bounds, so a player walking to the edge of a terrain that is still high there
// falls through. The falloff is what keeps them away from it, and it is the one
// part of this package's opinion that is load-bearing rather than aesthetic.
func TestIslandEdgesDropToZero(t *testing.T) {
	const grid = 129
	heights := generateHeights(grid, grid, 1)
	for i := 0; i < grid; i++ {
		for _, idx := range []int{i, (grid-1)*grid + i, i * grid, i*grid + grid - 1} {
			if heights[idx] != 0 {
				t.Fatalf("edge sample %d is %g, want 0", idx, heights[idx])
			}
		}
	}
	// And the middle is not flat, or the test above would pass on an empty
	// field.
	if heights[grid/2*grid+grid/2] == 0 {
		t.Fatal("the centre sample is 0; the field is empty and the edge check proves nothing")
	}
}

// TestRidgeHidesItsFarFlank is the only property the ridge has to have, and the
// property the measurement it exists for is void without: a camera low on one
// flank must not be able to see the TREE TOPS on the other.
//
// Tree tops rather than ground, because that is what the occlusion bench hides:
// placements 6 m tall standing on the far flank. Checked as geometry rather than
// as a picture, because it is a statement about the field and not about the
// renderer -- for a sight line from an eye 2 m over the near flank to a point 6 m
// over a far sample, the crest between them has to be higher than the line. A
// ridge that flattened would turn the occlusion bench into two controls, and
// nothing else in this repository would notice.
//
// Verified by breaking it, 2026-10-02, three ways. ridgeCrest 26 to 6 reports
// "the crest is only 8.2 m high; there is no ridge to hide behind". 26 to 14
// keeps a crest and still reports "far tree top at z=6.2 is visible over the
// crest (line 17.01 m, crest 16.25 m)". And ridgeWidth 46 to 180 -- a crest of
// the full height spread into a dome, which the crest check alone would pass --
// reports the same at line 31.78 m against the 28.25 m crest, because the far
// flank it lifts is what comes back into view.
func TestRidgeHidesItsFarFlank(t *testing.T) {
	const grid, tree = 129, 6.0
	heights := ridgeHeights(grid, grid, 1, ridgeCrest)
	at := func(iz int) (z, h float64) {
		// The grid spans -100..100 in world Z; samples are taken down the
		// middle in X, which is where the bench puts its camera.
		return -100 + 200*float64(iz)/float64(grid-1), float64(heights[iz*grid+grid/2])
	}
	eyeZ, eyeGround := at(grid / 3)
	eyeY := eyeGround + 2
	crestZ, crestY := at(grid / 2)
	if crestY < 10 {
		t.Fatalf("the crest is only %.1f m high; there is no ridge to hide behind", crestY)
	}
	hidden, visible := 0, 0
	for iz := grid/2 + 4; iz < grid-8; iz++ {
		z, h := at(iz)
		top := h + tree
		// Height of the eye-to-treetop line where it crosses the crest.
		line := eyeY + (top-eyeY)*(crestZ-eyeZ)/(z-eyeZ)
		if line < crestY {
			hidden++
			continue
		}
		visible++
		if visible < 4 {
			t.Errorf("far tree top at z=%.1f is visible over the crest (line %.2f m, crest %.2f m)", z, line, crestY)
		}
	}
	if hidden < visible {
		t.Fatalf("%d far tree tops hidden, %d visible: this ridge hides less than half of its far flank", hidden, visible)
	}
	t.Logf("crest %.2f m; %d of %d far tree tops hidden from an eye 2 m over the near flank", crestY, hidden, hidden+visible)
}

// TestRidgeEdgesDropToZero is TestIslandEdgesDropToZero for the ridge: the same
// falloff, the same reason -- HeightAt stops returning ground past the bounds.
func TestRidgeEdgesDropToZero(t *testing.T) {
	const grid = 129
	heights := ridgeHeights(grid, grid, 1, ridgeCrest)
	for i := 0; i < grid; i++ {
		for _, idx := range []int{i, (grid-1)*grid + i, i * grid, i*grid + grid - 1} {
			if heights[idx] != 0 {
				t.Fatalf("edge sample %d is %g, want 0", idx, heights[idx])
			}
		}
	}
	if heights[grid/2*grid+grid/2] == 0 {
		t.Fatal("the crest sample is 0; the field is empty and the edge check proves nothing")
	}
}

// TestAZeroCrestHidesNothing is the control's half of the property above, and
// the reason crest is a parameter: with the ridge taken out, the SAME ground,
// noise, grid and camera hide nothing, which is what an occlusion measurement
// has to be compared against.
//
// Verified by breaking it, 2026-10-02: passing ridgeCrest here instead of 0
// reports "0 of 53 far tree tops visible with no crest".
func TestAZeroCrestHidesNothing(t *testing.T) {
	const grid, tree = 129, 6.0
	heights := ridgeHeights(grid, grid, 1, 0)
	at := func(iz int) (z, h float64) {
		return -100 + 200*float64(iz)/float64(grid-1), float64(heights[iz*grid+grid/2])
	}
	eyeZ, eyeGround := at(grid / 3)
	eyeY := eyeGround + 2
	visible := 0
	for iz := grid/2 + 4; iz < grid-8; iz++ {
		z, h := at(iz)
		top := h + tree
		blocked := false
		// Every sample between the eye and the target, not just the crest line:
		// with no crest there is no single place the ground could rise.
		for s := grid / 3; s < iz; s++ {
			sz, sh := at(s)
			line := eyeY + (top-eyeY)*(sz-eyeZ)/(z-eyeZ)
			if sh > line {
				blocked = true
				break
			}
		}
		if !blocked {
			visible++
		}
	}
	if visible < 40 {
		t.Fatalf("%d of 53 far tree tops visible with no crest; the control still hides things", visible)
	}
	t.Logf("no crest: %d of 53 far tree tops visible from an eye 2 m up", visible)
}
