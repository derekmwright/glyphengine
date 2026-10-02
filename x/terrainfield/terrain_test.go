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
