# GlyphEngine — agent guide

Tool-neutral entry point for AI coding agents. Human docs live in `README.md`
and `docs/`; this file is the map.

## What this is

A 3D game engine for Go. Vulkan renderer (forward, reverse-Z, cascaded shadows,
GPU skinning, MSDF text, instanced grass, particles), generic ECS, AABB +
convex-hull physics, heightmap terrain, A* navgrid, 3D positional audio, and a
declarative YAML UI system.

Module path: `github.com/derekmwright/glyphengine` — root package name is
`glyphengine`. It is a **library**: the game owns `main()` and composes the
engine in. There is no editor application and no runtime that loads your game.

## Repository layout

| Path | Module | Purpose |
|---|---|---|
| `/` | `glyphengine` | Engine root package plus subpackages |
| `/x` | `glyphengine/x` | Opinionated systems on the engine's public seams, one package per system: `sky`, `water`, `terrainfield` |
| `/examples` | `glyphengine/examples` | Runnable examples, one concept each |
| `/docs/adr` | — | Architecture decision records and their index |
| `/docs/agents` | — | Machine-readable capability docs (see below) |
| `/shaders` | — | GLSL sources and **committed** SPIR-V |
| `/shaders/include` | `glyphengine` | The shared GLSL fragments, embedded and exported so `x` can compile against them |

Three separate Go modules in one git repo, tied together by `go.work`. This is
deliberate: `go get` of the engine never pulls example code, assets, or anyone's
opinions, while `git clone` still gets everything.

The order in `go.work` is the dependency direction, and it is enforced rather
than merely documented: **the engine depends on neither of the others, `x`
depends on the engine, the examples depend on both.** `ximport_test.go` fails if
an engine package reaches into `x`, including transitively. It has to exist
because `go.work` makes every module importable from every other, so a violation
builds, vets and tests clean on its own, and Go's cycle detection catches only
the `x` packages that happen to import the engine's root package. See
[ADR 0012](docs/adr/0012-an-x-module-for-opinionated-systems.md) and
[`x/README.md`](x/README.md).

## Rules that matter

1. **Never add a `replace` directive to a published module's `go.mod`.** That is
   the root `go.mod` and `x/go.mod`. A `replace` in a dependency is ignored by
   consumers, so it would silently give them different code than we build and
   test against. `examples/go.mod` has two, which is fine — it is a main module
   that is never published. `x` resolves the engine through `go.work` in the
   repository, and the `consumer` CI job builds it with `GOWORK=off` so the
   requirement is proved without the workspace.
2. **`shaders/*.spv` are committed on purpose.** `shaders/shaders.go` embeds
   them with `go:embed`, so without them the module does not build for anyone
   who runs `go get`. `glslc` is an authoring-only dependency. If you edit a
   `.vert`/`.frag`, run `task shaders` and commit the regenerated `.spv`.

   An `x` package commits its own `.spv` beside its GLSL for the same reason
   and regenerates them through the same mechanism: `task xsky:shaders`,
   `task xwater:shaders`. The engine embeds no dome, star or cloud shader at all
   — see rule 14's note on the sky slot.

   The shared fragments live in `shaders/include/` and are named bare:
   `#include "lighting.inc"`, resolved by the `-Iinclude` every `glslc`
   invocation passes. They are there so `shaders/include/include.go` can embed
   and export them to `x` — never include one by a relative path, in this
   repository or outside it, because a path into the engine's source tree does
   not survive `go get`.
3. **No Git LFS.** The Go module proxy does not run LFS smudge, so consumers
   would receive pointer stubs where `go:embed` and the asset loaders expect
   real content.
4. **Engine code must not contain game content.** No game-specific asset paths,
   no gameplay concepts (health, combat, inventory, quests). Those belong in the
   consuming game. This seam is the entire point of the project.
5. **The renderer uses reverse-Z.** Depth clears to `0.0` and compares with
   `CompareOpGreater`. Geometry authored for a conventional `0.0 → 1.0` depth
   range will fail the depth test and silently draw nothing.
6. **CGo is required** (`CGO_ENABLED=1`, a C compiler, and the Vulkan runtime).
   The GPU/driver must support `VK_KHR_dynamic_rendering`, `dynamicRendering`
   and its enabled dependency extensions; there is no render-pass fallback.
   See [ADR 0009](docs/adr/0009-execute-render-passes-with-dynamic-rendering.md).
   Builds with `CGO_ENABLED=0` will fail.
