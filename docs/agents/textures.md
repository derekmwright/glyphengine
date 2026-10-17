---
id: textures
title: Uploading textures from the CPU
summary: >
  Turn pixels a game computed or decoded into a sampled image, in eight bits per
  channel for colour and data maps or in half- and single-precision float for
  numbers a shader needs at more than eight bits; or a volume of them into a
  sampler3D, with a mip chain supplied or generated and addressing on every axis.
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
  - renderer.Renderer.CreateTexture3D
  - renderer.TextureOptions
  - renderer.Texture3DOptions
  - renderer.Texture3DFormat
  - renderer.Texture.Dimensions
  - renderer.AppPassDesc.Reads3D
  - renderer.AppComputeDesc.Reads3D
  - renderer.Capabilities.MaxTexture3DExtent
  - renderer.Capabilities.VolumeMipGeneration
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
verified: 2026-10-17 # sampled volumes: CreateTexture3D, Texture3DOptions, the supplied and generated chains, the device checks, Reads3D and the sampler3D contract, the two Capabilities fields and task tex3d (#190); the wide-format constructors, TextureOptions and the half-float transfer (#178); the eight-bit constructors and the Load* pair listed on an api list for the first time
---

# Uploading textures from the CPU

Seven constructors, one upload path. All of them take tightly packed pixels,
build a device-local image, generate a mip chain if asked, create a sampler and
a descriptor set, and return a `*Texture` ready to bind. Six make a 2D image;
the seventh, [`CreateTexture3D`](#volumes), makes a volume for a `sampler3D`.

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
| `CreateTexture3D` | `R8_UNORM`, `R8G8B8A8_UNORM` or `R16G16B16A16_SFLOAT` | `Texture3DOptions` | A volume: a cached density field, a 3D lookup table, a slab of baked noise. [Below](#volumes) |

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
| `SetShaderTexture(slot, tex)` | light-set bindings `7+slot`, four by default; `WithShaderTextureSlots` extends the layout at construction. Vertex, fragment and compute stages. See [`render-targets.md`](render-targets.md) |
| `AppPassDesc.Reads` / `AppComputeDesc.Reads` | set 2, bindings 0..3, for one application pass or dispatch. The only way for a [volume](#volumes), with its index in `Reads3D` |

`SetShaderTexture` is format-agnostic: a GLSL `sampler2D` reads whatever the
bound image holds, so a wide texture goes into the same slot an eight-bit one
does with no change to the shader beyond what it does with the value. It is not
shape-agnostic: a `sampler2D` cannot read a 3D view, and every slot but a pass's
own `Reads` is declared `sampler2D` by the engine, so the first two rows refuse a
volume by name.

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
only for a wide texture that is an *image* -- or upload the table as a
[volume](#volumes), whose levels average neighbours in all three axes.

## Volumes

```go
// density is []byte, w*h*d of them: x fastest, then y, then z.
field, err := r.CreateTexture3D(density, w, h, d, renderer.Texture3DOptions{
    Format: renderer.Texture3DR8,
    Filter: renderer.FilterLinear,
    WrapX:  renderer.WrapRepeat, // tiles across the layer
    WrapY:  renderer.WrapRepeat,
    WrapZ:  renderer.WrapClampToEdge, // a floor and a ceiling
    Mipmap: true,
})
if err != nil { return err }

march, err := r.CreateAppPass(renderer.AppPassDesc{
    Name: "cloud march", Stage: renderer.StageBeforeScene, Target: clouds,
    Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: marchSPV,
    Reads:   []*renderer.Texture{field, r.SceneDepth()},
    Reads3D: []int{0}, // Reads[0] is declared sampler3D; Reads[1] sampler2D
})
```

```glsl
layout(set = 2, binding = 0) uniform sampler3D density;   // Reads[0]
layout(set = 2, binding = 1) uniform sampler2D sceneDepth; // Reads[1]

float d = texture(density, p * invExtent).r;        // trilinear, level chosen by the derivatives
float coarse = textureLod(density, p * invExtent, 2.0).r; // the 4x4x4 box mean
```

This is the resource a volumetric march caches its field in: `x/sky/clouds.frag`
evaluates several octaves of procedural value noise at every step of every ray,
and a game that bakes that noise once into a volume reads one texel back with
the hardware interpolating between slices. The relative cost is the game's to
measure, not this page's to assume.

**`Format` is required.** `Texture3DR8` is one byte per texel, `Texture3DRGBA8`
four, and `Texture3DRGBA16F` eight: four `Float16` bit patterns, each as its two
little-endian bytes. There is no default because R8 and RGBA8 differ four times
in memory and in what a shader reads, and the zero value is refused rather than
guessed. One `[]byte` constructor for all three, where the wide 2D formats have a
typed constructor each, so that a mip chain can be one slice per level whatever
the format; the price is that half-float bits are packed by the caller, and the
trap on this page applies.

**`pixels` is `width*height*depth` texels in bytes**, x varying fastest, then y,
then z -- slice by slice. The three wrap fields are Vulkan's U, V and W, named
for the extent axes they govern. The zero value of each is clamp-to-edge, and the
zero `Filter` is nearest; a field a shader marches through wants `FilterLinear`,
because that is what makes the hardware interpolate between slices.

### The chain: supplied, or generated

```go
// Supplied: one slice per level, each extent halved and floored to one.
opts.Mips = [][]byte{level1, level2, level3} // a 64x32x8 base: 32x16x4, 16x8x2, 8x4x1
// Generated: 2x2x2 box means, by 3D linear blits on the device.
opts.Mipmap = true
```

Each level is the one above it halved on every axis and floored to one, so a
64x32x8 volume's chain is 32x16x4, 16x8x2, 8x4x1, 4x2x1, 2x1x1, 1x1x1: Z runs out
first and X keeps halving. A supplied chain may stop early, may not continue past
1x1x1, and has every level's byte count checked against that extent before
anything is allocated:

```
texture3d: Mips: level 2 holds 100 bytes for 16x8x2, want 1024 (4 bytes per texel)
texture3d: Mips: level 7 is past the 1x1x1 the chain ends at; 7 levels are the most a 64x32x8 volume has
```

A generated chain is built by successive linear blits, each level the 2x2x2 box
mean of the one above it; once an axis has reached one the blit's range on it is
one texel wide and the filter averages the two source slices it covers, so a
16x8x4 volume's level 2 (4x2x1) holds the mean of all four slices. `task tex3d`
reads both kinds back: `textureLod` at level 1 returns the supplied bytes for a
supplied chain and the box mean for a generated one, exactly.

**The generated chain is refused, not shortened, where the device cannot blit.**
The 2D path silently builds one level when the format cannot be linearly
blitted. A volume asked for `Mipmap` on such a device gets
`texture3d: Mipmap: format ... cannot be linearly blitted on this device; supply
Mips instead (Capabilities.VolumeMipGeneration)`, because a field whose coarse
levels are missing marches at full resolution everywhere and the only symptom is
the frame time. `Capabilities.VolumeMipGeneration` is that answer up front, so a
game can decide between the two at load time; core Vulkan requires the blit bits
for all three formats, so it is false only on a non-conformant device. `Mips` and
`Mipmap` together are refused.

**A volume's chain selects no anisotropic filtering**, where a 2D chain does.
Anisotropy is for a surface seen at a grazing angle; a volume is sampled along a
ray through its interior, and a sampler that picked a finer level along one axis
would make the level a shader reads at a point depend on the ray's direction.

### Checked on the device, before any allocation

In this order, each refusing with the message shown:

| Check | Message |
|---|---|
| Format set | `texture3d: Format: 0 is not a Texture3DFormat; choose ...` |
| Extent positive | `texture3d: 2x2x0 has no texels` |
| Extent within `maxImageDimension3D` | `texture3d: 8193x1x1 exceeds the device's 3D image limit of 8192 on every axis (Capabilities.MaxTexture3DExtent)` |
| Byte count of the base | `texture3d: 16 bytes of pixels for a 2x2x2 RGBA8 volume, want 32 (4 bytes per texel)` |
| Filter and wrap enums | `texture3d: WrapZ: unknown wrap 3` |
| Byte count and extent of every supplied level | above |
| `SAMPLED_IMAGE` for the format | `texture3d: format ... cannot be sampled on this device` |
| `SAMPLED_IMAGE_FILTER_LINEAR` when `Filter` is linear | `texture3d: Filter: format ... does not support linear sampling on this device` |
| `BLIT_SRC`, `BLIT_DST` and linear filtering when `Mipmap` | above |

`Capabilities.MaxTexture3DExtent` is the limit, at least 256 on any conformant
device and 8192 on the RX 7900 XTX. A refused upload creates nothing and counts
nothing; `renderer/texture3d_test.go` holds that for every row.

### Binding a volume: `Reads3D`

A volume binds through `AppPassDesc.Reads` or `AppComputeDesc.Reads`, at the same
set-2 bindings 0..3 a 2D read uses, and the pass lists its index in `Reads3D`
because the shader declares `sampler3D` there. The engine cannot read that
declaration off the SPIR-V, so the pass states it and creation checks it against
`Texture.Dimensions()` -- 3 for a volume, 2 for everything else -- in both
directions:

```
app pass "cloud march": Reads: index 0 is a 3D texture where the shader is declared 2D (Reads3D lists the sampler3D indices)
app pass "cloud march": Reads: index 1 is a 2D texture where the shader is declared 3D (Reads3D lists the sampler3D indices)
app pass "cloud march": Reads3D: index 2 names no read (Reads has 2)
```

Both directions are errors at creation rather than at the first frame, where the
validation layer reports `VUID-vkCmdDraw-viewType-07752` and nothing else does:
without the layer, a 2D texture behind a `sampler3D` samples as whatever the
driver makes of a 2D view read as a volume. `task tex3d` records exactly that --
with the check disabled, every probe of the mismatched row read the 2D texture's
own colour.

**The shared slots stay 2D.** `SetShaderTexture` refuses a volume, as do a
material's maps, a terrain material and an application mesh pass's draw
texture, each naming `sampler2D`. The one place the engine does not check is a
scene draw's `RenderObject.Texture` or `MeshRef.Texture`, which binds the
texture's own descriptor set per draw on the hot path; a volume there reaches
`lit.frag`'s `sampler2D` and the layer reports it at the draw. The shared slots
are declared `sampler2D` by the engine's own
stages -- lit, terrain, water, the sky slot's fillings -- and one slot layout
serves every pipeline, so a volume in a slot would be a 3D view behind a 2D
declaration in whichever engine shader next read that slot. A pass's `Reads`
has no such shared reader: the pass owns its shader and can say what it declared.

Per frame, a volume's binding is the same descriptor write a 2D read gets, into
the frame slot whose fence was waited on; replacing the volume behind a pass with
one of a different extent is a new pass over the new texture, and the old one
is released through `DestroyTexture` with the frames that sampled it still in
flight -- `task tex3d` does both three times over, under the layer, with a resize
in between. A volume is counted in `ResourceCounts.Textures` like any other.

### What `task tex3d` measures

`cmd/tex3dcheck` is the issue's acceptance: three volumes with a distinct value
per texel (8x8x8 RGBA8, 16x8x4 RGBA8, 8x4x4 R8), each uploaded with a supplied
two-level chain repeating on every axis, a generated chain repeating on every
axis, and the supplied chain clamping on Z; a graphics pass and a compute
dispatch each probe eleven points of each row into a chart, against a CPU
trilinear sampler over the same bytes. The points are chosen so every expected
value is a multiple of 1/255, which an 8-bit capture holds exactly: the stored
texel; the midpoint between two slices, two columns and two rows (the mean of
the pair, so Z interpolates as X and Y do); the edge on each axis, where repeat
blends the last texel into the first and clamp holds slice 0 and the last slice;
a quarter texel past the Z edge (three parts last slice, one part first); level
1 and level 2.

On an RX 7900 XTX on 2026-10-17, with validation on, all 198 probes read back
exactly -- a worst error of 0.000000 against a tolerance of 1.5/255 -- through a
replacement with frames in flight, a window resize and a swapchain rebuild, and
the eleven refusals above each reported their message. `-bind2d` is the break:
it binds a 2D texture at a `Reads3D` index, is refused at creation, and with that
refusal disabled it sampled `(1, 0, 1)` where `(0.188, 0.314, 0.439)` was
expected at every probe of the row while the layer reported
`VUID-vkCmdDraw-viewType-07752` and `VUID-vkCmdDispatch-viewType-07752` on every
frame.

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
- **`Reads: index N is a 3D texture where the shader is declared 2D`.** A
  volume went into `Reads` without its index in `Reads3D`; the reverse message
  is a 2D texture at a listed index. Both are the pass declaration and the
  texture disagreeing, and the fix is whichever of the two is wrong.
- **`shader texture: slot N: a 3D texture cannot fill a shared slot`.** A
  volume handed to `SetShaderTexture`. The shared slots are `sampler2D` in every
  engine stage; bind it through a pass's `Reads`.
- **`texture3d: Mipmap: format ... cannot be linearly blitted on this device`.**
  Check `Capabilities.VolumeMipGeneration` at load and supply `Mips` instead.
- **A volume samples as a flat colour, or validation reports
  `VUID-vkCmdDraw-viewType-07752`.** A 2D view is bound where the shader declares
  `sampler3D`. Creation refuses this when `Reads3D` is right, so the declaration
  and the shader disagree.
- **A volume's slices look transposed.** `pixels` is x fastest, then y, then z;
  a slab written z fastest uploads without an error and samples with its axes
  swapped.
