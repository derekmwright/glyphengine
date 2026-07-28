package glyphengine

import "math"

// DayNight tracks time-of-day and computes sun/moon direction, colors, and sky
// color for a continuous day/night cycle.
type DayNight struct {
	TimeOfDay float32 // [0,1) where 0=midnight, 0.25=sunrise, 0.5=noon, 0.75=sunset
	Speed     float32 // full cycles per second (e.g. 1/120 for a 2-minute cycle)
}

// Advance steps time forward by dt seconds.
func (dn *DayNight) Advance(dt float32) {
	if dn.Speed == 0 {
		return
	}
	dn.TimeOfDay += dn.Speed * dt
	// Wrap to [0,1)
	dn.TimeOfDay -= float32(math.Floor(float64(dn.TimeOfDay)))
}

// SunDir returns the direction toward the sun. The sun orbits in the XY plane
// with a slight Z tilt. It is above the horizon when TimeOfDay is in [0.25, 0.75].
func (dn *DayNight) SunDir() [3]float32 {
	angle := (float64(dn.TimeOfDay) - 0.25) * 2 * math.Pi
	x := float32(math.Cos(angle))
	y := float32(math.Sin(angle))
	z := float32(0.2)
	// Normalize
	l := float32(math.Sqrt(float64(x*x + y*y + z*z)))
	return [3]float32{x / l, y / l, z / l}
}

// SunAboveHorizon returns true when the sun is above the horizon.
func (dn *DayNight) SunAboveHorizon() bool {
	return dn.TimeOfDay >= 0.25 && dn.TimeOfDay <= 0.75
}

// SunColor returns the sun's directional light color, or zero when below horizon.
func (dn *DayNight) SunColor() [3]float32 {
	if !dn.SunAboveHorizon() {
		return [3]float32{0, 0, 0}
	}
	return dn.lerpSunColor()
}

// SunDiscColor returns the sun's visual color for the billboard disc, clamped
// to the keyframe range so it returns a warm color even slightly below horizon.
func (dn *DayNight) SunDiscColor() [3]float32 {
	t := dn.TimeOfDay
	if t < 0.25 {
		t = 0.25
	} else if t > 0.75 {
		t = 0.75
	}
	return lerpKeyframes(sunColorKeyframes, t)
}

// MoonDir returns the direction toward the moon (opposite side of the sun orbit).
func (dn *DayNight) MoonDir() [3]float32 {
	angle := (float64(dn.TimeOfDay) - 0.25) * 2 * math.Pi
	x := float32(-math.Cos(angle))
	y := float32(-math.Sin(angle))
	z := float32(0.2)
	l := float32(math.Sqrt(float64(x*x + y*y + z*z)))
	return [3]float32{x / l, y / l, z / l}
}

// MoonVisible returns true when the moon is above the horizon
// (TimeOfDay in [0.75,1.0) or [0.0,0.25]).
func (dn *DayNight) MoonVisible() bool {
	return dn.TimeOfDay >= 0.75 || dn.TimeOfDay <= 0.25
}

// MoonColor returns a dim blue-white directional light color scaled by moon elevation.
func (dn *DayNight) MoonColor() [3]float32 {
	if !dn.MoonVisible() {
		return [3]float32{0, 0, 0}
	}
	// Moon elevation: sin of moon's angle above horizon
	// Moon is highest at midnight (t=0), on horizon at t=0.25 and t=0.75
	var elevation float32
	if dn.TimeOfDay >= 0.75 {
		// 0.75 → 1.0 maps to 0 → π/2 (rising)
		frac := (dn.TimeOfDay - 0.75) / 0.25
		elevation = float32(math.Sin(float64(frac) * math.Pi / 2))
	} else {
		// 0.0 → 0.25 maps to π/2 → 0 (setting)
		frac := dn.TimeOfDay / 0.25
		elevation = float32(math.Cos(float64(frac) * math.Pi / 2))
	}
	return [3]float32{0.15 * elevation, 0.17 * elevation, 0.25 * elevation}
}

