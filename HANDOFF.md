# Handoff — after PBR material maps

Transient note for the next session. Delete once the work below has landed.

## Where things are

Branch `celestial-depth-and-glow`, five commits ahead of `main`, **not pushed**.
`main` is untouched. Everything below is verified with `task ci` and
`task validate` (all 15 examples silent under the Vulkan validation layer).

```
c45a334  Stop the water surface faceting when waves outrun the grid
9dc6ade  Add normal, metallic-roughness and occlusion maps
5bf98e2  Unify texture upload behind options, and add data textures
cdd0a65  Drive the atmosphere's scattering from the sun, not the current light
2150cf2  Depth-test the starfield and put the celestial discs at the far plane
```

The branch name predates the material work; rename or split before pushing if
that matters.

## What landed

**Material maps.** `renderer.Material` binds albedo, normal, metallic-roughness
and occlusion at set 0 plus a per-material flags UBO, with its own pipeline (and
a double-sided variant). Attach with `MaterialRef.PBR`. The tangent frame is
derived from screen-space derivatives, so no vertex format changed. glTF reads
`normalTexture`, `metallicRoughnessTexture` and `occlusionTexture`. New example
`16-materials` generates all four maps at startup from one height field.
Full detail in [`docs/agents/material-maps.md`](docs/agents/material-maps.md).

**Two celestial depth bugs.** The starfield had no depth test, so stars drew over
anything reaching into the sky. The sun and moon billboards sat at a fixed 80
world units — nearer than most terrain — so the discs occluded it.

**The moon's orange halo.** `atmSunGlow` was fed `pc.sunDir`, which is the moon
from dusk to dawn, so the fixed warm ember tint hung a sunset on the moon at
midnight. `EnvironmentState.RealSunDir` now carries the actual sun, packed into
the free `fog.zw` push-constant slots.

**Water faceting.** Wave components below two vertices per wavelength now fade
out instead of aliasing into hard facets, and steepness is clamped against the
real `sum(Q*k*A)` rather than against a constant that never bounded it.

## Still open on materials

- **Skinned meshes ignore `Material`.** The skinned pipeline spends set 1 on joint
  matrices. Needs a fourth descriptor set and a skinned material shader.
- **Terrain ignores `Material`.** The splat pipeline already spends all four set-0
  bindings on albedo; it needs its own arrangement. This is the largest remaining
  piece of the original task.
- **64 materials per renderer** (`maxMaterials` in `renderer/texture.go`). Fine for
  examples, likely too low for a real game — the pool is sized at startup.
- The material path has **no shadow-side difference**: a normal-mapped surface
  still casts and receives shadows from its geometric normal, which is correct,
  but the normal-offset bias in `lit.vert` does not know about the map.

## Hard-won context — read before touching the renderer

- **Push constants are FULL: 256 of 256 bytes.** Layout is documented above
  `pushConstantSize` in `renderer/commands.go`. Several vec4s carry a scalar in
  their `w` because there is nowhere else: `sunColor.w` is the real sun's
  elevation, `pointColor.w` roughness, `ambient.w` metallic, `cameraPos.w` fog
  density, and `fog.zw` the real sun's horizontal direction. **There is nothing
  left.** Anything new per-frame needs a uniform buffer — that is what the
  material flags UBO is.

- **`pc.sunDir` is not the sun.** It is whichever body currently lights the
  scene, which is the moon at night. The real sun rides in `sunColor.w`
  (elevation) and `fog.zw` (horizontal); `atmSunDirFrom` in
  `shaders/atmosphere.inc` reassembles it. Driving the atmosphere from `sunDir`
  paints a noon sky at midnight *and* an orange halo on the moon — the same
  mistake twice, and both have now been made.
  **The clouds deliberately still use `pc.sunDir`**: they are moonlit at night
  and their palette accounts for it.

