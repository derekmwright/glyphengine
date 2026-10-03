package lut

import (
	"math"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
)

// The table's three axes and its size. skylut.frag compiles the same four
// numbers in, because the slice arithmetic is written out there rather than
// derived from textureSize, and TestShaderAndBakeAgreeOnTheGrid is what holds
// the two copies together.
//
// The axes are (view elevation, sun elevation, sun proximity) and that third
// one is the reason the table is exact rather than approximate. The dome's
// model -- the gradient, the palette blend, the two scattering lobes and the
// below-horizon fade -- reads the view direction only through its y and through
// dot(dir, sunDir), and the sun direction only through its y. So those three
// scalars are the whole of its input, and tabulating them is tabulating the
// function rather than sampling a projection of it. A table indexed by azimuth
// difference instead would have been the same information in a worse place:
// equal azimuth steps are not equal angular steps away from the sun, which is
// what the halo's pow(prox, 8) cares about.
//
// The sizes are where the texels are needed rather than uniform:
//
//	view elevation  64, on a signed-square axis (see viewElevationAt) so the
//	                spacing near the horizon is 0.001 of elevation and 0.06 near
//	                the zenith. The gradient's pow(smoothstep, 0.65) does most
//	                of its moving in the first tenth of the climb, and the
//	                below-horizon fade happens in 0.3; the top half of the sky
//	                is almost flat.
//	sun elevation   32, on the same signed-square axis, so the twilight lobe --
//	                which is a Gaussian 0.115 wide and is the whole shape of
//	                dusk -- gets about 0.004 of elevation per slice at the
//	                horizon rather than the 0.065 a linear axis would give it.
//	                A linearly spaced sun axis bands dusk visibly as the clock
//	                moves, because the sun crosses a whole slice in a few
//	                seconds of a two-minute day.
//	proximity       32, on a chord axis (see proxAt), which is near enough
//	                linear in the angle to the sun for small angles: 3.7 degrees
//	                per texel there, against the 23.5-degree half width of the
//	                tight halo.
//
// 64 x 32 x 32 is 65,536 texels, 512 KB as RGBA16F. Laid out with proximity
// across within a sun-elevation slice, the slices across after it, and view
// elevation down: 1024 by 64.
const (
	lutView = 64
	lutSun  = 32
	lutProx = 32
)

// Bake returns the table a Sky uploads: half-float RGBA pixels, four per texel,
// width by height, in the layout renderer.CreateTextureRGBA16F takes.
//
// It is exported because the bake is the whole model and nothing else in this
// package can be checked without it -- the unit tests read the gradient, the sun
// side and the table's error out of these values with no GPU at all -- and
// because a game that wants to inspect or precompute the table should not have
// to reimplement it.
//
// It used to return RGBA8 bytes holding sqrt(v/3), with a refusal for any
// palette whose brightest texel passed 3.0. The transfer, the ceiling and the
// refusal are all gone: the engine can upload R16G16B16A16_SFLOAT now (issue
// #178), and radiance written straight into it costs 0.05 percent at every
// brightness instead of 2 percent at a tenth of full scale and 13 percent at two
// thousandths. There is no range to exceed -- half-float reaches 65504 -- so a
// bright palette is a bright sky rather than an error. See lut.md.
func Bake(opts Options) (pixels []uint16, width, height int, err error) {
	if err := opts.validate(); err != nil {
		return nil, 0, 0, err
	}
	pal := opts.Palette
	if pal == (glyph.SkyPalette{}) {
		pal = glyph.DefaultSkyPalette()
	}
	return bake(pal, opts.Keys)
}

