---
id: textures
title: Uploading textures from the CPU
summary: >
  Turn pixels a game computed or decoded into a sampled image, in eight bits per
  channel for colour and data maps or in half- and single-precision float for
  numbers a shader needs at more than eight bits.
capability: rendering
status: stable
since: v0.4.0
api:
  - renderer.Texture
  - renderer.Renderer.CreateTexture
  - renderer.Renderer.CreateDataTexture
  - renderer.Renderer.CreateTextureLinear
  - renderer.Renderer.CreateTextureNearest
  - renderer.Renderer.CreateTextureRGBA16F
  - renderer.Renderer.CreateTextureR32F
  - renderer.TextureOptions
  - renderer.Float16
  - renderer.Float16Value
  - renderer.Renderer.LoadTexture
  - renderer.Renderer.LoadDataTexture
  - renderer.Renderer.LoadTextureLinear
  - renderer.Renderer.LoadTextureNearest
example: examples/16-materials
run: task example:16-materials
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-10-03 # the wide-format constructors, TextureOptions and the half-float transfer (#178); the eight-bit constructors and the Load* pair listed on an api list for the first time
---

# Uploading textures from the CPU

Six constructors, one upload path. All of them take tightly packed pixels, build
a device-local image, generate a mip chain if asked, create a sampler and a
descriptor set, and return a `*Texture` ready to bind.

```go
// Colour: sRGB, mipmapped, repeat. Albedo, terrain detail, foliage cutouts.
albedo, err := r.CreateTexture(rgba, w, h)

// Numbers in eight bits: linear, mipmapped, repeat. Normal, roughness, occlusion.
normal, err := r.CreateDataTexture(rgba, w, h)

// Numbers at more than eight bits: a radiance table, a signed field.
// radiance is [][3]float32, w*h of them, row by row.
table := make([]uint16, w*h*4)
for i, v := range radiance {
    table[i*4] = renderer.Float16(v[0])
    table[i*4+1] = renderer.Float16(v[1])
    table[i*4+2] = renderer.Float16(v[2])
    table[i*4+3] = renderer.Float16(1) // 0x3c00; a plain 1 here would be 6e-8
}
lut, err := r.CreateTextureRGBA16F(table, w, h, renderer.TextureOptions{Filter: renderer.FilterLinear})

// One exact number per texel. elevations is []float32, w*h of them.
field, err := r.CreateTextureR32F(elevations, w, h, renderer.TextureOptions{})
```

