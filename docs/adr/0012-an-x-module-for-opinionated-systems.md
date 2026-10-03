# 0012: An x module for opinionated systems

- Status: Accepted
- Recorded: 2026-10-02

## Context

[AGENTS.md](../../AGENTS.md) rule 14 says the engine unblocks paths and does not
ship opinions, and [ADR 0002](0002-keep-game-ownership-outside-the-engine.md)
says the game owns `main()`. Neither tells anyone where an opinion is supposed
to go.

The consequence is visible in the tree. The sky dome with its day cycle and
palette curves, the cloud layers, the water surface and the grass all live in
the engine, because they predate the rule. A consumer arriving now with a water
shader and an atmosphere pass of their own has nowhere to put them except inside
their game, where nobody else can use them and nothing in this repository
exercises them. The rule then does the opposite of its job: it blocks the
contribution without offering a destination.

What must remain true:

- A game can build the engine and nothing else, and get a working renderer.
  Nothing the engine needs may live outside it.
- The engine must not acquire a dependency on anything of ours. It is the
  bottom of the stack and it has to stay there, or `go get` of the engine starts
  pulling in other people's opinions.
- An opinionated package has to be able to compile GLSL. The engine's shared
  GLSL -- the lighting pack, the cascade block, fog, the sky palette, the
  coverage ramp -- is the difference between a package that lights a surface the
  way the rest of the frame does and one that invents its own lighting.
- Quality has to be the same on both sides of the line. A second home for code
  is also a second place for it to rot.

## Decision

A second published Go module in this repository, `x`
(`github.com/derekmwright/glyphengine/x`), for systems built only on the
engine's public seams: application render targets and passes, `ShaderSet`
stages, storage buffers, mesh arenas, async uploads, `EnvironmentSource`, and
`yamlui`. One package per system, at `x/<package>`.

**Dependency direction, enforced by module layout rather than by convention:**
the engine depends on neither of the other modules, `x` depends on the engine,
and the examples module depends on both. The ordering in `go.work` is that
direction. `ximport_test.go` in the engine module walks `go list -deps` for the
engine root and `./renderer/...` and fails on any package in the `x` module
path, because `go.work` makes every module importable from every other and a
local build will not notice the violation on its own.

**`x` packages version independently of the engine and pin an engine version.**
`x/go.mod` requires the engine and carries no `replace`: rule 1 applies to it
for the same reason it applies to the engine's `go.mod`, since `x` is published
and a `replace` in a published module is ignored by consumers. In-repo,
`go.work` resolves the engine from disk; the `consumer` CI job builds `x`
against the archived engine with `GOWORK=off` so that resolution is proved
without the workspace.

**A package that cannot reach what it needs files a rule-14 issue against the
engine** rather than reaching inside it. That loop is what produced this month's
seams -- application passes, storage buffers, mesh ranges -- and it is the
mechanism that keeps `x` from becoming a second engine.

**The engine exports its shared GLSL include set.** `shaders/include` is a
package whose `embed.FS` carries every `*.inc`, with a documented build step: an
`x` package writes the set to a directory and runs `glslc -I` against it, at
test or generate time, never at startup. The engine's own shaders compile the
same way -- `task shaders` passes `-Iinclude` -- so there is one mechanism, not
two, and the GLSL names its includes bare (`#include "lighting.inc"`) whether it
lives in this repository or not. A changed include then fails at build rather
than at draw.

**Seam compatibility.** The stable identifiers are the `api` lists of the
capability pages in `docs/agents/`: a page's `api` entries are what an `x`
package may bind to and expect to keep working across a minor engine version.
Anything not on an `api` list is internal regardless of whether Go lets you
import it. A breaking change to one of those identifiers is announced in the
page's `verified` note and in the release notes, in the same change that breaks
it. The fixed shader layouts in
[`docs/agents/render-targets.md`](../agents/render-targets.md) and the include
set are part of that surface: an include's function signatures are as much an
API as a Go one, and the compile gate in `x` is what detects a change to them.

**Gates per package.** Every `x` package carries its own check with a control
and recorded breaks, and its own capability page with the same frontmatter as
`docs/agents/`, so the same tooling indexes both. `task ci` builds, vets and
tests all three modules.