func bake(skyPal glyph.SkyPalette, keys []Key) (pixels []uint16, width, height int, err error) {
	pal := endpoints(skyPal)
	width, height = lutSun*lutProx, lutView
	pixels = make([]uint16, width*height*4)

	for slice := 0; slice < lutSun; slice++ {
		s := sunElevationAt(slice)
		// The sun's own colour at this elevation, which the glow's tint mixes
		// toward. It is a function of the elevation here and a function of the
		// clock in the driver, and those are not the same thing: the cycle
		// passes most elevations twice, once rising and once setting, and
		// x/sky's table is a tenth redder in green and blue in the evening than
		// in the morning. glowColorAt averages the crossings, so the tint at the
		// horizon is the mean of sunrise's and sunset's -- a difference of about
		// 0.011 in the tint's green, since only clamp(day,0,1)*0.6 of the sun's
		// colour reaches it and day is 0.709 with the sun on the horizon.
		//
		// Carrying the asymmetry would mean a fourth axis, or a second table and
		// a per-frame scalar, for a hundredth of one channel of one term. It is
		// named here because it is the only place this sky cannot be the model
		// exactly, and being able to say that about the rest is worth more than
		// the hundredth.
		sunCol := glowColorAt(keys, s)
		for row := 0; row < lutView; row++ {
			e := viewElevationAt(row)
			for col := 0; col < lutProx; col++ {
				prox := proxAt(col)
				c := domeColor(pal, e, s, prox, sunCol)
				at := (row*width + slice*lutProx + col) * 4
				for ch := 0; ch < 3; ch++ {
					// Radiance, not a transfer of it. The one rounding left is
					// half-float's own, and it is relative rather than absolute
					// -- which is what a sky needs, since the night dome is four
					// thousandths of the daylit horizon and an absolute step
					// would quantise it to nothing.
					pixels[at+ch] = renderer.Float16(c[ch])
				}
				// Opaque. The dome is drawn with blending off and writes the
				// alpha the stars and the discs would blend against; this sky
				// draws neither, and 1 is "nothing in the way".
				pixels[at+3] = renderer.Float16(1)
			}
		}
	}
	return pixels, width, height, nil
}

// ── the axes ──
//
// Each returns the value a texel index stands for, and each is the exact inverse
// of the expression skylut.frag uses to turn a value back into a coordinate.
// They are the half of the table most easily got wrong by one texel, and a
// half-texel error is a sky that is subtly the wrong colour everywhere rather
// than a sky that is obviously broken -- TestAxesInvertTheShadersMapping is what
// holds them.

// viewElevationAt spaces the view axis as the signed square of a linear
// parameter, which puts the texels where the gradient is: the horizon.
func viewElevationAt(row int) float32 {
	return signedSquare(2*float32(row)/float32(lutView-1) - 1)
}

// sunElevationAt does the same for the sun's height, for the twilight lobe.
func sunElevationAt(slice int) float32 {
	return signedSquare(2*float32(slice)/float32(lutSun-1) - 1)
}

// proxAt spaces dot(dir, sunDir) by the chord to the sun rather than by the
// cosine, so the texels bunch where the halo is.
func proxAt(col int) float32 {
	d := 1 - float32(col)/float32(lutProx-1)
	return 1 - 2*d*d
}

func signedSquare(q float32) float32 {
	if q < 0 {
		return -q * q
	}
	return q * q
}

// ── the model ──
//
// A SECOND COPY of the dome. The first is GLSL: shaders/include/atmosphere.inc
// in the engine for the curves and the palette blend, and the gradient and the
// below-horizon fade in x/sky's sky.frag. There is no way to evaluate a shader
// from Go, so a table baked on the CPU has to compute it again, and the copy
// carries the usual risk with it: a changed curve in the engine moves x/sky's
// dome and does not move this one, and nothing fails to build.
//
// Two things are done about that rather than hoping.
// TestAtmosphereIncStillSaysWhatWeCopied reads the engine's own exported include
// set and fails if any of the expressions below have been reworded there, which
// covers everything in atmosphere.inc. The gradient and the fade are quoted
// verbatim beside the code that reimplements them, and nothing automatic holds
// those -- they are x/sky's file, which this package deliberately does not
// reach into. Changing either means changing this.
//
// This is the same hazard the engine already has between DayNight.Twilight and
// atmTwilight, which the repository records as having drifted once.

// atmDaylight is the day/night blend, 1 in full day and 0 in full night. The
// lower edge is below the horizon because the sky stays lit after the sun has
// gone.
//
//	float atmDaylight(float sunY) { return smoothstep(-0.18, 0.10, sunY); }
func atmDaylight(sunY float32) float32 { return smoothstep(-0.18, 0.10, sunY) }

