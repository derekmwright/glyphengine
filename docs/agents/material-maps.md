---
id: material-maps
title: Normal, roughness, occlusion and emissive maps
summary: >
  Give a surface per-pixel relief, roughness and emission with a Material,
  instead of one uniform material per object.
capability: rendering
status: stable
since: v0.4.0
api:
  - renderer.Material
  - renderer.MaterialOptions
  - renderer.Renderer.CreateMaterial
  - renderer.Renderer.DestroyMaterial
  - renderer.Renderer.CreateDataTexture
  - renderer.Renderer.LoadDataTexture
  - renderer.RenderObject.Material
  - renderer.ModelMesh.Material
  - glyphengine.MaterialRef
assets: procedural
example: examples/16-materials
run: go run ./16-materials
verified: 2026-08-02
---

# Material maps

```go
albedo, _ := r.LoadTexture(assets, "assets/tiles_albedo.png")
normal, _ := r.LoadDataTexture(assets, "assets/tiles_normal.png")
rough, _ := r.LoadDataTexture(assets, "assets/tiles_mr.png")
ao, _ := r.LoadDataTexture(assets, "assets/tiles_ao.png")

mat, err := r.CreateMaterial(renderer.MaterialOptions{
    Albedo:            albedo,
    Normal:            normal,
    MetallicRoughness: rough, // glTF packing: roughness in G, metallic in B
    Occlusion:         ao,    // in R
})
if err != nil {
    return err
}

e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: mesh, Roughness: 0.55})
e.C.MaterialRef.Set(ent, &glyph.MaterialRef{PBR: mat})
```

Every map is optional. A `MaterialOptions` with only `Albedo` set renders
identically to `MaterialRef{Texture: albedo}` — the shader is told which maps
exist and skips the rest, so the unmapped case is not merely close to the plain
lit path, it is the same arithmetic.

## What this buys over an albedo texture

`MaterialRef.Texture` varies colour per pixel while the surface still lights as
one perfectly smooth material. That is why a tiled floor with only an albedo map
reads as a photograph of a floor: the geometry is flat and the lighting knows it.

A `Material` varies the *shading normal* and the *roughness* per pixel as well,
so light grazing across the surface catches relief that is not in the geometry.
`examples/16-materials` puts the two side by side; the leftmost panel is albedo
only.

## Data textures are not colour textures

Normal, metallic-roughness, and occlusion maps hold numbers. They must be
uploaded with `CreateDataTexture` / `LoadDataTexture`, not `CreateTexture` /
`LoadTexture`.

**This is the failure mode to watch for.** sRGB is a property of the image, not
of the sampler, and an sRGB image decodes to linear on every read. A flat normal
stored as 128 arrives as 0.216 instead of 0.502, so every normal on the surface
leans the same wrong way. Nothing errors, the validation layer says nothing, and
the result looks like a badly authored map rather than a loading bug.

`LoadTexture` on a normal map is the single easiest way to get this wrong.

## Per-object factors still apply

`MeshRef.Metallic` and `MeshRef.Roughness` are not replaced by a
metallic-roughness map — the map multiplies them, which is glTF's rule:

```
roughness = MeshRef.Roughness * texture(mr).g
metallic  = MeshRef.Metallic  * texture(mr).b
```

So a material with no metallic-roughness map still honours the per-object
values, and one with a map uses them as an overall trim. A `MeshRef.Roughness`
of zero is treated as unset and becomes 0.5, as it always has.

## Occlusion applies to ambient only

The occlusion map scales the ambient term and nothing else. Baked occlusion
describes how much of the sky a crevice can see, so it has no business dimming a
light that can actually reach the surface. Applying it to direct light is what
makes AO-mapped geometry read as dirty rather than as shaped.

`OcclusionStrength` blends toward no occlusion; zero means one, matching glTF's
default. To switch occlusion off, leave `Occlusion` nil.

## Emission is not lighting

`EmissiveFactor` is radiance the surface adds regardless of what reaches it. It
takes no shadow and no occlusion — a surface that emits does so whether or not a
light can see it, and baked AO describes how much ambient arrives, not how much
leaves. Fog still applies, because the air between the surface and the eye
scatters emitted light like any other.