### The named test for opinion creep

Treat an opinion in the engine core as a code smell, and apply this list to any
engine change:

1. Does it encode **a specific look** -- palette curves, a day cycle, a material
   response, a cloud shape, a UI style?
2. Does it carry **a tuned constant with no measurement behind it**?
3. Is it **a system a game could reasonably want to swap** for a different one?

Any yes is an opinion, and it belongs in `x` behind a seam. A **mechanism** --
a barrier, a lifetime, a layout, a sort, a format -- belongs in the engine. The
distinction is not about size or quality: `x/terrainfield` is forty lines of
value noise and it is an opinion, while the clustered light binner is thousands
and it is a mechanism.

When one is found in the core, the response is **an ADR naming the seam it moves
behind and the `x` package it moves to, not a quiet deletion.** The sky, the
clouds, the water and the grass are all in the core today, all predate the rule,
and all are load-bearing for committed captures and for games already built on
them. Deleting one because it fails the list above would break those without
replacing them. Naming the seam is the work; the move is the easy part
afterwards.

### Sequence

This record is step 1 of five, from issue #161. Steps 2 to 5 are separate
branches.

1. **The module, the includes export and the policy**, with one package to prove
   the build and the CI check. This change.
2. **`x/water`**, from UniverseBuild: new code into an empty slot, no migration
   risk, and the proof of the shape.
3. **The environment contract carved clean** -- sun direction and colour,
   ambient, fog, the sky colour that fog, water and clouds read, the moon -- as
   the engine-side seam a sky package writes each frame. The atmosphere pass in
   #157 wants this carve anyway.
4. **`x/sky`**: the dome, the sun and moon discs, the palette curves, the cloud
   layers and the day cycle move out of the engine, gated on every committed
   capture staying byte-identical with `x/sky` plugged in where the built-in was.
   The engine keeps no sky of its own beyond the environment's flat colours.
   Done; the addendum below records the seam it took and why.
5. **`x/sky/lut`**: a second, cheap sky from a precomputed lookup table, proving
   the seam supports more than one implementation and giving low-end targets an
   option.

Clouds and grass follow the same path later if step 4 goes well.

## Alternatives

**Leave opinions in the game.** This is the status quo and it is what prompted
the issue. It costs the work twice: nothing in this repository builds or renders
the code, so it has no gate, no page, and no second user to find its bugs. It
also leaves rule 14 as a refusal with no alternative, which is how a rule stops
being followed.

**One module with an `x/` subtree.** Simpler to build, and wrong on the one
thing that matters: `go get` of the engine would pull every opinion with it, the
engine's `go.mod` would own their dependencies, and nothing would stop an engine
package importing one. The separate module is what makes the direction a fact
rather than a request.

**Separate repositories, one per package.** Correct dependency direction and
genuinely independent versioning, at the cost of the gates. A package in another
repository cannot run `task validate` against the engine it is being changed
with, and this engine's experience is that renderer regressions are found by the
example suite and the validation layer, not by unit tests. Keeping `x` in this
repository keeps those available; the module boundary is what provides the
isolation that separate repositories would have provided.

**Export the shared GLSL as strings a package concatenates.** No `glslc` needed
and no temp directory. It also loses `#include` line attribution, so an error in
a shared fragment is reported against a line number in the concatenated blob,
and it makes the engine's own build and a package's build two different
mechanisms. `-I` against a materialized directory is what the engine already
does, which is the argument for it.

**A version constant an `x` package checks at run time.** Would catch a mismatch
later and more expensively than a compile does, and nothing about a GLSL
signature change is visible to Go at all. The compile gate is earlier and
cheaper; a run-time check would be an addition to it, not a substitute.

## Consequences

- There is somewhere for an opinion to go, so rule 14 can be applied without
  blocking the contributor. That is the point of the record.
- The engine cannot absorb an opinion back without a test failing, and the
  failure is not one the compiler would have found: an `x` package that imports
  an engine leaf rather than the root makes no import cycle. Verified by
  breaking it -- see `ximport_test.go`.
- `task ci` is now three modules' worth of lint, build, test and race, and the
  `consumer` job is two consumers. Both got slower; neither got longer to read.
