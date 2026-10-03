---
id: render-targets
title: Application render targets, graphics and compute passes
summary: Allocate floating-point images, schedule graphics and compute work, and sample outputs through fixed shader bindings.
capability: rendering
status: experimental
api:
  - renderer.RenderTargetDesc
  - renderer.TargetFilter
  - renderer.TargetWrap
  - renderer.CreateRenderTarget
  - renderer.DestroyRenderTarget
  - renderer.RenderTarget.Texture
  - renderer.RenderTarget.Extent
  - renderer.AppPassDesc
  - renderer.CreateAppPass
  - renderer.DestroyAppPass
  - renderer.AppPass.SetEnabled
  - renderer.AppPass.SetDraws
  - renderer.AppPass.SetPushConstants
  - renderer.AppPass.SetParams
  - renderer.AppParamBytes
  - renderer.AppComputeDesc
  - renderer.AppComputeDesc.ReadsShadows
  - renderer.CreateAppCompute
  - renderer.DestroyAppCompute
  - renderer.AppCompute.SetEnabled
  - renderer.AppCompute.SetDispatch
  - renderer.AppCompute.SetPushConstants
  - renderer.AppCompute.SetParams
  - renderer.StorageBufferDesc
  - renderer.StorageBuffer
  - renderer.Capabilities
  - renderer.ErrCapabilityUnavailable
  - renderer.Renderer.CreateStorageBuffer
  - renderer.Renderer.UploadStorageBuffer
  - renderer.Renderer.DestroyStorageBuffer
  - renderer.SetShaderTexture
  - renderer.SetShaderTarget
  - renderer.SceneColor
  - renderer.SceneDepth
  - renderer.GPUTimings
  - renderer.Renderer.Stats
  - renderer.RenderStats
  - renderer.AppStats
  - renderer.AppStats.Pass
  - renderer.AppPassStats
example: examples/24-custom-passes
run: task example:24-custom-passes -- -timings
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-10-03 # per-pass uniform blocks (AppPassDesc.Params/SetParams, set 2 binding 12, #170), proved on hardware by `apppasscheck -params`; the include set listed per fragment, with the dependency-free group and volumetric_common.inc (#169); directional shadow sampling from compute; application submission counts per pass; storage buffers, sampler probes and explicit barriers; exported GLSL include set; Timed refused without device timestamps (#160)
---

# Application render targets, graphics and compute passes

Call these APIs on the renderer thread, normally from `Game.Init` or
`Game.Update`. The game supplies shaders and draws; the renderer owns images,
pass scheduling, synchronization, descriptor updates, resize and GPU lifetime.

```go
target, err := r.CreateRenderTarget(renderer.RenderTargetDesc{
    Name: "application field", Format: renderer.TargetR16F, Scale: 0.5,
})
if err != nil { return err }
pass, err := r.CreateAppPass(renderer.AppPassDesc{
    Name: "write field", Stage: renderer.StageBeforeScene, Target: target,
    Fullscreen: true, Vert: fullscreenSPV, Frag: fieldSPV, Timed: true,
})
if err != nil { return err }
if err := r.SetShaderTarget(0, target); err != nil { return err }
// Custom scene shaders now sample the field at light-set binding 7.
```

Formats are `TargetR16F`, `TargetRG16F`, `TargetRGBA16F`, `TargetR32F`, and
`TargetRGBA32F`. Both fixed `Width` and `Height` must be nonzero; otherwise
`Scale` must be positive and finite. A fixed extent ignores `Scale`.
`Depth: true` adds a single-sample depth attachment, cleared to reverse-Z zero
when its pass clears and retained when the pass loads. Newly allocated target
contents, including history, start at zero.

`Filter` selects both minification and magnification: `FilterNearest` (the
zero/default value) or `FilterLinear`. `Wrap` selects U and V addressing:
`WrapClampToEdge` (the zero/default value) or `WrapRepeat`. These options also
apply to storage and history targets. For a periodic field:

```go
field, err := r.CreateRenderTarget(renderer.RenderTargetDesc{
    Name: "periodic field", Format: renderer.TargetR16F, Width: 256, Height: 256,
    Filter: renderer.FilterLinear, Wrap: renderer.WrapRepeat,
})
```

Linear filtering requires the format's optimal-tiling
`SampledImageFilterLinear` feature. Creation checks it before allocating and
returns an error naming `Filter`, the format and target when unsupported.
Linear on a 32-bit float target (`TargetR32F` or `TargetRGBA32F`) fails at
create on devices without the feature; use 16-bit floats for fields you filter.
Unknown filter and wrap values return errors naming `Filter` or `Wrap`.

