# Handoff — materials, input, instrumentation, HDR

Transient note for the next session. Delete once the work below has landed.

## Where things are

Branch `celestial-depth-and-glow`, **not pushed**, `main` untouched. The branch
name is from its first two commits and now describes about a twentieth of what is
on it; rename before pushing.

Everything below is verified with `task ci` and `task validate` (all 16 examples
silent under the validation layer).

```
cdcc8bf  Regenerate the documentation screenshots
9209823  Add bloom, thresholded above 1
0a9fa35  Add emissive maps, the first thing that emits above 1
c0ff534  Render the scene to a half-float target and resolve it
ebd057c  Keep README screenshots out of the module download
91fa917  Document the profiling capability and refresh the handoff
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
0d3a7b6  Update the handoff for the material maps and the three fixes
086cb69  Make the lake read as water rather than as a wake
c45a334  Stop the water surface faceting when waves outrun the grid
9dc6ade  Add normal, metallic-roughness and occlusion maps
5bf98e2  Unify texture upload behind data textures
cdd0a65  Drive the atmosphere's scattering from the sun, not the current light
2150cf2  Depth-test the starfield and put the celestial discs at the far plane
```

## What landed

**HDR and tonemapping.** The scene renders into `R16G16B16A16_SFLOAT` and a
fullscreen pass resolves it. `Renderer.SetTonemap` gives exposure and a curve.
Default is identity, so nothing looks different until asked.
See [`docs/agents/hdr-tonemap.md`](docs/agents/hdr-tonemap.md).

**Emissive materials.** `MaterialOptions.Emissive*`, plus glTF `emissiveFactor`,
`emissiveTexture` and `KHR_materials_emissive_strength`. The first thing in the
engine that emits above 1.
See [`docs/agents/material-maps.md`](docs/agents/material-maps.md).

**Bloom.** Five-level chain, thresholded, composited in the resolve. Off by
default and free when off. See [`docs/agents/bloom.md`](docs/agents/bloom.md).

**Material maps, input, instrumentation** — as recorded in the earlier handoff;
their pages are `material-maps.md`, `input.md` and `profiling.md`.

**Performance.** 15-kitchen-sink GPU 6.53 → 4.09 ms (−37%), from a four-tap PCF
and thinning distant grass.

## Still open

- **Eight examples still poll raw keys** — 02, 03, 04, 06, 07, 08, 09, 15. They
  should move to named actions so a controller drives them. Deliberately parked
  until a pad has been tried against `17-input`, in case sensitivity or dead
  zone needs changing; the values live in the engine, though, so the migration
  itself does not bake anything in.
- **To see the sun glare, use `16-materials -time 0.76`.** Its camera orbits, so
  it sweeps past the sun on its own; `-speed 8` runs a full cycle in about 19
  seconds. This was written up as an unresolved gap after several failed attempts
  to find an angle by hand in `07-terrain` and `09-water` -- the answer was an
  example that was already pointing at it once a second.
- **"Sky is the top GPU pass" means CLOUDS, not the dome.** Measured on
  09-water: the sky pass costs 1.146 ms with the cloud march on and 0.008 ms
  with it off. Over 99 percent of it is the raymarch, and the sky dome is
  essentially free.

  This was got wrong once, expensively, and the mistake is cheap to repeat. A
  Hillaire-style LUT atmosphere was built on the grounds that it would make the
  top GPU pass cheaper, by comparing the engine's whole sky pass against the
  source paper's "Draw Far Sky" row of 0.105 ms. That row is the dome alone; the
  same table lists Draw Clouds separately at 7.15 ms. So it compared the
  engine's dome-plus-clouds against the paper's dome, and both halves of the
  conclusion were wrong: the engine's dome was already thirteen times cheaper
  than the paper's, and there was no saving to collect. Measured afterwards, the
  sky pass was 0.917 ms analytic and 0.918 ms with the LUT.

  It was reverted -- no performance gain, and the artistic dome looked better.
  The branch `atmosphere-lut-experiment` holds the work if it is ever wanted:
  transmittance, multiple-scattering and sky-view LUTs, a runtime-parameterised
  medium, and an F3 A/B toggle, all verified against reference images.

  **Read both rows of a performance table before believing the comparison.**

