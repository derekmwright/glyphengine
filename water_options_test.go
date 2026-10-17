package glyphengine

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// DefaultWaterOptions leaves both looks off: whether the engine's lake glows
// or foams by default is a decision for later, and a default that turned one
// on would move every capture of every lake.
func TestDefaultWaterOptionsLeaveFoamAndSubsurfaceOff(t *testing.T) {
	o := DefaultWaterOptions(3)
	if o.Subsurface != (WaterSubsurface{}) || o.Foam != (WaterFoam{}) {
		t.Fatalf("DefaultWaterOptions turns on %+v / %+v", o.Subsurface, o.Foam)
	}
	p := o.params()
	if p.SubsurfaceStrength != 0 || p.FoamDecay != 0 {
		t.Fatalf("the default reaches the renderer with an option on: %+v", p)
	}
}

// params carries every field of both options to the renderer, and nothing
// else moves when they are set.
func TestWaterParamsCarryBothOptions(t *testing.T) {
	o := DefaultWaterOptions(3)
	base := o.params()
	o.Subsurface = WaterSubsurface{Color: [3]float32{0.1, 0.2, 0.3}, Strength: 2}
	o.Foam = WaterFoam{Threshold: 0.7, Decay: 2.5, Color: [3]float32{0.9, 0.8, 0.7}}
	got := o.params()
	want := base
	want.SubsurfaceColor, want.SubsurfaceStrength = o.Subsurface.Color, o.Subsurface.Strength
	want.FoamThreshold, want.FoamDecay, want.FoamColor = o.Foam.Threshold, o.Foam.Decay, o.Foam.Color
	if got != want {
		t.Fatalf("params = %+v, want %+v", got, want)
	}
}

// The grid the foam target is laid over is the grid the mesh was built on,
// point for point: every vertex WaterMesh emits sits at a grid point and
// carries that point's depth. A target indexed by a grid that disagreed with
// the vertices would put a crest's foam beside it.
func TestWaterGridIsTheMeshGrid(t *testing.T) {
	hm := submergedHeightmap(t, 17, 40)
	opts := DefaultWaterOptions(0)
	opts.Resolution = 24
	g, err := NewWaterGrid(hm, opts)
	if err != nil {
		t.Fatal(err)
	}
	vertices, _, err := WaterMesh(hm, opts)
	if err != nil {
		t.Fatal(err)
	}
	if g.Width != 25 || g.Height != 25 || len(g.Depth) != 25*25 {
		t.Fatalf("grid %dx%d with %d depths", g.Width, g.Height, len(g.Depth))
	}
	at := map[[2]float32][2]float32{}
	for iz := range g.Height {
		for ix := range g.Width {
			x, z, d := g.Point(ix, iz)
			at[[2]float32{x, z}] = [2]float32{d, g.Spacing()}
		}
	}
	for i, v := range vertices {
		want, ok := at[[2]float32{v.Pos[0], v.Pos[2]}]
		if !ok {
			t.Fatalf("vertex %d at %v is on no grid point", i, v.Pos)
		}
		if v.UV != want {
			t.Fatalf("vertex %d carries depth/spacing %v, its grid point %v", i, v.UV, want)
		}
	}
}

// The Go mirror reads the shader's own tables, so the two cannot drift
// unnoticed: a wavelength, amplitude, direction or speed changed in
// water_waves.inc fails here until water_waves.go follows.
//
// Verified to fail: changing WAVE_SPEED's third entry in the include to 1.40
// reports "WAVE_SPEED[2]: shader 1.4, mirror 1.39".
func TestSurfaceAtMirrorsTheShaderTables(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("shaders", "include", "water_waves.inc"))
	if err != nil {
		t.Skipf("shader source unavailable: %v", err)
	}
	table := func(name string) []float64 {
		m := regexp.MustCompile(name + `\[\d+\]\s*=\s*(?:float|vec2)\[\d+\]\(([^;]*)\);`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("water_waves.inc no longer declares %s", name)
		}
		var out []float64
		for _, f := range regexp.MustCompile(`-?[0-9]+\.[0-9]+`).FindAllString(string(m[1]), -1) {
			v, _ := strconv.ParseFloat(f, 64)
			out = append(out, v)
		}
		return out
	}
	check := func(name string, shader []float64, mirror []float32) {
		if len(shader) != len(mirror) {
			t.Fatalf("%s: shader has %d entries, mirror %d", name, len(shader), len(mirror))
		}
		for i := range shader {
			if float32(shader[i]) != mirror[i] {
				t.Errorf("%s[%d]: shader %v, mirror %v", name, i, shader[i], mirror[i])
			}
		}
	}
	check("WAVE_LEN", table("WAVE_LEN"), waveLen[:])
	check("WAVE_AMP", table("WAVE_AMP"), waveAmp[:])
	check("WAVE_SPEED", table("WAVE_SPEED"), waveSpeed[:])
	var dirs []float32
	for _, d := range waveDir {
		dirs = append(dirs, d[0], d[1])
	}
	check("WAVE_DIR", table("WAVE_DIR"), dirs)
}

// The Jacobian is one on still water and below one under a crest, and the
// height it reports is normalised: the same sea at half the amplitude has the
// same crest field.
func TestSurfaceAtJacobianAndCrest(t *testing.T) {
	o := DefaultWaterOptions(3)
	o.WaveAmplitude = 0
	if s := o.SurfaceAt(1, 2, 5, 1, 3); s.Jacobian != 1 || s.Crest != 0 || s.Position != [3]float32{1, 3, 2} {
		t.Fatalf("still water: %+v", s)
	}
	o.WaveAmplitude, o.WaveNoise = 0.3, 0
	minJ := float32(1)
	for x := float32(0); x < 20; x += 0.25 {
		minJ = min(minJ, o.SurfaceAt(x, 0, 5, 1, 2).Jacobian)
	}
	if minJ >= 0.99 {
		t.Fatalf("a 0.3 sea never squeezes the surface: min Jacobian %v", minJ)
	}
	half := o
	half.WaveAmplitude = 0.15
	a, b := o.SurfaceAt(3, 4, 5, 1, 2).Crest, half.SurfaceAt(3, 4, 5, 1, 2).Crest
	if math.Abs(float64(a-b)) > 1e-5 {
		t.Fatalf("crest %v at full amplitude, %v at half: not normalised", a, b)
	}
}

// DefaultWaterFoam's threshold is chosen to sit clear of anything the default
// lake does, so the engine's own water never whitecaps; see water.md for the
// distribution. This pins that property on a grid finer than the default's,
// which is the harder case (the finest wave component survives the grid).
//
// Verified to fail: a threshold of 0.9 reports "the default lake reaches a
// Jacobian of 0.8920, under DefaultWaterFoam's threshold 0.90".
func TestDefaultWaterFoamClearsTheDefaultLake(t *testing.T) {
	o := DefaultWaterOptions(3)
	o.WaveNoise = 0 // vertical only; it cannot move the Jacobian
	th := DefaultWaterFoam().Threshold
	minJ := float32(1)
	for t := float32(0); t < 60; t += 0.5 {
		for x := float32(-50); x <= 50; x += 1 {
			for z := float32(-50); z <= 50; z += 1 {
				minJ = min(minJ, o.SurfaceAt(x, z, 5, 1, t).Jacobian)
			}
		}
	}
	if minJ <= th {
		t.Fatalf("the default lake reaches a Jacobian of %.4f, under DefaultWaterFoam's threshold %.2f", minJ, th)
	}
	t.Logf("default lake's lowest Jacobian %.4f, threshold %.2f", minJ, th)
}