- The shared GLSL moved to `shaders/include/`, and every `glslc` invocation in
  `task shaders` and `shaders/verify_test.go` gained `-Iinclude`. The committed
  SPIR-V is byte-identical across the move, which `task shaders:verify`
  confirms.
- An `x` package that compiles a shader needs `VULKAN_SDK` to run its gate, like
  `task shaders:verify` does, so that gate is local rather than in CI. CI still
  proves the include set reaches a consumer at all, which is the half that can
  break silently.
- Two published modules means two version streams, and a consumer can end up
  with an `x` package built against an engine version they are not running. The
  pin in `x/go.mod` makes that a resolvable requirement rather than a silent
  mismatch, but it does not make it impossible; the compatibility policy above
  is the only thing that limits the damage, and it is a promise rather than a
  mechanism. Revisit if it is broken in practice.
- The engine still has water and grass that this record's own test says are
  opinions. That is deliberate and sequenced, not an oversight. The sky and the
  clouds were paid down in step 4 (see the addendum), and the capture-identical
  gate on it is the control for the whole migration.

## References and evidence

- Issue #161, which proposed the module and the five-step sequence this record
  accepts.
- Issue #157 (the atmosphere pass that needs the environment seam), #160
  (capabilities report, which `x` packages will branch on), #159 (ray queries,
  where an `x/gi` would live).
- [ADR 0002](0002-keep-game-ownership-outside-the-engine.md) for the ownership
  seam this extends, [ADR 0003](0003-isolate-examples-in-a-separate-module.md)
  for the precedent of a second module in one repository, and
  [ADR 0006](0006-expose-application-graphics-nodes.md) and
  [ADR 0008](0008-buffer-resources-and-gpu-draw-generation.md) for the seams
  `x` packages are expected to build on.
- [`x/README.md`](../../x/README.md) for the layout and the compatibility
  policy as a working page.
- Checks run for this change: `task ci` across all three modules;
  `ximport_test.go`, broken with a throwaway `x` package blank-imported from
  `renderer/shadow.go` and confirmed failing while both builds stayed green;
  `task shaders:verify` byte-identical after the include move; the include
  compile gate in `x/internal/shaderinclude`, broken by renaming
  `srgbToLinear`; `x/terrainfield`'s field digests, taken from the generator as
  it stood before the move and broken with `heightScale` and the octave count;
  `task smoke`, `task validate`, `task determinism`, `task screenshots`,
  `task lod` and `task ranges` for the examples that moved to `x/terrainfield`;
  and the `consumer` job reproduced locally against the archived index, broken
  by `export-ignore`ing the include set.

## Addendum: step 4, the sky — 2026-10-02

The sky moved to `x/sky`: the day cycle and its keyframe curves, the dome, the
sun and moon discs, the star field with its galactic band, and the cloud layers.
This records the shape it took, because the record above named the step and not
the seam.

### The seam is a slot, not a pass set

Two shapes were available.

**An application pass set.** `x/sky` creates the dome, star and cloud passes
through `renderer.AppPass`, and the engine loses the sky passes entirely. This
was rejected. The five draws are not five independent passes: the cloud march
writes a half-resolution target the dome samples through a barrier, the dome
writes an alpha the stars and both discs blend against, the in-scattering shares
the dome's exact depth state so that it covers exactly the dome's pixels, and the
discs are depth-tested against the scene but drawn after the dome so a cloud can
pass in front of the sun. Moving the draws means moving the ordering, the blend
factors, the depth states and the barriers — all five of which this record's own
test calls mechanism — and it would leave the engine unable to draw an
in-scattered beam without a sky package.

**A slot.** `renderer.ShaderSet.SkyFrag`, `.StarsFrag` and `.CloudsFrag` become
the one exception to that type's fallback rule: `DefaultShaders()` leaves all
three nil, `withDefaults` does not fill them, a nil stage builds no pipeline, and
no draw is recorded where there is no pipeline. The engine keeps every mechanism
listed above and ships no dome, stars or clouds of its own; `x/sky` supplies the
three stages and the `EnvironmentSource` that asks for them. This is what
shipped.