- **The cloud march now runs at half resolution in its own pass**, which is
  where the sky's cost actually was. GPU totals: water 1.399 to 0.688 ms,
  terrain 1.296 to 0.591, kitchensink 3.515 to 3.131. The image is unchanged --
  RMS 0.00079 against a run-to-run noise floor of 0.00067, max difference 2 of
  255 -- because clouds are soft and there is no edge for the upscale to blur
  that was not soft already.

  Temporal reprojection is still open, and would be the next step: it would
  converge the raymarch jitter properly rather than attenuating it, which is
  what sky.frag does today.

- **Sky is the top GPU pass in most scenes** — 78 to 92 percent in 02-cube,
  07-terrain, 09-water, 12-particles and 16-materials, and about 30 percent
  where there is grass. `CloudSteps` is already exposed; the honest next step is
  measuring what `CloudsLow` costs against how it looks. Note the cloud march is
  **ALU-bound, not fill-bound**: doubling the target's bit depth did not move it
  at all (1.594 → 1.593 ms).
- **Grass past this point costs visible quality.** `grassThinNear/Far/Min` and
  `GrassMaxDistance` are the knobs. Everything free has been taken.
- **Terrain ignores `Material`.** Its splat pipeline already spends all four
  set-0 bindings on albedo, so this one really does need a rethink of the set
  layout rather than another shader variant. Skinned meshes now support
  materials.
- **The terrain splat path is unusable from a clean clone.** No example uses it,
  and `shaders/terrain.frag` says its `SPLAT_TILE` must match
  `cmd/tools/genterrain/main.go`, which does not exist in this repo.
- **Resizing leaks descriptor pool capacity.** `recreateSwapchain` rebuilds the
  HDR and bloom chains and reallocates their sets without resetting the pool.
  `maxHDRSets` and `maxBloomSets` are headroom for several resizes, not a fix.
- **Sliders for the examples** — the only missing piece is a drag callback on
  `ui.Clickable`.
- **`14-audio`** — still the only subsystem with neither example nor test.

## Hard-won context

- **Push constants are FULL: 256 of 256 bytes.** Layout is above
  `pushConstantSize` in `renderer/commands.go`. Several vec4s carry a scalar in
  their `w` because there is nowhere else: `sunColor.w` is the real sun's
  elevation, `pointColor.w` roughness, `ambient.w` metallic, `cameraPos.w` fog
  density, `fog.zw` the real sun's horizontal direction, and in the water path
  `sunDir.w` is the wave noise fraction. Anything new needs a uniform buffer —
  which is what the material path already does for emissive.

- **`pc.sunDir` is not the sun.** It is whichever body lights the scene, which is
  the moon at night. The real sun is `sunColor.w` plus `fog.zw`, reassembled by
  `atmSunDirFrom`. The clouds deliberately still use `pc.sunDir` — they are
  moonlit at night.

- **sRGB is a property of the image, not the sampler.** Normal, roughness and
  occlusion maps go through `CreateDataTexture`. Emissive maps do **not** —
  emission is a colour.

- **Everything at the far plane draws LAST**, depth-tested `GreaterOrEqual` —
  `farPlaneDepthState` in `renderer/pipeline.go`, shared by sky and stars because
  those two drifted apart once.

- **Every path that presents must record the tonemap resolve.** The scene passes
  no longer touch the swapchain at all. `DrawTriangle` has its own loop and was
  missed; `task ci` passed anyway and only `task validate` caught it.

- **A descriptor's declared layout is validated whether or not the shader reads
  it.** A uniform branch skipping the sample does nothing for the layout check.
  That is what `primeBloomLayouts` is for, and `initCubeShadowLayout` before it.

- **The cloud march must not sample detail its step length cannot resolve.**
  The finest fbm octave is about 113 world units across and a step covers 30 to
  170 at ordinary elevations, so that octave could only alias. Octaves now fade
  out by Nyquist against the live step length, renormalised so coverage does not
  shift with them.