// AmbientColor returns interpolated ambient light for the current time.
func (dn *DayNight) AmbientColor() [3]float32 {
	return dn.lerpAmbient()
}

// SkyColor returns the sky clear color as RGBA for the current time.
func (dn *DayNight) SkyColor() [4]float32 {
	c := dn.lerpSkyColor()
	return [4]float32{c[0], c[1], c[2], 1.0}
}

// StarVisibility returns a 0–1 factor indicating how visible stars should be.
// Full night (0.0–0.20 and 0.80–1.0) returns 1.0; daytime (0.25–0.75) returns 0.0;
// dawn/dusk transitions are linearly interpolated.
func (dn *DayNight) StarVisibility() float32 {
	t := dn.TimeOfDay
	switch {
	case t < 0.20:
		return 1.0
	case t < 0.25:
		return 1.0 - (t-0.20)/0.05
	case t < 0.75:
		return 0.0
	case t < 0.80:
		return (t - 0.75) / 0.05
	default:
		return 1.0
	}
}

// keyframe defines a color at a specific time for interpolation.
type keyframe struct {
	t       float32
	r, g, b float32
}

var skyKeyframes = []keyframe{
	{0.00, 0.005, 0.005, 0.015},
	{0.20, 0.01, 0.01, 0.03},
	{0.25, 0.80, 0.50, 0.30},
	{0.30, 0.40, 0.60, 0.90},
	{0.50, 0.30, 0.50, 1.00},
	{0.70, 0.40, 0.60, 0.90},
	{0.75, 0.90, 0.40, 0.20},
	{0.80, 0.02, 0.015, 0.04},
	{1.00, 0.005, 0.005, 0.015},
}

var sunColorKeyframes = []keyframe{
	{0.25, 1.0, 0.6, 0.3},
	{0.30, 1.0, 0.9, 0.8},
	{0.50, 1.0, 1.0, 0.95},
	{0.70, 1.0, 0.9, 0.8},
	{0.75, 1.0, 0.5, 0.2},
}

var ambientKeyframes = []keyframe{
	{0.00, 0.02, 0.02, 0.04},
	{0.20, 0.03, 0.03, 0.05},
	{0.25, 0.15, 0.12, 0.10},
	{0.30, 0.20, 0.20, 0.25},
	{0.50, 0.25, 0.25, 0.30},
	{0.70, 0.20, 0.20, 0.25},
	{0.75, 0.15, 0.10, 0.10},
	{0.80, 0.03, 0.03, 0.05},
	{1.00, 0.02, 0.02, 0.04},
}

// lerpKeyframes interpolates between adjacent keyframes for the given time.
func lerpKeyframes(kf []keyframe, t float32) [3]float32 {
	// Clamp
	if t <= kf[0].t {
		return [3]float32{kf[0].r, kf[0].g, kf[0].b}
	}
	last := kf[len(kf)-1]
	if t >= last.t {
		return [3]float32{last.r, last.g, last.b}
	}
	// Find bracketing pair
	for i := 0; i < len(kf)-1; i++ {
		a, b := kf[i], kf[i+1]
		if t >= a.t && t <= b.t {
			frac := (t - a.t) / (b.t - a.t)
			return [3]float32{
				a.r + (b.r-a.r)*frac,
				a.g + (b.g-a.g)*frac,
				a.b + (b.b-a.b)*frac,
			}
		}
	}
	return [3]float32{last.r, last.g, last.b}
}

func (dn *DayNight) lerpSkyColor() [3]float32 { return lerpKeyframes(skyKeyframes, dn.TimeOfDay) }
func (dn *DayNight) lerpSunColor() [3]float32 { return lerpKeyframes(sunColorKeyframes, dn.TimeOfDay) }
func (dn *DayNight) lerpAmbient() [3]float32  { return lerpKeyframes(ambientKeyframes, dn.TimeOfDay) }
