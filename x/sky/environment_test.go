package sky

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
)

// The three tables below are the ones the environment carve pinned, moved here
// with the code they pin and not re-derived. They were generated out of the
// engine's Environment.State and buildMoonObject as those stood BEFORE the
// carve, printed at full float32 precision, and pasted in unedited -- which is
// the only thing that makes "nothing moved" a claim rather than a hope. Two
// migrations now rest on them: the carve, and this move out of the engine.
//
// A capture gate cannot stand in for them. It needs a GPU, it visits a handful
// of times of day, and it cannot tell a palette that shifted in the last bit
// from a frame that dithered. These compare every field of every resolved state
// at twenty-five points round the clock, exactly. task skymigration is the other
// half -- it compares the pixels -- and neither replaces the other.

var dayCyclePins = []struct {
	tod   float32
	state glyph.EnvironmentState
}{
	{0, glyph.EnvironmentState{SunDir: [3]float32{-6.0043254e-17, 0.9805807, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{6.0043254e-17, -0.9805807, 0.19611615}, SunElevation: -0.9805807, Ambient: [3]float32{0.01, 0.013, 0.03}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{6.0043254e-17, -0.9805807, 0.19611615}, MoonDiscDir: [3]float32{-6.0043254e-17, 0.9805807, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.041666668, glyph.EnvironmentState{SunDir: [3]float32{-0.25379297, 0.94716823, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{0.25379297, -0.94716823, 0.19611615}, SunElevation: -0.94716823, Ambient: [3]float32{0.010694444, 0.013694445, 0.031388886}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.25379297, -0.94716823, 0.19611615}, MoonDiscDir: [3]float32{-0.25379297, 0.94716823, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.083333336, glyph.EnvironmentState{SunDir: [3]float32{-0.49029034, 0.84920776, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{0.49029034, -0.84920776, 0.19611615}, SunElevation: -0.84920776, Ambient: [3]float32{0.011388889, 0.014388889, 0.032777775}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.49029034, -0.84920776, 0.19611615}, MoonDiscDir: [3]float32{-0.49029034, 0.84920776, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.125, glyph.EnvironmentState{SunDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{0.69337523, -0.69337523, 0.19611615}, SunElevation: -0.69337523, Ambient: [3]float32{0.012083333, 0.0150833335, 0.034166664}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.69337523, -0.69337523, 0.19611615}, MoonDiscDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.16666667, glyph.EnvironmentState{SunDir: [3]float32{-0.8492078, 0.4902903, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{0.8492078, -0.4902903, 0.19611615}, SunElevation: -0.4902903, Ambient: [3]float32{0.012777777, 0.015777778, 0.035555553}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.8492078, -0.4902903, 0.19611615}, MoonDiscDir: [3]float32{-0.8492078, 0.4902903, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.20833333, glyph.EnvironmentState{SunDir: [3]float32{-0.94716823, 0.253793, 0.19611615}, SunColor: [3]float32{0.027125621, 0.031345163, 0.048223324}, RealSunDir: [3]float32{0.94716823, -0.253793, 0.19611615}, SunElevation: -0.253793, Ambient: [3]float32{0.042749994, 0.050708324, 0.08841665}, FogDensity: 0.0075, StarFade: 0.92728853, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.94716823, -0.253793, 0.19611615}, MoonDiscDir: [3]float32{-0.94716823, 0.253793, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.25, glyph.EnvironmentState{SunDir: [3]float32{0.9805807, 0, 0.19611615}, SunColor: [3]float32{0.784, 0.4704, 0.2352}, RealSunDir: [3]float32{0.9805807, 0, 0.19611615}, Ambient: [3]float32{0.14, 0.115, 0.105}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{0.9805807, 0, 0.19611615}, SunDiscColor: [3]float32{5, 3, 1.5}, MoonDiscDir: [3]float32{-0.9805807, -0, 0.19611615}, MoonDiscColor: [3]float32{0.7650001, 0.792, 0.855}, CloudSteps: 32, LightShafts: 0.25}},
	{0.29166666, glyph.EnvironmentState{SunDir: [3]float32{0.94716823, 0.2537929, 0.19611615}, SunColor: [3]float32{1, 0.8499999, 0.71666646}, RealSunDir: [3]float32{0.94716823, 0.2537929, 0.19611615}, SunElevation: 0.2537929, Ambient: [3]float32{0.1904762, 0.18551588, 0.22063492}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.94716823, 0.2537929, 0.19611615}, SunDiscColor: [3]float32{5, 4.2499995, 3.5833323}, MoonDiscDir: [3]float32{-0.94716823, -0.2537929, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.33333334, glyph.EnvironmentState{SunDir: [3]float32{0.84920776, 0.4902904, 0.19611615}, SunColor: [3]float32{1, 0.9166666, 0.825}, RealSunDir: [3]float32{0.84920776, 0.4902904, 0.19611615}, SunElevation: 0.4902904, Ambient: [3]float32{0.20238096, 0.1984127, 0.23650795}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.84920776, 0.4902904, 0.19611615}, SunDiscColor: [3]float32{5, 4.583333, 4.125}, MoonDiscDir: [3]float32{-0.84920776, -0.4902904, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.375, glyph.EnvironmentState{SunDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, SunColor: [3]float32{1, 0.9375, 0.85625}, RealSunDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, SunElevation: 0.69337523, Ambient: [3]float32{0.21428572, 0.21130952, 0.25238097}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, SunDiscColor: [3]float32{5, 4.6875, 4.28125}, MoonDiscDir: [3]float32{-0.69337523, -0.69337523, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.41666666, glyph.EnvironmentState{SunDir: [3]float32{0.4902904, 0.84920776, 0.19611615}, SunColor: [3]float32{1, 0.9583333, 0.8875}, RealSunDir: [3]float32{0.4902904, 0.84920776, 0.19611615}, SunElevation: 0.84920776, Ambient: [3]float32{0.22619048, 0.22420634, 0.26825398}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.4902904, 0.84920776, 0.19611615}, SunDiscColor: [3]float32{5, 4.7916665, 4.4375}, MoonDiscDir: [3]float32{-0.4902904, -0.84920776, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.45833334, glyph.EnvironmentState{SunDir: [3]float32{0.2537929, 0.94716823, 0.19611615}, SunColor: [3]float32{1, 0.9791667, 0.91875}, RealSunDir: [3]float32{0.2537929, 0.94716823, 0.19611615}, SunElevation: 0.94716823, Ambient: [3]float32{0.23809524, 0.23710318, 0.284127}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.2537929, 0.94716823, 0.19611615}, SunDiscColor: [3]float32{5, 4.8958335, 4.59375}, MoonDiscDir: [3]float32{-0.2537929, -0.94716823, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.5, glyph.EnvironmentState{SunDir: [3]float32{6.0043254e-17, 0.9805807, 0.19611615}, SunColor: [3]float32{1, 1, 0.95}, RealSunDir: [3]float32{6.0043254e-17, 0.9805807, 0.19611615}, SunElevation: 0.9805807, Ambient: [3]float32{0.25, 0.25, 0.3}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{6.0043254e-17, 0.9805807, 0.19611615}, SunDiscColor: [3]float32{5, 5, 4.75}, MoonDiscDir: [3]float32{-6.0043254e-17, -0.9805807, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.5416667, glyph.EnvironmentState{SunDir: [3]float32{-0.25379306, 0.94716823, 0.19611615}, SunColor: [3]float32{1, 0.9791666, 0.91875}, RealSunDir: [3]float32{-0.25379306, 0.94716823, 0.19611615}, SunElevation: 0.94716823, Ambient: [3]float32{0.23809522, 0.23710316, 0.284127}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.25379306, 0.94716823, 0.19611615}, SunDiscColor: [3]float32{5, 4.895833, 4.59375}, MoonDiscDir: [3]float32{0.25379306, -0.94716823, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.5833333, glyph.EnvironmentState{SunDir: [3]float32{-0.49029022, 0.8492078, 0.19611615}, SunColor: [3]float32{1, 0.9583333, 0.8875}, RealSunDir: [3]float32{-0.49029022, 0.8492078, 0.19611615}, SunElevation: 0.8492078, Ambient: [3]float32{0.22619048, 0.22420636, 0.26825398}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.49029022, 0.8492078, 0.19611615}, SunDiscColor: [3]float32{5, 4.7916665, 4.4375}, MoonDiscDir: [3]float32{0.49029022, -0.8492078, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.625, glyph.EnvironmentState{SunDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, SunColor: [3]float32{1, 0.9375, 0.85625}, RealSunDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, SunElevation: 0.69337523, Ambient: [3]float32{0.2142857, 0.21130952, 0.25238097}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, SunDiscColor: [3]float32{5, 4.6875, 4.28125}, MoonDiscDir: [3]float32{0.69337523, -0.69337523, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.6666667, glyph.EnvironmentState{SunDir: [3]float32{-0.8492078, 0.49029022, 0.19611615}, SunColor: [3]float32{1, 0.9166666, 0.825}, RealSunDir: [3]float32{-0.8492078, 0.49029022, 0.19611615}, SunElevation: 0.49029022, Ambient: [3]float32{0.20238094, 0.19841269, 0.23650792}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.8492078, 0.49029022, 0.19611615}, SunDiscColor: [3]float32{5, 4.583333, 4.125}, MoonDiscDir: [3]float32{0.8492078, -0.49029022, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.7083333, glyph.EnvironmentState{SunDir: [3]float32{-0.94716823, 0.25379306, 0.19611615}, SunColor: [3]float32{1, 0.8333334, 0.7000001}, RealSunDir: [3]float32{-0.94716823, 0.25379306, 0.19611615}, SunElevation: 0.25379306, Ambient: [3]float32{0.19047618, 0.18551588, 0.22063491}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.94716823, 0.25379306, 0.19611615}, SunDiscColor: [3]float32{5, 4.166667, 3.5000005}, MoonDiscDir: [3]float32{0.94716823, -0.25379306, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.75, glyph.EnvironmentState{SunDir: [3]float32{-0.9805807, 1.2008651e-16, 0.19611615}, SunColor: [3]float32{0.784, 0.392, 0.1568}, RealSunDir: [3]float32{-0.9805807, 1.2008651e-16, 0.19611615}, SunElevation: 1.2008651e-16, Ambient: [3]float32{0.14, 0.105, 0.1}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{-0.9805807, 1.2008651e-16, 0.19611615}, SunDiscColor: [3]float32{5, 2.5, 1}, MoonDiscDir: [3]float32{0.9805807, -1.2008651e-16, 0.19611615}, MoonDiscColor: [3]float32{0.7650001, 0.792, 0.855}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.7916667, glyph.EnvironmentState{SunDir: [3]float32{0.94716823, 0.25379306, 0.19611615}, SunColor: [3]float32{0.02712564, 0.03134518, 0.048223358}, RealSunDir: [3]float32{-0.94716823, -0.25379306, 0.19611615}, SunElevation: -0.25379306, Ambient: [3]float32{0.060708284, 0.05891663, 0.09479163}, FogDensity: 0.0075, StarFade: 0.9272887, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.94716823, -0.25379306, 0.19611615}, MoonDiscDir: [3]float32{0.94716823, 0.25379306, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.8333333, glyph.EnvironmentState{SunDir: [3]float32{0.8492078, 0.49029022, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.8492078, -0.49029022, 0.19611615}, SunElevation: -0.49029022, Ambient: [3]float32{0.023111114, 0.02844445, 0.062444452}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.8492078, -0.49029022, 0.19611615}, MoonDiscDir: [3]float32{0.8492078, 0.49029022, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.875, glyph.EnvironmentState{SunDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.69337523, -0.69337523, 0.19611615}, SunElevation: -0.69337523, Ambient: [3]float32{0.014083332, 0.017333332, 0.038833328}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.69337523, -0.69337523, 0.19611615}, MoonDiscDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.9166667, glyph.EnvironmentState{SunDir: [3]float32{0.49029022, 0.8492078, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.49029022, -0.8492078, 0.19611615}, SunElevation: -0.8492078, Ambient: [3]float32{0.012083333, 0.0150833335, 0.034166664}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.49029022, -0.8492078, 0.19611615}, MoonDiscDir: [3]float32{0.49029022, 0.8492078, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.9583333, glyph.EnvironmentState{SunDir: [3]float32{0.25379306, 0.94716823, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.25379306, -0.94716823, 0.19611615}, SunElevation: -0.94716823, Ambient: [3]float32{0.011041667, 0.014041668, 0.032083333}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.25379306, -0.94716823, 0.19611615}, MoonDiscDir: [3]float32{0.25379306, 0.94716823, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{1, glyph.EnvironmentState{SunDir: [3]float32{1.8012975e-16, 0.9805807, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-1.8012975e-16, -0.9805807, 0.19611615}, SunElevation: -0.9805807, Ambient: [3]float32{0.01, 0.013, 0.03}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-1.8012975e-16, -0.9805807, 0.19611615}, MoonDiscDir: [3]float32{1.8012975e-16, 0.9805807, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
}

// The edges, where the curves are not straight and a regular sweep steps over
// them: the first light before sunrise at 0.22, the two sides of the shaft
// window at 0.745 and 0.755 that the smoothstep was introduced to stop blinking
// across, sunrise and sunset themselves, and the blue hour after each.
var dayCycleEdgePins = []struct {
	tod   float32
	state glyph.EnvironmentState
}{
	{0.22, glyph.EnvironmentState{SunDir: [3]float32{-0.9632119, 0.18374251, 0.19611615}, SunColor: [3]float32{0.0055161547, 0.006374223, 0.009806497}, RealSunDir: [3]float32{0.9632119, -0.18374251, 0.19611615}, SunElevation: -0.18374251, Ambient: [3]float32{0.055, 0.065, 0.11}, FogDensity: 0.0075, StarFade: 0.62597257, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.9632119, -0.18374251, 0.19611615}, SunDiscColor: [3]float32{0.11499605, 0.06899764, 0.03449882}, MoonDiscDir: [3]float32{-0.9632119, 0.18374251, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.245, glyph.EnvironmentState{SunDir: [3]float32{0.9800968, -0.030800752, 0.19611615}, SunColor: [3]float32{0.5687998, 0.3412799, 0.17063995}, RealSunDir: [3]float32{0.9800968, -0.030800752, 0.19611615}, SunElevation: -0.030800752, Ambient: [3]float32{0.12583335, 0.10666668, 0.10583333}, FogDensity: 0.0075, StarFade: 0.004349053, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{0.9800968, -0.030800752, 0.19611615}, SunDiscColor: [3]float32{4.948153, 2.9688919, 1.4844459}, MoonDiscDir: [3]float32{-0.9800968, 0.030800752, 0.19611615}, MoonDiscColor: [3]float32{0.9220839, 0.95462805, 1.0305643}, CloudSteps: 32, LightShafts: 0.24510969}},
	{0.25, glyph.EnvironmentState{SunDir: [3]float32{0.9805807, 0, 0.19611615}, SunColor: [3]float32{0.784, 0.4704, 0.2352}, RealSunDir: [3]float32{0.9805807, 0, 0.19611615}, Ambient: [3]float32{0.14, 0.115, 0.105}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{0.9805807, 0, 0.19611615}, SunDiscColor: [3]float32{5, 3, 1.5}, MoonDiscDir: [3]float32{-0.9805807, -0, 0.19611615}, MoonDiscColor: [3]float32{0.7650001, 0.792, 0.855}, CloudSteps: 32, LightShafts: 0.25}},
	{0.3, glyph.EnvironmentState{SunDir: [3]float32{0.9325876, 0.30301616, 0.19611615}, SunColor: [3]float32{1, 0.9, 0.8}, RealSunDir: [3]float32{0.9325876, 0.30301616, 0.19611615}, SunElevation: 0.30301616, Ambient: [3]float32{0.19285715, 0.18809524, 0.22380953}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.9325876, 0.30301616, 0.19611615}, SunDiscColor: [3]float32{5, 4.5, 4}, MoonDiscDir: [3]float32{-0.9325876, -0.30301616, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.72, glyph.EnvironmentState{SunDir: [3]float32{-0.96321195, 0.18374233, 0.19611615}, SunColor: [3]float32{1, 0.7399997, 0.5599996}, RealSunDir: [3]float32{-0.96321195, 0.18374233, 0.19611615}, SunElevation: 0.18374233, Ambient: [3]float32{0.17749995, 0.16499992, 0.18999986}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.96321195, 0.18374233, 0.19611615}, SunDiscColor: [3]float32{5, 3.6999986, 2.7999978}, MoonDiscDir: [3]float32{0.96321195, -0.18374233, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.745, glyph.EnvironmentState{SunDir: [3]float32{-0.9800968, 0.030800752, 0.19611615}, SunColor: [3]float32{0.94227904, 0.50883067, 0.24499248}, RealSunDir: [3]float32{-0.9800968, 0.030800752, 0.19611615}, SunElevation: 0.030800752, Ambient: [3]float32{0.14625, 0.11499998, 0.11499998}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{-0.9800968, 0.030800752, 0.19611615}, SunDiscColor: [3]float32{5, 2.6999998, 1.2999997}, MoonDiscDir: [3]float32{0.9800968, -0.030800752, 0.19611615}, MoonDiscColor: [3]float32{0.6079162, 0.62937206, 0.67943573}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.755, glyph.EnvironmentState{SunDir: [3]float32{-0.9800968, -0.030800752, 0.19611615}, SunColor: [3]float32{0.5687998, 0.2843999, 0.11375996}, RealSunDir: [3]float32{-0.9800968, -0.030800752, 0.19611615}, SunElevation: -0.030800752, Ambient: [3]float32{0.12916666, 0.09916666, 0.100833334}, FogDensity: 0.0075, StarFade: 0.004349053, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{-0.9800968, -0.030800752, 0.19611615}, SunDiscColor: [3]float32{4.948153, 2.4740765, 0.98963064}, MoonDiscDir: [3]float32{0.9800968, 0.030800752, 0.19611615}, MoonDiscColor: [3]float32{0.9220839, 0.95462805, 1.0305643}, CloudSteps: 32, LightShafts: 0.24510969}},
	{0.78, glyph.EnvironmentState{SunDir: [3]float32{0.96321195, 0.18374233, 0.19611615}, SunColor: [3]float32{0.0055161137, 0.006374176, 0.009806424}, RealSunDir: [3]float32{-0.96321195, -0.18374233, 0.19611615}, SunElevation: -0.18374233, Ambient: [3]float32{0.075, 0.07, 0.105}, FogDensity: 0.0075, StarFade: 0.6259716, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.96321195, -0.18374233, 0.19611615}, SunDiscColor: [3]float32{0.114998505, 0.057499252, 0.022999702}, MoonDiscDir: [3]float32{0.96321195, 0.18374233, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.83, glyph.EnvironmentState{SunDir: [3]float32{0.85928947, 0.47239828, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.85928947, -0.47239828, 0.19611615}, SunElevation: -0.47239828, Ambient: [3]float32{0.023833336, 0.029333338, 0.06433334}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.85928947, -0.47239828, 0.19611615}, MoonDiscDir: [3]float32{0.85928947, 0.47239828, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
}

// The moon disc's colour, which was three constants, a horizon fade and a boost
// inside buildMoonObject until it moved onto the state as MoonDiscColor. Pinned
// because moving arithmetic between files is exactly where a reassociated
// float32 multiply hides, and because every committed night capture has a moon
// in it.
var moonDiscPins = []struct {
	tod   float32
	color [3]float32
}{
	{0, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.041666668, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.083333336, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.125, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.16666667, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.20833333, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.25, [3]float32{0.7650001, 0.792, 0.855}},
	{0.29166666, [3]float32{0, 0, 0}},
	{0.33333334, [3]float32{0, 0, 0}},
	{0.375, [3]float32{0, 0, 0}},
	{0.41666666, [3]float32{0, 0, 0}},
	{0.45833334, [3]float32{0, 0, 0}},
	{0.5, [3]float32{0, 0, 0}},
	{0.5416667, [3]float32{0, 0, 0}},
	{0.5833333, [3]float32{0, 0, 0}},
	{0.625, [3]float32{0, 0, 0}},
	{0.6666667, [3]float32{0, 0, 0}},
	{0.7083333, [3]float32{0, 0, 0}},
	{0.75, [3]float32{0.7650001, 0.792, 0.855}},
	{0.7916667, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.8333333, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.875, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.9166667, [3]float32{1.2750001, 1.3199999, 1.425}},
	{0.9583333, [3]float32{1.2750001, 1.3199999, 1.425}},
	{1, [3]float32{1.2750001, 1.3199999, 1.425}},
}

// fixedSkyCases are the sky-at-a-fixed-hour configurations from the same
// generation, as Environment with Cycle nil. The three cases with no Sky in them
// stayed in the engine with StaticSource, which is the type they belong to now;
// these five are the ones that draw a dome, so they came here with it.
var fixedSkyCases = []struct {
	name string
	env  Environment
}{
	{"fixed sky day", Environment{Sky: &Sky{Stars: true, StarDensity: 1, MilkyWay: 1, SunDisc: true, MoonDisc: true, CloudSteps: CloudsHigh, LightShafts: 0.25, FixedSunElevation: 0.6}}},
	{"fixed sky night", Environment{Sky: &Sky{Stars: true, StarDensity: 1, MilkyWay: 1, SunDisc: true, MoonDisc: true, CloudSteps: CloudsHigh, LightShafts: 0.25, FixedSunElevation: -0.5}}},
	{"fixed sky sunset", Environment{Sky: &Sky{Stars: true, StarDensity: 1, MilkyWay: 1, CloudSteps: CloudsHigh, LightShafts: 0.25, FixedSunElevation: -0.08}, Fog: &glyph.Fog{Density: 0.01, Height: 6, BaseHeight: 2}}},
	{"sun+sky+fog", Environment{
		Sun:     &glyph.DirectionalLight{Direction: [3]float32{0.8, 0.6, 0}, Color: [3]float32{1, 1, 1}},
		Ambient: &glyph.AmbientLight{Color: [3]float32{0.05, 0.05, 0.05}},
		Sky:     &Sky{Stars: true, StarDensity: 0.5, MilkyWay: 0.25, SunDisc: true, MoonDisc: true, Cirrus: 0.4, CloudSteps: CloudsLow, LightShafts: 0.4, FixedSunElevation: 0.2, LightShaftShape: glyph.LightShaftShape{Radius: 1.3, Decay: 0.9, Threshold: [2]float32{0.3, 0.5}}},
		Fog:     &glyph.Fog{Density: 0.02, Height: 4, BaseHeight: 1},
	}},
	{"clamped sky", Environment{Sky: &Sky{StarDensity: -2, MilkyWay: 3, FixedSunElevation: 0.1}}},
}

// fixedSkyPins is what each case above resolved to before the carve.
var fixedSkyPins = []struct {
	name  string
	state glyph.EnvironmentState
}{
	{"fixed sky day", glyph.EnvironmentState{SunElevation: 0.6, MilkyWay: 1, StarDensity: 1, DrawSky: true, CloudSteps: 32, LightShafts: 0.25}},
	{"fixed sky night", glyph.EnvironmentState{SunElevation: -0.5, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, CloudSteps: 32}},
	{"fixed sky sunset", glyph.EnvironmentState{SunElevation: -0.08, FogDensity: 0.01, FogHeight: 6, FogBaseHeight: 2, StarFade: 0.11807579, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, CloudSteps: 32, LightShafts: 0.13939464}},
	{"sun+sky+fog", glyph.EnvironmentState{SunDir: [3]float32{0.8, 0.6, 0}, SunColor: [3]float32{1, 1, 1}, RealSunDir: [3]float32{0.8, 0.6, 0}, SunElevation: 0.2, Ambient: [3]float32{0.05, 0.05, 0.05}, FogDensity: 0.02, FogHeight: 4, FogBaseHeight: 1, MilkyWay: 0.25, StarDensity: 0.5, DrawSky: true, CloudSteps: 16, Cirrus: 0.4, LightShafts: 0.4, LightShaftShape: glyph.LightShaftShape{Radius: 1.3, Decay: 0.9, Threshold: [2]float32{0.3, 0.5}}, CastShadows: true}},
	{"clamped sky", glyph.EnvironmentState{SunElevation: 0.1, MilkyWay: 1, DrawSky: true}},
}

// diffState names the fields two states disagree on. A %+v of an
// EnvironmentState is four lines of mostly identical numbers, and the whole
// value of a pin is being told which one moved.
func diffState(got, want glyph.EnvironmentState) []string {
	var out []string
	g, w := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := 0; i < g.NumField(); i++ {
		if g.Field(i).Interface() != w.Field(i).Interface() {
			out = append(out, fmt.Sprintf("%s: got %v want %v",
				g.Type().Field(i).Name, g.Field(i).Interface(), w.Field(i).Interface()))
		}
	}
	return out
}

func dayCycleFixture(tod float32) *DayCycleSource {
	return &DayCycleSource{
		Cycle: DayNight{TimeOfDay: tod},
		Sky:   DefaultSky(),
		Fog:   &glyph.Fog{Density: glyph.DefaultFogDensity},
	}
}

// TestDayCycleSourceIsUnchanged is the Go half of the whole proof of this move:
// the day cycle resolves, field for field, to exactly what it resolved to while
// it was in the engine -- and, before that, to what it resolved to before it was
// a source at all.
//
// Both ways in are checked, because both ship: DayCycleSource directly, and the
// Environment composite that delegates to it. A split that quietly gave the two
// different answers would leave every example on one of them and every new game
// on the other.
//
// Verified to fail, by two breaks in the moon's boost and one in the shaft
// window. Both of the first kind are reassociations that are the same number in
// real arithmetic, and they do not catch the same thing:
//
//   - `0.85 * (fade * moonBoost)` rather than `0.85 * fade * moonBoost` moves the
//     low bit only where the horizon fade is partial, so it fails t=0.25 and
//     t=0.75 in the sweep and t=0.245, 0.25, 0.745 and 0.755 at the edges:
//     `MoonDiscColor: got [0.76500005 0.792 0.855] want [0.7650001 0.792 0.855]`.
//   - `(0.85 * moonBoost) * fade` lets the two constants fold before they meet a
//     float32, which moves EVERY above-horizon hour:
//     `t=0: MoonDiscColor: got [1.275 1.32 1.425] want [1.2750001 1.3199999 1.425]`.
//
// The carve's version of this comment described the first break and quoted the
// second's numbers, which is why they are both written out here: the first is the
// narrow one and running it alone would make the second look covered.
//
// And the shaft window: replacing the smoothstep with `if s.SunElevation > 0`,
// which is what it was before, fails t=0.245 and t=0.755 with
// `LightShafts: got 0 want 0.24510969`, t=0.25 with `want 0.25`, and
// TestFixedSkyIsUnchanged's "fixed sky sunset" with `want 0.13939464`.
func TestDayCycleSourceIsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		pins []struct {
			tod   float32
			state glyph.EnvironmentState
		}
	}{
		{"sweep", dayCyclePins},
		{"edges", dayCycleEdgePins},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range tc.pins {
				if got := dayCycleFixture(p.tod).State(); got != p.state {
					t.Errorf("DayCycleSource at t=%g moved:\n  %s", p.tod, strings.Join(diffState(got, p.state), "\n  "))
				}
				env := DefaultEnvironment()
				env.Cycle.TimeOfDay = p.tod
				if got := env.State(); got != p.state {
					t.Errorf("Environment at t=%g moved:\n  %s", p.tod, strings.Join(diffState(got, p.state), "\n  "))
				}
			}
		})
	}
}

// TestFixedSkyIsUnchanged is the same proof for a dome with no cycle under it:
// the configuration 24-custom-passes renders, and the one whose capture is in
// the migration gate.
//
// This path is the one the move rearranged rather than merely relocated. The
// light and the air now come from glyphengine.StaticSource -- the engine kept
// that resolution -- and only the dome is added here. The pins are what says the
// rearrangement produced the same numbers.
//
// Verified to fail twice over. Giving resolveSky the cycle's `haveBodies` --
// true rather than false, so a fixed sky places discs it has no bodies for --
// fails "fixed sky day", "fixed sky night" and "sun+sky+fog" with
// `DrawSun: got true want false` and `DrawMoon: got true want false`, and takes
// TestEnvironmentPiecesAreIndependent/sky_without_a_cycle with it. Dropping the
// StarFade line from the Sky branch of staticState fails "fixed sky night" with
// `StarFade: got 0 want 1` and `DrawStars: got false want true`, and "fixed sky
// sunset" with `StarFade: got 0 want 0.11807579`.
func TestFixedSkyIsUnchanged(t *testing.T) {
	if len(fixedSkyCases) != len(fixedSkyPins) {
		t.Fatalf("%d fixed-sky cases against %d pins", len(fixedSkyCases), len(fixedSkyPins))
	}
	for i, c := range fixedSkyCases {
		p := fixedSkyPins[i]
		if c.name != p.name {
			t.Fatalf("case %d is %q and its pin is %q", i, c.name, p.name)
		}
		env := c.env
		if got := env.State(); got != p.state {
			t.Errorf("fixed sky %q moved:\n  %s", c.name, strings.Join(diffState(got, p.state), "\n  "))
		}
	}
}

// TestMoonDiscColorIsUnchanged pins the colour that moved out of the engine's
// draw path in the carve and out of the engine entirely in this change.
//
// Verified to fail: either reassociation of the boost recorded on
// TestDayCycleSourceIsUnchanged above takes this table down with it, which is the
// point of having both -- this one says the arithmetic moved, that one says the
// frame moved with it.
//
// The other half of the carve's version of this test -- that the engine's
// billboard is actually coloured from the state rather than from something else
// -- stayed in the engine, where buildMoonObject is. It no longer needs a cycle
// to ask the question.
func TestMoonDiscColorIsUnchanged(t *testing.T) {
	for _, p := range moonDiscPins {
		dn := DayNight{TimeOfDay: p.tod}
		if got := dn.MoonDiscColor(); got != p.color {
			t.Errorf("t=%g: MoonDiscColor %v, want %v", p.tod, got, p.color)
		}
	}
}

// TestEnvironmentPiecesAreIndependent checks each piece can be present or
// absent on its own, which is what "composable" has to mean to be worth doing.
func TestEnvironmentPiecesAreIndependent(t *testing.T) {
	t.Run("sky without a cycle", func(t *testing.T) {
		env := &Environment{Sky: DefaultSky()}
		s := env.State()
		if !s.DrawSky {
			t.Error("sky not drawn")
		}
		// No cycle means no bodies to place, so no discs.
		if s.DrawSun || s.DrawMoon {
			t.Error("celestial discs drawn with no cycle to position them")
		}
		if s.SunColor != ([3]float32{}) {
			t.Errorf("sun light %v with no cycle and no fixed sun", s.SunColor)
		}
	})

	t.Run("cycle without a sky", func(t *testing.T) {
		env := &Environment{Cycle: &DayNight{TimeOfDay: 0.5}}
		s := env.State()
		if s.DrawSky || s.DrawStars || s.DrawSun || s.DrawMoon {
			t.Error("something was drawn with no Sky")
		}
		// The light still works: a game can supply its own skybox and keep this
		// package's sun.
		if s.SunColor == ([3]float32{}) {
			t.Error("no sun light at noon")
		}
		if !s.CastShadows {
			t.Error("no shadows at noon")
		}
	})

	t.Run("fixed light without a cycle", func(t *testing.T) {
		env := &Environment{
			Sun:     &glyph.DirectionalLight{Direction: [3]float32{0, 1, 0}, Color: [3]float32{1, 1, 1}},
			Ambient: &glyph.AmbientLight{Color: [3]float32{0.2, 0.2, 0.2}},
		}
		s := env.State()
		if s.SunDir != ([3]float32{0, 1, 0}) || s.SunColor != ([3]float32{1, 1, 1}) {
			t.Errorf("fixed sun not used: dir %v color %v", s.SunDir, s.SunColor)
		}
		if s.Ambient != ([3]float32{0.2, 0.2, 0.2}) {
			t.Errorf("fixed ambient not used: %v", s.Ambient)
		}
		if !s.CastShadows {
			t.Error("a fixed sun with colour should cast shadows")
		}
	})

	t.Run("a cycle overrides fixed light", func(t *testing.T) {
		env := &Environment{
			Cycle: &DayNight{TimeOfDay: 0.5},
			Sun:   &glyph.DirectionalLight{Direction: [3]float32{1, 0, 0}, Color: [3]float32{9, 9, 9}},
		}
		if s := env.State(); s.SunColor == ([3]float32{9, 9, 9}) {
			t.Error("fixed sun used while a cycle is present")
		}
	})

	t.Run("fog is independent", func(t *testing.T) {
		if s := (&Environment{}).State(); s.FogDensity != 0 {
			t.Errorf("fog %v with no Fog", s.FogDensity)
		}
		env := &Environment{Fog: &glyph.Fog{Density: 0.02}}
		if s := env.State(); s.FogDensity != 0.02 {
			t.Errorf("fog density %v; want 0.02", s.FogDensity)
		}
	})

	t.Run("stars can be disabled independently", func(t *testing.T) {
		env := &Environment{
			Cycle: &DayNight{TimeOfDay: 0}, // midnight
			Sky:   &Sky{Stars: false, SunDisc: true, MoonDisc: true},
		}
		s := env.State()
		if !s.DrawSky {
			t.Error("sky not drawn")
		}
		if s.DrawStars {
			t.Error("stars drawn with Stars=false")
		}
	})
}

// TestDefaultEnvironmentIsWhatTheEngineUsedToGive guards the migration from the
// other end: this is the environment NewScene installed by itself until the sky
// moved out, so a scene that swaps nil for DefaultEnvironment() has to get the
// sky it had -- sunrise, frozen, with the engine's haze.
func TestDefaultEnvironmentIsWhatTheEngineUsedToGive(t *testing.T) {
	env := DefaultEnvironment()
	got := env.State()

	if !got.DrawSky {
		t.Error("the default environment has no sky")
	}
	if env.Cycle == nil {
		t.Fatal("the default environment has no day/night cycle")
	}
	if env.Cycle.TimeOfDay != 0.25 {
		t.Errorf("default time of day %v; want sunrise at 0.25", env.Cycle.TimeOfDay)
	}
	if env.Cycle.Speed != 0 {
		t.Errorf("default cycle speed %v; time should not pass unless asked", env.Cycle.Speed)
	}
	if got.FogDensity != glyph.DefaultFogDensity {
		t.Errorf("default fog %v; want %v", got.FogDensity, glyph.DefaultFogDensity)
	}
}

// TestCloudStepsFlowThrough checks the quality knob actually reaches the
// resolved state, including the off case.
//
// It is the one setting here with a measured frame-time cost attached to it, so
// a game turning clouds off has to actually get a sky without them rather than
// a sky that quietly ignores the request.
func TestCloudStepsFlowThrough(t *testing.T) {
	for _, steps := range []int{CloudsOff, CloudsLow, CloudsHigh, 7} {
		env := &Environment{Sky: &Sky{CloudSteps: steps}}
		if got := env.State().CloudSteps; got != steps {
			t.Errorf("CloudSteps %d resolved to %d", steps, got)
		}
	}
	// No sky at all means no clouds, whatever the field said.
	env := &Environment{}
	if got := env.State().CloudSteps; got != 0 {
		t.Errorf("CloudSteps %d with no Sky", got)
	}
	if DefaultSky().CloudSteps != CloudsHigh {
		t.Errorf("DefaultSky has CloudSteps %d, want CloudsHigh", DefaultSky().CloudSteps)
	}

	// Changing it between frames has to take effect without rebuilding
	// anything, because a graphics-settings slider will do exactly that.
	live := &Environment{Sky: DefaultSky()}
	if got := live.State().CloudSteps; got != CloudsHigh {
		t.Fatalf("CloudSteps %d before change", got)
	}
	live.Sky.CloudSteps = CloudsOff
	if got := live.State().CloudSteps; got != CloudsOff {
		t.Errorf("CloudSteps %d after setting CloudsOff at runtime", got)
	}
	live.Sky.CloudSteps = CloudsLow
	if got := live.State().CloudSteps; got != CloudsLow {
		t.Errorf("CloudSteps %d after setting CloudsLow at runtime", got)
	}
}

// TestRealSunDirTracksTheSunNotTheLight guards the split that keeps the sunset
// glow off the midnight moon.
//
// EnvironmentState carries two directions on purpose: SunDir is whichever body
// lights the scene, and RealSunDir is the sun itself. From dusk to dawn those
// point opposite ways, and the atmosphere must follow the second. Collapsing
// RealSunDir onto SunDir in dayCycleState fails the midnight case here.
func TestRealSunDirTracksTheSunNotTheLight(t *testing.T) {
	env := &Environment{Cycle: &DayNight{}, Sky: DefaultSky()}

	// Midnight: the moon is the primary light, so the two directions disagree
	// and RealSunDir has to be the one pointing below the horizon.
	env.Cycle.TimeOfDay = 0.0
	s := env.State()
	if s.RealSunDir[1] >= 0 {
		t.Errorf("midnight: RealSunDir.y = %g, want below the horizon", s.RealSunDir[1])
	}
	if s.SunDir[1] <= 0 {
		t.Fatalf("midnight: SunDir.y = %g, expected the moon to be the primary light", s.SunDir[1])
	}
	if s.RealSunDir == s.SunDir {
		t.Error("midnight: RealSunDir equals SunDir, so the glow would follow the moon")
	}

	// SunElevation is documented as RealSunDir's y; if they can drift, the glow
	// gets positioned by one and shaped by the other.
	if s.SunElevation != s.RealSunDir[1] {
		t.Errorf("SunElevation = %g but RealSunDir.y = %g", s.SunElevation, s.RealSunDir[1])
	}

	// Noon: the sun is the primary light, so the two agree and nothing about
	// daytime scattering changes.
	env.Cycle.TimeOfDay = 0.5
	s = env.State()
	if s.RealSunDir[1] <= 0 {
		t.Errorf("noon: RealSunDir.y = %g, want above the horizon", s.RealSunDir[1])
	}
	if s.RealSunDir != s.SunDir {
		t.Errorf("noon: RealSunDir %v and SunDir %v should be the same body", s.RealSunDir, s.SunDir)
	}

	// A fixed sun has no handover, so the light is the sun. That rule is the
	// engine's StaticSource now, and this is what says the delegation kept it.
	fixed := &Environment{Sun: &glyph.DirectionalLight{Direction: [3]float32{0, 1, 0}, Color: [3]float32{1, 1, 1}}}
	if fs := fixed.State(); fs.RealSunDir != fs.SunDir {
		t.Errorf("fixed sun: RealSunDir %v != SunDir %v", fs.RealSunDir, fs.SunDir)
	}
}

// TestFixedSunElevationDrawsStars covers the sky-without-a-cycle path, which is
// the documented way to get a static sky at a chosen hour.
//
// It used to give a static *night* an empty one. StarFade is only meaningful
// with a Cycle to compute it from, so on this path it was never assigned, and
// DrawStars reads it -- a scene frozen at midnight got the night palette,
// night ambient and no stars at all. Nothing failed; there was simply nothing
// in the sky.
//
// Verified by removing the StarFade assignment from staticState's Sky branch:
// this fails with DrawStars false at every elevation below the horizon.
func TestFixedSunElevationDrawsStars(t *testing.T) {
	night := (&Environment{Sky: &Sky{Stars: true, FixedSunElevation: -0.5}}).State()
	if !night.DrawStars {
		t.Errorf("a sky frozen at elevation -0.5 draws no stars")
	}
	if night.StarFade < 0.99 {
		t.Errorf("StarFade %.2f at elevation -0.5; want fully out", night.StarFade)
	}

	day := (&Environment{Sky: &Sky{Stars: true, FixedSunElevation: 0.6}}).State()
	if day.DrawStars {
		t.Errorf("a sky frozen at midday draws stars")
	}

	// And the two paths must agree. A cycle parked at the same elevation and a
	// fixed sky at that elevation are the same sky; they read the curve from
	// one place so they cannot disagree.
	for _, tod := range []float32{0.02, 0.20, 0.30, 0.78, 0.90} {
		dn := &DayNight{TimeOfDay: tod}
		elev := dn.SunDir()[1]
		cycled := (&Environment{Sky: &Sky{Stars: true}, Cycle: dn}).State()
		fixed := (&Environment{Sky: &Sky{Stars: true, FixedSunElevation: elev}}).State()
		if math.Abs(float64(cycled.StarFade-fixed.StarFade)) > 1e-6 {
			t.Errorf("at elevation %+.3f a cycle gives StarFade %.4f and a fixed sky %.4f",
				elev, cycled.StarFade, fixed.StarFade)
		}
	}
}

// TestLightShaftShapeReachesTheFrameState: what a game sets on the Sky is what
// State hands the engine, untouched -- including the zeros, because resolving
// them is the renderer's job and doing it here as well would make "zero means
// default" true in two places that could come to disagree.
//
// Verified to fail: without the pass-through line in resolveSky the frame state
// reports a zero shape for the custom case.
func TestLightShaftShapeReachesTheFrameState(t *testing.T) {
	env := DefaultEnvironment()
	if got := env.State().LightShaftShape; got != (glyph.LightShaftShape{}) {
		t.Errorf("an untouched Sky hands on the shape %+v, want the zero value", got)
	}

	want := glyph.LightShaftShape{Radius: 1.3, Threshold: [2]float32{0.3, 0.5}}
	env.Sky.LightShaftShape = want
	if got := env.State().LightShaftShape; got != want {
		t.Errorf("State hands on the shape %+v, want %+v", got, want)
	}
}

// TestEnvironmentResolvesWithoutAllocating is the per-frame cost of the seam,
// measured on this side of it.
//
// The state is a plain value and the engine copies it once a frame; a per-frame
// allocation in the draw path is paid by every frame of every game. The
// fixed-light path is the one with something new to prove: it builds a
// glyphengine.StaticSource to delegate to, and a composite literal that escaped
// would be an allocation per frame bought for a comment about not duplicating
// rules.
//
// Verified to fail: making the delegation escape -- a package-level
// `var sink *glyph.StaticSource` and `src := &glyph.StaticSource{...}; sink =
// src; s := src.State()` in staticState -- reports `1 allocations per frame` for
// the two fixed-light cases and leaves the cycle's at zero, which is exactly the
// shape of that mistake.
func TestEnvironmentResolvesWithoutAllocating(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  glyph.EnvironmentSource
	}{
		{"Environment on a cycle", DefaultEnvironment()},
		{"Environment at a fixed hour", &Environment{Sky: DefaultSky(), Sun: &glyph.DirectionalLight{Direction: [3]float32{0, 1, 0}, Color: [3]float32{1, 1, 1}}, Fog: &glyph.Fog{Density: 0.01}}},
		{"Environment with no sky", &Environment{Sun: &glyph.DirectionalLight{Direction: [3]float32{0, 1, 0}, Color: [3]float32{1, 1, 1}}}},
		{"DayCycleSource", dayCycleFixture(0.3)},
	} {
		if n := testing.AllocsPerRun(200, func() { _ = tc.src.State() }); n != 0 {
			t.Errorf("%s: %v allocations per frame resolving the environment", tc.name, n)
		}
		if n := testing.AllocsPerRun(200, func() { tc.src.Advance(1.0 / 60) }); n != 0 {
			t.Errorf("%s: %v allocations per tick advancing", tc.name, n)
		}
	}
}

// TestSceneAdvancesTheCycle is the seam from the engine's side: Scene.Tick calls
// EnvironmentSource.Advance on the fixed tick, so a cycle in Scene.Env moves in
// simulation seconds without the engine knowing what a cycle is.
//
// TestDayNightAdvancesInSeconds, which moved here from the engine's scene_test.go,
// measures the same clock with no Scene in front of it. This one is about the
// seam rather than the clock: an Advance the scene did not call is a sun that
// never rises, with nothing else failing.
//
// Verified to fail: giving DayCycleSource an Advance that does nothing leaves the
// clock at 0.2 and reports `after 1s TimeOfDay = 0.200000, want 0.208333`.
func TestSceneAdvancesTheCycle(t *testing.T) {
	src := &DayCycleSource{Cycle: DayNight{TimeOfDay: 0.2, Speed: 1.0 / 120}, Sky: DefaultSky()}
	s := glyph.NewScene()
	s.Env = src

	// One second of simulation at the fixed tick.
	for i := 0; i < 60; i++ {
		s.Tick(1.0 / 60)
	}
	want := float32(0.2 + 1.0/120)
	if math.Abs(float64(src.Cycle.TimeOfDay-want)) > 1e-5 {
		t.Errorf("after 1s TimeOfDay = %.6f, want %.6f", src.Cycle.TimeOfDay, want)
	}

	// SetTimeOfDay carries the same wrap, which is why it exists: the engine
	// used to apply it on its side of the seam.
	src.Cycle.SetTimeOfDay(1.25)
	if src.Cycle.TimeOfDay != 0.25 {
		t.Errorf("SetTimeOfDay(1.25) left the clock at %v, want 0.25", src.Cycle.TimeOfDay)
	}
	src.Cycle.SetTimeOfDay(-0.25)
	if src.Cycle.TimeOfDay != 0.75 {
		t.Errorf("SetTimeOfDay(-0.25) left the clock at %v, want 0.75", src.Cycle.TimeOfDay)
	}
}

// TestShadersFillTheSkySlotAndNothingElse is the other half of the move: a
// source that asks for a dome, with a renderer that has no shader to draw one,
// is a frame that silently has no sky. So the package has to supply exactly the
// three stages the engine leaves empty -- and no others, because a package that
// quietly replaced the lit shaders would change every surface in the frame.
//
// Verified to fail both ways. Starting Shaders() from
// renderer.DefaultShaders() rather than from an empty set names all 42 stages it
// would overwrite, beginning
// `Shaders() set TriangleVert (1504 bytes); it must leave every stage but the sky
// slot alone`. Dropping the CloudsFrag assignment from Fill reports
// `CloudsFrag is nil; the sky slot is not filled`.
func TestShadersFillTheSkySlotAndNothingElse(t *testing.T) {
	got := Shaders()
	slot := map[string][]byte{"SkyFrag": got.SkyFrag, "StarsFrag": got.StarsFrag, "CloudsFrag": got.CloudsFrag}
	for name, spv := range slot {
		if len(spv) == 0 {
			t.Errorf("%s is nil; the sky slot is not filled", name)
		}
	}

	// Every other field has to come back nil, so renderer's own defaults fill it.
	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if _, isSlot := slot[name]; isSlot {
			continue
		}
		if b := v.Field(i).Bytes(); b != nil {
			t.Errorf("Shaders() set %s (%d bytes); it must leave every stage but the sky slot alone", name, len(b))
		}
	}

	// And the engine's defaults have to leave the slot empty, or none of the
	// above matters: a dome would arrive from the engine and this package would
	// be decoration.
	d := renderer.DefaultShaders()
	if d.SkyFrag != nil || d.StarsFrag != nil || d.CloudsFrag != nil {
		t.Errorf("renderer.DefaultShaders() fills the sky slot: SkyFrag %d, StarsFrag %d, CloudsFrag %d bytes",
			len(d.SkyFrag), len(d.StarsFrag), len(d.CloudsFrag))
	}

	// Fill leaves a stage the caller already set, which is what makes a dome
	// from here and clouds from somewhere else possible.
	mine := []byte("my clouds")
	if got := Fill(renderer.ShaderSet{CloudsFrag: mine}); string(got.CloudsFrag) != string(mine) {
		t.Error("Fill overwrote a CloudsFrag the caller had already set")
	} else if got.SkyFrag == nil {
		t.Error("Fill skipped SkyFrag when CloudsFrag was already set")
	}
}
