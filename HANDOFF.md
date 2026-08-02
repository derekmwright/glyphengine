# Handoff — materials, input, and instrumentation

Transient note for the next session. Delete once the work below has landed.

## Where things are

Branch `celestial-depth-and-glow`, **not pushed**, `main` untouched. The branch
name is from its first two commits and now describes about a tenth of what is on
it; rename before pushing.

Everything below is verified with `task ci` and `task validate` (all 16 examples
silent under the validation layer).

```
9af2ddb  Thin distant grass, and count what the frame submits
4dc1926  Add task bench
8d178f9  Time the CPU side of the frame, including the waits
f7cbb18  Cut frame time 15% with a four-tap PCF and front-to-back grass
7e36f49  Measure per-pass GPU time with timestamp queries
9cdec58  Take input out of the six examples that are not about input
acc94de  Add example 17-input and the input capability page
270a43a  Give both cameras a thumbstick look path
692e7e4  Let a thumbstick's deflection scale walking speed
9b6a4c5  Add the action mapping layer
d04635e  Add gamepad support to the input package
ecad49d  Note the slider idea in the handoff, with what it would actually take
0d3a7b6  Update the handoff for the material maps and the three fixes
086cb69  Make the lake read as water rather than as a wake
c45a334  Stop the water surface faceting when waves outrun the grid
9dc6ade  Add normal, metallic-roughness and occlusion maps
5bf98e2  Unify texture upload behind data textures
cdd0a65  Drive the atmosphere's scattering from the sun, not the current light
2150cf2  Depth-test the starfield and put the celestial discs at the far plane
```

## What landed

**Material maps.** `renderer.Material` binds albedo, normal, metallic-roughness
and occlusion at set 0 plus a flags UBO, with its own pipeline and a
double-sided variant. Attach with `MaterialRef.PBR`. Tangent frame comes from
screen-space derivatives, so no vertex format changed. glTF reads the three
extra textures. Example `16-materials`.
See [`docs/agents/material-maps.md`](docs/agents/material-maps.md).

**Input.** Gamepads, an action-mapping layer, analog movement, and thumbstick
look on both cameras. See [`docs/agents/input.md`](docs/agents/input.md).

**Instrumentation.** Per-pass GPU timestamps, per-phase CPU timing including the
GPU wait, render counters, and `task bench`.
See [`docs/agents/profiling.md`](docs/agents/profiling.md).

**Performance.** 15-kitchen-sink GPU 6.53 → 4.09 ms (−37%), from a four-tap PCF
and thinning distant grass.

**Four bug fixes:** stars drawing through terrain, celestial discs occluding it,
an orange sunset halo on the midnight moon, and water faceting when waves
outran the vertex grid.

## Still open

- **Eight examples still poll raw keys** — 02, 03, 04, 06, 07, 08, 09, 15. They
  should move to named actions so a controller drives them. Deliberately parked
  until a pad has been tried against `17-input`, in case sensitivity or dead
  zone needs changing; the values live in the engine, though, so the migration
  itself does not bake anything in.
- **`task screenshots` has not been run.** Six examples changed framing when
  their cameras became automatic, and `16-materials.png` and `17-input.png` do
  not exist. Nothing blocks it now — see the entry below — it just has not been
  run, and it needs a GPU.
- **Sky is now the top GPU pass in most scenes** — 83 to 93 percent in 02-cube,
  07-terrain, 09-water, 12-particles and 16-materials, and about 30 percent
  where there is grass. `CloudSteps` is already an exposed setting; the honest
  next step is measuring what `CloudsLow` costs against how it looks rather than
  optimising the march.
- **Grass past this point costs visible quality.** `grassThinNear/Far/Min` and
  `GrassMaxDistance` are the knobs. Everything free has been taken.
- **Terrain and skinned meshes ignore `Material`.** Terrain is the larger piece:
  its splat pipeline already spends all four set-0 bindings on albedo.
- **The terrain splat path is unusable from a clean clone.** No example uses it,
  and `shaders/terrain.frag` says its `SPLAT_TILE` must match
  `cmd/tools/genterrain/main.go`, which does not exist in this repo. Generating
  the weight map from the heightmap would fix both.
- **Sliders for the examples** — scoped in an earlier handoff entry; the only
  missing piece is a drag callback on `ui.Clickable`.
- **`14-audio`** — still the only subsystem with neither example nor test.