7. **Convex hulls belong on `Static` entities only.** The parallel movement
   phase (`Scene.MoveCharactersParallel`) runs hull narrow-phase tests against
   *live* transforms while AABB queries read a frozen snapshot. That is sound
   only because hull entities never move during the phase. Putting a
   `ConvexHullCollider` on a moving entity is a data race, not a bug you will
   see in a single-threaded test. `controller_race_test.go` guards it.
8. **The engine's component set is closed.** `Scene.C` holds only what the
   engine itself reads. Game components go in the game's own struct on the same
   `ecs.World`. Adding a game concept to `Components` reintroduces exactly the
   coupling this extraction removed.
9. **Simulation goes in `FixedUpdate`, input goes in `Update`.** `FixedUpdate`
   runs on the fixed tick — zero or several times per frame — so anything that
   must be deterministic belongs there, and anything edge-triggered must be
   latched in `Update` and consumed there. Reading `KeyPressed` inside
   `FixedUpdate` silently drops inputs on the ~59% of frames that run no tick
   at a 144Hz refresh.
10. **Adding a resource to `renderer.New` means adding its teardown there too.**
    Push an `r.onInit(...)` step immediately after creating it, and read the
    resource through `r` inside the closure rather than capturing the value —
    `recreateSwapchain` swaps several of them out on every resize. `New` and
    `Destroy` unwind that one stack, so this is the only place destruction order
    is written down. Verify with `task validate`.
11. **No AI attribution in commits.** No `Co-Authored-By`, no generation notices.

12. **A comment claiming to prevent something is a hypothesis, not evidence.**
    Two mitigations in `grass.frag` carried confident explanations of the
    artifact they stopped. Measured, they stopped nothing, and one named a
    cause the shader does not even have. They cost a day of looking in the
    wrong place, because the comment read as settled. Before you build on a
    claim like that, turn it off and measure. If it earns its place, record the
    number next to it; if it does not, delete it.

13. **Compare renders under `GLYPHENGINE_FIXED_FRAME_TIME`.** Wall-clock
    animation makes two runs of the same build differ by RMS 0.009 to 0.05,
    which is the size of changes worth measuring. Ablations judged one run each
    have already produced a "50 percent improvement" that five runs each showed
    to be nothing. See `WithFixedFrameTime`.

14. **Unblock a path; do not ship an opinion.** The bar for adding a feature is
    that a game cannot reach it from outside the engine — not that having it
    provided would be convenient. Convenience approves everything.

    Pausing qualifies: `Scene.Tick` runs before `FixedUpdate`, so a game that
    returns early has stopped its own simulation and none of the engine's, and
    no amount of game code can stop the engine's. Translucency, instancing and
    a replaceable `ShaderSet` qualify for the same reason — a pipeline or a
    push-constant layout is not reachable from a consuming game.

    Scene management is the counter-example, and it is why this rule is
    written down. `Engine` embeds `*Scene` as an exported field, so swapping
    scenes is `e.Scene = other`, and GPU resources live on the `Renderer` and
    survive the swap. A `SceneManager` with push, pop and transitions would add
    no capability; it would add *structure*, and the README's promise is that
    your program owns `main()`. Loading screens are the same shape — the real
    question underneath them is whether assets can be uploaded off the frame
    thread, which is a threading contract rather than a screen feature.

    The one narrower case that also qualifies: **completing a seam the engine
    already committed to.** Shipping `Button`, `Panel` and `InputField` and
    then having no focus traversal means the toolkit has to be abandoned
    wholesale for the first screen of most games. That is not unblocking, it is
    finishing.

    **An opinion now has somewhere to go: `x`.** Rule 14 used to be a refusal
    with no alternative, which is how a rule stops being followed. Apply this
    test to any engine change, and treat a yes as a code smell:

    1. Does it encode **a specific look** — palette curves, a day cycle, a
       material response, a cloud shape, a UI style?
    2. Does it carry **a tuned constant with no measurement behind it**?
    3. Is it **a system a game could reasonably want to swap** for a different
       one?

    Any yes is an opinion and belongs in `x`, behind a seam. A **mechanism** — a
    barrier, a lifetime, a layout, a sort, a format — belongs in the engine.
    Size is not the signal: `x/terrainfield` is forty lines of value noise and
    it is an opinion, while the clustered light binner is thousands and it is a
    mechanism.

    **When one is found in the core, the response is an ADR naming the seam it
    moves behind and the `x` package it moves to — not a quiet deletion.** The
    sky, the clouds, the water surface and the grass all fail the test above, all
    predate the rule, and all hold up committed captures and games already built
    on them. Deleting one because it fails the test breaks those without
    replacing them. Naming the seam is the work; the move is the easy part
    afterwards. See
    [ADR 0012](docs/adr/0012-an-x-module-for-opinionated-systems.md) and
    [`x/README.md`](x/README.md), which carries the same test as a page.

    The sky and the clouds have gone, to [`x/sky`](x/sky/sky.md), and the slot
    they went behind now has a second filling in
    [`x/sky/lut`](x/sky/lut/lut.md) — which is what makes it a slot rather than
    a rename. The seam that got them out is worth knowing because the water
    surface and the grass will want one like it: the engine kept every mechanism — the far-plane triangle,
    the pass order, the depth states, the cloud target and its barriers — and
    `renderer.ShaderSet` grew a **sky slot**, three fragment stages
    (`SkyFrag`, `StarsFrag`, `CloudsFrag`) that `DefaultShaders()` leaves nil and
    that build no pipeline and record no draw when they are. The engine therefore
    draws no sky of its own, and `NewScene` leaves `Scene.Env` nil. A package
    filling a slot like that ships **two halves that must arrive together**, the
    Go source and the SPIR-V, and neither errors without the other.