- **Keep the march's jitter keyed to the world, not to the screen.** It was
  `hash2D(fragUV)`, which nails the noise pattern to the display: turning the
  camera drags the clouds through a stationary field and the result reads as a
  Photoshop add-noise filter rather than as grain. `hash3D(dir * 4096)` gives a
  patch of sky its own jitter, so the noise travels with the cloud.

  Do not "fix" this by turning the jitter down. Removing it entirely gives the
  best static grain number and brings back visible horizontal banding at the
  horizon; a partial amplitude is worse than either, because the residual
  banding beats against the hash into a structured dither.

- **Check a bloom or godray threshold against the SKY.** Daytime sky is about
  0.68 in linear. A ramp reaching below that lifts the whole frame and reads as
  haze rather than as a mistake. Measured: RMS 0.025 across the sky before the
  threshold was raised, 0.002 after.

- **Measure, and check the measurement.** Every performance number here came from
  `task bench`. Read `docs/agents/profiling.md` before trusting a number,
  including one written here.

- **Establish a noise floor before believing a diff.** This caught a fabricated
  regression on this branch: the light-shaft contribution in `09-water` measured
  RMS 0.00074 before HDR and 0.00036 after, which reads as the shafts halving —
  except two identical runs of the same build differ by 0.00067. Both numbers
  were noise. Shafts are simply not visible in that view.

  Two identical runs first.
  Frame timing spreads about 0.02 ms with means. Screenshot RMS spreads
  0.0006 on 07-terrain, which is the most static scene, and 0.004 to 0.007 on
  anything with a day cycle or wind — high enough to swamp a subtle change, so
  compare regions rather than whole frames when the change is local.

- **`task validate` is not optional.** It has now caught four things this branch
  that nothing else did: a query pool read before reset, a swapchain presented in
  UNDEFINED, MSAA images left at the swapchain format, and the bloom layout
  above.

- **The shader staleness test earns its keep.** Run `task shaders` after touching
  any `.inc` — `bloom.inc` is now one of them.

- **The sun disc has always emitted above 1** — `SunDiscColor` is the sun colour
  times a 1.7 boost, so it peaks at 1.7 at midday. An earlier version of this
  handoff claimed it did not; that was wrong. The 8-bit target was throwing the
  excess away, and the half-float one now carries it, which means a bloom
  threshold above 1 already selects the sun with no further work. Pinned by
  `TestSunDiscExceedsOne` — clamping that return to 1 looks like a tidy-up and
  silently stops the sun being a highlight.

- **ACES is the answer now, and the old note saying otherwise was right when it
  was written.** That note said not to reach for a film curve because nothing
  emitted above 1, so a curve built to compress highlights had nothing to
  compress. The precondition is gone: the sun disc emits 5, emissive materials
  emit 6, and at sunset 63 percent of the sky was measured sitting within a
  whisker of the top of the 8-bit range. That is photographic blowout, and it is
  why a sunset read wrong -- the sun clipped, the sky clipped with it, and
  nothing distinguished them.

  Measured on 16-materials at a fixed sun, panel local contrast against sky
  pinned at the ceiling: identity 0.01234 / 63.9%, Reinhard 0.01095 / 0.0%,
  ACES 0.01725 / 2.5%. ACES beats identity on both at once because it lifts
  midtones -- aces(0.5) is 0.62 against Reinhard's 0.34 -- while rolling
  highlights off. It is `SetTonemap` curve 2.

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
check fails.** The emissive work was verified that way against six separate
breaks — dropping the strength multiply, defaulting an unset strength to zero,
reading the map flag off the wrong field, returning zero from a glTF parse
failure, dropping the negative guard, and removing the vec4 from the shader's
`MaterialBlock`. Each fails exactly the test that covers it.

Two things this branch verified by experiment rather than by test, because no
unit test can see them:

- **The HDR target really holds values above 1.** `lit.frag` temporarily emitting
  `4.0x`, resolved at `0.25` exposure, reproduces 11-lights intact. An 8-bit
  target would have returned a flat quarter-grey silhouette.
- **Emission takes no lighting.** At midnight, panels 1-4 of 16-materials go
  black and panel 5 keeps glowing.
