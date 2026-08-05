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
| `/examples` | `glyphengine/examples` | Runnable examples, one concept each |
| `/docs/agents` | — | Machine-readable capability docs (see below) |
| `/shaders` | — | GLSL sources and **committed** SPIR-V |

Three separate Go modules in one git repo, tied together by `go.work`. This is
deliberate: `go get` of the engine never pulls example code or assets, while
`git clone` still gets everything.

## Rules that matter

1. **Never add a `replace` directive to the root `go.mod`.** A `replace` in a
   dependency is ignored by consumers, so it would silently give them different
   code than we build and test against. `examples/go.mod` has one, which is
   fine — it is a main module that is never published.
2. **`shaders/*.spv` are committed on purpose.** `shaders/shaders.go` embeds
   them with `go:embed`, so without them the module does not build for anyone
   who runs `go get`. `glslc` is an authoring-only dependency. If you edit a
   `.vert`/`.frag`, run `task shaders` and commit the regenerated `.spv`.
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

## Verification

| Command | Checks |
|---|---|
| `task build` | Engine and all examples compile |
| `task test` | Unit tests (no GPU required) |
| `task test:race` | Same under `-race`; the parallel movement phase needs it |
| `task lint` | gofmt, then `go vet -unsafeptr=false` |
| `task smoke` | Renders real frames of every example, exits non-zero on failure |
| `task validate` | Every example under the Vulkan validation layer; must be completely silent (needs a GPU and the SDK) |
| `task determinism` | Renders repeat byte for byte under a fixed frame clock (needs a GPU) |
| `task bench` | Per-pass GPU and per-phase CPU cost over a fixed scene set (needs a GPU) |
| `task ci` | Lint, build, test, race |

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
- **Record what you measured, in the code.** Scene, metric, and numbers before
  and after, next to the line they justify -- so the next person can re-run it
  rather than trust it. Rule 12 exists because that was missing.

`go vet` needs `-unsafeptr=false`: the GLFW-to-Vulkan surface handle bridge in
`window/window.go` is a deliberate, documented `unsafe.Pointer` round-trip.

## Status

v0.x. APIs break without notice. The engine is being extracted from a shipping
Vulkan MMO; it exists to ship games, not to be a stable platform yet.