- **sRGB is a property of the image, not the sampler.** Normal, roughness and
  occlusion maps must go through `CreateDataTexture` / `LoadDataTexture`.
  Uploaded as colour, a flat normal of 128 arrives as 0.216 instead of 0.502 and
  every normal leans the same wrong way, silently. `LoadTexture` on a normal map
  is the easiest way to get this wrong.

- **Sampling `sceneColor` returns LINEAR values.** The frame is copied from an
  sRGB framebuffer and the sampler decodes on read. A brightness threshold picked
  by eye from sRGB numbers lands far too high. This cost real time on the light
  shafts: the sunset sun disc displays near white but reads 0.71, and an 0.80
  cutoff rejected it entirely.

- **Do not reach for ACES or any film curve.** Tried and reverted. Those are
  built for HDR input; nothing here emits above 1, so scene values sit between 0
  and ~0.7 — exactly where ACES *lifts*. It made a measured frame worse: 87% of
  pixels ended up inside one tenth of the range. Real tonemapping needs a
  floating-point render target first.

- **The washed-out look is content, not the engine.** Measured and disproved:
  cutting ambient 0.25→0.11 *and* the shadow floor 0.35→0.12 moved the histogram
  by 0.008. Do not "fix" the floors.

- **Everything at the far plane draws LAST**, after opaque geometry, depth-tested
  with `CompareOpGreaterOrEqual` — see `farPlaneDepthState` in
  `renderer/pipeline.go`, shared by the sky and the stars precisely because those
  two drifted apart. Depth is reversed and cleared to 0, which is the far plane,
  so `Greater` rejects them everywhere. This ordering is worth 12% and is what
  makes the raymarched clouds affordable.
  Corollary: anything standing in for a body at infinity must sit *just inside*
  the far plane — nearer occludes the world, and at the far plane exactly the sky
  wins the tie and erases it. That is `celestialDistance` in `app.go`.

- **Screenshot A/B diffs need a noise floor.** Clouds and waves animate on
  wall-clock elapsed, not frame count, so two runs at the same `-frames` differ.
  Measured: RMS luminance 0.0004 between identical runs of `09-water`. Anything
  smaller than that is drift. Diff two identical runs before trusting any
  comparison, and prefer numbers over stacked images.

- **`ecs.Store.Get` on an entity without the component returns not-ok**, so the
  write goes nowhere. Set the component before trying to modify it.

- **go-task's embedded shell does not honour `trap … EXIT`.** Put checks in Go
  tests instead — `shaders/verify_test.go` is the model, and it runs under
  `task ci` for free. It earned its keep this session: it caught `lit.frag`,
  `skinned_lit.frag` and `terrain.frag` going stale after an edit to
  `lighting.inc`.

- **`-novsync` benchmarking causes audible GPU coil whine.** Ask before using it,
  keep frame counts low, or prefer GPU timestamp queries.

- **The LSP in this workspace reports phantom errors** — undefined symbols for
  APIs that plainly exist, wrong function arities. It appears to resolve a stale
  snapshot of the package. `go build ./...` is the authority; ignore the
  diagnostics or you will chase nothing for a while.

## Verification

```
task ci                 # lint, build, test, race — the minimum
task validate           # all 15 examples, must be silent
task shaders:verify     # committed .spv match their GLSL (needs VULKAN_SDK)
task screenshots        # regenerate docs/images
```

`task validate` is not optional for renderer work.

Per `CLAUDE.md`: when adding a check for a bug, **break the fix and confirm the
check fails.** Every check added this session was verified that way, and the
verification is recorded in the test's own comment. One of them could not be
broken as intended — removing the baked vertex spacing from `WaterMesh` makes the
package fail to compile, because the variable goes unused, so the value had to be
corrupted instead to exercise the test.

## Other open threads

- **`task screenshots` has not been re-run.** `docs/images/` is stale with respect
  to the water change and has no `16-materials.png` yet, though the Taskfile
  entry for it exists.

