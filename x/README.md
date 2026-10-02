# x — opinionated systems built on the engine

`github.com/derekmwright/glyphengine/x` is a separate published Go module in the
engine's repository. It is where a system that has *a look* goes.

[ADR 0012](../docs/adr/0012-an-x-module-for-opinionated-systems.md) is the
decision and the reasoning. This page is the working version: what belongs here,
how to add a package, and what you may rely on.

## The test for whether something belongs here

Apply this to anything you are about to add to the engine:

1. Does it encode **a specific look** — palette curves, a day cycle, a material
   response, a cloud shape, a UI style?
2. Does it carry **a tuned constant with no measurement behind it**?
3. Is it **a system a game could reasonably want to swap** for a different one?

Any yes and it is an opinion: it goes in `x`, behind a seam. A **mechanism** — a
barrier, a lifetime, a layout, a sort, a format — goes in the engine.

Size is not the signal. `x/terrainfield` is forty lines of value noise and it is
an opinion; the clustered light binner is thousands of lines and it is a
mechanism. The question is whether a reasonable game would want a different one.

When an opinion is found already in the engine core, the response is **an ADR
naming the seam it moves behind and the `x` package it moves to** — not a quiet
deletion. The sky, clouds, water and grass are all in the core today and all
fail the list above; they also hold up committed captures and games already
built on them. Naming the seam is the work.

## Layout

One package per system, at `x/<package>`. Each carries:

| File | Why |
|---|---|
| `<package>.go` and friends | The system. Public API; the package is importable by games. |
| `<package>_test.go` | Its gate, with a control and recorded breaks. |
| `<package>.md` | Its capability page, with the same YAML frontmatter as `docs/agents/*.md`, so one index covers both. |

`x/internal/...` is for things that ship nothing — today, the gate that proves
the engine's exported GLSL include set still compiles.

## Dependency direction

```
examples  ──depends on──▶  x  ──depends on──▶  engine
```

The engine depends on neither. That is not a convention here, it is the module
layout, and `go.work` lists the three in that order. `ximport_test.go` in the
engine module walks `go list -deps` for the engine root and `./renderer/...` and
fails on any package under this module path — because `go.work` makes every
module importable from every other, so an engine file that imports an `x` package
builds, vets and tests clean locally. Go's own cycle detection catches only the
subset of `x` packages that import the engine's root package.

`x/go.mod` requires the engine and has **no `replace`**. `AGENTS.md` rule 1
applies to this module too: it is published, and a `replace` in a published
module is ignored by consumers, so it would give them different code than we
build against. In the repository `go.work` resolves the engine from disk. The
`consumer` CI job is what proves the requirement resolves *without* the
workspace, by building `x` against the archived engine with `GOWORK=off`.

Packages here version independently of the engine, Go-style, and pin an engine
version. If a package cannot reach what it needs through the engine's public
seams, **file a rule-14 issue against the engine** rather than reaching inside
it. That loop is what produced the application pass, storage buffer and mesh
range seams these packages are built on.

## What you may rely on — the seam compatibility policy

The stable surface is **the `api` lists of the capability pages in
`docs/agents/`**. A page's `api` entries are what an `x` package may bind to and
expect to keep working across a minor engine version. Anything not on an `api`
list is internal, whether or not Go lets you import it, and whether or not it is
currently exported.

Three parts of that surface are not Go identifiers and are easy to forget:

- **The fixed shader layouts** in
  [`docs/agents/render-targets.md`](../docs/agents/render-targets.md): the
  descriptor set and binding numbers a package's shaders must declare, and the
  push-constant block.
- **The GLSL include set** (below): an include's function signatures are as much
  an API as a Go one.
- **`EnvironmentSource`** and the per-frame values the engine reads from it.

A breaking change to any of them is announced **in the page's `verified` note and
in the release notes, in the same change that breaks it**. A page that still
claims an identifier the engine no longer has is worse than no page, because an
agent will trust it.

The engine is v0.x and its own README says APIs break without notice. The policy
above is what that sentence becomes once something outside the engine module
depends on a seam: the break is still allowed, but it is announced where someone
building on it will see it.

## Shaders: compiling against the engine's include set

The engine's shared GLSL — the lighting pack, the cascade block, fog, the sky
palette, the coverage ramp — is embedded and exported by
`github.com/derekmwright/glyphengine/shaders/include`. Do not vendor a copy: a
copy still compiles and still renders, with the lighting of whatever engine
version you copied it from.

The build step, which is the same one `task shaders` uses for the engine's own
shaders:

```go
dir, err := os.MkdirTemp("", "glyphinc")
if err != nil { ... }
defer os.RemoveAll(dir)
if err := include.WriteTo(dir); err != nil { ... }

cmd := exec.Command(glslc, "-I", dir, "water.frag", "-o", "water.frag.spv")
```

Name the includes bare in your GLSL — `#include "lighting.inc"` — so `-I` is what
resolves them. A relative path into the engine's source tree does not survive
`go get`.

Run that compile **from a test or a generator, never at startup**. `glslc` is an
authoring-only dependency; shelling out to it when a game launches makes it a
runtime one. Compiling in a test is also what makes a changed include fail at
build rather than at draw, which is the whole point of the export.
`x/internal/shaderinclude` is the minimal worked example, and it skips cleanly
when `VULKAN_SDK` is absent, exactly as `shaders/verify_test.go` does.

## Gates

Every package carries its own check, and the standard in
[`AGENTS.md`](../AGENTS.md) applies here unchanged: **break the fix and confirm
the check fails, then record what you broke and what it said.** A check that has
never failed is decoration, and this repository has shipped several.

For a package that renders, that means a fixed-clock capture check with a
control, under `GLYPHENGINE_FIXED_FRAME_TIME` — renders are not comparable
without it. For one that does not, pin the output it is responsible for:
`x/terrainfield` digests the float32 field it generates, with a second seed as
the control, because three GPU gates and a set of committed screenshots render
that exact island.

`task ci` lints, builds, tests and race-tests this module alongside the other
two.

## Packages

| Package | What it is | Page |
|---|---|---|
| [`terrainfield`](terrainfield/) | An island heightmap from value-noise fBm | [`terrainfield.md`](terrainfield/terrainfield.md) |
| [`water`](water/) | The underwater volume: absorption, the water's own colour and sun shafts | [`water.md`](water/water.md) |

The environment carve, `x/sky` and `x/sky/lut` are steps 3 to 5 of the sequence
in ADR 0012. `x/water` is step 2, and its page records which parts of the
atmosphere it deliberately left to a sibling package and why -- that boundary
argument is the part worth reading before adding the next one.