Every one of them is released by
[`DestroyTexture`](models.md#releasing-anything-at-runtime-one-contract), which
is safe at any point in a frame, and counted in `ResourceCounts.Textures`.

## Which one

| Constructor | Format | Sampler | For |
|---|---|---|---|
| `CreateTexture` | `R8G8B8A8_SRGB` | linear, repeat, mipmapped | Colour: albedo, terrain detail, cutouts |
| `CreateDataTexture` | `R8G8B8A8_UNORM` | linear, repeat, mipmapped | Material maps: normal, metallic-roughness, occlusion |
| `CreateTextureLinear` | `R8G8B8A8_UNORM` | linear, clamp, no mips | MSDF atlases and anything wanting exact texel values |
| `CreateTextureNearest` | `R8G8B8A8_SRGB` | nearest, clamp, no mips | Pixel art |
| `CreateTextureRGBA16F` | `R16G16B16A16_SFLOAT` | `TextureOptions` | Four numbers per texel at 0.05 percent |
| `CreateTextureR32F` | `R32_SFLOAT` | `TextureOptions` | One exact float32 per texel |

The four eight-bit constructors are the engine's oldest; the two wide ones arrived
with issue #178 and are the only part of this page that is new, so `since` above
is the family's rather than theirs.

The four eight-bit constructors take no options: they are named for their four
intents, and the names are the choice. The two wide ones take
`TextureOptions`, because there is no such short list — a radiance table, a
height field and a precomputed noise slab want different filters out of the same
format. Its zero value is nearest, clamp-to-edge and no mip chain, which is what
a lookup table wants.

`LoadTexture`, `LoadDataTexture`, `LoadTextureLinear` and `LoadTextureNearest`
are the same four over an `fs.FS`, decoding a PNG or JPEG first. There is no
`Load` for the wide formats: PNG and JPEG hold eight or sixteen bits of
*integer*, so there is nothing to decode into a float texture without a format
this engine does not read.

**sRGB is the one that fails silently.** It is a property of the image, not of
the sampler, and an sRGB image decodes to linear on every read. A flat normal
stored as 128 arrives as 0.216 instead of 0.502, so every normal on the surface
leans the same wrong way. Nothing errors, the validation layer says nothing, and
the result looks like a badly authored map. See
[`material-maps.md`](material-maps.md#data-textures-are-not-colour-textures).

## Eight bits is 2 to 13 percent, and no curve fixes it

A texture holding *numbers over a wide range* is the case the wide formats exist
for. `x/sky/lut` is the worked example: a sky radiance table whose values run
from four thousandths at midnight to 2.5 in the sun's halo.

Eight bits cannot hold that. Spreading the 255 levels with a `sqrt(v/3)` transfer
and squaring back in the shader — which is what that package did before these
constructors existed — measured, over the whole table against the model evaluated
at each texel:

| | `RGBA8`, `sqrt(v/3)` | `RGBA16F`, radiance |
|---|---|---|
| Worst relative error above 0.1 | 2.1 % | 0.049 % |
| Worst relative error above 0.01 | 6.5 % | 0.049 % |
| Worst relative error above 0.002 | 13.4 % | 0.049 % |
| Worst absolute error | 0.0105 | 0.00098 |
| 1024x64 texels | 256 KiB | 512 KiB |

The eight-bit figure is `2*(0.5/255)/sqrt(v/3)` and it is **the transfer and
nothing else**: any monotone curve spending 255 levels over a 0-to-3 range lands
within a few percent of it, so a different curve is not a fix. The half-float
figure is `2^-11 = 0.0488 percent`, half a step of a 10-bit significand, and it
is the same at every brightness because the format's error is *relative*. That is
the whole difference: the eight-bit table was worst in absolute terms at the top
of its range and worst in relative terms at the bottom, and this one is relative
everywhere.

Two consequences beyond the precision:

- **There is no ceiling.** The eight-bit table had a compiled-in 3.0 and `Bake`
  refused a palette that would exceed it, because a clipped table is a flat white
  patch where the sun is. Half-float reaches 65504, so a bright palette is a
  bright sky.
- **Interpolation happens in the right space.** The sampler filters in whatever
  the texture holds, so an encoded table's blends bowed toward the darker of two
  neighbours. Radiance blends in radiance.

### The same thing through a real sampler

The table above is the CPU arithmetic. `task widetex` is what a shader reads
back: a 256-entry ramp over 0.001 to 2.0 uploaded both ways, compared against an
`R32F` copy of itself, measured on an RX 7900 XTX on 2026-10-03.

| Worst relative error above | `RGBA16F` | `RGBA8`, `sqrt(v/3)` |
|---|---|---|
| 0.001 | 0.000482 | 0.168627 |
| 0.01 | 0.000482 | 0.058431 |
| 0.1 | 0.000482 | 0.018431 |
| 1.0 | 0.000380 | 0.005882 |
| anywhere | **0.000482** | **0.168627** |

350x apart, and **the shape is the point**: the half-float column is flat,
because that format's error is relative; the eight-bit one grows 29-fold from the
top of the range to the bottom (0.005882 to 0.168627), because a transfer's is
not. 0.000482
is `2^-11` less whatever the ramp's values happen to land on, so it is the format
and nothing else — and it is the same figure the unit tests measure on the CPU,
which is what says the sampler and the 8-bit readout add nothing of their own.

The readout is worth knowing before trusting the number. The capture is 8-bit, so
the probe writes the relative error at three gains (1000, 10, 1 in red, green and
blue) and the reader takes the finest channel that has not saturated — which
resolves down to 3.9e-6, where a single channel would have stopped at 1/255, four
times coarser than the claim being made.

`renderer/widetexture_test.go` and `x/sky/lut`'s `TestTheTableIsTheModel` are the
CPU halves of the same measurement; `widetexcheck -eightbit` is the break, which
routes the asserted claim through the eight-bit constructor and fails it.

## Half-float bits, not float32s

`CreateTextureRGBA16F` takes `[]uint16` of IEEE binary16 **bits**, four per texel
in RGBA order. `Float16` encodes a float32 into one and `Float16Value` decodes it
back.

```go
bits := renderer.Float16(1.5)        // 0x3e00
back := renderer.Float16Value(bits)  // 1.5
```

**This is the trap on this page.** A `[]uint16` of ordinary small integers
compiles, uploads and samples as values near zero, because 1 as half-float bits
is 6e-8. If a wide texture comes out black, check that something encoded it.

It is bits rather than float32s so that nothing is converted behind the caller's
back, and so that a caller who already holds halves does not have to widen them
to have them narrowed again. The cost is that encoding is the caller's, which is
why `Float16` is here: rounding, subnormals below 2^-14 and the overflow above
65504 are all places a hand-rolled conversion is wrong, and none of them show up
in the middle of a gradient.

`CreateTextureR32F` takes `[]float32` directly, one per texel, and a nearest
fetch at a texel centre returns the float32 that was written, bit for bit. That
makes it the reference to measure anything else against.

## Format support is checked on the device

Both wide constructors ask
`vkGetPhysicalDeviceFormatProperties` before uploading, and refuse rather than
proceeding:

- **`SAMPLED_IMAGE`.** Core Vulkan requires it for both formats with optimal
  tiling, so this cannot fire on a conformant device. It is checked anyway
  because the alternative to failing at the constructor is a descriptor write
  naming an image view the device never had to support. **This is why neither
  format appears in `Capabilities`** — a report would be a constant true that
  every game would still have to branch on.
- **`SAMPLED_IMAGE_FILTER_LINEAR`.** Mandatory for `R16G16B16A16_SFLOAT` and
  **not** for `R32_SFLOAT`. So `TextureOptions{Filter: FilterLinear}` on an
  `R32F` texture is a real refusal on real hardware, reported as an error rather
  than a sampler quietly demoted to nearest. `CreateRenderTarget` refuses the
  same thing for the same format.

## The extent must match the slice

```
texture: 16 half-floats for a 64x4 RGBA16F texture, want 1024 (4 per texel)
texture: 3 bytes of pixels for 2x2 at 4 bytes per texel, want 16
```

Every constructor checks this now, in the units the caller passed. It used to be
unchecked: the staging copy is a `copy`, so a short slice left the rest of the
image holding whatever the allocation came with and a long one was silently
truncated. Neither errors, neither trips the validation layer, and both sample —
a 2x2 texture built from three bytes was uploaded, counted and drawn with.

## Binding

A texture reaches a shader three ways:

| | Where |
|---|---|
| `MeshRef.Texture` / `MaterialRef` | set 0, binding 0, for a draw. See [`material-maps.md`](material-maps.md) |
| `SetShaderTexture(slot, tex)` | light-set bindings 7..10, `ShaderTextureSlots` of them, visible to every vertex and fragment stage. See [`render-targets.md`](render-targets.md) |
| `AppPassDesc.Reads` | set 2, bindings 0..3, for one application pass |

`SetShaderTexture` is format-agnostic: a GLSL `sampler2D` reads whatever the
bound image holds, so a wide texture goes into the same slot an eight-bit one
does with no change to the shader beyond what it does with the value.

## Mip chains

`Mipmap` and the three mipmapped eight-bit constructors build the full chain by
successive linear blits from level 0, and select anisotropic filtering with it —
the two want the same surfaces, tiling world textures seen at grazing angles. If
the format cannot be linearly blitted the chain silently falls back to one level,
which on desktop never happens for `R8G8B8A8`.

**That fallback is silent, and for `R32_SFLOAT` it can actually happen**, because
the bit it tests is the same optional `SAMPLED_IMAGE_FILTER_LINEAR` that the
linear-filter refusal above tests. A `TextureOptions{Mipmap: true}` R32F texture
on a device without it is not an error — the blit would be illegal, so the upload
builds one level and the sampler's `MaxLod` follows. `Filter: FilterLinear` on the
same texture *is* an error, so the two differ: an explicit filter request is
refused and a mip request is quietly honoured as far as it can be.

**A lookup table does not want one.** Every level below 0 averages whatever the
layout puts side by side, which for a 3D table packed into 2D is the next slice,
so a minified fetch would blend two unrelated parts of the function. Ask for it
only for a wide texture that is an *image*.

## Failure modes

- **A wide texture samples as black or near-black.** The `[]uint16` holds plain
  integers rather than `Float16` bits.
- **A data map lights wrong, like a badly authored map.** It went through
  `CreateTexture` instead of `CreateDataTexture`, so it is decoding from sRGB.
- **`texture: N ... want M`.** The slice and the extent disagree; see above.
- **`texture: Filter: format R32 Signed Float does not support linear sampling
  on this device`.** `R32F` linear filtering is optional in core Vulkan. Use
  `FilterNearest`, or `RGBA16F`, whose linear filtering is required.
- **A table bands visibly where the model is smooth.** Eight bits over a range
  wider than a couple of decades; this page's table is the measurement.
- **`out of pool memory` from `allocate descriptor set`.** Each texture takes a
  descriptor set out of the renderer's one pool and keeps it until
  `DestroyTexture`. See [`models.md`](models.md).