## Hard-won context

- **Push constants are FULL: 256 of 256 bytes.** Layout is above
  `pushConstantSize` in `renderer/commands.go`. Several vec4s carry a scalar in
  their `w` because there is nowhere else: `sunColor.w` is the real sun's
  elevation, `pointColor.w` roughness, `ambient.w` metallic, `cameraPos.w` fog
  density, `fog.zw` the real sun's horizontal direction, and in the water path
  `sunDir.w` is the wave noise fraction. Anything new needs a uniform buffer.

- **`pc.sunDir` is not the sun.** It is whichever body lights the scene, which is
  the moon at night. The real sun is `sunColor.w` plus `fog.zw`, reassembled by
  `atmSunDirFrom`. The clouds deliberately still use `pc.sunDir` — they are
  moonlit at night.

- **sRGB is a property of the image, not the sampler.** Normal, roughness and
  occlusion maps go through `CreateDataTexture`. Loaded as colour, a flat normal
  arrives tilted and nothing says so.

- **Everything at the far plane draws LAST**, depth-tested `GreaterOrEqual` —
  `farPlaneDepthState` in `renderer/pipeline.go`, shared by sky and stars because
  those two drifted apart once. Anything standing in for a body at infinity must
  sit *just inside* the far plane: nearer occludes the world, and at the far
  plane the sky wins the tie.

- **Measure, and check the measurement.** Every performance number here came from
  `task bench`. Three separate times this session a confident measurement was
  wrong until something checked it: the GPU pass sum exceeding the frame total
  (mixed `TopOfPipe`/`BottomOfPipe`), 6.3 ms of "CPU" that was `vkQueuePresentKHR`
  blocking on vsync, and a per-frame ranking generalised from the only two scenes
  that had grass in them. Read `docs/agents/profiling.md` before trusting a
  number, including one written here.

- **Establish a noise floor before believing a diff.** Two identical runs first.
  Frame timing spreads 0.05 ms with means, 0.32 ms without. Screenshots spread
  RMS 0.00003 on a static scene and 0.0036 on grass, because wind moves
  everything.

- **`task validate` is not optional.** It caught a query pool being read before
  it was ever reset — undefined behaviour that returns `NotReady`, so the
  early-out already there looked like it handled the case.

- **The shader staleness test earns its keep.** It caught `lit.frag`,
  `skinned_lit.frag` and `terrain.frag` going stale after an edit to
  `lighting.inc`. Run `task shaders` after touching any `.inc`.

- **Do not reach for ACES or any film curve.** Tried and reverted; nothing here
  emits above 1, which is exactly where ACES lifts.

- **The washed-out look is content, not the engine.** Measured and disproved.

- **`-novsync` benchmarking is obsolete.** GPU timestamps measure GPU work with
  vsync on, so the coil whine is no longer a cost of measuring anything.

- **`docs/images/` no longer ships to module consumers.** `.gitattributes`
  marks it `export-ignore`, which cmd/go honours because it builds module zips
  with `git archive`. Verified: the archive drops from 11690 KB to 7100 KB and
  all fourteen images are gone, while `git clone` and GitHub still have them.

  The obvious way to test this reports no change, which is worth knowing before
  concluding it does not work: `git archive HEAD` reads `.gitattributes` from
  the tree being archived, so an uncommitted one does nothing at all.

- **The LSP in this workspace reports phantom errors** — undefined symbols for
  APIs that plainly exist. `go build ./...` is the authority.

- **Avoid non-ASCII in Python heredocs on this machine.** An em-dash written
  that way came out as a lone 0x97 byte and made the file invalid UTF-8. Use
  `open(p, 'w', encoding='utf-8')` explicitly, or ASCII.

## Verification

```
task ci                 # lint, build, test, race — the minimum
task validate           # all 16 examples, must be silent
task bench              # per-pass GPU and per-phase CPU cost
task shaders:verify     # committed .spv match their GLSL
task screenshots        # regenerate docs/images
```

Per `CLAUDE.md`: when adding a check for a bug, **break the fix and confirm the
check fails.** Everything added this session was verified that way, with the
verification recorded in the test's own comment. Two of those checks did not bite
on the first attempt and had to be retuned — a dead-zone test whose values were
not strictly inside the threshold, and an input harness that snapshotted state in
the wrong order so every edge read false. Both reasons are written into the
tests.
