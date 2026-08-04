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
	return dn.SunDir()[1] > 0
}

// smoothstep is the standard Hermite interpolant, matching GLSL's, so the
// curves computed here and the ones computed in the shaders agree.
func smoothstep(edge0, edge1, x float32) float32 {
	if edge1 == edge0 {
		return 0
	}
	t := (x - edge0) / (edge1 - edge0)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return t * t * (3 - 2*t)
}

// SunIntensity is how much directional light the sun delivers, from its
// elevation rather than the clock.
//
// It has to be continuous. Cutting the sun off the instant it crosses the
// horizon removes every directional light in the scene in a single frame, and
// because the sunset glow is tinted by the sun's colour, it also deletes the
// scattering at exactly the moment there should be most of it.
//
// The lower edge sits below zero on purpose: the sun still lights the sky for
// a while after it sets, which is what civil twilight is.
func (dn *DayNight) SunIntensity() float32 {
	return smoothstep(-0.14, 0.06, dn.SunDir()[1])
}

// MoonIntensity is the moon's equivalent.
//
// It starts exactly where SunIntensity ends. The moon sits opposite the sun, so
// its elevation is the sun's negated, and beginning the ramp at +0.14 means the
// two are both zero at the moment the scene's single directional light changes
// hands -- no pop, and no 180-degree flip of every shadow while anything is
// still bright enough to cast one.
func (dn *DayNight) MoonIntensity() float32 {
	return smoothstep(0.14, 0.34, dn.MoonDir()[1])
}

// SunColor returns the sun's directional light color, faded out by elevation.
func (dn *DayNight) SunColor() [3]float32 {
	i := dn.SunIntensity()
	c := dn.lerpSunColor()
	return [3]float32{c[0] * i, c[1] * i, c[2] * i}
}

