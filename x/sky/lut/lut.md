---
id: x-sky-lut
title: A cheap sky from a lookup table
summary: >
  Fill the engine's sky slot with a dome whose whole per-pixel cost is a texture
  fetch: a table baked on the CPU at startup, indexed by view elevation, sun
  elevation and proximity to the sun, with a four-key day driving it.
capability: environment
status: experimental
since: v0.1.0
api:
  - lut.Shaders
  - lut.Fill
  - lut.ShaderTextureSlot
  - lut.Options
  - lut.DefaultOptions
  - lut.Key
  - lut.DefaultKeys
  - lut.Bake
  - lut.Sky
  - lut.New
  - lut.Sky.Destroy
  - lut.Sky.Options
  - lut.Sky.TimeOfDay
  - lut.Sky.SetTimeOfDay
  - lut.Sky.Speed
  - lut.Sky.SetSpeed
  - lut.Sky.Advance
  - lut.Sky.State
example: examples/07-terrain
run: task example:07-terrain -- -sky lut
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-10-03 # the table moved to renderer.CreateTextureRGBA16F (#178): the sqrt transfer, the 3.0 ceiling and Bake's refusal are gone, Bake returns []uint16 of half-float bits (an api BREAK), and the error is 0.049 percent at every level instead of 2 to 13; new package before that, ADR 0012 step 5, with its own gate (task xskylut) and a recorded break above every unit check
---

# x/sky/lut

A second sky, on the same seam as [`x/sky`](../sky.md), for a target that cannot
afford the first one. The dome is a texture lookup and there is nothing else in
it: no cloud march, no star field, no sun or moon billboard, no light shafts.

It exists for two reasons, and the first one is not the cheapness. ADR 0012's
claim is that the look left the engine and the engine kept a *slot*; a slot with
one filling is a slot by assertion. This is the second filling, and it is what
makes the claim checkable: `renderer.ShaderSet.SkyFrag` takes a dome that shares
no Go identifier, no shader and no table with `x/sky`, and the engine's pass
order, depth state, push-constant packing and alpha contract carry it unchanged.

```go
import (
	glyph "github.com/derekmwright/glyphengine"
	skylut "github.com/derekmwright/glyphengine/x/sky/lut"
)

func main() {
	e, err := glyph.New(&game{}, glyph.WithShaders(skylut.Shaders()))
	...
}

func (g *game) Init(e *glyph.Engine) error {
	opts := skylut.DefaultOptions()
	opts.Speed = 1.0 / 120 // a two-minute day; 0 freezes it
	sky, err := skylut.New(e.Renderer(), opts)
	if err != nil {
		return err
	}
	e.Scene.Env = sky
	return nil
}
```

## Two halves and a table

`x/sky`'s page records that a package filling the sky slot ships **two halves
that have to arrive together**, the Go seam and the SPIR-V, and that neither
errors without the other. This has a third: the table.

| Missing | What you see |
|---|---|
| `glyph.WithShaders(skylut.Shaders())` | No sky. The source asks for a dome, the renderer has no pipeline, the frame is the clear colour with the right light on it. Nothing errors. |
| `e.Scene.Env = sky` | A dome pipeline built and never used. |
| The table | Not possible: `New` takes the renderer and binds it, so there is no way to hold the source without it. An unbound slot samples the renderer's white fallback, which would be a white sky — and white is 1.0, which the dome now takes at face value because there is no transfer to flatten it. |

`New` is where the table is baked, uploaded and bound, which is why it needs the
renderer and why it is called from `Init` rather than from `main`. `Destroy`
releases the slot and then the texture.

## The table

Three axes, 64 by 32 by 32 texels, 512 KB as `R16G16B16A16_SFLOAT`, laid out 1024
by 64 with proximity across inside a sun-elevation slice, the slices across after
it, and view elevation down.

| Axis | Texels | Spacing |
|---|---|---|
| View elevation, -1 to 1 | 64 | Signed square: 0.001 of elevation at the horizon, 0.06 near the zenith |
| Sun elevation, -1 to 1 | 32 | Signed square: 0.004 at the horizon, 0.06 at the poles |
| `dot(dir, sunDir)`, -1 to 1 | 32 | Chord: 3.7 degrees per texel near the sun |