The slot is the smaller mechanism in the sense that matters: it moves the look
and nothing else. It is also what made the migration provable. The three
fragment shaders compile byte-identically from `x/sky` through the exported
include set — 9568, 17760 and 35296 bytes, the same files, the only change being
which `-I` resolved `atmosphere.inc` — so every committed capture being unchanged
is a consequence rather than a hope. A pass set would have rebuilt the draws and
left that claim to be re-established by eye.

The cost is that a package now has **two halves that must arrive together**: the
source and the shaders. Neither errors without the other — a source asking for a
dome the renderer cannot draw renders the clear colour with the right light on
it. That is the first failure mode on `x/sky`'s page and in `x/README.md`, and it
is the price of not making `renderer.New` reject a configuration (`cmd/skyshadowcheck`
is a legitimate custom dome with no cloud stage beside it).

### What the engine kept

`EnvironmentSource`, `EnvironmentState` unchanged field for field, `StaticSource`
with its `Sky` field removed, `DirectionalLight`, `AmbientLight`, `Fog`,
`DefaultFogDensity`, `LightShaftShape` and its measured defaults, `SkyPalette`,
`NightGrade` and both defaults with the sentinel rule, the volumetric march, the
fog, the light-shaft pass, `shaders/sky.vert` and `stars.vert`,
`shaders/include/atmosphere.inc` untouched, the cloud target and its barriers,
the celestial billboard placement, and `EquirectToSkyMap`.

Removed, because each could only work by knowing a concrete source type that no
longer exists in the engine: `Scene.DayNight`, `Scene.TimeOfDay`,
`Scene.SetTimeOfDay`, `Scene.SetDayCycleSpeed` and `Engine.SetFogDensity`.
Leaving the setters as no-ops was the alternative and is worse than removing
them: `e.SetFogDensity(0.008)` would have compiled, returned, and done nothing
for every example in the tree. `Scene.StarVisibility` and `Engine.FogDensity`
stay, because they read the resolved state rather than reaching for a clock.

`NewScene` leaves `Scene.Env` nil. It installed `DefaultEnvironment()` before, so
every scene got a dome and a sunrise whether it asked or not; the engine has
nothing left to put there that would not be an opinion.

### The one tuned constant the engine kept

`celestialScale` draws a body at the zenith 45 percent smaller than the same body
at the horizon. That is a look, and it is still in `app.go`.

The reason is the migration's own control. `env=` in the state trace hashes every
field of `EnvironmentState`, and the capture gate compares that hash against
traces taken from the built-in path before the move, so adding a field for a
package to write the scale through would change the hash and destroy the only
measurement that says the move was faithful. The honest order is this change
first and the field afterwards — the same reason the environment carve came
before the move. Recorded rather than left as an oversight.

### Evidence

- `task ci` across all three modules.
- The carve's pinned tables moved with the code they pin and still pass
  unmodified: 25 resolved states round the clock, 9 more at the curve edges, and
  25 moon disc colours. They were generated from the engine as it stood *before*
  the carve, so two migrations now rest on them. Broken and confirmed failing by
  reassociating one multiply in `MoonDiscColor` and by dropping the shaft
  window's smoothstep.
- The fixed-hour path was rearranged rather than relocated — the light and air
  come from the engine's `StaticSource` now — and its five pre-move pins hold it.
- `x/sky/spirv_test.go`: the committed SPIR-V is what `glslc -I` produces from
  the materialized include set, which is also the compile gate for
  `atmosphere.inc`'s signatures.
- `task skymigration`: three scenes (a full day cycle, a night, and a dome at a
  fixed hour with no cycle) re-rendered with `x/sky` and compared against
  captures and `env=` traces taken from the built-in path on the commit before
  the move. Pixels rather than PNG bytes, because Go's encoder emits different
  bytes for identical pixels across toolchain versions.
- `task screenshots` with no changed image, `task determinism`,
  `task shaders:verify`, `task sky`, `skypalette`, `clouds`, `shafts`,
  `volumetric`, `nightlight`, `waterlight`, `xwater`, `validate`, `syncvalidate`
  and `smoke`.
- The empty slot is exercised rather than assumed: `examples/11-lights`,
  `examples/23-shadow-coverage` and `examples/01-triangle` import no sky package,
  so the renderer builds no dome, star or cloud pipeline at all in three of the
  scenes `task validate` and `task smoke` run.
