# renderer/testdata/blender/level.glb

Produced by real Blender, not hand-authored glTF -- this is the fixture
issue #67 exists to provide: a file the reference world-building pipeline
actually wrote, checked against by `renderer/gltfblender_test.go`.

**Never edit this file by hand.** Regenerate it with:

```sh
blender -b --factory-startup --python tools/blender/build_fixture.py -- renderer/testdata/blender/level.glb
```

run from the repository root (`blender` is
`C:\Program Files\Blender Foundation\Blender <version>\blender.exe` on
Windows). `tools/blender/build_fixture.py` builds the scene procedurally in
an empty file -- no committed `.blend`, so the fixture's entire source of
truth is that script -- and exports it through `tools/blender/export_level.py`.

- **Blender version used for the committed file:** 5.0.1 (the newest of the
  four installed on the machine this was built on: 4.2, 4.3, 4.4, 5.0).
- **Date:** 2026-09-19.
- **Exact command run:**
  `"C:\Program Files\Blender Foundation\Blender 5.0\blender.exe" -b --factory-startup --python tools/blender/build_fixture.py -- renderer/testdata/blender/level.glb`
- **Exporter:** `asset.generator` in the file itself records
  `Khronos glTF Blender I/O v5.0.21` -- `renderer/gltfblender_test.go`
  asserts this contains "Blender", which is what proves this file came from
  the real exporter rather than a Go-side generator like
  `examples/22-level/gen`.

See `docs/agents/blender-pipeline.md` for what every object in the scene
probes, the exact numbers this file's nodes carry (computed by hand from
`build_fixture.py`'s own inputs and cross-checked against this exact
export), and the per-Blender-version table from running the same script on
4.2/4.3/4.4/5.0.

If `tools/blender/build_fixture.py` or `tools/blender/export_level.py`
changes in a way that moves a number this file carries (a translation, a
light intensity, a material name), regenerate this file AND update the hand-
computed constants in `renderer/gltfblender_test.go` and the tables in
`docs/agents/blender-pipeline.md` in the same commit -- the three are
checked against each other, the same way `examples/22-level/gen` and
`renderer/gltflevel_test.go` are (see `docs/agents/models.md`).
