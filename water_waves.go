package glyphengine

import "math"

// The Go side of shaders/include/water_waves.inc and water_wave_sum.inc,
// written out operation for operation in float32 so a check can ask what the
// GPU drew without reading the GPU back. The wave table, the clamps and the
// noise are the shader's own constants; change one there and this file has to
// follow, and `task waterfx` is what notices -- its foam and crest checks are
// CPU predictions compared against a real render, and a mirror that drifts
// fails them.

var (
	waveDir = [5][2]float32{
		{0.92, 0.39}, {0.64, 0.77}, {0.99, -0.14}, {0.29, 0.96}, {0.78, -0.63},
	}
	waveLen   = [5]float32{1.000, 0.730, 0.520, 0.370, 0.260}
	waveAmp   = [5]float32{0.315, 0.252, 0.196, 0.142, 0.095}
	waveSpeed = [5]float32{1.00, 1.17, 1.39, 1.64, 1.96}
)

const (
	waveSteepness   = 0.55
	waveFoldLimit   = 0.85
	waveMinSamples  = 1.2
	waveFullSamples = 2.0
	waveNoiseOcts   = 3
)

// WaterSample is the engine's water surface at one point of its still grid.
type WaterSample struct {
	// Position is where the vertex stage moves the rest point (x, Level, z)
	// to, before the entity's model matrix.
	Position [3]float32
	// Normal is the analytic normal of the displaced surface: the vertex
	// stage's, before the fragment stage adds its ripples.
	Normal [3]float32
	// Jacobian is the determinant of the horizontal Gerstner map at this rest
	// point. One is undisturbed water; it falls toward zero where neighbouring
	// points are pulled together under a crest, and would go negative where the
	// surface folds through itself, which the steepness clamp prevents. Foam is
	// marked where it falls below WaterFoam.Threshold.
	Jacobian float32
	// Crest is the height above still water as a fraction of WaveAmplitude:
	// about 1 on the tallest crests, negative in the troughs. It is what the
	// subsurface term is proportional to.
	Crest float32
}

// SurfaceAt evaluates the surface the water vertex stage draws, at the rest
// point (x, z) with depth of still water under it, at shader time t (the
// engine's Elapsed). spacing is the surface grid's vertex spacing, which decides
// which wave components the mesh can carry -- WaterSpacing gives it for a mesh
// built over a heightmap -- and zero keeps them all.
//
// It is a mirror for checks and for gameplay that needs to know where the
// water is, not a second implementation: GPU sines are not Go's, so expect
// agreement to a few parts in ten thousand rather than to the bit.
func (o WaterOptions) SurfaceAt(x, z, depth, spacing, t float32) WaterSample {
	amplitude := o.WaveAmplitude
	baseLen := max32(o.WaveLength, 0.001)
	shoal := clamp32(depth*0.7, 0, 1)

	var amps [5]float32
	var kaSum float32
	for i := range 5 {
		l := baseLen * waveLen[i]
		a := amplitude * waveAmp[i] * shoal
		if spacing > 0 {
			a *= smoothstep32(waveMinSamples, waveFullSamples, l/spacing)
		}
		amps[i] = a
		kaSum += (6.2831853 / l) * a
	}
	steep := float32(waveSteepness)
	if kaSum > 0 {
		steep = min(waveSteepness, waveFoldLimit/kaSum)
	}

	pos := [3]float32{x, o.Level, z}
	var dhdx, dhdz, fold, jxx, jzz, jxz float32
	for i := range 5 {
		dir := normalize2(waveDir[i])
		l := baseLen * waveLen[i]
		a := amps[i]
		k := 6.2831853 / l
		speed := float32(math.Sqrt(float64(9.81/k))) * waveSpeed[i]
		phase := k*(dir[0]*x+dir[1]*z) - speed*k*t*0.15
		s := float32(math.Sin(float64(phase)))
		c := float32(math.Cos(float64(phase)))
		pos[1] += a * s
		pos[0] += steep * a * dir[0] * c
		pos[2] += steep * a * dir[1] * c
		dhdx += dir[0] * k * a * c
		dhdz += dir[1] * k * a * c
		q := steep * k * a * s
		fold += q
		jxx += q * dir[0] * dir[0]
		jzz += q * dir[1] * dir[1]
		jxz += q * dir[0] * dir[1]
	}

	noiseAmp := amplitude * o.WaveNoise * shoal
	if noiseAmp > 0 {
		eps := max32(spacing, 0.01)
		s0 := swell32(x, z, t, baseLen, spacing)
		sx := swell32(x+eps, z, t, baseLen, spacing)
		sz := swell32(x, z+eps, t, baseLen, spacing)
		pos[1] += noiseAmp * s0
		dhdx += noiseAmp * (sx - s0) / eps
		dhdz += noiseAmp * (sz - s0) / eps
	}

	n := [3]float32{-dhdx, 1 - fold, -dhdz}
	nl := float32(math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])))
	out := WaterSample{
		Position: pos,
		Normal:   [3]float32{n[0] / nl, n[1] / nl, n[2] / nl},
		Jacobian: (1-jxx)*(1-jzz) - jxz*jxz,
	}
	if amplitude > 0 {
		out.Crest = (pos[1] - o.Level) / amplitude
	}
	return out
}

func smoothstep32(e0, e1, x float32) float32 {
	t := clamp32((x-e0)/(e1-e0), 0, 1)
	return t * t * (3 - 2*t)
}

func normalize2(v [2]float32) [2]float32 {
	l := float32(math.Sqrt(float64(v[0]*v[0] + v[1]*v[1])))
	return [2]float32{v[0] / l, v[1] / l}
}

func fract32(v float32) float32 { return v - float32(math.Floor(float64(v))) }

func hashNoise32(px, py float32) float32 {
	px = fract32(px * 443.897)
	py = fract32(py * 441.423)
	d := px*(py+19.19) + py*(px+19.19)
	px += d
	py += d
	return fract32(px * py)
}

func valueNoise32(px, py float32) float32 {
	ix, iy := float32(math.Floor(float64(px))), float32(math.Floor(float64(py)))
	fx, fy := px-ix, py-iy
	fx = fx * fx * (3 - 2*fx)
	fy = fy * fy * (3 - 2*fy)
	a := hashNoise32(ix, iy)
	b := hashNoise32(ix+1, iy)
	c := hashNoise32(ix, iy+1)
	d := hashNoise32(ix+1, iy+1)
	ab := a*(1-fx) + b*fx
	cd := c*(1-fx) + d*fx
	return (ab*(1-fy)+cd*fy)*2 - 1
}

func swell32(x, z, t, baseLen, spacing float32) float32 {
	var total float32
	amp := float32(0.62)
	feature := baseLen * 1.7
	for range waveNoiseOcts {
		carry := smoothstep32(waveMinSamples, waveFullSamples, feature/max32(spacing, 1e-4))
		if carry > 0 {
			dx := 0.021 * t * baseLen
			dz := -0.013 * t * baseLen
			total += amp * carry * valueNoise32((x+dx)/feature, (z+dz)/feature)
		}
		feature *= 0.5
		amp *= 0.55
	}
	return total
}
