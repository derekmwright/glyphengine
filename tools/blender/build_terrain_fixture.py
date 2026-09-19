"""build_terrain_fixture.py -- builds cmd/heightmapconv/testdata/blender_terrain.glb
procedurally in an empty Blender file and exports it through export_level.py's
function, the same relationship tools/blender/build_fixture.py has to
renderer/testdata/blender/level.glb (see that file's module doc for why
procedural-from-a-script beats a committed .blend).

This fixture exists to answer one question with a REAL Blender export rather
than an assumption: does cmd/heightmapconv's rasteriser put an asymmetric
terrain's features in the right place once Blender's Z-up has become glTF's
Y-up, AND once a node transform (translation and rotation, on two different
objects) has been composed in? A symmetric hill cannot answer this -- a
left/right or front/back axis swap looks identical on one. This mesh has a
single-vertex spike at one named corner and a ridge that runs along one axis
only, so a swapped axis or a dropped rotation moves them somewhere a test can
catch.

The scene:

  - TerrainParent (an Empty, the scene ROOT): location (200, 0, 0) in
    Blender's own X/Y/Z, rotated 90 degrees about Blender's Z axis (its own
    "up"). Root because a non-root's rotation would compose differently --
    see cmd/heightmapconv/testdata/README.md for the derivation this
    fixture's own Go test checks itself against.
  - Terrain (a mesh, parented to TerrainParent, "Keep Transform" NOT used --
    matrix_parent_inverse is left at identity for the same reason
    build_fixture.py's ShearChild leaves it alone: the authored local
    numbers must be the actual composed local transform, not adjusted to
    cancel out the parent's placement): location (0, 0, 3) in TerrainParent's
    local space, identity rotation.
  - Terrain's mesh: an 11x11 grid of vertices at integer Blender X/Y from 0
    to 10, flat (Z=0) except:
      - a RIDGE running along Blender +X (constant across every X column,
        so its long axis is the X axis) on the +Y half of the grid: rows
        Y=6..10 get a tent profile peaking at Y=8 (height 4), falling to 2
        at the Y=6/Y=10 edges of the ridge and 0 at Y=5 and below.
      - a single-vertex PEAK of height 20 at the one corner (X=10, Y=0) --
        far taller than the ridge, and on the opposite Y edge from it, so
        the two features cannot be confused with each other either.

No bmesh needed for something this simple: the grid is built directly with
bpy.data.meshes.new()/from_pydata(), the same low-level approach
build_fixture.py's build_geometry_nodes_scatter() uses for its point mesh.

Run headless:

    blender -b --factory-startup --python tools/blender/build_terrain_fixture.py -- out.glb

built and exported on Blender 5.0.1, 2026-09-19 -- see
cmd/heightmapconv/testdata/README.md for the exact command and the hand
computation this fixture's Go test checks the real export against.
"""

import math
import os
import sys

import bpy


def _here():
    return os.path.dirname(os.path.abspath(__file__))


sys.path.insert(0, _here())
import export_level  # noqa: E402  (path must be set up first)


# ---------------------------------------------------------------------------
# Geometry: an 11x11 grid, flat except a ridge along +X and one corner peak.
# ---------------------------------------------------------------------------

GRID_N = 11  # vertices per side -- 0..10 inclusive
RIDGE_H = 4.0
RIDGE_CENTER = 8  # Y row the ridge peaks at
RIDGE_HALF_WIDTH = 4  # ridge is nonzero for Y in [RIDGE_CENTER-4, RIDGE_CENTER+4] intersected with Y>=6
PEAK_H = 20.0
PEAK_X, PEAK_Y = 10, 0  # the one named corner: max X, min Y


def height(ix, iy):
    """Blender-local Z for grid vertex (ix, iy), both in 0..GRID_N-1.

    Documented and hand-verified in cmd/heightmapconv/testdata/README.md --
    change this function and that file's numbers both, or the Go test that
    checks the real export against them will be checking stale arithmetic.
    """
    if ix == PEAK_X and iy == PEAK_Y:
        return PEAK_H
    if iy >= 6:
        t = 1.0 - abs(iy - RIDGE_CENTER) / RIDGE_HALF_WIDTH
        return max(0.0, RIDGE_H * t)
    return 0.0


def build_terrain_mesh():
    verts = []
    index = {}
    for iy in range(GRID_N):
        for ix in range(GRID_N):
            index[(ix, iy)] = len(verts)
            verts.append((float(ix), float(iy), height(ix, iy)))

    faces = []
    for iy in range(GRID_N - 1):
        for ix in range(GRID_N - 1):
            v00 = index[(ix, iy)]
            v10 = index[(ix + 1, iy)]
            v01 = index[(ix, iy + 1)]
            v11 = index[(ix + 1, iy + 1)]
            # Two triangles per cell, explicit rather than relying on the
            # exporter to triangulate a quad -- see the module doc.
            faces.append((v00, v10, v11))
            faces.append((v00, v11, v01))

    mesh = bpy.data.meshes.new("TerrainMesh")
    mesh.from_pydata(verts, [], faces)
    mesh.update()

    obj = bpy.data.objects.new("Terrain", mesh)
    obj.location = (0.0, 0.0, 3.0)  # the "terrain object translated" the issue asks for
    bpy.context.scene.collection.objects.link(obj)
    return obj


def build_parent_empty():
    empty = bpy.data.objects.new("TerrainParent", None)
    empty.empty_display_type = "PLAIN_AXES"
    empty.location = (200.0, 0.0, 0.0)
    empty.rotation_euler = (0.0, 0.0, math.radians(90.0))  # "its parent empty rotated 90 degrees about Z"
    bpy.context.scene.collection.objects.link(empty)
    return empty


def build_scene():
    bpy.ops.wm.read_factory_settings(use_empty=True)

    parent = build_parent_empty()
    terrain = build_terrain_mesh()
    terrain.parent = parent
    # No matrix_parent_inverse compensation, deliberately -- see the module
    # doc and build_fixture.py's identical note on ShearChild: this needs
    # the RAW composed local transform, not one adjusted to cancel the
    # parent's placement.

    report = "build_terrain_fixture: objects: %s" % ", ".join(
        sorted(o.name for o in bpy.data.objects)
    )
    print(report)
    return report


def main(out_path):
    build_scene()
    export_level.export_level(out_path)


if __name__ == "__main__":
    argv = sys.argv
    if "--" not in argv:
        print(
            "build_terrain_fixture: run from Blender's Scripting tab -- call "
            "build_scene() to build it in this session without exporting, or "
            "main('/path/out.glb') to build and export. From a shell: "
            "blender -b --factory-startup --python tools/blender/build_terrain_fixture.py -- out.glb"
        )
    else:
        args = argv[argv.index("--") + 1:]
        if len(args) != 1:
            print(
                "usage: blender -b --factory-startup --python tools/blender/build_terrain_fixture.py -- out.glb",
                file=sys.stderr,
            )
            sys.exit(2)
        try:
            main(args[0])
        except Exception as exc:  # noqa: BLE001 -- turned into a process exit code
            print("build_terrain_fixture: FAILED: %s" % exc, file=sys.stderr)
            sys.exit(1)
