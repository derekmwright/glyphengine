package glyphengine

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// submergedHeightmap returns a flat basin low enough that every grid point is
// under level, so WaterMesh keeps the whole grid and every vertex is testable.
func submergedHeightmap(t *testing.T, grid int, world float32) *Heightmap {
	t.Helper()
	heights := make([]float32, grid*grid)
	for i := range heights {
		heights[i] = -10
	}
	hm, err := NewHeightmap(grid, grid, world, world, -world/2, -world/2, heights)
	if err != nil {
		t.Fatalf("heightmap: %v", err)
	}
	return hm
}

// TestWaterMeshBakesVertexSpacing checks WaterMesh writes the surface grid's
// vertex spacing into UV.y.
//
// water.vert needs it to know which wave components its own mesh is too coarse
// to represent, and it has no other way to find out — the vertex shader sees one
// vertex at a time. Dropping it is silent in the worst way: the shader treats a
// zero spacing as "unknown" and skips the Nyquist fade entirely, which restores
// the hard faceted tearing the fade exists to prevent, with nothing failing.
//
// It has teeth: reverting the UV.y write to 0 fails every case here.
func TestWaterMeshBakesVertexSpacing(t *testing.T) {
	cases := []struct {
		world      float32
		resolution int
		want       float32
	}{
		{world: 200, resolution: 160, want: 1.25},
		{world: 200, resolution: 400, want: 0.5},
		{world: 64, resolution: 64, want: 1.0},
	}

	for _, c := range cases {
		hm := submergedHeightmap(t, 33, c.world)
		opts := DefaultWaterOptions(0)
		opts.Resolution = c.resolution

		verts, _, err := WaterMesh(hm, opts)
		if err != nil {
			t.Fatalf("world=%g res=%d: %v", c.world, c.resolution, err)
		}
		if len(verts) == 0 {
			t.Fatalf("world=%g res=%d: no vertices", c.world, c.resolution)
		}

		for i, v := range verts {
			if v.UV[1] != c.want {
				t.Errorf("world=%g res=%d: vertex %d spacing = %g, want %g",
					c.world, c.resolution, i, v.UV[1], c.want)
				break
			}
		}
	}
}

// TestDefaultWaterOptionsStayInsideTheFoldLimit checks the shipped defaults do
// not ask the water shader for a surface that folds through itself.
//
// Gerstner's horizontal displacement stops being invertible once
// sum(steepness*k*amplitude) reaches one: adjacent vertices swap order, the
// surface passes through itself, and the same sum turns the analytic normal
// inside out. The shader clamps steepness to stay under FOLD_LIMIT, so nothing
// can actually fold — but a default that only renders correctly *because* of
// that clamp is a default whose crests are being quietly flattened, so it is
// worth knowing.
//
// The wave tables and constants are read out of water.vert rather than copied,
// because a copy is exactly the kind of thing that drifts and then proves
// nothing. It has teeth: raising DefaultWaterOptions' WaveAmplitude to 0.7, or
// dropping WaveLength to 2, fails it.
func TestDefaultWaterOptionsStayInsideTheFoldLimit(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("shaders", "water.vert"))
	if err != nil {
		t.Skipf("shader source unavailable: %v", err)
	}

	floats := func(name string) []float64 {
		m := regexp.MustCompile(name + `\[4\]\s*=\s*float\[4\]\(([^)]*)\)`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("water.vert no longer declares %s; this check is unanchored", name)
		}
		var out []float64
		for _, f := range regexp.MustCompile(`[-0-9.]+`).FindAllString(string(m[1]), -1) {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil {
				t.Fatalf("parse %s entry %q: %v", name, f, err)
			}
			out = append(out, v)
		}
		return out
	}
	constant := func(name string) float64 {
		m := regexp.MustCompile(`const float ` + name + `\s*=\s*([-0-9.]+)`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("water.vert no longer declares %s; this check is unanchored", name)
		}
		v, err := strconv.ParseFloat(string(m[1]), 64)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return v
	}

	waveLen := floats("WAVE_LEN")
	waveAmp := floats("WAVE_AMP")
	steepness := constant("STEEPNESS")
	foldLimit := constant("FOLD_LIMIT")

	if len(waveLen) != 4 || len(waveAmp) != 4 {
		t.Fatalf("expected 4 wave components, got %d lengths and %d amplitudes", len(waveLen), len(waveAmp))
	}
	if foldLimit >= 1 {
		t.Errorf("FOLD_LIMIT = %g, must be below 1: at 1 the surface folds through itself", foldLimit)
	}

	opts := DefaultWaterOptions(0)
	base := float64(opts.WaveLength)
	amplitude := float64(opts.WaveAmplitude)

	// The sum peaks where every component's sine aligns, so |sin| = 1.
	var sum float64
	for i := range waveLen {
		k := 2 * 3.141592653589793 / (base * waveLen[i])
		sum += steepness * k * amplitude * waveAmp[i]
	}
	if sum >= foldLimit {
		t.Errorf("defaults reach a fold sum of %.3f, at or past FOLD_LIMIT %g: crests are being clamped flat",
			sum, foldLimit)
	}
}