```go
mat, _ := r.CreateMaterial(renderer.MaterialOptions{
    Albedo:           albedo,
    Emissive:         glowMap,               // optional; multiplies the factor
    EmissiveFactor:   [3]float32{0.35, 0.75, 1.0},
    EmissiveStrength: 6,                     // above 1 on purpose
})
```

`Emissive` multiplies `EmissiveFactor`, which is glTF's rule and the reason a
model that should glow but comes out black usually has a factor of zero rather
than a missing map. Author the map as a mask and put the colour in the factor;
putting the colour in both squares it.

**`EmissiveStrength` is the one place the engine deliberately produces values
above 1.** That only means something because the scene renders into a half-float
target — see [`hdr-tonemap.md`](hdr-tonemap.md). Under the default identity
curve a strength of 6 still clips to white, so `16-materials` selects extended
Reinhard to keep the colour. Zero means one, matching glTF.

It is also the only thing in the engine a bloom threshold can select — see
[`bloom.md`](bloom.md).

## No tangent attribute

The tangent frame is derived in the fragment shader from screen-space
derivatives of world position and UV, not from a vertex attribute. That is a
deliberate trade: a real tangent attribute is higher quality on heavily
distorted UVs, but it would touch every mesh builder, every vertex buffer, and
every pipeline's vertex input state. The derivative frame costs four derivatives
and reads the frame out of data the rasterizer already interpolates.

Two consequences:

- A face with **zero UV area** has no frame to derive. The shader detects this
  and falls back to the geometric normal rather than producing NaN, which would
  propagate through the lighting and paint garbage.
- Quality degrades on **badly stretched or mirrored UVs**. If a model looks wrong
  only in the places its UVs are distorted, this is why.

## Green channel convention

glTF and OpenGL store normal maps green-up; DirectX tools store them green-down.
The format records nothing about which was used, so a map from the wrong pipeline
looks lit-from-the-wrong-side along one axis but correct along the other. Set
`FlipGreen: true` for those.

## glTF

`LoadGLTF` reads `normalTexture`, `metallicRoughnessTexture`,
`occlusionTexture` and `emissiveTexture`, along with `normalTexture.scale`,
`occlusionTexture.strength`, `emissiveFactor` and
`KHR_materials_emissive_strength`, and returns a built `Material` on
`ModelMesh.Material`. It is nil when the glTF material has none of those maps and
no emissive factor, so existing models stay on the plain textured path unchanged.

`KHR_materials_emissive_strength` is not registered with the decoder, so it
arrives as raw JSON and is parsed by hand. Every failure path returns 1, glTF's
default: a malformed extension should make a material look like one without the
extension, not delete its emission.

Emissive textures go through the **sRGB** path, unlike normal, roughness and
occlusion. Emission is a colour.

```go
model, _ := r.LoadGLTF(assets, "assets/rock.gltf")
for _, m := range model.Meshes {
    ent := e.Spawn()
    e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: m.Mesh, Metallic: m.Metallic, Roughness: m.Roughness})
    // PBR when the model brought maps, Texture when it did not.
    e.C.MaterialRef.Set(ent, &glyph.MaterialRef{PBR: m.Material, Texture: m.Texture})
}
```

The loader decides sRGB versus linear per image by walking the document's
materials first, because glTF only says which encoding an image wants indirectly
— through the slot a material binds it to.

## Limits

- **Skinned meshes ignore `Material`.** The skinned pipeline already spends set 1
  on joint matrices, so a material's set 0 has nowhere to sit alongside them
  without a fourth descriptor set and a skinned material shader. A skinned draw
  with a material falls back to `Texture`.
- **Terrain ignores `Material`.** The terrain splat pipeline spends all four of
  its set-0 bindings on albedo already; it needs its own arrangement.
- **64 materials per renderer** (`maxMaterials`). Each takes a descriptor set,
  four combined image samplers, and a uniform buffer out of the pool. Exceeding
  it makes `CreateMaterial` return an allocation error rather than corrupting
  anything. `LoadGLTF` caches by glTF material index, so a model with twenty
  primitives sharing two materials allocates two.
- **`DoubleSided` is supported**, via a second pipeline — without it a material on
  a double-sided entity would silently lose its back faces.

## Teardown

`DestroyMaterial` releases the uniform buffer after the in-flight frames drain;
the descriptor set returns with the pool. Textures are **not** released — several
materials commonly share one albedo, and they outlive any single material. Call
`DestroyTexture` separately. `Renderer.Destroy` sweeps both, materials first.