// SunDiscColor returns the sun's visual color for the billboard disc.
//
// The disc keeps reddening as it descends rather than freezing at the last
// above-horizon keyframe, and it is deliberately brighter than the colour used
// for lighting: the sun is the brightest thing in the sky, and a disc that
// merely matches the sky's luminance reads as a dull orange ball rather than a
// light source. The excess clamps to white in the core, which is what a real
// one looks like.
func (dn *DayNight) SunDiscColor() [3]float32 {
	c := dn.lerpSunColor()
	// Fading here rather than only at the draw call keeps the returned colour
	// continuous across midnight. The keyframe table only spans the daylight
	// half of the cycle and clamps to a different endpoint on each side, so
	// without this the disc's colour steps as the cycle wraps.
	//
	// The fade window sits BELOW the horizon on purpose. Centred on it -- which
	// is what -0.17..0.05 did -- the disc is dimmest exactly when it is most
	// visible and most worth looking at: at an elevation of -0.06, sitting on
	// the horizon at sunset, the boost was cancelled down to 0.81 and the disc
	// read as a dull orange ball darker than the sky behind it. It also fell
	// under the bloom threshold, so the one moment the sun should obviously
	// glare was the one moment it could not.
	//
	// Below the horizon the sun is hidden by terrain, or by the horizon itself
	// over open water, so there is nothing to fade for until it is genuinely
	// down. Keep this window wider than about 0.15 or TestCycleIsContinuous
	// starts seeing it as a step rather than a ramp.
	// The boost is what makes the disc glare rather than merely sit there.
	//
	// 1.7 was chosen when the framebuffer was 8-bit and everything above 1 was
	// thrown away, so its only job was to clip the core to white. Against a
	// bloom threshold it is nearly useless: the contribution is
	// (bright-threshold)/bright, so a 1.7 disc against a 1.2 threshold spends 29
	// percent of itself on the glare while an emissive material at 6 spends 80.
	// The sun came out visibly duller than a glowing panel, which is the wrong
	// way round.
	//
	// 5 is not physical -- a real sun is thousands of times the sky, not five --
	// but it is where the disc reads as a light source at every elevation it is
	// visible without the halo swallowing the horizon at noon.
	const boost = 5.0
	f := boost * smoothstep(-0.20, -0.02, dn.SunDir()[1])
	return [3]float32{c[0] * f, c[1] * f, c[2] * f}
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

// MoonColor returns a dim blue-white directional light scaled by elevation.
//
// Driven by the moon's actual direction rather than by time fractions, so it
// cannot disagree with where the moon is drawn.
func (dn *DayNight) MoonColor() [3]float32 {
	e := dn.MoonIntensity()
	// Far dimmer than it looks like it should be on paper. Moonlight is about
	// a millionth of sunlight; games exaggerate it heavily, but at a sixth of
	// the sun -- where this started -- the ground came out brighter than the
	// night sky above it.
	return [3]float32{0.045 * e, 0.052 * e, 0.080 * e}
}

// PrimaryLight returns the directional light the scene should be lit by, and
// its colour: the sun while it has any strength left, the moon afterwards.
//
// The engine has one directional light, so the two have to hand over. Doing it
// on whichever is currently brighter means the swap lands in the window where
// both are near zero -- the sun has set and the moon has not yet climbed --
// rather than at a clock boundary where the light and every shadow in the
// scene would flip direction in one frame.
func (dn *DayNight) PrimaryLight() (dir [3]float32, color [3]float32) {
	if dn.SunIntensity() > 0 {
		return dn.SunDir(), dn.SunColor()
	}
	return dn.MoonDir(), dn.MoonColor()
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

// StarVisibility returns a 0–1 factor for how visible the stars should be.
//
// Elevation-driven, and deliberately lagging the sky's darkening: stars come
// out during the blue hour, well after the sun has gone, not the moment it
// touches the horizon.
//
// This is no longer the sky's day/night blend. It used to be both, which is
// why dusk had no shape -- one linear ramp over five percent of the cycle drove
// the entire palette, and multiplying the sunset glow by (1 - it) meant night
// arriving actively erased the sunset. The sky now derives its own blend from
// sun elevation; see Daylight and Twilight.
func (dn *DayNight) StarVisibility() float32 {
	return 1 - smoothstep(-0.30, -0.02, dn.SunDir()[1])
}

// Daylight is the sky's day/night blend: 1 in full day, 0 in full night.
//
// Shaders compute the same function from pc.sunDir.y (see atmosphere.inc); this
// exists so game code can ask the same question without duplicating the curve.
func (dn *DayNight) Daylight() float32 {
	return smoothstep(-0.18, 0.10, dn.SunDir()[1])
}

// Twilight peaks when the sun sits on the horizon and falls off either side.
//
// It is what drives the warm scattering near the horizon, and it is
// independent of Daylight on purpose: the glow has to survive the sun going
// down, since that is when it is strongest.
func (dn *DayNight) Twilight() float32 {
	y := dn.SunDir()[1] / 0.20
	return float32(math.Exp(-float64(y * y)))
}

// keyframe defines a color at a specific time for interpolation.
type keyframe struct {
	t       float32
	r, g, b float32
}

// The sky dome is opaque and drawn first, so this is only the clear colour
// behind it and is never seen in a normal frame. It is kept in step with the
// dome anyway, so that a game which disables the dome still gets a sky rather
// than whatever these last happened to say.
//
// The dome's actual colours live in shaders/atmosphere.inc, driven by sun
// elevation.
var skyKeyframes = []keyframe{
	{0.00, 0.008, 0.010, 0.035},
	{0.18, 0.010, 0.013, 0.042},
	{0.22, 0.055, 0.085, 0.260},
	{0.25, 0.880, 0.420, 0.220},
	{0.30, 0.450, 0.620, 0.920},
	{0.50, 0.300, 0.500, 1.000},
	{0.70, 0.450, 0.620, 0.920},
	{0.75, 0.880, 0.420, 0.220},
	{0.79, 0.180, 0.150, 0.280},
	{0.83, 0.055, 0.085, 0.260},
	{0.90, 0.010, 0.013, 0.042},
	{1.00, 0.008, 0.010, 0.035},
}

var sunColorKeyframes = []keyframe{
	{0.25, 1.0, 0.6, 0.3},
	{0.30, 1.0, 0.9, 0.8},
	{0.50, 1.0, 1.0, 0.95},
	{0.70, 1.0, 0.9, 0.8},
	{0.75, 1.0, 0.5, 0.2},
}

// Ambient is the sky's contribution to lighting, so it tracks the sky: warm
// and dim at the horizon, cool through the blue hour, and a floor at night
// rather than a cliff into black. The extra keys either side of sunrise and
// sunset are what give dusk and dawn any duration at all.
var ambientKeyframes = []keyframe{
	{0.00, 0.010, 0.013, 0.030},
	{0.18, 0.013, 0.016, 0.036},
	{0.22, 0.055, 0.065, 0.110},
	{0.25, 0.140, 0.115, 0.105},
	{0.29, 0.190, 0.185, 0.220},
	{0.50, 0.250, 0.250, 0.300},
	{0.71, 0.190, 0.185, 0.220},
	{0.75, 0.140, 0.105, 0.100},
	{0.78, 0.075, 0.070, 0.105},
	{0.82, 0.026, 0.032, 0.070},
	{0.88, 0.013, 0.016, 0.036},
	{1.00, 0.010, 0.013, 0.030},
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