**The third axis is why the table is the model rather than an approximation of
it.** The dome reads the view direction only through its `y` and through
`dot(dir, sunDir)`, and the sun only through its `y` — the gradient's
`pow(smoothstep(-0.08, 0.75, elevation), 0.65)`, the palette's two blends, the
halo's `pow(prox, 8)`, the wash's `pow(prox, 1.6) * exp(-3.5*abs(dir.y))` and the
below-horizon ground fade are all functions of those three scalars and nothing
else. Tabulating them tabulates the function. An azimuth-difference axis would
have held the same information in a worse place: equal steps in azimuth are not
equal angular steps away from the sun, which is what a `pow(prox, 8)` halo cares
about.

The spacings are not uniform because the sky is not. Most of the gradient's
movement is in the first tenth of the climb from the horizon, and the twilight
lobe is a Gaussian 0.115 wide centred on the horizon — on a linear sun axis it
would get two slices and dusk would band visibly as the clock moved.

The shader filters the first two axes with the sampler and the third itself: two
`textureLod` fetches of adjacent sun slices and a `mix`. It has to do that one
itself, because `u` already carries proximity and letting the sampler interpolate
along `u` between slices would blend the far side of one sky with the near side of
the next. Every axis is inset half a texel so the filter never crosses a slice
boundary, which is the whole of what prevents a wrong-coloured ring around the sun
at some hours and not others.

### The fetch is `textureLod`, and the reason is not the one you would guess

Level 0 is the only level a lookup has any use for, and `textureLod` says so
without depending on a derivative or on a mip chain. That is the whole of the
justification, and it is smaller than the one this page used to give.

The claim that was here — that the square-root view axis makes `dv/dy` unbounded
at the horizon, so an implicit-derivative `texture()` would pick a high mip along
the horizon line — is **false, measured**. With `texture()` in place of both
fetches the frame is pixel-identical, both with the horizon out of shot and with it
across the middle of a 640x480 frame (`lutskycheck -pitch 0`, compared with
`cmd/pngsame`). The axis is scaled so that near the horizon one screen pixel is
about one texel: at 480 pixels over a 60-degree vertical field of view the first
pixel above the horizon moves `v` by 1.05 texels, an LOD of 0.07. The chain was
reachable at the time of that measurement — `CreateDataTexture` built 11 levels
for this 1024x64 image and set `MaxLod` to the count — and simply never reached,
which made those 11 levels about 85 KB of waste on top of the table's 256.