## Architecture decisions

Read the relevant accepted records in [`docs/adr/README.md`](docs/adr/README.md)
before changing an architectural boundary or contract. Add an ADR in the same
change when making a significant decision about engine/game ownership, public
contracts, resource lifetime, concurrency, rendering conventions, or dependency
and asset distribution. Routine fixes and local refactors do not need one.

Use the [template](docs/adr/template.md) and keep the index current. If a new
decision replaces an accepted one, write a new record and link both directions;
preserve the old rationale. Keep this guide's rules and the capability docs
consistent with the accepted decision. The ADR records why; those docs remain
the instructions for working with the current engine.

## Getting a window on screen

```
task example:01-triangle
```

If that draws a red/green/blue triangle, the whole toolchain works. Start any
debugging there — it uses no assets, no vertex buffers, and no descriptor sets,
so a failure is in core Vulkan setup rather than in anything above it.

Then `task example:02-cube` for the engine's actual shape (a `Game`, a `Scene`,
entities), `task example:04-first-person` to walk around, and
`task example:07-terrain` for terrain. None of them load anything from disk.

## Capability docs

`docs/agents/*.md` each carry YAML frontmatter so a harness can index them
without parsing prose. Schema and conventions: `docs/agents/README.md`.

Query them by the `capability` and `api` frontmatter fields to find the right
entry point for a task, then read the body for working code.

An `x` package's page lives beside its code (`x/<package>/<package>.md`) with the
same frontmatter, so one index covers both. The `api` lists are also the seam
compatibility surface: they are what a package outside the engine module may bind
to and expect to keep working, and a break is announced in the page's `verified`
note and in the release notes. See [`x/README.md`](x/README.md).

Blender is the reference world-building and modelling pipeline this engine
targets (`docs/agents/blender-pipeline.md`) -- the engine still reads only
the open glTF format, but the recipe, the export script under
`tools/blender/`, and a fixture a real Blender wrote
(`renderer/testdata/blender/level.glb`) are checked against what Blender
actually exports rather than assumed. A change to `renderer/gltf*.go`
should be checked against that fixture too (`renderer/gltfblender_test.go`),
not only against the hand-authored one in `examples/22-level/assets`.

Terrain is the one part of that pipeline that does not travel through
`renderer` at all: `docs/agents/terrain-heightmap.md` and
`cmd/heightmapconv` are how a sculpted Blender mesh (or a heightmap image)
becomes the `.heightmap` file `LoadHeightmap` reads, and
`tools/blender/build_terrain_fixture.py`/`cmd/heightmapconv/testdata/blender_terrain.glb`
are that pipeline's own real-Blender fixture, in the same spirit as
`level.glb` above.

## Verification

