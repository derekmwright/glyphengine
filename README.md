# GlyphEngine

A 3D game engine for Go, built on Vulkan.

Go has no living 3D engine — Ebitengine is 2D, g3n is quiet, Azul3D is gone.
This is an attempt at one, extracted from a working Vulkan MMO rather than
written speculatively, so the parts that exist have shipped something.

```
task example:01-triangle    # toolchain proof: no assets, no vertex buffers
task example:04-first-person
task example:07-terrain
```

## Status: v0.x, and it means it

**APIs break without notice.** The engine is mid-extraction from its parent
game; roughly half the planned surface is in place. It exists to ship games,
not to be a stable platform yet. Pin a commit if you build on it.

What works today:

- Vulkan forward renderer — reverse-Z depth, cascaded shadow maps, MSAA,
  GPU skinning, MSDF text, instanced grass, particles, day/night lighting
- Generic ECS with typed component stores
- AABB and convex-hull physics, raycasts, spatial hashing
- Heightmap terrain that is both collision and renderable geometry
- Character controller with deterministic fixed-timestep movement
- A* navigation grid with amortized pathfinding
- Declarative YAML UI
- 3D positional audio

Not there yet:

- **Asset loading is path-relative.** Textures, glTF models, fonts, and
  heightmaps use `os.Open`, so the working directory matters. An `fs.FS`
  layer is the next major piece.
- No editor, and none planned. This is a library: your program owns `main()`.
- No networking layer, though movement is deterministic and snapshot/replay
  primitives exist for client-side prediction.

## Requirements

- Go 1.26+
- **CGo enabled**, with a C compiler. `CGO_ENABLED=0` will not build.
- Vulkan runtime and a GPU that supports it
- [go-task](https://taskfile.dev) for the build targets

## Platform support

| Platform | Status |
|---|---|
| Windows | Works, and is what development happens on |
| Linux | Blocked on an upstream release. `vkngwrapper/core` v3.1.1 is missing an `unsafe` import in `system_nonwindows.go` and does not compile; the fix is merged upstream but has never been tagged. |
| macOS | Untested. Expect missing CGo flags and no MoltenVK portability handling. |

That Linux caveat is the one thing most likely to waste your afternoon, which
is why it is this far up the page.

## Design

The engine is a **library**, not a framework with a runtime that loads your
game. You implement a `Game` interface and call `Run`.

Simulation runs at a fixed tick rate; rendering runs at the display rate and
interpolates between ticks. That split is why movement is reproducible — the
same inputs produce bit-identical results on a 60Hz machine and a 300Hz one,
which is the precondition for an authoritative server ever agreeing with a
client.

Game content does not belong in the engine. No health, no inventory, no
combat, no asset paths. That seam is the entire point of the project.

## Documentation

- [`AGENTS.md`](AGENTS.md) — entry point for AI coding agents, and the fastest
  orientation for humans too
- [`docs/agents/`](docs/agents) — one page per capability, each with working
  code and its failure modes, indexed by YAML frontmatter
- [`examples/`](examples) — one concept per example, none of them loading
  anything from disk

## License

MIT. See [`LICENSE`](LICENSE), and [`THIRD_PARTY.md`](THIRD_PARTY.md) for
vendored code.