| Stage | Available inputs and destination |
|---|---|
| `StageBeforeScene` | After clouds and shadows, before scene geometry. Earlier application outputs and history; own targets only. |
| `StageAfterScene` | Resolved HDR and scene depth, before water copies the scene. Own targets only. |
| `StageBeforeBloom` | The completed HDR scene including water; scene depth and earlier outputs. Own target or loaded HDR. |
| `StageBeforeTonemap` | After scene bloom and the UI glow chain. Earlier outputs, HDR and scene depth. Own target or loaded HDR; changes here do not feed this frame's bloom. |

Graphics and compute passes at a stage interleave in creation order. The scene
input availability in every row applies to both kinds. Compute always writes
its own storage targets; only graphics can load the HDR destination.

Graphics passes at a stage execute in creation order. A nil `Target` writes the engine
HDR scene, requires `Load: true`, and is restricted to the last two stages.
`BlendAdditive` adds source and destination. `BlendAlpha` uses premultiplied
source-over. `BlendNone` replaces covered pixels. `DepthTest` requires a
target with depth, or nil `Target`; in the latter case it loads scene depth
without writing it. With MSAA, HDR passes load the scene's multisample colour
and resolve it back to HDR.

`Reads` supplies up to four `*Texture` inputs: `target.Texture()`,
`r.SceneColor()`, `r.SceneDepth()`, or an ordinary loaded image. Nil and
destroyed textures are rejected. Scene colour can only be read after the
scene and with an own destination target. Scene depth can only be read after
the scene. Ordinary textures need no graph resource declaration. Sampling and
writing the same target is rejected unless it has `History`. A slot naming
the current destination must likewise not be sampled by that pass unless the
target has history; shader source determines which global slots it uses.

`r.SceneColor()` returns a borrowed, stable texture for resolved HDR colour.
Its image, view and existing descriptor set follow the acquired swapchain
image. `r.SceneDepth()` enables a graphics node immediately after the scene
and returns a borrowed, stable texture for its output. Pass either texture
in `Reads` at the binding your shader expects. Allocation occurs at the next `DrawFrame`; do
not inspect its GPU descriptor before then. The node remains enabled for the
renderer lifetime. It stores the scene's reverse-Z depth in R32F: 1 is near,
0 is far/background. With MSAA it takes the maximum sample. It captures
opaque scene depth before water, whose pipeline does not write depth.
Scene depth always uses nearest/clamp sampling so reverse-Z values stay exact
instead of interpolating across geometry edges. Scene colour retains the HDR
sampler's existing linear/clamp behavior. Application target options affect
neither borrowed scene texture.

## Compute dispatches

```go
output, err := r.CreateRenderTarget(renderer.RenderTargetDesc{
    Name: "computed field", Format: renderer.TargetR32F, Scale: 1,
    Storage: true, History: true,
})
if err != nil { return err }
compute, err := r.CreateAppCompute(renderer.AppComputeDesc{
    Name: "update field", Stage: renderer.StageBeforeScene, Comp: computeSPV,
    Reads: []*renderer.Texture{target.Texture(), output.Texture()},
    Writes: []*renderer.RenderTarget{output}, Timed: true,
})
if err != nil { return err }
w, h := output.Extent()
compute.SetDispatch((w+7)/8, (h+7)/8, 1)
```

`RenderTargetDesc.Storage` adds storage-image usage without changing the resting
`ShaderReadOnlyOptimal` layout. Creation checks the device's optimal-tiling
storage format feature; an unsupported format returns an error naming that
format and target. `Writes` accepts up to four distinct, live targets, each with
`Storage`; a missing flag is an error naming the target. Reading an output in
the same pass requires `History`, with the sampled previous instance distinct
from the storage destination. Scene colour and depth can be sampled after the
scene but cannot be storage outputs. `Reads` follows the graphics input rules.