- **Sliders in the examples, for live tuning.** Wanted, not started. The idea is
  that `09-water` should let you drag wave amplitude around rather than editing a
  constant and rebuilding, and the same for material and fog knobs.

  Less missing than it sounds. `ui.UIManager` already hit-tests in z-order,
  routes keyboard focus, reports whether the click was consumed so the game does
  not also act on it, and — the part that matters — holds `activeClickable`
  across frames until release, which is pointer capture. `Button`, `InputField`,
  `ProgressBar`, `Label` and `Panel` exist, and `ui/yamlui` binds a declarative
  tree to values.

  What is actually absent is one method. `Clickable` has `OnMouseDown`,
  `OnMouseUp` and `SetPressed` but nothing that tells a captured widget where the
  mouse moved to, so no widget can track a drag. A `Draggable` interface with
  `OnMouseMove(mx, my)`, called on `activeClickable` each frame in
  `HandleInput`, plus a `ui/slider.go`, is the whole job.

  Which knobs would feel good is decided by where the value lives, and that is
  worth knowing before building the UI rather than after:

  - **Free to drag** — anything packed per-draw into push constants. Water's
    `WaveAmplitude`, `WaveLength`, `WaveNoise`, `RefractStrength` and
    `AbsorptionDepth`; `MeshRef.Metallic` and `.Roughness`; fog density and
    height; cloud steps; light shafts; time of day. No upload, no rebuild.
  - **Needs a mesh rebuild** — water's `Resolution`, `Level`, `ShallowColor` and
    `DeepColor`. `WaterMesh` bakes those into the vertices (the colours ride in
    the Color and Normal attributes, depth and grid spacing in UV), so changing
    one means rebuilding and re-uploading. Fine for a button, poor for a drag.
  - **Needs a buffer rewrite that does not exist yet** — a material's
    `NormalScale`, `FlipGreen` and `OcclusionStrength`. `CreateMaterial` maps its
    32-byte UBO, writes once, and unmaps; there is no update path. Adding one
    means either per-frame-in-flight copies the way `JointBuffer` does it, or a
    `DeviceWaitIdle`, because the buffer may be in use by a frame still in
    flight. Small, but it is real work rather than a slider.

  So a first pass over the free list alone would cover wave amplitude, which is
  what prompted this.

- **Showcase scene for beauty shots.** Agreed but not started. Must live under
  `examples/` — that directory is its own module, so nothing in it ships to
  anyone running `go get`. Textures: ambientCG or Poly Haven, both CC0. Now that
  material maps exist, this is where they would earn their keep, and the splat
  weight map should be *generated* from the heightmap rather than shipped.

- **`renderer.BuildTerrainMaterial` — the four-texture terrain splat pipeline —
  is used by no example and has never run outside Glyphborne.** A showcase scene
  would exercise it.

- **Light shafts are weaker than the name suggests.** They read as a warm glow
  spreading from the sun rather than distinct rays; this sky has no bright region
  large enough to smear into one. Sharpening likely means brightening the sun's
  halo specifically for the effect to feed on.

- **`docs/images/` is 4744 KB of a 10283 KB engine module** — 46% of what every
  consumer downloads is README screenshots. Wants a hosting or `.gitattributes`
  decision, and it gets worse with each example added.

- **`14-audio` deliberately deferred.** Audio is the only subsystem with neither
  an example nor a test, and it is the cgo path. Two open questions: `task smoke`
  runs every example, and an audio example will try to open an output device on a
  machine that may not have one; and there is no sound asset, so a procedurally
  generated WAV at startup is probably the right answer.

- **Launch polish still outstanding:** `task doctor` (separates "your C toolchain
  is broken" from "your GPU is broken"), `assets:verify`, and the lavapipe
  software-Vulkan CI job.

- **Known wart:** fullscreen always takes the primary monitor; there is no
  monitor selection. Recorded in `docs/agents/windowing.md`.