Kept anyway, because it is free and unconditional. A coarser axis or a smaller
table would be relying on that measurement rather than on the call, which is the
kind of thing [AGENTS.md rule 12](../../../AGENTS.md#rules-that-matter) is about:
the number is recorded next to the line so the next person can re-run it.

Since the table moved to `CreateTextureRGBA16F` there is no chain to reach at all
— the upload asks for none — so what the measurement now says is that losing
those 11 levels changed nothing.

### Half-float radiance, and the eight bits it replaced

The table is `R16G16B16A16_SFLOAT` and holds **radiance**. There is no transfer in
the bake, nothing to undo in the shader, and no ceiling for a palette to exceed.

It used to be RGBA8 holding `sqrt(v/3)`, squared back in `skylut.frag`, with 3.0
compiled into both sides and `Bake` refusing any palette whose brightest texel
passed it. That was not a choice; it was the only CPU upload the engine had. It
has `CreateTextureRGBA16F` now (issue #178, and this package is that issue's
worked example), and the whole encoding went with the move.

What it cost and what it costs, measured over the whole default table against the
model evaluated at each texel's own coordinates — `TestTheTableIsTheModel`, which
logs all four lines:

| | `RGBA8`, `sqrt(v/3)` | `RGBA16F`, radiance |
|---|---|---|
| Relative, above 0.1 | 2.1 percent | **0.0488 percent** |
| Relative, above 0.01 | 6.5 percent | **0.0488 percent** |
| Relative, above 0.002 | 13.4 percent | **0.0488 percent** |
| Absolute, anywhere | 0.0105 | **0.00098** |
| Size | 256 KB, plus 85 KB of mip levels nothing read | **512 KB, no chain** |

The eight-bit figure was `2*(0.5/255)/sqrt(v/3)` — the transfer and nothing else,
unimprovable by any other curve over 255 levels. The half-float figure is
`2^-11`, half a step of a 10-bit significand, and **it is the same at every
level**, which is the whole shape of the change: the eight-bit table was worst in
absolute terms at the top of its range and worst in relative terms at the bottom,
and this one is relative everywhere. The three rows reading alike is the result,
not a copy-paste.

Three more things went with the transfer:

- **The ceiling, and `Bake`'s refusal.** The default palette's brightest texel is
  **2.504** — in red, looking straight at a sun 1.5 degrees up, where the tight
  halo, the broad wash and the twilight horizon colour all peak together — which
  used to be 84 percent of a 3.0 range. Half-float reaches 65504.
  `TestABrightPaletteNeedsNoRefusing` bakes a palette five times Earth's, peaking
  at 5.750, and holds it to the same 0.0488 percent.
- **Interpolation in an encoded domain.** Both blends — the sampler's inside a
  texel pair and the shader's `mix` across two sun slices — bowed toward the
  darker of two neighbours, consistently, because the hardware cannot filter in a
  space it does not know about. They are in linear radiance now.
- **`Bake`'s signature.** It returns `[]uint16` of half-float bits where it
  returned `[]byte`. That is an **api break** on an experimental package, recorded
  here and in `verified`; `renderer.Float16` and `renderer.Float16Value` are the
  transfer in both directions.

What is left is the table's own resolution, which is a different thing from its
precision: 64 by 32 by 32 texels of a smooth function, interpolated. That has not
changed and this page's axis table is where it is described.

The cost of the move is **256 KB** of video memory, against a frame this package
exists to make cheaper. See [Cost](#cost), which is unchanged — the fetch is the
same fetch, minus a multiply.

## The day: four keys

The clock is a `TimeOfDay` in `[0,1)` and a `Speed` in cycles per simulation
second, the same shape `x/sky`'s `DayNight` has, advanced by `Scene.Tick` on the
fixed tick so a paused scene has a stationary sun. What it drives is a list of
`Key`s, interpolated and wrapped through midnight.

`DefaultKeys` is **`x/sky`'s cycle sampled at four hours**, so the two skies are
two skies of the same world:

| Time | Sun | `SunColor` at full strength | `Ambient` |
|---|---|---|---|
| 0.00 midnight | straight down | 1.0, 0.6, 0.3 (unread; the fade is zero) | 0.010, 0.013, 0.030 |
| 0.25 sunrise | on the horizon | 1.0, 0.6, 0.3 | 0.140, 0.115, 0.105 |
| 0.50 noon | overhead | 1.0, 1.0, 0.95 | 0.250, 0.250, 0.300 |
| 0.75 sunset | on the horizon | 1.0, 0.5, 0.2 | 0.140, 0.105, 0.100 |

The sun directions are `x/sky`'s orbit expression transcribed, so they are
bit-identical at the keys; the colours are its `sunColorKeyframes` and
`ambientKeyframes` read at the same four times.
`TestDefaultKeysAreXSkysCycle` compares all three against `x/sky` itself, and it
is the only place in the package that mentions `x/sky` — a test, so nothing here
depends on it at run time.

`Key.SunColor` is the sun's colour **at full strength**, and the fade that takes
it out is a function of elevation (`smoothstep(-0.14, 0.06, sunY)`, `x/sky`'s
`SunIntensity` re-derived) rather than of the clock. That matters: with the fade
keyed instead, interpolating from a black midnight toward a lit sunrise lights the
scene from a sun 40 degrees below the horizon for a sixth of the cycle.
`TestTheSunGoesOutWhenItSets` is what holds it.

What four keys cost, measured over 2000 times of day: the largest disagreement
with `x/sky`'s sun elevation is **0.0689**, four degrees of altitude, at time
0.191 and its three mirrors — the quarter points of the four segments, where a
chord sags furthest from its arc. It is **not** a disagreement about sunrise and
sunset, because the keys sit on the horizon crossings and both skies put the sun
at elevation zero at exactly 0.25 and 0.75.

Dawn and dusk are shorter-shaped than `x/sky`'s. Its ambient table has twelve
keys, with extra ones two or three hundredths of a cycle either side of sunrise
and sunset put there deliberately to give dusk a duration; four keys cannot have
that, so the blue hour arrives earlier and more gradually. That is the look this
sky has. A game that wants the other one hands `Options.Keys` a longer list, or
uses `x/sky`.

## What it lacks

| | |
|---|---|
| Clouds | `CloudsFrag` stays nil, so the engine builds no cloud pipeline and records no march. The half-resolution target is still allocated — 3.7 MB at 1280x720 — because the engine allocates it unconditionally so that a dome supplied without a cloud stage still has a primed target to composite. No GPU work. |
| Stars and the Milky Way | `StarsFrag` stays nil and the state leaves `DrawStars` false. |
| The sun and moon discs | The state leaves `DrawSun` and `DrawMoon` false, so the engine places no billboards. The sun is visible as the scattering halo the table carries around its direction. |
| Light shafts | They radiate from the sun billboard, so there is nothing for them to radiate from. `LightShafts` is zero. |
| The moon | There is no second body and no handover: the directional light fades out with the sun, and night is ambient plus whatever lamps the game placed. `x/sky`'s handover needs a second intensity ramp and a window placed so no shadow flips direction in a frame — three tuned things for a light a twentieth of the sun's. |
| `Scene.SetSkyPalette` reaching the dome | The palette is baked into the table. See below. |

### The palette is baked, and that is the real cost

`Scene.SetSkyPalette` moves the fog distant geometry fades into, the water's
reflection and every lit surface's night grade — and it does **not** move this
dome, because the dome is a texture that was baked before the scene said
anything. A game with its own palette sets it in both places:

```go
pal := glyph.SkyPalette{ /* ... */ }
e.Scene.SetSkyPalette(pal) // the fog, the water, the night grade
opts := skylut.DefaultOptions()
opts.Palette = pal // the dome
```

Set only one and the result is the mistake
[environment.md](../../../docs/agents/environment.md#a-sky-that-is-not-earths)
describes from the other direction: a dome and a haze from different planets.
`Options.Palette`'s zero value is `glyph.DefaultSkyPalette`, which is what a new
`Scene` starts with, so a game that says nothing gets a matched pair.

This is the one thing `x/sky` does better and not merely differently, and it is
inherent: a per-frame palette means a per-frame bake.

## Where the numbers come from, and what can drift

Two different places.

**The keys are sampled** from `x/sky`, and a test compares them against it.

**The model is re-derived.** The dome lives in GLSL — the engine's
`shaders/include/atmosphere.inc` for the curves and the palette blend, and the
gradient and the below-horizon fade in `x/sky`'s `sky.frag` — and there is no way
to evaluate a shader from Go, so `bake.go` computes it again. That is a copy, and
the engine already has one of these that
[drifted once](../../../AGENTS.md#rules-that-matter): `DayNight.Twilight` against
`atmTwilight`.

Two things are done about it rather than hoping:

- `TestAtmosphereIncStillSaysWhatWeCopied` reads the engine's **own exported
  bytes** (`shaders/include`'s `embed.FS`, the export ADR 0012 added for exactly
  this kind of coupling) and fails if any of the thirteen expressions `bake.go`
  copied have been reworded. That covers everything in `atmosphere.inc`.
- `skylut.frag` `#include`s `atmosphere.inc` for the one function it calls,
  `atmSunDirFrom`, so `spirv_test.go` is a compile gate on the same file's
  signatures.

What is **not** covered: the gradient and the below-horizon fade came from
`x/sky`'s `sky.frag`, which is a sibling package this one deliberately does not
reach into, so those two expressions are quoted verbatim beside the Go that
reimplements them and nothing automatic holds them. Changing either means
changing `bake.go`.

## Seams this binds to

| Seam | Used for |
|---|---|
| `renderer.ShaderSet.SkyFrag` | The dome. `StarsFrag` and `CloudsFrag` left nil, which is what turns those draws off. |
| `glyphengine.EnvironmentSource` / `EnvironmentState` | The per-frame light, air and `DrawSky`. |
| `glyphengine.StaticSource` | Called, not repeated, for the light and the air — the same choice `x/sky`'s fixed-hour path makes, for the same reason. |
| `glyphengine.SkyPalette`, `DefaultSkyPalette` | The six endpoints the table is baked from. |
| `renderer.Renderer.CreateTextureRGBA16F`, `DestroyTexture` | The table, at `TextureOptions{Filter: FilterLinear}` — the sampler is what interpolates two of the three axes. |
| `renderer.TextureOptions`, `renderer.Float16`, `renderer.Float16Value` | The sampler choice, and the transfer in both directions: `Bake` encodes with the first and the unit tests read the table back with the second. |
| `renderer.SetShaderTexture`, `renderer.ShaderTextureSlots` | Binding it. |
| `shaders/include`'s `FS` | Reading `atmosphere.inc` back in a test, and `-I` for the compile. |
| The fixed shader layouts in [render-targets.md](../../../docs/agents/render-targets.md) | Set 1 binding 10 for the table, and the push block member order that lands `sunColor.w` and `fog.zw` where the recorder writes them. |

`ShaderTextureSlot` is **3**, the last of the renderer's four, and it is fixed
rather than an option because `skylut.frag` names its binding at compile time: a
configurable slot would mean four compiled variants of the same shader. Last
rather than first so a game allocating slots from zero — which is what
`examples/24-custom-passes` does at binding 7 — collides with this one last. A
game that needs all four cannot use this sky.

## Cost

Measured by `task xskylut`, on a scene that is nothing but sky: a camera at the
origin pitched up 0.6 radians with no geometry at all, so every pixel is a dome
fragment and the sky pass's own GPU bracket (`renderer.PassSky`) is the cost of
shading a full frame of it. 640x480, MSAA off, 300 frames with the first 60
discarded, three interleaved trials of four configurations.

Three independent runs of the whole comparison, each figure the mean of that
run's three interleaved trials, in milliseconds. RX 7900 XTX, 2026-10-03, with
the `R16G16B16A16_SFLOAT` table.

| | sky pass | cloud pass | frame |
|---|---|---|---|
| empty slot | 0.0001 · 0.0001 · 0.0001 | 0.0001 | 0.0490 · 0.0500 · 0.0494 |
| **this package** | **0.0080 · 0.0081 · 0.0081** | 0.0001 | **0.0549 · 0.0554 · 0.0555** |
| `x/sky`, dome only | 0.0098 · 0.0102 · 0.0100 | 0.0001 | 0.0563 · 0.0577 · 0.0571 |
| `x/sky`, whole | 0.0089 · 0.0090 · 0.0090 | 0.4836 – 0.4972 | 0.6162 · 0.6250 · 0.6183 |

- **Against the empty slot**: the dome adds **12.0 / 10.8 / 12.3 percent** to the
  frame. That is the worst case for it — every pixel is dome, with no geometry
  covering any of them.
- **Against `x/sky`'s dome**, pass bracket to pass bracket, one draw each: **81.6 /
  79.4 / 81.0 percent**. Read this one PAIRED — within each interleaved trial —
  because that is what the interleaving is for and because the unpaired ranges do
  overlap: 0.0077–0.0099 here against 0.0095–0.0112 there across every trial on
  record. Within a trial, with both configurations measured minutes apart at most,
  **this dome's pass is cheaper in twelve of twelve trials, by 0.0013 to 0.0028 ms
  (median 0.0019)**. That is what makes 19 percent a reading rather than noise at
  eight microseconds; a range comparison across runs would not survive the card's
  own drift, and the empty slot's frame moved 13.5 percent over those same twelve
  trials.
- **Against `x/sky` whole**: **8.9 / 8.9 / 9.0 percent** of the frame, eleven times
  cheaper. Nearly all of the difference is the cloud march at 0.484 ms, which a nil
  `CloudsFrag` does not pay at all.

### Did moving to a half-float table change the cost? Not measurably

The table was RGBA8 with a `sqrt(v/3)` transfer until issue #178. Two things
changed that could move this: the fragment lost `encoded * encoded * LUT_RANGE`
(two vec3 multiplies), and the upload stopped building a mip chain — both in this
package's favour — while the table doubled from 256 KB to 512 KB, which costs a
sampler fetch more bandwidth, against it.

**The answer is that the two are not distinguishable on this machine, and no
direction is claimed.** The eight-bit build's nine trials put the dome's sky pass
at 0.0081–0.0085 ms; this one's put it at 0.0077–0.0084. Those ranges overlap over
most of their width.

The reason to believe that is a null result rather than a measurement too crude to
see a real change is the **control**: `x/sky`'s dome-only configuration is
untouched code, measured in the same interleaved trials, and it moved too —
0.0099–0.0102 before, and 0.0095–0.0112 across every trial taken since. Whatever
moves that moves the LUT's figure as well, so a difference of the size the LUT
shows cannot be attributed to the change. Read the numbers above as "the same
cost, re-measured", not as an improvement or a regression.

Two further readings make the point sharper, and widen it. Re-running the gate
twice right after the three runs above gave LUT pass means of 0.0092 and 0.0095
against `x/sky`'s 0.0111 both times, with the empty slot's frame at 0.0525 and
0.0572 against 0.0490–0.0500 — everything up together, control included. Both
passed, at 14.0 / 8.7 percent over the empty slot, 82.6 / 85.6 percent of `x/sky`'s
dome and 9.5 / 9.9 percent of its frame.

So the ratios are **stable within a band rather than to the digit**: across all
five readings the dome share runs 79.4 to 85.6 percent and the over-empty figure
8.7 to 14.0. The three-run figures in the table are a sample of that band, not its
edges, and the gate's budgets (95 and 20 percent) are sized for the band rather
than for the sample. Quote the band; treat any single millisecond figure as "this
machine, that afternoon".

What is unambiguous is the comparison the page is actually about, because both
sides of it are measured inside the same trial: the LUT dome's pass is below
`x/sky`'s dome's pass in **twelve trials out of twelve**, with the smallest margin
(0.0013 ms) still larger than the drift between adjacent trials of either one.

Two things in that table are worth knowing before quoting it.

`x/sky`'s whole-sky pass reads **0.0089–0.0090, lower than its own dome-only
0.0098–0.0102**, while drawing strictly more (the dome plus a sun billboard), and
it does so in all three runs. The explanation that fits is clock state: that
configuration's frame is 0.62 ms of work against 0.057, so the card is in a higher
clock state for the whole of it and every pass inside it is faster. It is recorded
rather than explained away because it is exactly the trap a pass-bracket comparison
across two different frame loads falls into — which is why the dome-to-dome
comparison is made against the dome-only configuration, whose frame is the same
size as this one's. The whole-frame comparison crosses the same boundary in the
direction that *understates* this package's advantage.

And the ratio against the empty slot swings by 1.5 points between runs, because the
frames being divided are 50 microseconds and the empty slot's own frame-to-frame
spread reached 8.2 percent in one of these runs. The gate's budget is 20 percent
for that reason rather than because 12 percent needed the room.

**One run was discarded and re-taken, which is the third thing to know.** The first
attempt at run three read a 0.1643 ms LUT frame against 0.0468 and 0.0568 in its
own other two trials — a 251 percent spread — with `x/sky`'s cloud pass swinging
0.3864 / 0.7775 / 0.5290 and even the empty slot's frame dropping 23 percent. A
second application on this machine (`universebuild.exe`, taking 52 percent of the
GPU's 3D engine) had started 13 seconds before that run finished. Everything moved,
including configurations this change cannot touch, which is what identifies it as
contention rather than a result. It was re-run once the card was quiet — checked by
GPU engine utilisation, not by process name, because a process-name poll does not
see another game — and the replacement is the tightest of the three (frame spreads
2.0 to 3.2 percent). An averaged-in contaminated run would have produced a number
with nothing wrong with it except that it was false.

## Gates

| What | Where |
|---|---|
| The keys are `x/sky`'s cycle at four hours: sun direction to the bit, directional light and ambient to 1e-6 | `TestDefaultKeysAreXSkysCycle` |
| What four keys cost: the largest sun-elevation disagreement over the whole cycle | `TestTheSunAgreesBetweenTheKeysToo` |
| Every axis inverts the shader's mapping, and the ends are the ends | `TestAxesInvertTheShadersMapping` |
| The engine's `atmosphere.inc` still says what `bake.go` copied | `TestAtmosphereIncStillSaysWhatWeCopied` |
| The gradient falls from horizon to zenith at all 32 hours, with the floor split where there is light to measure | `TestTheGradientFallsFromHorizonToZenith` |
| The sun's side is brighter at dawn, which is the proximity axis doing anything | `TestTheSunSideIsBrighterAtDawn` |
| The decoded table is the model, and by how much it is not | `TestTheTableIsTheModel`, `TestTheDefaultPaletteBrightestTexel` |
| A palette five times Earth's bakes, because there is no ceiling left to refuse | `TestABrightPaletteNeedsNoRefusing` |
| The shader's grid matches the bake, it fetches level 0, and it does NOT decode | `TestShaderAndBakeAgreeOnTheGrid` |
| The state is `StaticSource` plus one flag, and every zero in it is load-bearing | `TestStateIsStaticSourcePlusTheDome` |
| The directional light goes out when the sun sets | `TestTheSunGoesOutWhenItSets` |
| The clock wraps, and advances in simulation seconds through `Scene.Tick` | `TestTheClockWrapsAndAdvancesInSeconds` |
| Zero allocations per frame resolving and per tick advancing | `TestStateAndAdvanceAllocateNothing` |
| `Shaders` fills the dome and nothing else, and the engine's defaults leave the slot empty | `TestShadersFillOnlyTheDome` |
| The committed `.spv` is what `glslc` produces through the exported include set | `spirv_test.go` |
| The pixels, three hours, the sun's side, the empty-slot control and the cost | `task xskylut` |
| It runs and is validation-clean in a real example | `task smoke`, `task validate`, `task syncvalidate`, through `07-terrain -sky lut` |

The unit checks and the gate do not substitute for each other. The units read the
gradient and the sun out of the baked bytes at all 32 hours with no tonemap, no
bloom and no camera in the way; the gate reads what a frame actually does with
them, through the sampler, the slice blend, the tonemap and eight more bits of
rounding.

Every unit check above was broken and watched to fail, and each break's observed
message is recorded in the comment above the check it broke — including three in
the shader (a grid constant, the `textureLod`, and putting the squaring back) and
one in the engine's own `atmosphere.inc`. The count is deliberately not given
here: this page said "eighteen" before the move added breaks and replaced two
others, and a tally in prose is a number that goes stale without anything
noticing. Read the comments. The gate's break is
re-runnable and needs no SDK: `lutskycheck -flat` binds one constant texel over
the table through the same `SetShaderTexture` slot, from outside the package,
which is "sample a constant texel" without touching the shader. It builds that
texel with `renderer.CreateTextureRGBA16F` too, so the break goes in through the
same constructor the table does rather than through a narrower one that could
behave differently.

## The engine gap, and what closing it looked like

This section used to read "**there is no public way to upload a texture wider
than eight bits per channel**", with the error table above as the entire reason
this sky banded where `x/sky`'s did not. It is kept, rewritten, because the loop
it went through is the one [`x/README.md`](../../README.md#dependency-direction)
describes and the record of a closed gap is more useful than its absence.

What happened: the gap was filed as a rule-14 issue against the engine (#178)
rather than patched from here, the engine grew `CreateTextureRGBA16F` and
`CreateTextureR32F` with `TextureOptions` and the half-float transfer, and this
package moved onto them as the issue's worked example and re-measured. The
engine's own page for them is
[`docs/agents/textures.md`](../../../docs/agents/textures.md); the mechanism is
the upload path and the format, and the opinion — which range of radiance a sky
needs and how finely — stayed here.

The two documentation gaps noticed alongside it are closed in the same change:
`renderer.ShaderTextureSlots` is on
[`render-targets.md`](../../../docs/agents/render-targets.md)'s `api` list beside
`SetShaderTexture`, and the whole constructor family — `CreateTexture`,
`CreateTextureLinear`, `CreateTextureNearest` and the `Load*` pair, none of which
was on any list — is on `textures.md`. This package used `CreateDataTexture`
because it was the only listed one; it now uses the one that is right.

## Failure modes

- **A white sky.** The table is not bound: the slot is sampling the renderer's
  white fallback. Either `New` was never called, or something else bound
  `ShaderTextureSlot` afterwards.
- **No sky at all, and the light is right.** `glyph.WithShaders(skylut.Shaders())`
  is missing. The source asked for a dome the renderer has no pipeline for, which
  is not an error.
- **An alien dome over Earth-blue haze, or the reverse.** The palette is set in
  one of the two places it has to be set. See
  [the palette is baked](#the-palette-is-baked-and-that-is-the-real-cost).
- **No clouds, no stars, no sun.** Working as intended; use `x/sky`.
- **A dark dome, black at night, with the light and the fog right.** A
  `skylut.frag` that still squares its fetch against a table that holds radiance.
  `TestShaderAndBakeAgreeOnTheGrid` fails on it; a stale committed `.spv` is the
  way it gets there, so run `task xskylut:shaders`.
- **A palette bright enough to clip the table.** No longer a thing: `Bake`
  refused one above 3.0 when the table was eight-bit, and half-float has no
  ceiling.
- **A thin wrong-coloured ring around the sun at some hours.** The half-texel
  inset on the proximity axis has been lost, so the sampler's filter is reaching
  across a slice boundary into the next hour's sky.
- **Everything is black.** `Scene.Env` is nil. `NewScene` leaves it nil; the
  engine has no environment of its own beyond `StaticSource`.
