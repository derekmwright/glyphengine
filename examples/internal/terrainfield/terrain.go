// Package terrainfield shares 07-terrain's heightmap recipe with the forest example.
package terrainfield

import (
	"fmt"
	"math"
	"os"
	"path/filepath"

	glyph "github.com/derekmwright/glyphengine"
)

const heightScale = 14.0

// Load uses the terrain example's procedural grid unless a .heightmap is named.
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