`SetDispatch` takes workgroup counts, initially zero. Any zero axis records no
compute commands; both timing edges still execute. `SetEnabled(false)` also
skips the node and its barriers. After a relative target resizes, update the
counts from `Extent()` and bounds-check the shader's global invocation IDs.
`SetPushConstants` uses the same 128 application bytes at offset 128 as graphics;
offsets 0–127 carry scene VP and an identity model. Compute's full push range
is visible to the compute stage. `AppComputeDesc.Params` adds a private uniform
block when 128 bytes is not enough; see
[a pass's own uniform block](#a-passs-own-uniform-block).

Compute set 0 binds the unused fallback texture set, set 1 binds the shared
light set, and set 2 has four combined samplers at bindings 0–3, four storage
images at 4–7 in `General`, four storage buffers at 8–11, and the dispatch's own
uniform block at 12 when `Params` declares one. In set 1, the application bindings 6–10 and the
directional shadow bindings 0 and 1 are visible to compute; engine bindings 2–5
(the point cube map and the clustered light buffers) remain fragment-only.
Unused sampled inputs hold the white fallback. Declare only the storage bindings
provided in `Writes`.

```glsl
layout(set=2, binding=0) uniform sampler2D input0;            // Reads[0..3]
layout(set=2, binding=4, r32f) uniform image2D output0;       // Writes[0..3]; format qualifier must match the target's format
layout(set=2, binding=12, std140) uniform Params { vec4 block[4]; } params; // only when Params > 0
layout(push_constant) uniform ApplicationPush { layout(offset=128) vec4 data[8]; } pc;
layout(local_size_x = 8, local_size_y = 8) in;
```

Use `r16f`, `rg16f`, `rgba16f`, `r32f`, or `rgba32f` to match the target.
The device enables extended storage-image formats when supported. Compute
executes on the existing graphics queue. There is no second queue and no async
compute. The graph derives graphics-to-compute and compute-to-graphics barriers;
a dispatch runs without a render pass.

### Directional shadows from compute

```go
compute, err := r.CreateAppCompute(renderer.AppComputeDesc{
    Name: "air scatter", Stage: renderer.StageBeforeScene, Comp: scatterSPV,
    Writes: []*renderer.RenderTarget{radiance}, ReadsShadows: true,
})
```

`ReadsShadows` declares that the shader samples the directional cascades at
**set 1 bindings 0 and 1** — the `ShadowData` block and the comparison
`sampler2DArrayShadow`. It adds the frame-graph edge from the cascade passes to
the dispatch, so their depth writes are made visible to the compute reads with
the cascade map in `DepthStencilReadOnlyOptimal` on both sides. Declare it
whenever the shader reads either binding. The descriptors are bound either way,
so a shader sampling them without declaring it reads whatever a neighbouring
pass's barriers happened to leave visible; a dispatch that leaves the field
false records exactly the stream it recorded before.

Only the directional cascades are reachable. Bindings 2–5 — the point cube map
and the three clustered light buffers — stay fragment-only, and a compute shader
that declares one of them is rejected by the validation layer with
`VUID-VkComputePipelineCreateInfo-layout-07988`.

```glsl
// Set 1 bindings 0 and 1 in a .comp, identical to the fragment declaration.
layout(set = 1, binding = 0, std140) uniform ShadowData {
    mat4 cascadeVP[2];
    vec4 nightGrade;
    vec4 skyPalette[6];
    vec4 volumetric;
} shadow;
layout(set = 1, binding = 1) uniform sampler2DArrayShadow shadowMap;
```

The lookup is the one `shaders/lighting.inc` performs: project the world position
by `shadow.cascadeVP[c]`, map XY into 0..1, take the first cascade whose
projection lies inside its frustum, and compare `proj.z` minus a bias. The
sampler is `CompareOpLessOrEqual` with linear filtering, so every tap is a
hardware percentage-closer fetch returning 0 (occluded) to 1 (lit). There is no
non-comparison view of the map.

**With shadows disabled the lookup reads exactly 1.0 — fully lit — and that is
the documented unshadowed value.** No dummy image and no second code path: the
cascade passes still run every frame and still clear each layer to depth 1.0,
and `SceneLighting.CascadeVPs` is then the zero matrix, so the projected position
is the cascade centre at depth 0 and the comparison answers 1.0. The compute and
fragment paths agree because they sample the same descriptor.

The dispatch must run after the cascade passes. Every `PassStage` does, because
the cascades are recorded before the first application stage; a declaration that
landed ahead of them is a graph compile error naming the node rather than a
silent read of the previous frame's layers. See
[frame graph](frame-graph.md#application-nodes).

The cascade map is not swapchain-sized, so a resize or rebuild leaves it and its
light-set descriptors in place; only the dispatch's own relative targets and
their input sets are replaced.
## Counted work

Every application draw is in `Stats()`. `RenderStats.DrawCalls`, `Instances` and
`Triangles` are submitted work wherever it came from, and `RenderStats.App` is
the application's share of the same numbers, so scene-only figures are the
subtraction:

```go
st := r.Stats()
sceneDraws := st.DrawCalls - st.App.DrawCalls
sceneTriangles := st.Triangles - st.App.Triangles
if p, ok := st.App.Pass("caustic atlas"); ok {
    log.Printf("%s: %d draws, %d triangles", p.Name, p.DrawCalls, p.Triangles)
}
```

The counting rule:

- A mesh draw counts as one draw call, one instance and the triangles its index
  count asks for — its vertex count when it is not indexed.
- A fullscreen pass counts as one draw call, one instance and one triangle. It
  is a real submission, and a postprocess chain reading as zero is how an
  expensive one stays invisible.
- A dispatch counts in `App.Dispatches` and nowhere else. It is not a draw and
  it has no triangles, so it is absent from `DrawCalls` and from `Triangles`. A
  dispatch with a zero axis records no command and so counts as nothing.
- A disabled pass keeps its row, reading zero, so a HUD line does not move when
  an effect is switched off.

The engine's own post-process triangles -- bloom, the tonemap, light shafts --
are not in these counters, and that asymmetry is deliberate: that chain is a
fixed cost of having a renderer and each stage already has a named GPU timer,
while an application pass is one the game created and can switch off.

`App.Passes` holds one row per application pass in frame-graph order — creation
order within a stage — and `App.Pass(name)` finds one by name. `CreateAppPass`
and `CreateAppCompute` reject a `Name` a live pass already holds, with an error
naming `Name`, so that lookup is unambiguous; destroying a pass frees its name
again. `App.Passes` is the renderer's own storage, refilled in place by each
`DrawFrame` rather than reallocated, so read it before the next frame or copy
what you keep.

## Application storage buffers

```go
buffer, err := r.CreateStorageBuffer(renderer.StorageBufferDesc{
    Name: "simulation data", Size: 4096, History: true,
})
if err != nil { return err }
if err := r.UploadStorageBuffer(buffer, initialBytes); err != nil { return err }
compute, err := r.CreateAppCompute(renderer.AppComputeDesc{
    Name: "update simulation", Stage: renderer.StageBeforeScene, Comp: computeSPV,
    Buffers: []*renderer.StorageBuffer{buffer},
})
```

`Buffers` takes up to four distinct live buffers owned by the renderer. They
bind as read/write storage buffers at **set 2, bindings 8–11**, independent of
sampled inputs and storage-image outputs. The graph declares StorageReadWrite
for each. Unprovided storage bindings must not be statically accessed by the
shader. Read-only shaders can use `readonly` with the same API binding.

```glsl
layout(set=2, binding=8, std430) buffer Simulation {
    vec4 values[];
} simulation; // Buffers[0]; bindings 9, 10 and 11 are the remaining slots
```

Size must be positive and within `MaxStorageBufferRange`. Contents start at
zero. Memory is device-local with storage, transfer-destination, vertex-buffer
and indirect-buffer usage. `UploadStorageBuffer` stages a whole buffer or a
prefix, preserving the tail, and initializes **both** history instances with
the supplied bytes. It waits for earlier graphics-queue access and for the
copy to finish before returning. Nil/foreign/destroyed buffers and oversized
uploads return errors; a zero-byte upload is a no-op on a live buffer.

`History` allocates two independent read/write copies and selects `frame % 2`
for each binding. Unlike a target's sampled Texture view, this API has no
separate previous-copy view: shaders read and write the selected copy, which
contains that slot's preceding result. The two copies start at zero. Fixed
buffer sizes and contents survive swapchain resize; only pass descriptors are
rebuilt. Descriptor updates remain fence-scoped.

Destroying a buffer destroys every referring compute pass, detaches it from
future graphs and defers the Vulkan objects past frames in flight. Destroy
is nil-safe and idempotent. Renderer shutdown releases any remaining buffers.
The public API currently exposes buffers to compute; the engine's GPU LOD
path demonstrates vertex-input and indirect consumption without exposing raw
Vulkan handles. A public shared-mesh-range API remains future work under #96.

`cmd/apppasscheck -buffers -churn -provoke-recreate -validate -frames 300`
uses all four bindings, alternating history flags, whole/prefix uploads and
buffer destruction that retires its compute user. The resulting visible
pattern must retain the existing contrast/blur checks. `task syncvalidate`
runs this check after its full example matrix.

## A pass's own uniform block

```go
pass, err := r.CreateAppPass(renderer.AppPassDesc{
    Name: "caustic atlas", Stage: renderer.StageBeforeScene, Target: atlas,
    Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: atlasSPV,
    Params: 256, // bytes, a multiple of 16, at most renderer.AppParamBytes
})
if err != nil { return err }
// Per frame, after the camera has moved:
if err := pass.SetParams(block[:]); err != nil { return err }
```

```glsl
layout(set = 2, binding = 12, std140) uniform Params {
    mat4 inverseVP;
    vec4 anchor[4];
} params;
```

`Params` is a uniform block **private to one pass**, at **set 2, binding 12** --
past the four pass-input samplers a graphics pass declares and past the storage
images and buffers a dispatch declares, so one binding number serves both kinds.
`AppComputeDesc.Params` is the same field with the same rules, visible to the
compute stage. The declared size must be a multiple of 16 and at most
`AppParamBytes` (4096); anything else is an error naming `Params`.

**0 is not a small block, it is no block.** A pass that declares none allocates
its descriptor set from a layout byte-identical to the one it always had, builds
the same pipeline, and makes the same per-frame descriptor writes. Declaring one
adds exactly two things: the uniform binding on a second layout, and one
descriptor write per frame naming that frame's buffer. It adds **no command** to
the recorded stream -- a declared block is a descriptor, not a draw.

**Why this exists next to the game's block.** `SetShaderParameters` is 4096
bytes at set 1 binding 6 and it is *the game's*: it replaces the whole block, so
a package the game imports cannot claim a slice of it without the game
hand-partitioning bytes between its own shaders and every package it uses. The
two are independent -- a pass can read both -- and the global one is unchanged.
See [application data for custom shaders](game-loop.md#application-data-for-custom-shaders).

**The slot rule is the same rule the global block follows.** `SetParams` writes a
staging copy on the CPU and nothing else. `DrawFrame` copies it into the frame
slot's own host-visible buffer *after* waiting on that slot's fence, so the
frame still in flight reads memory nothing has touched. The consequence worth
knowing: a block staged now is read by the next frame submitted, and the other
slot still holds what it was given -- which is why a block must be staged every
frame it should take effect, exactly as the push constants are.

Supply little-endian std140 bytes padded to a multiple of 16; the tail past what
you hand it is zeroed, and `nil` clears the whole block. The caller owns field
packing -- no Go struct layout or shader reflection is inferred. Oversized or
unaligned input returns an error naming the pass and leaves the previous value
intact. The block's buffers are allocated once at pass creation and mapped for
the pass's lifetime, so a frame that only calls `SetParams` allocates nothing,
on the CPU or on the device. Fixed sizes and contents survive swapchain resize.

**Budget.** Per pass, up to 4096 bytes; there is no shared pool and no
partitioning. 4096 is the size the renderer already proves every device it
starts on can address through one uniform descriptor, since the global block
requires the same range at startup. A pass that declares a block holds four
uniform-buffer descriptors in each stage it runs -- the texture set's unused
one, the light set's two, and this -- against Vulkan's guaranteed twelve.

`cmd/apppasscheck -params -frames 12 -validate` is the hardware check: a
fullscreen pass declaring a 64-byte block writes its own block out to a 16x1
target, one float per column, a second pass stretches it over the frame with the
sRGB encoding undone, and the capture is read back byte for byte. The last four
frames each stage a different generation of values and each is checked against
the generation that frame staged -- a block copied into the slot the GPU is
still reading publishes one generation late, and the check says so by name
rather than reporting a wrong number. `task custompasses` runs it.

## Fixed shader layouts

As with `ShaderSet`, these are fixed layouts, not reflected material layouts.
SPIR-V must obey the bindings, push offsets, vertex inputs and output formats.
Vulkan pipeline errors include the pass name. Replacing shaders does not
negotiate a different interface.

Set 0 binding 0 is the mesh draw's texture, or the white fallback when absent
or destroyed. Fullscreen passes use the fallback. It reuses the ordinary
texture layout and is visible to the fragment stage; its unused environment
UBO at binding 1 must not be read. Set 2 has four pass input samplers, visible
to vertex and fragment stages. Unused bindings hold the white fallback.

```glsl
layout(set=0, binding=0) uniform sampler2D drawTexture;
layout(set=2, binding=0) uniform sampler2D input0; // Reads[0]
layout(set=2, binding=1) uniform sampler2D input1; // Reads[1]
layout(set=2, binding=2) uniform sampler2D input2; // Reads[2]
layout(set=2, binding=3) uniform sampler2D input3; // Reads[3]
layout(set=2, binding=12, std140) uniform Params { vec4 block[4]; } params; // only when Params > 0
```

Set 1 is the shared shadow/light set. It is also set 1 for custom sky, static
lit, terrain and water shaders, and set 2 for skinned lit shaders. Bindings 0
and 1 are also visible to compute, as described under `ReadsShadows` above;
bindings 2-5 retain their engine layouts and fragment-only visibility. Use the
engine `lit.frag`/`lights.inc` layouts for their complete block declarations.

| Binding | Descriptor |
|---|---|
| 0 | `ShadowData` uniform buffer: cascades, night grade, palette, volumetrics; vertex, fragment and compute |
| 1 | Directional shadow `sampler2DArrayShadow`; fragment and compute |
| 2 | Point shadow `samplerCube`; fragment |
| 3 | Light storage buffer; fragment |
| 4 | Cluster grid storage buffer; fragment |
| 5 | Cluster index storage buffer; fragment |
| 6 | Application std140 uniform buffer, 4096 bytes; vertex, fragment and compute |
| 7â€“10 | Four application combined image samplers; vertex and fragment |

```glsl
// Application-pass set 1; change set to 2 for a skinned lit shader.
layout(set=1, binding=0, std140) uniform ShadowData {
    mat4 cascadeVP[2];
    vec4 nightGrade;
    vec4 skyPalette[6];
    vec4 volumetric;
} shadow;
layout(set=1, binding=1) uniform sampler2DArrayShadow shadowMap;
layout(set=1, binding=2) uniform samplerCube pointShadowMap;
struct GpuLight { vec4 posRange; vec4 color; vec4 dirCone; vec4 params; };
layout(set=1, binding=3, std430) readonly buffer LightBuffer {
    uvec4 grid; vec4 zParams; vec4 screen; uvec4 flags; GpuLight lights[];
} lb;
struct Cell { uint offset; uint count; };
layout(set=1, binding=4, std430) readonly buffer ClusterGrid { Cell cells[]; } cg;
layout(set=1, binding=5, std430) readonly buffer LightIndices { uint indices[]; } li;
layout(set=1, binding=6, std140) uniform ApplicationParameters {
    vec4 values[256];
} application;
layout(set=1, binding=7) uniform sampler2D applicationTexture0;
layout(set=1, binding=8) uniform sampler2D applicationTexture1;
layout(set=1, binding=9) uniform sampler2D applicationTexture2;
layout(set=1, binding=10) uniform sampler2D applicationTexture3;

layout(push_constant) uniform ApplicationPush {
    layout(offset=0) mat4 viewProjection; // engine SceneLighting.VP
    layout(offset=64) mat4 model;         // RenderObject.Model; fullscreen identity
    layout(offset=128) vec4 data[8];      // application-owned 128 bytes
} pc;
```

`SetPushConstants` copies little-endian bytes, at most 128, padded to a multiple
of 16. It zeroes the unused tail; nil clears the application block. Invalid
sizes return an error without changing previous data. The full 256-byte push
range is visible to vertex and fragment stages.

Fullscreen shaders use `gl_VertexIndex` to generate a triangle and declare no
location vertex inputs. Mesh shaders use `Vertex`: locations 0 position
(`vec3`), 1 colour (`vec3`), 2 normal (`vec3`), 3 UV (`vec2`). A shader can use
a subset. Creation rejects a `Fullscreen` flag inconsistent with these inputs.

For mesh passes, `SetDraws` retains the supplied `[]RenderObject`; keep it alive
through `DrawFrame`. Only `Mesh`, `Model` and `Texture` are used. `MVP` is
ignored because the engine supplies VP separately. `Texture` always supplies
set 0 binding 0 using its existing descriptor set; pass inputs at set 2 stay
independent. A mesh pass with no draws still begins
and ends dynamic rendering, including a requested clear. Fullscreen passes draw
one triangle; supplying mesh draws to one is an error at `DrawFrame`.

### Reaching the engine's shared GLSL

A pass shader that wants to light, fog or grade the way the rest of the frame
does needs the engine's shared GLSL, not a reimplementation of it. It is embedded
and exported by `github.com/derekmwright/glyphengine/shaders/include`: write the
set to a directory and compile with `glslc -I` against it, naming the fragments
bare (`#include "lighting.inc"`). That package's doc comment carries the build
step. The layouts above are what the includes expect the including file to have
declared already, and `lighting.inc` additionally needs `LIGHT_SET` `#define`d.

The set, and what each fragment expects you to have declared first:

| Fragment | Needs declared first |
|---|---|
| `srgb.inc`, `atmosphere.inc`, `bloom.inc`, `lod_coverage.inc`, `volumetric_common.inc` | nothing |
| `lights.inc` | `LIGHT_SET` |
| `volumetric.inc` | `LIGHT_SET`, `pc`, the `shadow` UBO |
| `lighting.inc` | those, plus `shadowMap` and `pointShadowMap` |
| `material_shading.inc`, `grass_fragment.inc` | everything `lighting.inc` wants, the material bindings and the vertex inputs — these are fragment bodies, not helpers |

The first row is measured rather than read off the sources: each of those five
compiles on its own behind nothing but a `#version` line and an output.

**A helper that binds to nothing belongs in the first group, not buried in a
fragment that does.** `volumetric_common.inc` is there because of what the
second group costs: a pass that wanted only the volumetric march's start jitter
had to declare the clustered light buffers, the shadow UBO and a push block with
`cameraPos` to get at it, so `x/water` copied the function instead -- which is
the vendored-copy failure this export exists to remove. `x/internal/shaderinclude`
compiles `volumetric_common.inc` with nothing declared at all, with
`volumetric.inc` as the control that must still fail, so the line stays where it
is.

Do this at test or generate time, never at startup: `glslc` is an authoring-only
dependency, and compiling in a test is what makes a changed include fail at build
rather than at draw. `shaders/terrain.frag` is a worked preamble;
`examples/24-custom-passes/lit.frag` is the same thing from outside the engine's
own shader directory.

Never include a shared fragment by a relative path into the engine's source tree.
It works in a `git clone` and fails for everyone who ran `go get`. This is the
seam [ADR 0012](../adr/0012-an-x-module-for-opinionated-systems.md) exists to
provide, and [`x/README.md`](../../x/README.md) is the policy for packages
outside the engine module.

## History, resize and lifetime

`History` allocates two colour instances and alternates by frame-in-flight
index, independently of swapchain image acquisition. During a frame,
`Texture()` and every sampled reference select the previous frame's write,
while the attachment selects the other instance. Both start at zero. Disabling
a pass skips its whole graph node; frame indices continue to advance.

`*RenderTarget` and its `Texture()` pointer remain valid through resize.
Relative targets are reallocated on swapchain recreation and their history
starts over. Fixed-size images survive. Descriptors and Go-side rendering bindings are
rebuilt against replacement views. Use `Extent()` for the current dimensions.
Recreated target samplers retain `Filter` and `Wrap`, and the stable
`Texture()` is updated with a descriptor set referencing the new view and
sampler, including both history instances.

`SetShaderTexture` and `SetShaderTarget` record intent. They update only the
frame slot whose fence was waited on. Nil restores the fallback. Sampling an
unbound slot returns the fallback, not uninitialized data. Pass inputs follow
the same fence rule. A renderer resize waits for the GPU before replacing all
affected descriptors.

Destroy passes with `DestroyAppPass` or `DestroyAppCompute`, and targets with
`DestroyRenderTarget`.
Destroying a target also destroys passes writing it; other readers and slots
fall back to white. GPU destruction is deferred across frames in flight.
Descriptor sets retire before their owned image views. Undestroyed resources
are returned by `Renderer.Destroy`. The renderer owns scene colour and depth;
these borrowed views cannot be destroyed independently.

Creation/destruction marks the graph dirty. After the next frame fence wait,
the renderer compiles new declarations and rebuilds Go-side attachment bindings.
It creates no render-pass or framebuffer objects. Pipeline construction uses
formats and sample counts from the same compiled description immediately at
`CreateAppPass`. Explicit graph barriers handle attachment entry/exit, including
return to sampled layouts. See [ADR 0009](../adr/0009-execute-render-passes-with-dynamic-rendering.md).

`Timed` reserves one of 16 application timing entries shared by graphics and
compute. A seventeenth active
timed pass is rejected. `GPUTimings.App` contains `{Name, Ms}` in creation
order, with both timestamp edges written even for disabled passes. Existing
`Pass` values and `GPUTimings.Pass` retain their meaning. Timing slices are
renderer-owned views; copy them if retaining them across frames.

`Timed` also needs the device to timestamp the graphics queue. When it cannot,
`CreateAppPass` and `CreateAppCompute` refuse the descriptor with an error
wrapping `renderer.ErrCapabilityUnavailable` rather than creating a pass whose
row would never appear in `GPUTimings.App` — a missing row reads as a free
effect. Ask once instead of handling the error:

```go
timed := r.Capabilities().GPUTimestamps
```

That is the one optional capability an application pass needs today. Which
fallbacks the engine takes on its own and which it hands back is on
[`game-loop.md`](game-loop.md#what-the-device-granted-and-who-owns-the-fallback);
`Capabilities` also reports the negotiated MSAA count every pass pipeline is
built against.

Recording uses retained command scratch and allocates zero bytes per frame.
Creating resources and rebuilding the graph are setup work and can allocate.
Draws reuse existing texture sets; only the pass input set is allocated per
frame slot. Changing a draw count does not allocate descriptor sets.

The GPU check compares a pattern-modulated terrain quad plus a half-resolution
HDR/depth filter and additive composite against disabled passes. It requires
nonzero terrain pixels with at least 4/255 contrast. `-history` exercises
self-reading history; `-churn -provoke-recreate` replaces resources every 30
frames while resizing. `task determinism` repeats both ordinary and history
runs. The recording fixture pins 3382 calls with application passes against
3342 without them, and reports zero recording allocations at 7, 97 and 511
engine draws.

With `-compute`, a pre-scene dispatch reads the R16F pattern, applies a 3x3 box
blur and accumulates half the previous output in an R32F history target. The
custom lit shader samples that target. The edge probe must lie between the two
band interiors. Runs of at least 260 frames without churn/resize additionally
compare frames 2/60 (at least 4/255 visible change) and frames 200/260 (exact
convergence across the whole capture). `task determinism` repeats both compute and compute-plus-history
runs. Compute is included in churn and resize when the flag is set.

The graphics-plus-compute recording fixture pins 3390 calls against 3382 for
graphics alone: four compute commands, two incoming barrier groups, one layout
return barrier and one additional scene-input barrier. Both kinds together
still record zero allocations at 7, 97 and 511 engine draws.

`go run ./cmd/apppasscheck -shadowcompute -validate -provoke-recreate` is the
sampling gate for `ReadsShadows`, and it samples rather than merely creating a
pipeline. A half-resolution dispatch writes the cascade comparison of a known
world position per texel, a half-resolution fullscreen pass writes the same
lookup through the fragment path, and a comparison pass reports both plus their
difference at 255x, so one captured byte of blue is 1/65025 of disagreement. The
two must agree, the probe patch must be both shadowed and lit, moving the sun
must change what the dispatch read, and a shadows-off phase must read exactly 1.0
everywhere. The dispatch also reads a 4x4 LUT at set 2 and the application
uniform block at set 1 binding 6, whose product is reported in the capture's top
band — the white fallback texture is 1.0, so a lost LUT binding would otherwise
hide itself. `task custompasses` runs it and keeps the capture in
`.task/custompasses/shadowcompute.png`; `task validate`, `task syncvalidate` and
`task determinism` run it too.

`go run ./cmd/apppasscheck -filter -validate -provoke-recreate` writes four
known values into 2x2 R16F targets and samples fractional and out-of-range
coordinates into a 12x4 chart. It checks nearest/linear crossed with
clamp/repeat, plus `SetShaderTarget` sampling of a storage/history target.
The nearest/clamp row is a visible control. Captured values must be within
1/255 of the expected samples, both before and after recreation; fixed images
survive while relative samplers and descriptors are replaced. `task custompasses`
runs this check and keeps its chart in `.task/custompasses/filter.png`.

## Walkthrough

[`24-custom-passes`](../../examples/24-custom-passes/main.go) builds a plain
plaza with three shadow-casting boxes. All effect policy lives in the example;
copy its shaders and these five steps into a game:

1. **Accumulate a light pattern.** `CreateRenderTarget` allocates a half-size
   `TargetR16F` image. `CreateAppPass` schedules a mesh pass at
   `StageBeforeScene` with `BlendAdditive`; `SetDraws` retains three quads whose
   models sweep in world XZ. Their soft edges and overlaps sum into the image.
2. **Smooth it over time.** A second `CreateRenderTarget` requests `TargetR32F`,
   `Storage: true`, and `History: true`. `CreateAppCompute` is created after the
   pattern at the same stage, reads the pattern and its own previous image,
   and writes an exponential average. Each `LateUpdate` uses `Extent()` and
   `SetDispatch((w+7)/8, (h+7)/8, 1)` and supplies the blend factor through
   `SetPushConstants`. The shader bounds-checks every invocation. A pending
   window resize also contributes to the dispatch bounds; history resets on
   recreation.
3. **Light the scene with it.** `renderer.DefaultShaders()` supplies the existing
   shader set; replace only `LitFrag` and pass it through `glyph.WithShaders`.
   `SetShaderTarget(0, smoothed)` binds the field at set 1, binding 7. The copied
   lit shader adds a world-XZ lookup and modulates only ground lighting,
   retaining the engine's shadows. Because it is a history target, scene
   sampling sees the previous write, one frame behind the current compute.
4. **Scatter and composite fog.** `SceneColor()` and `SceneDepth()` become
   `Reads` for a half-size `TargetRGBA16F` fullscreen `CreateAppPass` at
   `StageBeforeBloom`. Inverse VP reconstructs distance from reverse-Z; the
   light colour and scene colour supply the scattered light. A second
   `CreateAppPass` uses `Target: nil`, `Load: true`, and `BlendAdditive` to add
   that target to HDR. Its four low-resolution neighbours are weighted by
   similarity to full-resolution scene depth, retaining silhouettes. A shallow
   fog bank touches the horizon and leaves the upper sky clear.
5. **Measure the work.** Every descriptor sets `Timed: true`. With `-timings`,
   `ResetGPUTimings()` drops the first 30 frames and `MeanGPUTimings().App`
   prints the four named graphics/compute brackets when the run ends.

`-passes off` creates no application targets or passes and uses the ordinary
lit shader, giving the same scene as a control. `-frames` and `-screenshot`
work as in the other examples; `GLYPHENGINE_FIXED_FRAME_TIME=16.667ms` makes the
fixed-tick rectangle paths and temporal smoothing repeatable.

`task custompasses` captures frame 120 twice with passes on and once with them
off. It checks a visible ground change (at least 8/255 in 30000 pixels), a
visible fog band, unchanged upper sky, and byte-identical repeat captures. It
also requires silence from validation through three swapchain recreations.
The captures remain in `.task/custompasses/` for inspection. The renderer
releases all example resources at shutdown; effects removed earlier should
use `DestroyAppPass`/`DestroyAppCompute`, then `DestroyRenderTarget`.
