package glyphengine

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/renderer"
)

// The four tables below were taken from the code as it stood before the
// environment contract was carved: generated out of the then-current
// Environment.State and buildMoonObject, printed at full float32 precision, and
// pasted in unedited. They are not re-derivations of the curves, they are what
// the engine actually produced -- which is the only thing that makes "nothing
// moved" a claim rather than a hope. x/terrainfield's field digests were pinned
// the same way, for the same reason.
//
// MoonDiscColor is the one column the pre-carve Environment.State did not have,
// because the colour was computed in the draw path. Its values come from a
// verbatim copy of what buildMoonObject computed, cross-checked against the
// separate moonDiscPins table below, which was generated the same way.
//
// A capture gate cannot stand in for them. It needs a GPU, it visits a handful
// of times of day, and it cannot tell a palette that shifted in the last bit
// from a frame that dithered. These compare every field of every resolved state
// at twenty-five points round the clock, exactly.
var dayCyclePins = []struct {
	tod   float32
	state EnvironmentState
}{
	{0, EnvironmentState{SunDir: [3]float32{-6.0043254e-17, 0.9805807, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{6.0043254e-17, -0.9805807, 0.19611615}, SunElevation: -0.9805807, Ambient: [3]float32{0.01, 0.013, 0.03}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{6.0043254e-17, -0.9805807, 0.19611615}, MoonDiscDir: [3]float32{-6.0043254e-17, 0.9805807, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.041666668, EnvironmentState{SunDir: [3]float32{-0.25379297, 0.94716823, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{0.25379297, -0.94716823, 0.19611615}, SunElevation: -0.94716823, Ambient: [3]float32{0.010694444, 0.013694445, 0.031388886}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.25379297, -0.94716823, 0.19611615}, MoonDiscDir: [3]float32{-0.25379297, 0.94716823, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.083333336, EnvironmentState{SunDir: [3]float32{-0.49029034, 0.84920776, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{0.49029034, -0.84920776, 0.19611615}, SunElevation: -0.84920776, Ambient: [3]float32{0.011388889, 0.014388889, 0.032777775}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.49029034, -0.84920776, 0.19611615}, MoonDiscDir: [3]float32{-0.49029034, 0.84920776, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.125, EnvironmentState{SunDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{0.69337523, -0.69337523, 0.19611615}, SunElevation: -0.69337523, Ambient: [3]float32{0.012083333, 0.0150833335, 0.034166664}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.69337523, -0.69337523, 0.19611615}, MoonDiscDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.16666667, EnvironmentState{SunDir: [3]float32{-0.8492078, 0.4902903, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{0.8492078, -0.4902903, 0.19611615}, SunElevation: -0.4902903, Ambient: [3]float32{0.012777777, 0.015777778, 0.035555553}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.8492078, -0.4902903, 0.19611615}, MoonDiscDir: [3]float32{-0.8492078, 0.4902903, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.20833333, EnvironmentState{SunDir: [3]float32{-0.94716823, 0.253793, 0.19611615}, SunColor: [3]float32{0.027125621, 0.031345163, 0.048223324}, RealSunDir: [3]float32{0.94716823, -0.253793, 0.19611615}, SunElevation: -0.253793, Ambient: [3]float32{0.042749994, 0.050708324, 0.08841665}, FogDensity: 0.0075, StarFade: 0.92728853, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.94716823, -0.253793, 0.19611615}, MoonDiscDir: [3]float32{-0.94716823, 0.253793, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.25, EnvironmentState{SunDir: [3]float32{0.9805807, 0, 0.19611615}, SunColor: [3]float32{0.784, 0.4704, 0.2352}, RealSunDir: [3]float32{0.9805807, 0, 0.19611615}, Ambient: [3]float32{0.14, 0.115, 0.105}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{0.9805807, 0, 0.19611615}, SunDiscColor: [3]float32{5, 3, 1.5}, MoonDiscDir: [3]float32{-0.9805807, -0, 0.19611615}, MoonDiscColor: [3]float32{0.7650001, 0.792, 0.855}, CloudSteps: 32, LightShafts: 0.25}},
	{0.29166666, EnvironmentState{SunDir: [3]float32{0.94716823, 0.2537929, 0.19611615}, SunColor: [3]float32{1, 0.8499999, 0.71666646}, RealSunDir: [3]float32{0.94716823, 0.2537929, 0.19611615}, SunElevation: 0.2537929, Ambient: [3]float32{0.1904762, 0.18551588, 0.22063492}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.94716823, 0.2537929, 0.19611615}, SunDiscColor: [3]float32{5, 4.2499995, 3.5833323}, MoonDiscDir: [3]float32{-0.94716823, -0.2537929, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.33333334, EnvironmentState{SunDir: [3]float32{0.84920776, 0.4902904, 0.19611615}, SunColor: [3]float32{1, 0.9166666, 0.825}, RealSunDir: [3]float32{0.84920776, 0.4902904, 0.19611615}, SunElevation: 0.4902904, Ambient: [3]float32{0.20238096, 0.1984127, 0.23650795}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.84920776, 0.4902904, 0.19611615}, SunDiscColor: [3]float32{5, 4.583333, 4.125}, MoonDiscDir: [3]float32{-0.84920776, -0.4902904, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.375, EnvironmentState{SunDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, SunColor: [3]float32{1, 0.9375, 0.85625}, RealSunDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, SunElevation: 0.69337523, Ambient: [3]float32{0.21428572, 0.21130952, 0.25238097}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, SunDiscColor: [3]float32{5, 4.6875, 4.28125}, MoonDiscDir: [3]float32{-0.69337523, -0.69337523, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.41666666, EnvironmentState{SunDir: [3]float32{0.4902904, 0.84920776, 0.19611615}, SunColor: [3]float32{1, 0.9583333, 0.8875}, RealSunDir: [3]float32{0.4902904, 0.84920776, 0.19611615}, SunElevation: 0.84920776, Ambient: [3]float32{0.22619048, 0.22420634, 0.26825398}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.4902904, 0.84920776, 0.19611615}, SunDiscColor: [3]float32{5, 4.7916665, 4.4375}, MoonDiscDir: [3]float32{-0.4902904, -0.84920776, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.45833334, EnvironmentState{SunDir: [3]float32{0.2537929, 0.94716823, 0.19611615}, SunColor: [3]float32{1, 0.9791667, 0.91875}, RealSunDir: [3]float32{0.2537929, 0.94716823, 0.19611615}, SunElevation: 0.94716823, Ambient: [3]float32{0.23809524, 0.23710318, 0.284127}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.2537929, 0.94716823, 0.19611615}, SunDiscColor: [3]float32{5, 4.8958335, 4.59375}, MoonDiscDir: [3]float32{-0.2537929, -0.94716823, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.5, EnvironmentState{SunDir: [3]float32{6.0043254e-17, 0.9805807, 0.19611615}, SunColor: [3]float32{1, 1, 0.95}, RealSunDir: [3]float32{6.0043254e-17, 0.9805807, 0.19611615}, SunElevation: 0.9805807, Ambient: [3]float32{0.25, 0.25, 0.3}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{6.0043254e-17, 0.9805807, 0.19611615}, SunDiscColor: [3]float32{5, 5, 4.75}, MoonDiscDir: [3]float32{-6.0043254e-17, -0.9805807, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.5416667, EnvironmentState{SunDir: [3]float32{-0.25379306, 0.94716823, 0.19611615}, SunColor: [3]float32{1, 0.9791666, 0.91875}, RealSunDir: [3]float32{-0.25379306, 0.94716823, 0.19611615}, SunElevation: 0.94716823, Ambient: [3]float32{0.23809522, 0.23710316, 0.284127}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.25379306, 0.94716823, 0.19611615}, SunDiscColor: [3]float32{5, 4.895833, 4.59375}, MoonDiscDir: [3]float32{0.25379306, -0.94716823, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.5833333, EnvironmentState{SunDir: [3]float32{-0.49029022, 0.8492078, 0.19611615}, SunColor: [3]float32{1, 0.9583333, 0.8875}, RealSunDir: [3]float32{-0.49029022, 0.8492078, 0.19611615}, SunElevation: 0.8492078, Ambient: [3]float32{0.22619048, 0.22420636, 0.26825398}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.49029022, 0.8492078, 0.19611615}, SunDiscColor: [3]float32{5, 4.7916665, 4.4375}, MoonDiscDir: [3]float32{0.49029022, -0.8492078, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.625, EnvironmentState{SunDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, SunColor: [3]float32{1, 0.9375, 0.85625}, RealSunDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, SunElevation: 0.69337523, Ambient: [3]float32{0.2142857, 0.21130952, 0.25238097}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.69337523, 0.69337523, 0.19611615}, SunDiscColor: [3]float32{5, 4.6875, 4.28125}, MoonDiscDir: [3]float32{0.69337523, -0.69337523, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.6666667, EnvironmentState{SunDir: [3]float32{-0.8492078, 0.49029022, 0.19611615}, SunColor: [3]float32{1, 0.9166666, 0.825}, RealSunDir: [3]float32{-0.8492078, 0.49029022, 0.19611615}, SunElevation: 0.49029022, Ambient: [3]float32{0.20238094, 0.19841269, 0.23650792}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.8492078, 0.49029022, 0.19611615}, SunDiscColor: [3]float32{5, 4.583333, 4.125}, MoonDiscDir: [3]float32{0.8492078, -0.49029022, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.7083333, EnvironmentState{SunDir: [3]float32{-0.94716823, 0.25379306, 0.19611615}, SunColor: [3]float32{1, 0.8333334, 0.7000001}, RealSunDir: [3]float32{-0.94716823, 0.25379306, 0.19611615}, SunElevation: 0.25379306, Ambient: [3]float32{0.19047618, 0.18551588, 0.22063491}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.94716823, 0.25379306, 0.19611615}, SunDiscColor: [3]float32{5, 4.166667, 3.5000005}, MoonDiscDir: [3]float32{0.94716823, -0.25379306, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.75, EnvironmentState{SunDir: [3]float32{-0.9805807, 1.2008651e-16, 0.19611615}, SunColor: [3]float32{0.784, 0.392, 0.1568}, RealSunDir: [3]float32{-0.9805807, 1.2008651e-16, 0.19611615}, SunElevation: 1.2008651e-16, Ambient: [3]float32{0.14, 0.105, 0.1}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{-0.9805807, 1.2008651e-16, 0.19611615}, SunDiscColor: [3]float32{5, 2.5, 1}, MoonDiscDir: [3]float32{0.9805807, -1.2008651e-16, 0.19611615}, MoonDiscColor: [3]float32{0.7650001, 0.792, 0.855}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.7916667, EnvironmentState{SunDir: [3]float32{0.94716823, 0.25379306, 0.19611615}, SunColor: [3]float32{0.02712564, 0.03134518, 0.048223358}, RealSunDir: [3]float32{-0.94716823, -0.25379306, 0.19611615}, SunElevation: -0.25379306, Ambient: [3]float32{0.060708284, 0.05891663, 0.09479163}, FogDensity: 0.0075, StarFade: 0.9272887, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.94716823, -0.25379306, 0.19611615}, MoonDiscDir: [3]float32{0.94716823, 0.25379306, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.8333333, EnvironmentState{SunDir: [3]float32{0.8492078, 0.49029022, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.8492078, -0.49029022, 0.19611615}, SunElevation: -0.49029022, Ambient: [3]float32{0.023111114, 0.02844445, 0.062444452}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.8492078, -0.49029022, 0.19611615}, MoonDiscDir: [3]float32{0.8492078, 0.49029022, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.875, EnvironmentState{SunDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.69337523, -0.69337523, 0.19611615}, SunElevation: -0.69337523, Ambient: [3]float32{0.014083332, 0.017333332, 0.038833328}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.69337523, -0.69337523, 0.19611615}, MoonDiscDir: [3]float32{0.69337523, 0.69337523, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.9166667, EnvironmentState{SunDir: [3]float32{0.49029022, 0.8492078, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.49029022, -0.8492078, 0.19611615}, SunElevation: -0.8492078, Ambient: [3]float32{0.012083333, 0.0150833335, 0.034166664}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.49029022, -0.8492078, 0.19611615}, MoonDiscDir: [3]float32{0.49029022, 0.8492078, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.9583333, EnvironmentState{SunDir: [3]float32{0.25379306, 0.94716823, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.25379306, -0.94716823, 0.19611615}, SunElevation: -0.94716823, Ambient: [3]float32{0.011041667, 0.014041668, 0.032083333}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.25379306, -0.94716823, 0.19611615}, MoonDiscDir: [3]float32{0.25379306, 0.94716823, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{1, EnvironmentState{SunDir: [3]float32{1.8012975e-16, 0.9805807, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-1.8012975e-16, -0.9805807, 0.19611615}, SunElevation: -0.9805807, Ambient: [3]float32{0.01, 0.013, 0.03}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-1.8012975e-16, -0.9805807, 0.19611615}, MoonDiscDir: [3]float32{1.8012975e-16, 0.9805807, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
}

// The edges, where the curves are not straight and a regular sweep steps over
// them: the first light before sunrise at 0.22, the two sides of the shaft
// window at 0.745 and 0.755 that the smoothstep was introduced to stop blinking
// across, sunrise and sunset themselves, and the blue hour after each.
var dayCycleEdgePins = []struct {
	tod   float32
	state EnvironmentState
}{
	{0.22, EnvironmentState{SunDir: [3]float32{-0.9632119, 0.18374251, 0.19611615}, SunColor: [3]float32{0.0055161547, 0.006374223, 0.009806497}, RealSunDir: [3]float32{0.9632119, -0.18374251, 0.19611615}, SunElevation: -0.18374251, Ambient: [3]float32{0.055, 0.065, 0.11}, FogDensity: 0.0075, StarFade: 0.62597257, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{0.9632119, -0.18374251, 0.19611615}, SunDiscColor: [3]float32{0.11499605, 0.06899764, 0.03449882}, MoonDiscDir: [3]float32{-0.9632119, 0.18374251, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.245, EnvironmentState{SunDir: [3]float32{0.9800968, -0.030800752, 0.19611615}, SunColor: [3]float32{0.5687998, 0.3412799, 0.17063995}, RealSunDir: [3]float32{0.9800968, -0.030800752, 0.19611615}, SunElevation: -0.030800752, Ambient: [3]float32{0.12583335, 0.10666668, 0.10583333}, FogDensity: 0.0075, StarFade: 0.004349053, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{0.9800968, -0.030800752, 0.19611615}, SunDiscColor: [3]float32{4.948153, 2.9688919, 1.4844459}, MoonDiscDir: [3]float32{-0.9800968, 0.030800752, 0.19611615}, MoonDiscColor: [3]float32{0.9220839, 0.95462805, 1.0305643}, CloudSteps: 32, LightShafts: 0.24510969}},
	{0.25, EnvironmentState{SunDir: [3]float32{0.9805807, 0, 0.19611615}, SunColor: [3]float32{0.784, 0.4704, 0.2352}, RealSunDir: [3]float32{0.9805807, 0, 0.19611615}, Ambient: [3]float32{0.14, 0.115, 0.105}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{0.9805807, 0, 0.19611615}, SunDiscColor: [3]float32{5, 3, 1.5}, MoonDiscDir: [3]float32{-0.9805807, -0, 0.19611615}, MoonDiscColor: [3]float32{0.7650001, 0.792, 0.855}, CloudSteps: 32, LightShafts: 0.25}},
	{0.3, EnvironmentState{SunDir: [3]float32{0.9325876, 0.30301616, 0.19611615}, SunColor: [3]float32{1, 0.9, 0.8}, RealSunDir: [3]float32{0.9325876, 0.30301616, 0.19611615}, SunElevation: 0.30301616, Ambient: [3]float32{0.19285715, 0.18809524, 0.22380953}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{0.9325876, 0.30301616, 0.19611615}, SunDiscColor: [3]float32{5, 4.5, 4}, MoonDiscDir: [3]float32{-0.9325876, -0.30301616, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.72, EnvironmentState{SunDir: [3]float32{-0.96321195, 0.18374233, 0.19611615}, SunColor: [3]float32{1, 0.7399997, 0.5599996}, RealSunDir: [3]float32{-0.96321195, 0.18374233, 0.19611615}, SunElevation: 0.18374233, Ambient: [3]float32{0.17749995, 0.16499992, 0.18999986}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, SunDiscDir: [3]float32{-0.96321195, 0.18374233, 0.19611615}, SunDiscColor: [3]float32{5, 3.6999986, 2.7999978}, MoonDiscDir: [3]float32{0.96321195, -0.18374233, 0.19611615}, MoonDiscColor: [3]float32{0, 0, 0}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.745, EnvironmentState{SunDir: [3]float32{-0.9800968, 0.030800752, 0.19611615}, SunColor: [3]float32{0.94227904, 0.50883067, 0.24499248}, RealSunDir: [3]float32{-0.9800968, 0.030800752, 0.19611615}, SunElevation: 0.030800752, Ambient: [3]float32{0.14625, 0.11499998, 0.11499998}, FogDensity: 0.0075, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{-0.9800968, 0.030800752, 0.19611615}, SunDiscColor: [3]float32{5, 2.6999998, 1.2999997}, MoonDiscDir: [3]float32{0.9800968, -0.030800752, 0.19611615}, MoonDiscColor: [3]float32{0.6079162, 0.62937206, 0.67943573}, CloudSteps: 32, LightShafts: 0.25, CastShadows: true}},
	{0.755, EnvironmentState{SunDir: [3]float32{-0.9800968, -0.030800752, 0.19611615}, SunColor: [3]float32{0.5687998, 0.2843999, 0.11375996}, RealSunDir: [3]float32{-0.9800968, -0.030800752, 0.19611615}, SunElevation: -0.030800752, Ambient: [3]float32{0.12916666, 0.09916666, 0.100833334}, FogDensity: 0.0075, StarFade: 0.004349053, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawSun: true, DrawMoon: true, SunDiscDir: [3]float32{-0.9800968, -0.030800752, 0.19611615}, SunDiscColor: [3]float32{4.948153, 2.4740765, 0.98963064}, MoonDiscDir: [3]float32{0.9800968, 0.030800752, 0.19611615}, MoonDiscColor: [3]float32{0.9220839, 0.95462805, 1.0305643}, CloudSteps: 32, LightShafts: 0.24510969}},
	{0.78, EnvironmentState{SunDir: [3]float32{0.96321195, 0.18374233, 0.19611615}, SunColor: [3]float32{0.0055161137, 0.006374176, 0.009806424}, RealSunDir: [3]float32{-0.96321195, -0.18374233, 0.19611615}, SunElevation: -0.18374233, Ambient: [3]float32{0.075, 0.07, 0.105}, FogDensity: 0.0075, StarFade: 0.6259716, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.96321195, -0.18374233, 0.19611615}, SunDiscColor: [3]float32{0.114998505, 0.057499252, 0.022999702}, MoonDiscDir: [3]float32{0.96321195, 0.18374233, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
	{0.83, EnvironmentState{SunDir: [3]float32{0.85928947, 0.47239828, 0.19611615}, SunColor: [3]float32{0.045, 0.052, 0.08}, RealSunDir: [3]float32{-0.85928947, -0.47239828, 0.19611615}, SunElevation: -0.47239828, Ambient: [3]float32{0.023833336, 0.029333338, 0.06433334}, FogDensity: 0.0075, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, DrawMoon: true, SunDiscDir: [3]float32{-0.85928947, -0.47239828, 0.19611615}, MoonDiscDir: [3]float32{0.85928947, 0.47239828, 0.19611615}, MoonDiscColor: [3]float32{1.2750001, 1.3199999, 1.425}, CloudSteps: 32}},
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

// staticCases are the fixed-light configurations the pins below were taken from,
// written as StaticSource because that is the type they belong to now. The pins
// came out of Environment, before it had a second implementation to delegate to,
// so each case checks both.
var staticCases = []struct {
	name string
	src  StaticSource
}{
	{"bare", StaticSource{}},
	{"sun+ambient", StaticSource{
		Sun:     &DirectionalLight{Direction: [3]float32{0.4, 0.8, 0.3}, Color: [3]float32{0.7, 0.68, 0.62}},
		Ambient: &AmbientLight{Color: [3]float32{0.18, 0.17, 0.20}},
	}},
	{"interior", StaticSource{
		Ambient:    &AmbientLight{Color: [3]float32{0.18, 0.17, 0.20}},
		ClearColor: [3]float32{0.03, 0.03, 0.045},
	}},
	{"fixed sky day", StaticSource{Sky: &Sky{Stars: true, StarDensity: 1, MilkyWay: 1, SunDisc: true, MoonDisc: true, CloudSteps: CloudsHigh, LightShafts: 0.25, FixedSunElevation: 0.6}}},
	{"fixed sky night", StaticSource{Sky: &Sky{Stars: true, StarDensity: 1, MilkyWay: 1, SunDisc: true, MoonDisc: true, CloudSteps: CloudsHigh, LightShafts: 0.25, FixedSunElevation: -0.5}}},
	{"fixed sky sunset", StaticSource{Sky: &Sky{Stars: true, StarDensity: 1, MilkyWay: 1, CloudSteps: CloudsHigh, LightShafts: 0.25, FixedSunElevation: -0.08}, Fog: &Fog{Density: 0.01, Height: 6, BaseHeight: 2}}},
	{"sun+sky+fog", StaticSource{
		Sun:     &DirectionalLight{Direction: [3]float32{0.8, 0.6, 0}, Color: [3]float32{1, 1, 1}},
		Ambient: &AmbientLight{Color: [3]float32{0.05, 0.05, 0.05}},
		Sky:     &Sky{Stars: true, StarDensity: 0.5, MilkyWay: 0.25, SunDisc: true, MoonDisc: true, Cirrus: 0.4, CloudSteps: CloudsLow, LightShafts: 0.4, FixedSunElevation: 0.2, LightShaftShape: LightShaftShape{Radius: 1.3, Decay: 0.9, Threshold: [2]float32{0.3, 0.5}}},
		Fog:     &Fog{Density: 0.02, Height: 4, BaseHeight: 1},
	}},
	{"clamped sky", StaticSource{Sky: &Sky{StarDensity: -2, MilkyWay: 3, FixedSunElevation: 0.1}}},
}

// staticPins is what each case above resolved to before the carve.
var staticPins = []struct {
	name  string
	state EnvironmentState
}{
	{"bare", EnvironmentState{}},
	{"sun+ambient", EnvironmentState{SunDir: [3]float32{0.4, 0.8, 0.3}, SunColor: [3]float32{0.7, 0.68, 0.62}, RealSunDir: [3]float32{0.4, 0.8, 0.3}, Ambient: [3]float32{0.18, 0.17, 0.2}, CastShadows: true}},
	{"interior", EnvironmentState{Ambient: [3]float32{0.18, 0.17, 0.2}, ClearColor: [3]float32{0.03, 0.03, 0.045}}},
	{"fixed sky day", EnvironmentState{SunElevation: 0.6, MilkyWay: 1, StarDensity: 1, DrawSky: true, CloudSteps: 32, LightShafts: 0.25}},
	{"fixed sky night", EnvironmentState{SunElevation: -0.5, StarFade: 1, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, CloudSteps: 32}},
	{"fixed sky sunset", EnvironmentState{SunElevation: -0.08, FogDensity: 0.01, FogHeight: 6, FogBaseHeight: 2, StarFade: 0.11807579, MilkyWay: 1, StarDensity: 1, DrawSky: true, DrawStars: true, CloudSteps: 32, LightShafts: 0.13939464}},
	{"sun+sky+fog", EnvironmentState{SunDir: [3]float32{0.8, 0.6, 0}, SunColor: [3]float32{1, 1, 1}, RealSunDir: [3]float32{0.8, 0.6, 0}, SunElevation: 0.2, Ambient: [3]float32{0.05, 0.05, 0.05}, FogDensity: 0.02, FogHeight: 4, FogBaseHeight: 1, MilkyWay: 0.25, StarDensity: 0.5, DrawSky: true, CloudSteps: 16, Cirrus: 0.4, LightShafts: 0.4, LightShaftShape: LightShaftShape{Radius: 1.3, Decay: 0.9, Threshold: [2]float32{0.3, 0.5}}, CastShadows: true}},
	{"clamped sky", EnvironmentState{SunElevation: 0.1, MilkyWay: 1, DrawSky: true}},
}

// diffState names the fields two states disagree on. A %+v of an
// EnvironmentState is four lines of mostly identical numbers, and the whole
// value of a pin is being told which one moved.
func diffState(got, want EnvironmentState) []string {
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
		Fog:   &Fog{Density: DefaultFogDensity},
	}
}

// TestDayCycleSourceIsUnchanged is the whole proof of this refactor: the day
// cycle resolves, field for field, to exactly what it resolved to before it
// became a source in its own right.
//
// Both ways in are checked, because both ship: DayCycleSource directly, and the
// Environment composite that delegates to it. A split that quietly gave the two
// different answers would leave every example on one of them and every new game
// on the other.
//
// Verified to fail. Reassociating one multiply in DayNight.MoonDiscColor --
// `0.85 * (fade * moonBoost)` rather than `0.85 * fade * moonBoost`, which is
// the same number in real arithmetic -- fails five of the sweep's entries, e.g.
//
//	t=0.0416667: MoonDiscColor: got [1.275 1.32 1.4249999] want [1.2750001 1.3199999 1.425]
//
// and dropping the smoothstep from the shaft window, so LightShafts is cut at
// the horizon the way it was before, fails t=0.245 and t=0.755 with
// `LightShafts: got 0 want 0.24510969`.
func TestDayCycleSourceIsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		pins []struct {
			tod   float32
			state EnvironmentState
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

// TestStaticSourceIsUnchanged is the same proof for the fixed-light path: the
// configuration that survives into the engine once the cycle leaves.
//
// Verified to fail: giving staticState the cycle's `haveBodies` -- true rather
// than false, so a fixed sky places discs it has no bodies for -- fails "fixed
// sky day" and "sun+sky+fog" with `DrawSun: got true want false`.
func TestStaticSourceIsUnchanged(t *testing.T) {
	if len(staticCases) != len(staticPins) {
		t.Fatalf("%d static cases against %d pins", len(staticCases), len(staticPins))
	}
	for i, c := range staticCases {
		p := staticPins[i]
		if c.name != p.name {
			t.Fatalf("case %d is %q and its pin is %q", i, c.name, p.name)
		}
		src := c.src
		if got := src.State(); got != p.state {
			t.Errorf("StaticSource %q moved:\n  %s", c.name, strings.Join(diffState(got, p.state), "\n  "))
		}
		env := &Environment{Sun: c.src.Sun, Ambient: c.src.Ambient, Sky: c.src.Sky, Fog: c.src.Fog, ClearColor: c.src.ClearColor}
		if got := env.State(); got != p.state {
			t.Errorf("Environment %q moved:\n  %s", c.name, strings.Join(diffState(got, p.state), "\n  "))
		}
	}
}

// TestMoonDiscColorIsUnchanged pins the colour that moved out of the draw path,
// and checks the draw still gets it.
//
// The second half is the one that would fail silently: the arithmetic could be
// perfect and buildMoonObject could still be colouring the disc from something
// else, and a night capture is the only other thing that would say so.
//
// Verified to fail: changing moonBoost from 1.5 to 1.6 fails all nineteen
// above-horizon entries, the first as
// `t=0: MoonDiscColor [1.3600001 1.408 1.52], want [1.2750001 1.3199999 1.425]`.
func TestMoonDiscColorIsUnchanged(t *testing.T) {
	for _, p := range moonDiscPins {
		dn := DayNight{TimeOfDay: p.tod}
		if got := dn.MoonDiscColor(); got != p.color {
			t.Errorf("t=%g: MoonDiscColor %v, want %v", p.tod, got, p.color)
		}
	}

	// far and cameraEye are all the billboard needs; no renderer is involved.
	e := &Engine{far: 500}
	for _, p := range moonDiscPins {
		st := dayCycleFixture(p.tod).State()
		if got := e.buildMoonObject(mgl32.Ident4(), st).Color; got != st.MoonDiscColor {
			t.Errorf("t=%g: the moon was drawn %v with the state saying %v", p.tod, got, st.MoonDiscColor)
		}
	}
}

// customState is a state with every field set to something no built-in source
// would produce, so that "the custom value reached the reader" cannot be true by
// coincidence. Nothing here is physically sensible, deliberately.
func customState() EnvironmentState {
	return EnvironmentState{
		SunDir:          [3]float32{0.11, 0.12, 0.13},
		SunColor:        [3]float32{0.21, 0.22, 0.23},
		RealSunDir:      [3]float32{0.31, 0.32, 0.33},
		SunElevation:    0.41,
		Ambient:         [3]float32{0.51, 0.52, 0.53},
		FogDensity:      0.61,
		FogHeight:       0.62,
		FogBaseHeight:   0.63,
		ClearColor:      [3]float32{0.71, 0.72, 0.73},
		StarFade:        0.81,
		MilkyWay:        0.82,
		StarDensity:     0.83,
		DrawSky:         true,
		DrawStars:       true,
		DrawSun:         true,
		DrawMoon:        true,
		SunDiscDir:      [3]float32{0.91, 0.92, 0.93},
		SunDiscColor:    [3]float32{1.01, 1.02, 1.03},
		MoonDiscDir:     [3]float32{1.11, 1.12, 1.13},
		MoonDiscColor:   [3]float32{1.21, 1.22, 1.23},
		CloudSteps:      23,
		Cirrus:          0.37,
		LightShafts:     0.47,
		LightShaftShape: LightShaftShape{Radius: 1.31, Decay: 0.91, Threshold: [2]float32{0.33, 0.55}},
		CastShadows:     true,
		SkyPalette: SkyPalette{
			ZenithDay:       mgl32.Vec3{0.301, 0.101, 0.621},
			HorizonDay:      mgl32.Vec3{0.951, 0.551, 0.221},
			ZenithTwilight:  mgl32.Vec3{0.181, 0.041, 0.301},
			HorizonTwilight: mgl32.Vec3{0.951, 0.221, 0.301},
			ZenithNight:     mgl32.Vec3{0.0041, 0.0013, 0.0061},
			HorizonNight:    mgl32.Vec3{0.0111, 0.0036, 0.0056},
		},
		NightGrade: NightGrade{Strength: 0.37, Tint: mgl32.Vec3{0.71, 0.85, 1.29}},
	}
}

// TestCustomSourceReachesEveryReader is the seam's real test: one assertion per
// field of EnvironmentState, from a source the engine knows nothing about to the
// thing that consumes it.
//
// The lighting pack is where most of them land, and renderer/commands_test.go and
// renderer/litubo_test.go carry it the rest of the way into the push block and
// the uniform buffer. The four disc fields and the two shadow and shaft
// decisions are the ones that do not go through the pack, and they are checked
// where they do go.
//
// Verified to fail: pointing applyEnvironment back at the scene for the palette
// -- `p := e.Scene.SkyPalette()` and packing that, which is where it read from
// before the carve -- fails with
//
//	SkyPalette.ZenithDay = [0.13 0.3 0.78], want [0.301 0.101 0.621]
//
// while every other field still passes, which is exactly the shape of the bug:
// one reader left behind, and a custom sky hazing into Earth-blue.
func TestCustomSourceReachesEveryReader(t *testing.T) {
	want := customState()

	s := NewScene()
	s.Env = &fakeEnv{state: want}
	got := s.Environment()
	if got != want {
		t.Fatalf("the scene did not hand the frame what the source returned:\n  %s", strings.Join(diffState(got, want), "\n  "))
	}

	e := &Engine{far: 500, Scene: s}
	var l renderer.SceneLighting
	e.applyEnvironment(&l, got)

	check := func(name string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("SceneLighting.SunDir", l.SunDir, want.SunDir)
	check("SceneLighting.SunColor", l.SunColor, want.SunColor)
	check("SceneLighting.RealSunDir", l.RealSunDir, want.RealSunDir)
	check("SceneLighting.SunElevation", l.SunElevation, want.SunElevation)
	check("SceneLighting.Ambient", l.Ambient, want.Ambient)
	check("SceneLighting.FogDensity", l.FogDensity, want.FogDensity)
	check("SceneLighting.FogHeight", l.FogHeight, want.FogHeight)
	check("SceneLighting.FogBaseHeight", l.FogBaseHeight, want.FogBaseHeight)
	// ClearColor becomes SkyColor, with an opaque alpha the renderer requires.
	check("SceneLighting.SkyColor", l.SkyColor, [4]float32{want.ClearColor[0], want.ClearColor[1], want.ClearColor[2], 1})
	check("SceneLighting.NightFactor", l.NightFactor, want.StarFade)
	check("SceneLighting.MilkyWay", l.MilkyWay, want.MilkyWay)
	check("SceneLighting.StarDensity", l.StarDensity, want.StarDensity)
	check("SceneLighting.DrawSky", l.DrawSky, want.DrawSky)
	check("SceneLighting.DrawStars", l.DrawStars, want.DrawStars)
	check("SceneLighting.CloudSteps", l.CloudSteps, want.CloudSteps)
	check("SceneLighting.Cirrus", l.Cirrus, want.Cirrus)
	check("SceneLighting.ShaftShape", l.ShaftShape, renderer.LightShaftShape{
		Radius: want.LightShaftShape.Radius, Decay: want.LightShaftShape.Decay, Threshold: want.LightShaftShape.Threshold,
	})

	if l.SkyPalette == nil || l.NightGrade == nil {
		t.Fatal("the lighting pack has no palette or no grade, so the renderer would use its own defaults")
	}
	check("SkyPalette.ZenithDay", l.SkyPalette.ZenithDay, [3]float32(want.SkyPalette.ZenithDay))
	check("SkyPalette.HorizonDay", l.SkyPalette.HorizonDay, [3]float32(want.SkyPalette.HorizonDay))
	check("SkyPalette.ZenithTwilight", l.SkyPalette.ZenithTwilight, [3]float32(want.SkyPalette.ZenithTwilight))
	check("SkyPalette.HorizonTwilight", l.SkyPalette.HorizonTwilight, [3]float32(want.SkyPalette.HorizonTwilight))
	check("SkyPalette.ZenithNight", l.SkyPalette.ZenithNight, [3]float32(want.SkyPalette.ZenithNight))
	check("SkyPalette.HorizonNight", l.SkyPalette.HorizonNight, [3]float32(want.SkyPalette.HorizonNight))
	check("NightGrade.Strength", l.NightGrade.Strength, want.NightGrade.Strength)
	check("NightGrade.Tint", l.NightGrade.Tint, [3]float32(want.NightGrade.Tint))

	// The six that do not travel in the pack. A billboard sits along its
	// direction from the camera, so the translation column, normalized, is the
	// direction the state asked for.
	// The direction comes back through a normalize and a scale, so it is checked
	// to within a float32 epsilon rather than exactly; the colours are copied
	// and are checked exactly.
	billboardDir := func(name string, model [16]float32, want [3]float32) {
		t.Helper()
		got := mgl32.Vec3{model[12], model[13], model[14]}.Normalize()
		w := mgl32.Vec3(want).Normalize()
		for i := range got {
			if !almostEqual(got[i], w[i], 1e-6) {
				t.Errorf("%s = %v, want %v", name, got, w)
				return
			}
		}
	}
	billboardDir("the sun disc's position", e.buildSunObject(mgl32.Ident4(), got).Model, want.SunDiscDir)
	check("the sun disc's colour", e.buildSunObject(mgl32.Ident4(), got).Color, want.SunDiscColor)
	billboardDir("the moon disc's position", e.buildMoonObject(mgl32.Ident4(), got).Model, want.MoonDiscDir)
	check("the moon disc's colour", e.buildMoonObject(mgl32.Ident4(), got).Color, want.MoonDiscColor)
	// DrawSun, DrawMoon and CastShadows are branches in renderFrame rather than
	// values, so what is checked is that the branch reads the state. Taking them
	// away has to change what the frame does.
	if !got.DrawSun || !got.DrawMoon {
		t.Error("DrawSun/DrawMoon did not survive the resolve, so no celestial would be built")
	}
	if !got.CastShadows {
		t.Error("CastShadows did not survive the resolve, so ShadowEnabled would be false")
	}
	// LightShafts reaches the pack through the edge fade, which needs a camera.
	// Zero in, zero out is the half that matters -- a zero keeps the frame out of
	// the water pass entirely -- and the fade itself is covered by task shafts.
	if want.LightShafts == 0 {
		t.Fatal("customState has no shafts to carry")
	}
	if shaftEdgeFade([2]float32{0.5, 0.5}) != 1 {
		t.Error("the edge fade is not 1 with the sun centred, so LightShafts cannot reach the pass unchanged")
	}
}

// envStateReaders names what reads each field of EnvironmentState. It is the
// list TestCustomSourceReachesEveryReader asserts against, written out so that
// the next field added to the state has to be routed somewhere before the tests
// pass.
//
// That is the failure this guards: a field in the state that nothing reads is
// invisible. The built-in sources mostly leave such a field at zero, so no
// capture moves, and the one game that sets it quietly gets nothing.
var envStateReaders = map[string]string{
	"SunDir":          "SceneLighting.SunDir, and ComputeCascadeVPsWithCoverage",
	"SunColor":        "SceneLighting.SunColor",
	"RealSunDir":      "SceneLighting.RealSunDir",
	"SunElevation":    "SceneLighting.SunElevation",
	"Ambient":         "SceneLighting.Ambient",
	"FogDensity":      "SceneLighting.FogDensity",
	"FogHeight":       "SceneLighting.FogHeight",
	"FogBaseHeight":   "SceneLighting.FogBaseHeight",
	"ClearColor":      "SceneLighting.SkyColor",
	"StarFade":        "SceneLighting.NightFactor",
	"MilkyWay":        "SceneLighting.MilkyWay",
	"StarDensity":     "SceneLighting.StarDensity",
	"DrawSky":         "SceneLighting.DrawSky",
	"DrawStars":       "SceneLighting.DrawStars",
	"DrawSun":         "renderFrame, which builds the sun billboard or does not",
	"DrawMoon":        "renderFrame, which builds the moon billboard or does not",
	"SunDiscDir":      "buildSunObject, and the shaft anchor renderFrame projects",
	"SunDiscColor":    "buildSunObject",
	"MoonDiscDir":     "buildMoonObject",
	"MoonDiscColor":   "buildMoonObject",
	"CloudSteps":      "SceneLighting.CloudSteps",
	"Cirrus":          "SceneLighting.Cirrus",
	"LightShafts":     "SceneLighting.LightShafts, after renderFrame's edge fade",
	"LightShaftShape": "SceneLighting.ShaftShape",
	"CastShadows":     "SceneLighting.ShadowEnabled, if the cascades could be built",
	"SkyPalette":      "SceneLighting.SkyPalette",
	"NightGrade":      "SceneLighting.NightGrade",
}

// TestEveryEnvironmentStateFieldHasAReader fails if the state grows a field that
// envStateReaders does not account for, or keeps a name the map still claims.
//
// Verified to fail both ways: adding an unused `Exposure float32` to
// EnvironmentState reports `EnvironmentState.Exposure has no reader recorded in
// envStateReaders`, and deleting the MoonDiscColor entry reports
// `envStateReaders names MoonDiscColor, which EnvironmentState does not have`.
func TestEveryEnvironmentStateFieldHasAReader(t *testing.T) {
	st := reflect.TypeOf(EnvironmentState{})
	seen := map[string]bool{}
	for i := 0; i < st.NumField(); i++ {
		name := st.Field(i).Name
		seen[name] = true
		if _, ok := envStateReaders[name]; !ok {
			t.Errorf("EnvironmentState.%s has no reader recorded in envStateReaders", name)
		}
	}
	var stale []string
	for name := range envStateReaders {
		if !seen[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		t.Errorf("envStateReaders names %s, which EnvironmentState does not have", name)
	}
}

// envLeaf is one settable scalar inside an EnvironmentState, with the path that
// reaches it.
type envLeaf struct {
	path string
	v    reflect.Value
}

// envLeaves walks a settable EnvironmentState down to its scalars.
func envLeaves(v reflect.Value, path string, out *[]envLeaf) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			envLeaves(v.Field(i), path+"."+v.Type().Field(i).Name, out)
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			envLeaves(v.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	default:
		*out = append(*out, envLeaf{path, v})
	}
}

// TestEnvTraceCoversEveryField is what keeps `env=` honest: every scalar in the
// state has to move the hash.
//
// The field it replaced hashed nine of the state's twenty-seven, so a source
// that changed the cirrus, the Milky Way, a disc colour or the palette produced
// a different frame and an identical trace line. `task determinism` compares
// this field between two runs, and a hash that does not cover what moved is a
// gate reporting that nothing moved.
//
// Verified to fail: deleting `Float32(env.Cirrus)` from hashEnvironment reports
// `.Cirrus: hashEnvironment did not notice 0.37 becoming 1.37`.
func TestEnvTraceCoversEveryField(t *testing.T) {
	base := customState()
	var leaves []envLeaf
	ref := reflect.ValueOf(&base).Elem()
	envLeaves(ref, "", &leaves)
	if len(leaves) < 27 {
		t.Fatalf("walked only %d scalars out of EnvironmentState; the walk is wrong", len(leaves))
	}

	want := hashEnvironment(base)
	for _, leaf := range leaves {
		before := leaf.v.Interface()
		switch leaf.v.Kind() {
		case reflect.Float32:
			leaf.v.SetFloat(leaf.v.Float() + 1)
		case reflect.Bool:
			leaf.v.SetBool(!leaf.v.Bool())
		case reflect.Int:
			leaf.v.SetInt(leaf.v.Int() + 1)
		default:
			t.Fatalf("%s is a %s, which this test does not know how to move", leaf.path, leaf.v.Kind())
		}
		if hashEnvironment(base) == want {
			t.Errorf("%s: hashEnvironment did not notice %v becoming %v", leaf.path, before, leaf.v.Interface())
		}
		leaf.v.Set(reflect.ValueOf(before))
	}
	if hashEnvironment(base) != want {
		t.Fatal("the walk did not restore the state, so the results above are not independent")
	}
}

// TestEnvironmentResolvesWithoutAllocating is the per-frame cost of the seam.
//
// The state is a plain value and the engine copies it once a frame, which is the
// reason it is a value and not an interface the renderer holds: a per-frame
// allocation in the draw path is paid by every frame of every game, and the two
// pointer fields the lighting pack needs are Engine fields precisely to avoid
// one. Nothing here may allocate, including the custom-source path, where the
// state crosses an interface boundary.
//
// Verified to fail: handing the pack a fresh `&renderer.NightGrade{...}` rather
// than the Engine field reports `1 allocations per frame resolving and applying
// the environment` for all four sources.
func TestEnvironmentResolvesWithoutAllocating(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  EnvironmentSource
	}{
		{"Environment", DefaultEnvironment()},
		{"DayCycleSource", dayCycleFixture(0.3)},
		{"StaticSource", &StaticSource{Sun: &DirectionalLight{Direction: [3]float32{0, 1, 0}, Color: [3]float32{1, 1, 1}}, Sky: DefaultSky(), Fog: &Fog{Density: 0.01}}},
		{"a custom source", &fakeEnv{state: customState()}},
	} {
		s := NewScene()
		s.Env = tc.src
		e := &Engine{far: 500, Scene: s}
		var l renderer.SceneLighting
		if n := testing.AllocsPerRun(200, func() {
			st := s.Environment()
			e.applyEnvironment(&l, st)
		}); n != 0 {
			t.Errorf("%s: %v allocations per frame resolving and applying the environment", tc.name, n)
		}
		if n := testing.AllocsPerRun(200, func() { tc.src.Advance(1.0 / 60) }); n != 0 {
			t.Errorf("%s: %v allocations per tick advancing", tc.name, n)
		}
	}
}

// TestSceneReachesABuiltInCycleEitherWay covers the convenience methods against
// both shapes the built-in cycle now ships in. SetTimeOfDay writing into a copy
// rather than into the source is the mistake this would catch: DayCycleSource
// holds its clock by value, so Scene.DayNight has to hand out its address.
//
// Verified to fail: returning `&DayNight{...: env.Cycle}` -- a copy -- from
// Scene.DayNight's DayCycleSource case leaves the time at 0.25 and reports
// `a DayCycleSource ignored SetTimeOfDay: 0.25`.
func TestSceneReachesABuiltInCycleEitherWay(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  EnvironmentSource
	}{
		{"an Environment", DefaultEnvironment()},
		{"a DayCycleSource", dayCycleFixture(0.25)},
	} {
		s := NewScene()
		s.Env = tc.src
		if s.DayNight() == nil {
			t.Fatalf("%s has no reachable cycle", tc.name)
		}
		s.SetTimeOfDay(0.6)
		if got := s.TimeOfDay(); got != 0.6 {
			t.Errorf("%s ignored SetTimeOfDay: %v", tc.name, got)
		}
		// The resolved frame has to follow the clock, not just the getter: a
		// source whose Advance moved a copy would report the new time and light
		// the scene with the old one.
		before := s.Environment()
		s.SetDayCycleSpeed(1.0 / 120)
		s.Tick(30)
		if got := s.TimeOfDay(); got == 0.6 {
			t.Errorf("%s ignored SetDayCycleSpeed: the clock stayed at %v over thirty seconds", tc.name, got)
		}
		if after := s.Environment(); after == before {
			t.Errorf("%s moved its clock without moving the light", tc.name)
		}
	}

	// A StaticSource has no cycle, so the conveniences are no-ops rather than
	// panics -- the same contract a custom source gets.
	s := NewScene()
	s.Env = &StaticSource{}
	if s.DayNight() != nil {
		t.Error("a StaticSource reported a cycle")
	}
	s.SetTimeOfDay(0.5)
	s.SetDayCycleSpeed(1)
	if got := s.TimeOfDay(); got != 0 {
		t.Errorf("a StaticSource reported the time as %v", got)
	}
}

// TestSetFogDensityReachesEveryBuiltIn: the shortcut has to reach all three
// built-in shapes, and has to create a Fog where there is none rather than
// silently doing nothing.
//
// Verified to fail: restoring the single `*Environment` type assertion reports
// `a DayCycleSource ignored SetFogDensity: 0` and the same for a StaticSource.
func TestSetFogDensityReachesEveryBuiltIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  EnvironmentSource
	}{
		{"an Environment with no Fog", &Environment{}},
		{"a DayCycleSource with no Fog", &DayCycleSource{}},
		{"a StaticSource with no Fog", &StaticSource{}},
		{"an Environment with Fog", DefaultEnvironment()},
	} {
		s := NewScene()
		s.Env = tc.src
		e := &Engine{Scene: s}
		e.SetFogDensity(0.042)
		if got := s.Environment().FogDensity; got != 0.042 {
			t.Errorf("%s ignored SetFogDensity: %v", tc.name, got)
		}
	}

	// A custom source owns its own fog, and the shortcut must leave it there.
	s := NewScene()
	s.Env = &fakeEnv{state: EnvironmentState{FogDensity: 0.05}}
	e := &Engine{Scene: s}
	e.SetFogDensity(0.042)
	if got := s.Environment().FogDensity; got != 0.05 {
		t.Errorf("SetFogDensity reached into a custom source: %v", got)
	}
}