// atmTwilight peaks with the sun on the horizon and falls off either side,
// asymmetrically: above the horizon the warmth builds while there is still a sun
// lighting the air, below it there is progressively less lit air left.
//
//	float width = sunY > 0.0 ? 0.20 : 0.115;
//	float y = sunY / width;
//	return exp(-y * y);
func atmTwilight(sunY float32) float32 {
	width := float32(0.115)
	if sunY > 0 {
		width = 0.20
	}
	y := sunY / width
	return float32(math.Exp(-float64(y * y)))
}

// atmSkyPalette blends the six endpoints into this sun elevation's zenith and
// horizon. Three palettes rather than two: the blue hour is the one that gives
// dusk a shape instead of reading as the lights being turned down.
//
//	zenith  = mix(pal[ATM_ZENITH_NIGHT].rgb,  pal[ATM_ZENITH_DAY].rgb,  day);
//	horizon = mix(pal[ATM_HORIZON_NIGHT].rgb, pal[ATM_HORIZON_DAY].rgb, day);
//	zenith  = mix(zenith,  pal[ATM_ZENITH_TWI].rgb,  twi);
//	horizon = mix(horizon, pal[ATM_HORIZON_TWI].rgb, twi);
func atmSkyPalette(pal palette6, day, twi float32) (zenith, horizon [3]float32) {
	zenith = lerp3(pal[palZenithNight], pal[palZenithDay], day)
	horizon = lerp3(pal[palHorizonNight], pal[palHorizonDay], day)
	zenith = lerp3(zenith, pal[palZenithTwi], twi)
	horizon = lerp3(horizon, pal[palHorizonTwi], twi)
	return zenith, horizon
}

// palette6 is the six endpoints as the shader sees them: a flat array in the
// order atmosphere.inc's ATM_* defines name and renderer/shadow.go packs. The
// engine's glyphengine.SkyPalette is six named mgl32.Vec3 fields, which is the
// right shape for a game to write and the wrong one for indexing, and converting
// once at the top of the bake is also the one place the order is written down on
// this side of the seam.
type palette6 [6][3]float32

const (
	palZenithDay = iota
	palHorizonDay
	palZenithTwi
	palHorizonTwi
	palZenithNight
	palHorizonNight
)

func endpoints(p glyph.SkyPalette) palette6 {
	return palette6{
		[3]float32(p.ZenithDay), [3]float32(p.HorizonDay),
		[3]float32(p.ZenithTwilight), [3]float32(p.HorizonTwilight),
		[3]float32(p.ZenithNight), [3]float32(p.HorizonNight),
	}
}

// atmSunGlow is the scattering around the sun: a tight halo on top of a broad
// wash, both strongest at the horizon, and both a single scalar times one tint
// -- which is what lets the whole glow live on the proximity axis of an RGB
// table.
//
//	float prox = max(dot(dir, sunDir), 0.0);
//	vec3 tint = mix(vec3(1.0, 0.45, 0.16), sunCol, clamp(day, 0.0, 1.0) * 0.6);
//	float halo = pow(prox, 8.0) * (0.35 + 0.9 * twi);
//	float wash = pow(prox, 1.6) * twi * 0.55;
//	float horizonBand = exp(-3.5 * abs(dir.y));
//	return tint * (halo + wash * horizonBand);
func atmSunGlow(viewY, prox, day, twi float32, sunCol [3]float32) [3]float32 {
	if prox < 0 {
		prox = 0
	}
	tint := lerp3([3]float32{1.0, 0.45, 0.16}, sunCol, clampf(day, 0, 1)*0.6)
	halo := powf(prox, 8.0) * (0.35 + 0.9*twi)
	wash := powf(prox, 1.6) * twi * 0.55
	band := float32(math.Exp(-3.5 * math.Abs(float64(viewY))))
	k := halo + wash*band
	return [3]float32{tint[0] * k, tint[1] * k, tint[2] * k}
}