| Command | Checks |
|---|---|
| `task build` | Engine, `x` and all examples compile |
| `task test` | Unit tests, engine and `x` (no GPU required) |
| `task test:race` | Same under `-race`; the parallel movement phase needs it |
| `task lint` | gofmt, then `go vet -unsafeptr=false` across all three modules |
| `task smoke` | Renders real frames of every example, exits non-zero on failure |
| `task validate` | Every example under the Vulkan validation layer; must be completely silent (needs a GPU and the SDK) |
| `task syncvalidate` | The validation matrix plus application compute churn under synchronization validation; must be silent (needs a GPU and the SDK) |
| `task determinism` | Renders repeat byte for byte under a fixed frame clock, and the recorded draw sequence repeats with them (needs a GPU) |
| `task reload` | A level swapped for a freshly loaded copy never drops a frame's geometry (needs a GPU) |
| `task sky` | Celestial bodies are occluded by terrain, not drawn over it (needs a GPU) |
| `task hud` | Screen-space overlays survive water, bloom and the tonemap (needs a GPU) |
| `task indicator` | A yamlui `indicator:` covers the widget region it names, nothing else, stays under its label and vanishes at zero (needs a GPU) |
| `task flatquad` | A flat yamlui `bg_color` panel and progress-bar fill reach the colour they ask for instead of half alpha (needs a GPU) |
| `task transition` | A yamlui `transition:` moves while the game is paused, settles onto the untransitioned frame and moves nothing else (needs a GPU) |
| `task scroll` | A yamlui `scroll_view` clips its content to its view rect, exactly up to the edge and nowhere past it (needs a GPU) |
| `task custompasses` | Application light/compute/fog passes are visible, confined, repeatable and validation-clean (needs a GPU) |
| `task waterblend` | A blended effect in front of the water is not painted over by it (needs a GPU) |
| `task xwater` | `x/water`'s underwater volume darkens and shifts with depth, changes nothing above the surface, and balances its passes (needs a GPU) |
| `task xskylut` | `x/sky/lut`'s lookup-table sky has the gradient and the sun its model says, and costs less than `x/sky` on the same scene (needs a GPU) |
| `task bench` | Per-pass GPU and per-phase CPU cost over a fixed scene set (needs a GPU) |
| `task ranges` | Shared mesh ranges, both index widths, indirect/instanced/LOD equivalence and deferred replacement (needs a GPU) |
| `task stream` | A patch grid streamed in over 100 frames matches one built synchronously, standalone and in an arena (needs a GPU) |
| `task lod` | CPU/GPU LOD culling, fades, impostors, exact indexed/nonindexed captures, replacement lifetime and interleaved performance (needs a GPU) |
| `task lights` | Clustered and brute-force light renderers produce byte-identical images (needs a GPU) |
| `task nightlight` | Warm lamps stay warm on the ground after the night shift (needs a GPU) |
| `task waterlight` | A lamp beside a lake reaches the water, in the lamp's colour (needs a GPU) |
| `task volumetric` | A light's beam is in the air inside its cone and nowhere else (needs a GPU) |
| `task clouds` | Sunset gold/rose, independent cirrus, layer occlusion and repeatability (needs a GPU) |
| `task skypalette` | The sky palette reaches the fog, the water and the clouds, not just the dome (needs a GPU) |
| `task ci` | Lint, build, test, race, across all three modules |
| `task shaders:verify` | The committed `.spv` match their GLSL, compiled with `-Iinclude` (needs the SDK) |
| `task xwater:shaders` | Recompile `x/water`'s `.spv` through the exported include set; its own test verifies them under `task ci` (needs the SDK) |
| `task xsky:shaders` | The same for `x/sky`'s dome, star and cloud shaders (needs the SDK) |
| `task xskylut:shaders` | The same for `x/sky/lut`'s one dome shader (needs the SDK) |
| `task skymigration` | Three scenes render exactly what the built-in sky rendered before it moved to `x/sky`, in pixels and in `env=` (needs a GPU) |

### Signing off a fix

A fix is not done when the symptom disappears. It is done when you can say
which change removed it and show the number:

- **Isolate it.** Fix one thing at a time under a fixed clock. Two changes in
  one measurement tell you nothing about either.
- **Break it and watch the check fail.** A check that has never failed is
  decoration. This repo has shipped green tests that compared a value to
  itself, and a gate whose captures were empty files.
- **Prove the check is not vacuous.** `task determinism` renders a control
  with the real clock that *must* differ; the first version of it passed on
  four examples while hashing nothing at all.
- **Give a visual metric a visibility floor, and do not trust it alone.** A
  ratio test ("2.5x the local median") reports invisible noise as a defect in
  any dark scene: at midnight it flagged 213 pixels sitting 12/255 from their
  neighbours, which no one can see. Pair every ratio with an absolute contrast
  and report both. And if the artifact only shows up in motion -- shimmer,
  sparkle, crawl -- no still-frame metric will find it. Run the example and
  have a person watch it; every visual bug found today was found that way,
  while `task ci`, `task validate`, and the metrics all stayed green.

- **Record what you measured, in the code.** Scene, metric, and numbers before
  and after, next to the line they justify -- so the next person can re-run it
  rather than trust it. Rule 12 exists because that was missing.

`go vet` needs `-unsafeptr=false`: the GLFW-to-Vulkan surface handle bridge in
`window/window.go` is a deliberate, documented `unsafe.Pointer` round-trip.

## Status

v0.x. APIs break without notice. The engine is being extracted from a shipping
Vulkan MMO; it exists to ship games, not to be a stable platform yet.
