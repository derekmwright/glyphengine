# Third-party code

## Vendored sources

Code redistributed in this repository, as opposed to fetched by the Go module
system.

| Path | Project | License |
|---|---|---|
| `audio/miniaudio.c`, `audio/miniaudio.h` | [miniaudio](https://github.com/mackron/miniaudio) by David Reid | Public domain (Unlicense) or MIT-0, at your choice. Full statements are at the end of `miniaudio.h`. |

## Go module dependencies

Everything in `go.mod` is fetched by the Go toolchain rather than redistributed
here, and each carries its own license. Run `go mod download` and inspect the
module cache, or use `go-licenses`, for the authoritative list.

Direct dependencies:

- `github.com/go-gl/glfw/v3.3/glfw` — windowing and input
- `github.com/go-gl/mathgl` — vector and matrix math
- `github.com/vkngwrapper/core/v3`, `github.com/vkngwrapper/extensions/v3` — Vulkan bindings
- `github.com/qmuntal/gltf` — glTF parsing
- `github.com/golang/geo` — geometry primitives, used by convex hull generation
- `github.com/markus-wa/quickhull-go/v2` — convex hull generation
- `gopkg.in/yaml.v3` — YAML UI parsing
- `golang.org/x/image` — TrueType/OpenType outline parsing for `msdf`, and the Go
  Regular typeface that `cmd/msdfatlas -builtin` generates from (BSD 3-Clause)

A full per-dependency license audit is part of launch polish and is not
complete. If you are shipping a product on this engine, do your own.

## Fonts

`examples/10-text` ships a generated MSDF atlas, not a font file. It is
produced from Go Regular, which arrives through `golang.org/x/image` under the
same BSD 3-Clause license as the Go standard library — chosen over the usual
OFL-licensed candidates so that nothing in this repository carries a copyleft
or attribution obligation. See `examples/10-text/assets/ASSETS.md`.

Atlases you generate from your own fonts inherit those fonts' licenses.

## Shaders

`shaders/*.spv` are compiled from the `.vert`/`.frag` sources beside them and
are committed deliberately — see `AGENTS.md`. They contain no third-party code.
