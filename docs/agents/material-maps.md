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
  - renderer.RenderObject.Material
  - glyphengine.MaterialRef
assets: procedural
example: examples/16-materials
run: go run ./16-materials
verified: 2026-09-21
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

Roughness changes the specular response, not the supplied normal. Ordinary lit
meshes now retain their normals above 0.9 too (issue #93); older builds biased
those normals toward world +Y. Games that compensated by using 0.9 can use their
intended roughness again. Very rough ordinary meshes may consequently look
darker on surfaces facing away from the light. The separate grass pipeline is
unchanged. `task normals` checks perpendicular, downward and lit swatches at
0.9, 0.98 and 1.0, with a lit control that rejects empty captures.
It also checks actual rotated planes whose shading and geometric normals agree.

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
target — see [`hdr-tonemap.md`](hdr-tonemap.md). Zero means one, matching glTF.

A strength of 6 still clips to white under the default identity curve, and with
bloom on that is fine: the halo carries the colour and the white core reads as a
bright light. Reaching for a tonemap curve to recover it costs midtone contrast
everywhere else in the scene — see the note in `hdr-tonemap.md`.

It is also the only thing in the engine a bloom threshold can select — see
[`bloom.md`](bloom.md).

## Skinned meshes

A `Material` works on an animated mesh exactly as on a static one — set it via
`MaterialRef.PBR` on an entity that also has a `SkeletonRef`.

An earlier version of this page, and the comment on `RenderObject.Material`,
said skinned draws ignored materials and that supporting them would need a
fourth descriptor set. That was wrong. A `Material` takes **set 0**, which is
where the plain texture already sat, so joints stay at set 1 and shadow at set 2
— the layout the skinned pipeline has always used. The only thing genuinely
missing was a fragment shader declaring the shadow set at 2 instead of 1.

`shaders/material_shading.inc` holds the shading both variants share;
`lit_material.frag` and `skinned_lit_material.frag` differ only in that
declaration. That the split changed nothing is checkable: extracting it left
`lit_material.frag.spv` byte-identical.

`examples/06-skinned` wraps its character's texture in a Material with a
generated normal map. Run it with `-plain` to compare.

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

## KHR_texture_transform: tiling, rotation, offset

A Blender ground plane tiled with a Mapping node (the ordinary way to tile a
texture) exports UVs left at 0..1 and the tiling itself as
`KHR_texture_transform` on the texture reference — `LoadGLTF` and
`LoadGLTFSkinned` now read it and **bake it into the primitive's vertex UVs
at load**, so nothing changes at draw time and no shader gained a UV matrix.
There is no API for this — it is automatic, unlike `AlphaMode`
(`docs/agents/models.md`), because there is nowhere for a game to opt out
usefully: an unbaked tiled ground is simply wrong.

Baking was the only option with teeth: the push-constant block is already at
its 256-byte guaranteed minimum (see `createDescriptorSetLayout`'s comment in
`texture.go`), so a per-material UV matrix had nowhere to live. Each glTF
primitive decodes to its own vertex copy even when several primitives split
from one doc mesh share an accessor — `modeler.ReadTextureCoord`'s `nil`
destination buffer allocates fresh every call — so baking per primitive
cannot leak between them.

Measured against a real Blender 5.0.1 export: a Mapping node scaled 8x8
exports as `{"offset":[0,-7],"scale":[8,8]}` (rendered and rigorously
checked — 8 columns counted by scanning marker-colour runs across a render,
not eyeballed); a 30-degree rotation exports as `"rotation":0.5235987...`
(exactly 30° in radians, sign preserved) with a non-obvious nonzero offset
even though the Mapping node's own Location was left at zero — Blender's
own V-flip on export, not a bug in the formula, and the formula does not
need to know why, only to apply translation·rotation·scale in that order.

**One transform per primitive.** When maps on the SAME material carry
different transforms, baking cannot satisfy all of them: the base colour
texture's transform wins (or the first present, in order: base colour,
normal, metallic-roughness, occlusion, emissive), and the loader logs ONE
line naming the material and which maps disagree. Measured to be the
uncommon case: a material with base colour and normal fed from ONE Mapping
node (Blender's ordinary setup) exports the byte-identical
`KHR_texture_transform` object on both texture references.

**`texCoord` override.** The extension can name a UV set other than the
texture's own `texCoord` — this engine reads `TEXCOORD_0` only (no second UV
set), so a map whose extension targets anything else is skipped as a bake
candidate (logged once) rather than silently baked into data it does not
use.

## Sampler wrap modes

glTF's sampler carries `wrapS`/`wrapT` per axis; `LoadGLTF`'s image upload
now honours them (clamp-to-edge, mirrored-repeat, or glTF's repeat default)
instead of hardcoding repeat. `renderer.Texture`'s address mode was already
per-call data (`textureOptions.addressU`/`addressV` in `texture.go`); this
is the glTF loader choosing it from the document instead of always passing
repeat.

**One wrap mode per IMAGE, not per (image, sampler) pair.** `loadGLTFImages`
uploads one engine `Texture` per glTF `Image`, a design this predates and
does not widen — glTF itself binds a sampler per `Texture` (an image+sampler
pair), so an image referenced by two glTF Textures with different samplers
can only be uploaded with one. The first (`doc.Textures` order) wins; the
loader logs once per image when this actually happens. Measured to be a real
case, not hypothetical: Blender's exporter deduplicates identical image
*content* into one glTF Image even across separate texture nodes, so two
materials that both reference the same source PNG with different wrap
settings (e.g. one tiled, one clamped) collide here by default.

## Loading a level: alpha and tiling arrive together

`examples/22-level`'s `spawnPrimitive` shows both this page's tiling and
`docs/agents/models.md`'s `AlphaMode` together: `MaterialRef.PBR` when
`LoadGLTF` built a `Material`, `.Texture` for a plain base colour, and
`Translucent` for a `BLEND` material — three independent, composable reads
of the same `ModelMesh`, none of which required the others.

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

`DestroyMaterial` releases the uniform buffer after the in-flight frames drain,
and the descriptor set goes back to the pool in that same deferred step — it
names the buffer, so it has to stop naming it first. (Until issue #82 the set
was never returned at all — the pool carried no
`VK_DESCRIPTOR_POOL_CREATE_FREE_DESCRIPTOR_SET_BIT` — so materials created and
destroyed while a game ran spent the pool's fixed budget permanently.
`docs/agents/models.md` has the measurement that found it.) Textures are **not** released — several
materials commonly share one albedo, and they outlive any single material. Call
`DestroyTexture` separately. `Renderer.Destroy` sweeps both, materials first.
