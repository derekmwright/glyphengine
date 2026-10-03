// Package terrainfield builds an island heightmap from value-noise fBm.
//
// This is an opinion, not a mechanism, which is why it lives in x rather than in
// the engine (ADR 0012). The engine owns glyphengine.Heightmap: the grid, the
// O(1) height query, the file format, and the renderable mesh built from the
// same grid -- none of which a game can reach past from outside. What it does
// not own is a *shape*. Five octaves at halving amplitude, a radial falloff
// starting at 0.35 of the half-diagonal, and a 14-unit height scale are one
// particular island, picked because it read well in 07-terrain and gives
// 25-lod-forest slopes to put trees on. A game wanting ridges, dunes or a
// crater replaces all of it and still wants the engine's Heightmap underneath.
//
// Deterministic for a given seed by construction: the lattice is hashed rather
// than drawn from a stateful generator, so the same seed gives the same
// []float32 regardless of platform or evaluation order. The examples' committed
// screenshots depend on that, and terrain_test.go pins it.
package terrainfield

import (
	"fmt"
	"math"
	"os"
	"path/filepath"

	glyph "github.com/derekmwright/glyphengine"
)

const heightScale = 14.0

// Load returns the procedural island unless path names a .heightmap file, in
// which case that file is loaded instead and seed is ignored. The escape hatch
// is here because a sculpted terrain exported through cmd/heightmapconv has to
// be swappable for the generated one without the caller branching -- see
// docs/agents/terrain-heightmap.md.
func Load(path string, seed int64) (*glyph.Heightmap, error) {
	if path != "" {
		dir, base := filepath.Split(path)
		if dir == "" {
			dir = "."
		}
		hm, err := glyph.LoadHeightmap(os.DirFS(dir), base)
		if err != nil {
			return nil, fmt.Errorf("load heightmap %q: %w", path, err)
		}
		return hm, nil
	}
	return glyph.NewHeightmap(129, 129, 200, 200, -100, -100, generateHeights(129, 129, seed))
}

func generateHeights(gridW, gridH int, seed int64) []float32 {
	heights := make([]float32, gridW*gridH)
	for iz := 0; iz < gridH; iz++ {
		for ix := 0; ix < gridW; ix++ {
			// Normalized [0,1] grid coordinates.
			u := float64(ix) / float64(gridW-1)
			v := float64(iz) / float64(gridH-1)

			h := fbm(u*4, v*4, seed)

			// Radial falloff so the terrain is an island: the edges drop to
			// zero, which also keeps the player away from the heightmap
			// boundary where HeightAt stops returning ground.
			dx, dz := u-0.5, v-0.5
			d := math.Sqrt(dx*dx+dz*dz) * 2 // 0 at center, 1 at edge midpoints
			falloff := 1 - smoothstep(clamp((d-0.35)/0.5, 0, 1))

			heights[iz*gridW+ix] = float32(h * falloff * heightScale)
		}
	}
	return heights
}

// hash returns a deterministic pseudo-random value in [0,1) for a lattice point.
func hash(x, y int, seed int64) float64 {
	n := int64(x)*374761393 + int64(y)*668265263 + seed*1442695040888963407
	n = (n ^ (n >> 13)) * 1274126177
	n = n ^ (n >> 16)
	return float64(n&0x7fffffff) / float64(0x7fffffff)
}

func smoothstep(t float64) float64 { return t * t * (3 - 2*t) }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// valueNoise interpolates the hashed lattice with a smoothstep falloff.
func valueNoise(x, y float64, seed int64) float64 {
	xi, yi := math.Floor(x), math.Floor(y)
	xf, yf := x-xi, y-yi
	ix, iy := int(xi), int(yi)

	v00 := hash(ix, iy, seed)
	v10 := hash(ix+1, iy, seed)
	v01 := hash(ix, iy+1, seed)
	v11 := hash(ix+1, iy+1, seed)

	sx, sy := smoothstep(xf), smoothstep(yf)
	top := v00 + (v10-v00)*sx
	bot := v01 + (v11-v01)*sx
	return top + (bot-top)*sy
}

// fbm sums octaves of value noise at halving amplitude and doubling frequency.
func fbm(x, y float64, seed int64) float64 {
	const octaves = 5
	amp, freq, sum, norm := 1.0, 1.0, 0.0, 0.0
	for i := 0; i < octaves; i++ {
		sum += valueNoise(x*freq, y*freq, seed) * amp
		norm += amp
		amp *= 0.5
		freq *= 2
	}
	return sum / norm
}

// Ridge generates a long ridge across the +Z axis: ground that hides what is on
// the other side of it, for measuring occlusion.
//
// A second opinion in this package rather than a parameter on the first, because
// it is a different shape and not a different setting of the same one. It exists
// because an island cannot be the scene an occlusion test is judged on: nothing
// on an island reliably hides anything else from a camera low on its flank, and a
// measurement needs ground that definitely does. The crest runs along X at z = 0,
// so a camera on one side sees a near slope, the crest, and nothing of the far
// slope but its tops, while both flanks hold the same number of placements.
//
// What the opinion is: a 129x129 grid over 200x200 world units centred on the
// origin, a crest 26 units high with a cosine profile 46 units wide, two octaves
// of the same value noise as the island for texture, and the island's radial
// falloff so the border samples still reach zero -- see the note on that in the
// package page, it is load-bearing rather than aesthetic. Flatter and the far
// flank is visible, which measures nothing; sharper and it hides more than real
// ground would, which measures the ridge rather than the mechanism.
//
// The crest height is a parameter of the generator below rather than of this
// function, so that terrain_test.go can take the ridge out and check that the
// same ground then hides nothing -- which is what keeps the hiding property from
// being a statement about the test's own arithmetic. A caller gets the shape.
//
// Deterministic for a given seed, by the same construction as the island.
func Ridge(seed int64) (*glyph.Heightmap, error) {
	return glyph.NewHeightmap(129, 129, 200, 200, -100, -100, ridgeHeights(129, 129, seed, ridgeCrest))
}

const (
	ridgeCrest = 26.0
	ridgeWidth = 46.0
	ridgeNoise = 3.5
)

func ridgeHeights(gridW, gridH int, seed int64, crestHeight float64) []float32 {
	heights := make([]float32, gridW*gridH)
	for iz := 0; iz < gridH; iz++ {
		for ix := 0; ix < gridW; ix++ {
			u := float64(ix) / float64(gridW-1)
			v := float64(iz) / float64(gridH-1)
			// Distance from the crest line in world units, so the profile is
			// written in the units the camera and the placements are in.
			d := math.Abs((v - 0.5) * 200)
			crest := 0.0
			if d < ridgeWidth/2 {
				crest = crestHeight * 0.5 * (1 + math.Cos(math.Pi*d/(ridgeWidth/2)))
			}
			// Two octaves, not the island's five: the ridge is a silhouette
			// here, and detail on it only adds triangles to a terrain mesh the
			// measurement is not about.
			rough := (valueNoise(u*6, v*6, seed)-0.5)*ridgeNoise +
				(valueNoise(u*12, v*12, seed+7)-0.5)*ridgeNoise*0.5
			dx, dz := u-0.5, v-0.5
			dist := math.Sqrt(dx*dx+dz*dz) * 2
			falloff := 1 - smoothstep(clamp((dist-0.35)/0.5, 0, 1))
			heights[iz*gridW+ix] = float32((crest + rough + 1) * falloff)
		}
	}
	return heights
}