// domeColor is x/sky's sky.frag main, with the three scalars the table is
// indexed by standing in for the view ray and the sun.
//
// The gradient and the below-horizon fade, from that file:
//
//	float t = pow(smoothstep(-0.08, 0.75, elevation), 0.65);
//	vec3 skyColor = mix(horizon, zenith, t);
//	skyColor += atmSunGlow(dir, realSunDir, sunCol, sunElevation);
//	if (elevation < 0.0) {
//	    float belowFade = smoothstep(0.0, -0.3, elevation);
//	    vec3 groundColor = mix(horizon, vec3(0.15, 0.18, 0.12), belowFade)
//	                       * mix(0.3, 1.0, atmDaylight(sunElevation));
//	    skyColor = mix(skyColor, groundColor, belowFade);
//	}
//
// The cloud composite that follows it there has nothing to composite here: this
// sky supplies no cloud stage, so the engine marches nothing and the dome is the
// whole of the pixel.
func domeColor(pal palette6, viewY, sunY, prox float32, sunCol [3]float32) [3]float32 {
	day := atmDaylight(sunY)
	twi := atmTwilight(sunY)
	zenith, horizon := atmSkyPalette(pal, day, twi)

	// Rayleigh-ish falloff rather than a linear ramp: most of the colour change
	// happens in the first part of the climb from the horizon, which is what
	// gives the sky depth instead of a flat wash.
	t := powf(smoothstep(-0.08, 0.75, viewY), 0.65)
	c := lerp3(horizon, zenith, t)

	g := atmSunGlow(viewY, prox, day, twi, sunCol)
	c = [3]float32{c[0] + g[0], c[1] + g[1], c[2] + g[2]}

	if viewY < 0 {
		belowFade := smoothstep(0, -0.3, viewY)
		m := 0.3 + (1.0-0.3)*day
		ground := lerp3(horizon, [3]float32{0.15, 0.18, 0.12}, belowFade)
		ground = [3]float32{ground[0] * m, ground[1] * m, ground[2] * m}
		c = lerp3(c, ground, belowFade)
	}
	return c
}

// glowColorAt is the sun's colour at a given elevation: the driver's own key
// interpolation, evaluated at every time of day the cycle reaches that height,
// averaged, and faded by the same elevation ramp the directional light uses.
//
// Averaging rather than picking is what makes it a function of elevation at all;
// see the comment in bake, which also has the size of what it costs. Outside the
// elevations the cycle reaches -- the table's axis runs to plus and minus one,
// and an orbit with any tilt never gets there -- it clamps to the nearest key,
// which is the same thing x/sky's keyframe tables do at their ends.
func glowColorAt(keys []Key, sunY float32) [3]float32 {
	var sum [3]float32
	n := 0
	for i := range keys {
		a, b := keys[i], keys[(i+1)%len(keys)]
		y0 := normalize3(a.SunDir)[1]
		y1 := normalize3(b.SunDir)[1]
		if y0 == y1 {
			continue
		}
		// Half open in the direction of travel, so an elevation that is exactly
		// a key is counted by the segment leaving it and not also by the one
		// arriving -- otherwise the keys would carry twice the weight of
		// everything between them.
		if !((sunY >= y0 && sunY < y1) || (sunY <= y0 && sunY > y1)) {
			continue
		}
		c := lerp3(a.SunColor, b.SunColor, bisectElevation(a.SunDir, b.SunDir, sunY))
		sum = [3]float32{sum[0] + c[0], sum[1] + c[1], sum[2] + c[2]}
		n++
	}
	if n == 0 {
		best, bestDist := 0, float32(math.MaxFloat32)
		for i := range keys {
			if d := absf(normalize3(keys[i].SunDir)[1] - sunY); d < bestDist {
				best, bestDist = i, d
			}
		}
		sum, n = keys[best].SunColor, 1
	}
	f := sunIntensity(sunY) / float32(n)
	return [3]float32{sum[0] * f, sum[1] * f, sum[2] * f}
}

// bisectElevation finds how far between two keys the interpolated sun sits at a
// given elevation. Bisection rather than algebra because the interpolation is a
// lerp followed by a normalize, which is not invertible in closed form; 40
// halvings take the interval below float32's resolution.
//
// It assumes the elevation moves one way across the segment, which is true of
// any key set whose sun actually rises and sets. A set where it does not gets
// one of the crossings rather than all of them, which is a worse average and not
// a wrong colour.
func bisectElevation(d0, d1 [3]float32, target float32) float32 {
	at := func(f float32) float32 { return normalize3(lerp3(d0, d1, f))[1] }
	lo, hi := float32(0), float32(1)
	rising := at(1) > at(0)
	for i := 0; i < 40; i++ {
		mid := (lo + hi) / 2
		if (at(mid) < target) == rising {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

func powf(x, y float32) float32 { return float32(math.Pow(float64(x), float64(y))) }

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
