"""build_fixture.py -- builds renderer/testdata/blender/level.glb procedurally
in an empty Blender file and exports it through export_level.py's function.

Procedural rather than a committed .blend: a binary .blend in the repo can
only be inspected by opening Blender, cannot be diffed, and (AGENTS.md rule
3 in spirit, though that rule is about LFS specifically) is exactly the kind
of opaque binary this repo avoids committing when a script can reproduce it.
This file IS the fixture's source of truth; renderer/testdata/blender/level.glb
is its build output, the same relationship examples/22-level/gen/main.go has
to examples/22-level/assets/level.glb, except this one actually goes through
Blender rather than writing glTF directly, because the whole point of issue
#67 is a fixture real Blender wrote.

Run it headless:

    blender -b --factory-startup --python tools/blender/build_fixture.py -- out.glb

`--factory-startup` matters: without it, Blender loads whatever the user's
default startup file and preferences contain (an addon-added object, a
non-default unit scale), and this script would inherit that silently. The
script also calls `wm.read_factory_settings(use_empty=True)` itself so the
fixture does not depend on the factory startup SCENE either (just its
add-ons/preferences) -- belt and suspenders, since a stray default cube in
the file is exactly the kind of thing that would make the fixture's node
count depend on who/what ran this.

Every object below is built with plain bpy/bmesh calls and named for what it
probes, because renderer/gltfblender_test.go and docs/agents/blender-pipeline.md
both refer to these names -- changing one without the others is exactly the
drift issue #67's fixture exists to make impossible (see
renderer/testdata/blender/README.md).
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
# Small helpers
# ---------------------------------------------------------------------------

def _link(obj, collection=None):
    """Links obj into collection (default: the active scene collection).

    bpy.data.objects.new() does not link an object into any collection by
    itself -- an unlinked object is invisible to the view layer and, in
    turn, to the exporter, which is a way to build something and have it
    silently not appear in the file.
    """
    (collection or bpy.context.scene.collection).objects.link(obj)
    return obj


def _new_material(name):
    mat = bpy.data.materials.new(name)
    mat.use_nodes = True
    return mat


def _principled(mat):
    return mat.node_tree.nodes["Principled BSDF"]


# ---------------------------------------------------------------------------
# Ground: an 8x8-tiled texture through a Mapping node (KHR_texture_transform,
# issue #69), self-contained via a packed generated image.
# ---------------------------------------------------------------------------

def build_ground():
    bpy.ops.mesh.primitive_plane_add(size=20, location=(0, 0, 0))
    obj = bpy.context.object
    obj.name = "Ground"
    # No extras on purpose -- renderer/gltfblender_test.go asserts the ground
    # carries none, as the counterpoint to Building's extras below. A real
    # level would tag its floor too (examples/22-level's Ground does), but
    # this fixture needs at least one node that proves "no extras" decodes
    # to nil rather than an empty object.

    # A small checkerboard, generated rather than loaded from disk, so the
    # exported .glb is self-contained -- nothing under tools/blender/ has to
    # ship an image asset for this fixture to be reproducible from source.
    size = 4
    img = bpy.data.images.new("GroundTex", width=size, height=size, alpha=False)
    pixels = []
    for y in range(size):
        for x in range(size):
            on = (x + y) % 2 == 0
            c = (0.75, 0.72, 0.60, 1.0) if on else (0.30, 0.28, 0.22, 1.0)
            pixels.extend(c)
    img.pixels = pixels
    img.pack()  # embeds pixel data in the .blend/export rather than a file path

    mat = _new_material("Ground")
    nodes = mat.node_tree.nodes
    links = mat.node_tree.links
    principled = _principled(mat)

    tex_coord = nodes.new("ShaderNodeTexCoord")
    mapping = nodes.new("ShaderNodeMapping")
    # The tiling case issue #69 needs: scale (8, 8) on the UV mapping, which
    # the exporter's own node search (get_texture_transform_from_mapping_node,
    # confirmed by reading io_scene_gltf2/blender/exp/material/texture_info.py
    # in the Blender 5.0 install) turns into KHR_texture_transform on the
    # baseColorTexture -- it looks for exactly
    # [UV] -> [Mapping] -> [Image Texture], which is this graph.
    mapping.inputs["Scale"].default_value = (8.0, 8.0, 1.0)
    image_tex = nodes.new("ShaderNodeTexImage")
    image_tex.image = img

    links.new(tex_coord.outputs["UV"], mapping.inputs["Vector"])
    links.new(mapping.outputs["Vector"], image_tex.inputs["Vector"])
    links.new(image_tex.outputs["Color"], principled.inputs["Base Color"])

    obj.data.materials.append(mat)
    return obj


# ---------------------------------------------------------------------------
# Building + its Alt-D linked duplicate: mixed-type extras, one shared mesh
# datablock, a real translation + Z-rotation + non-uniform scale on the copy.
# ---------------------------------------------------------------------------

def build_building():
    bpy.ops.mesh.primitive_cube_add(size=2, location=(4, 0, 1))
    obj = bpy.context.object
    obj.name = "Building"
    mat = _new_material("Stone")
    _principled(mat).inputs["Base Color"].default_value = (0.55, 0.50, 0.42, 1.0)
    obj.data.materials.append(mat)

    # Mixed types on purpose -- issue #67 asks for a bool, a string and a
    # number on the same node, because a naive extras reader that only
    # handles one JSON type would pass a single-type test and still be
    # wrong. See ModelNode.Extras in renderer/gltf.go and the nodeTags
    # struct in examples/22-level/main.go for the reader this mirrors.
    obj["collider"] = "box"
    obj["static"] = True
    obj["floors"] = 3

    # Alt-D, not Shift-D: object.copy() shares the source mesh DATABLOCK
    # (obj.data is the same ID, not a duplicate of it) -- the Python
    # equivalent of Blender's "Duplicate Linked", which is exactly the case
    # Model.NodeMeshes (docs/agents/models.md) exists for: two nodes
    # instancing one doc mesh. bpy.ops.object.duplicate(linked=True) would
    # do the same thing through the operator rather than the API; .copy()
    # is used here because it is the one that does not also require
    # fighting with which object ends up "active"/selected in a headless
    # context.
    dup = obj.copy()
    dup.name = "Building_Linked"
    _link(dup)
    # Translation, a rotation about Z (Blender's vertical axis, which the
    # +Y-up export turns into a rotation about glTF's Y -- see
    # renderer/testdata/blender/README.md for the measured conversion), and
    # a non-uniform scale: the same shape examples/22-level/gen/main.go's
    # Building1 exercises, except this one is a real Blender export rather
    # than hand-authored glTF.
    dup.location = (10, 0, 2)
    dup.rotation_euler = (0, 0, math.radians(30))
    dup.scale = (1.5, 2.0, 1.0)

    return obj, dup


# ---------------------------------------------------------------------------
# Alpha 0.3 (glass, issue #68) and backface culling on/off (doubleSided).
# ---------------------------------------------------------------------------

def build_material_probes():
    # Alpha 0.3 on the Principled BSDF's own Alpha socket. Confirmed by
    # reading io_scene_gltf2/blender/exp/material/search_node_tree.py in the
    # Blender 5.0 install: alphaMode is read off this socket's value
    # ("Alpha has the general form alpha = alpha_clip(factor * ...)" --
    # gather_alpha_info), NOT off Eevee's old blend_method, which that same
    # file's comment says the exporter used to key off of. use_backface_culling
    # is set on every material here too (default False), matching
    # __gather_double_sided in the same install's materials.py, so this fixture
    # does not depend on which detection path a given Blender version uses.
    bpy.ops.mesh.primitive_plane_add(size=2, location=(-4, 4, 1), rotation=(math.radians(90), 0, 0))
    glass_obj = bpy.context.object
    glass_obj.name = "GlassPane"
    glass_mat = _new_material("Glass")
    glass_p = _principled(glass_mat)
    glass_p.inputs["Alpha"].default_value = 0.3
    glass_p.inputs["Base Color"].default_value = (0.6, 0.8, 0.9, 1.0)
    glass_mat.use_backface_culling = False
    glass_obj.data.materials.append(glass_mat)

    bpy.ops.mesh.primitive_plane_add(size=2, location=(-4, 6, 1), rotation=(math.radians(90), 0, 0))
    culled_obj = bpy.context.object
    culled_obj.name = "CulledPanel"
    culled_mat = _new_material("Culled")
    culled_mat.use_backface_culling = True  # -> doubleSided false
    culled_obj.data.materials.append(culled_mat)

    bpy.ops.mesh.primitive_plane_add(size=2, location=(-4, 8, 1), rotation=(math.radians(90), 0, 0))
    open_obj = bpy.context.object
    open_obj.name = "OpenPanel"
    open_mat = _new_material("Open")
    open_mat.use_backface_culling = False  # -> doubleSided true (the exporter's own default)
    open_obj.data.materials.append(open_mat)

    return glass_obj, culled_obj, open_obj


# ---------------------------------------------------------------------------
# Lights: a warm SPOT and a white POINT, plain Blender data, no rotation
# rigging needed -- a Blender light with identity rotation already points
# down its local -Z, which in Blender's Z-up scene IS straight down.
# ---------------------------------------------------------------------------

def build_lights():
    spot_data = bpy.data.lights.new("Spot", type='SPOT')
    spot_data.energy = 1000.0  # Watts
    spot_data.spot_size = 1.2  # radians, full cone angle
    spot_data.spot_blend = 0.3
    spot_data.color = (1.0, 0.7, 0.4)  # warm
    spot_obj = bpy.data.objects.new("SpotLamp", spot_data)
    spot_obj.location = (0, 0, 5)
    # rotation_euler left at (0, 0, 0) on purpose: Blender lights aim down
    # their own local -Z with no rotation applied, and Blender's scene is
    # Z-up, so identity rotation already means "straight down" -- see
    # renderer/testdata/blender/README.md for the exported form of this
    # (a node rotated -90 degrees about X, aiming down glTF's -Y).
    _link(spot_obj)

    point_data = bpy.data.lights.new("Point", type='POINT')
    point_data.energy = 100.0
    point_data.color = (0.9, 0.92, 1.0)
    point_obj = bpy.data.objects.new("PointLamp", point_data)
    point_obj.location = (3, 3, 3)
    _link(point_obj)

    return spot_obj, point_obj


# ---------------------------------------------------------------------------
# The spawn empty (issue #46's pattern: identified by extras, not by name a
# game has to hard-code).
# ---------------------------------------------------------------------------

def build_spawn():
    empty = bpy.data.objects.new("spawn", None)
    empty.empty_display_type = 'PLAIN_AXES'
    empty.location = (0, -8, 0)
    empty["spawn"] = "player"
    _link(empty)
    return empty


# ---------------------------------------------------------------------------
# Mirrored object: negative scale on X, deliberately NOT applied (Object >
# Apply > Scale would remove the exact thing this probes).
# ---------------------------------------------------------------------------

def build_mirrored():
    bpy.ops.mesh.primitive_cube_add(size=1.5, location=(8, 6, 1))
    obj = bpy.context.object
    obj.name = "Mirrored"
    obj.scale = (-1.0, 1.0, 1.0)
    # A mirror on Blender's X axis is chosen specifically because the
    # Z-up -> Y-up conversion (RotX(-90), see the README) leaves X alone --
    # it is the one axis where "negative in Blender" and "negative in the
    # exported glTF" are the same statement with no relabeling to track.
    mat = _new_material("Mirrored")
    _principled(mat).inputs["Base Color"].default_value = (0.7, 0.3, 0.3, 1.0)
    obj.data.materials.append(mat)
    return obj


# ---------------------------------------------------------------------------
# Shear: a non-uniformly-scaled parent (NOT applied) with a rotated child --
# TransformFromMatrix (root package) cannot hold this; that is the point.
# ---------------------------------------------------------------------------

def build_shear():
    parent = bpy.data.objects.new("ShearParent", None)
    parent.empty_display_type = 'ARROWS'
    parent.location = (-8, -6, 1)
    parent.scale = (1.0, 1.0, 3.0)
    _link(parent)

    bpy.ops.mesh.primitive_cube_add(size=0.6)
    child = bpy.context.object
    child.name = "ShearChild"
    mat = _new_material("ShearChild")
    _principled(mat).inputs["Base Color"].default_value = (0.4, 0.6, 0.8, 1.0)
    child.data.materials.append(mat)
    child.location = (0, 0, 0.6)
    child.rotation_euler = (math.radians(45), 0, 0)
    child.parent = parent
    # No matrix_parent_inverse compensation: this is deliberate. Blender's
    # own "Keep Transform" parenting (bpy.ops.object.parent_set) bakes the
    # parent's CURRENT world matrix into matrix_parent_inverse so the child
    # does not visibly jump -- which would cancel out exactly the shear this
    # object exists to produce. Leaving matrix_parent_inverse at identity
    # means the child's authored TRS is the child's LOCAL transform relative
    # to the parent with nothing removed, the same relationship a rotated
    # object inside a non-uniformly-scaled parent has in any glTF scene
    # graph.

    return parent, child


# ---------------------------------------------------------------------------
# Instancing probe (a): a collection instance -- an empty pointing at a
# collection that holds one prop, Blender's "Add > Collection Instance".
# ---------------------------------------------------------------------------

def build_collection_instance():
    prop_collection = bpy.data.collections.new("PropCollection")
    # Deliberately NOT linked into the scene's own collection hierarchy: the
    # prop is reachable ONLY through the instancing empty below, which is
    # the shape a real prop library asset has (the source collection lives
    # in its own file/collection, never placed directly).

    bpy.ops.mesh.primitive_cone_add(radius1=0.3, depth=0.6)
    prop = bpy.context.object
    prop.name = "Prop"
    mat = _new_material("Prop")
    _principled(mat).inputs["Base Color"].default_value = (0.8, 0.6, 0.2, 1.0)
    prop.data.materials.append(mat)
    for col in list(prop.users_collection):
        col.objects.unlink(prop)
    prop_collection.objects.link(prop)

    instance = bpy.data.objects.new("PropCollectionInstance", None)
    instance.instance_type = 'COLLECTION'
    instance.instance_collection = prop_collection
    instance.location = (8, -6, 0)
    _link(instance)

    # A second instance of the SAME collection, so the fixture can show --
    # not just assert in the abstract -- that several collection instances
    # produce several child nodes sharing ONE doc mesh, the exact shape
    # Model.NodeMeshes (docs/agents/models.md) exists to place correctly.
    # One instance alone cannot distinguish "shares a mesh" from "the only
    # copy that exists".
    instance2 = bpy.data.objects.new("PropCollectionInstance2", None)
    instance2.instance_type = 'COLLECTION'
    instance2.instance_collection = prop_collection
    instance2.location = (10, -6, 2)
    _link(instance2)

    return prop, instance, instance2


# ---------------------------------------------------------------------------
# Instancing probe (b): a geometry-nodes scatter -- Mesh to Points feeding
# Instance on Points, referencing the SAME Prop object built above. Wrapped
# in its own try/except: this is the probe the issue says to leave out
# rather than ship flaky, so a failure here must not take the rest of the
# fixture down with it.
# ---------------------------------------------------------------------------

def build_geometry_nodes_scatter(prop_obj):
    try:
        mesh = bpy.data.meshes.new("GNScatterPoints")
        # Three loose points -- Mesh to Points needs vertices, not faces, and
        # a handful keeps this "a few KB of geometry" the way every other
        # probe here is.
        mesh.from_pydata([(0, 0, 0), (0.6, 0, 0), (0, 0.6, 0)], [], [])
        mesh.update()
        source = bpy.data.objects.new("GNSourcePoints", mesh)
        source.location = (8, -9, 0)
        _link(source)

        node_group = bpy.data.node_groups.new("GNScatterProps", 'GeometryNodeTree')
        node_group.interface.new_socket(name="Geometry", in_out='INPUT', socket_type='NodeSocketGeometry')
        node_group.interface.new_socket(name="Geometry", in_out='OUTPUT', socket_type='NodeSocketGeometry')

        nodes = node_group.nodes
        links = node_group.links
        n_in = nodes.new('NodeGroupInput')
        n_out = nodes.new('NodeGroupOutput')
        n_pts = nodes.new('GeometryNodeMeshToPoints')
        n_obj = nodes.new('GeometryNodeObjectInfo')
        n_obj.inputs['Object'].default_value = prop_obj
        n_obj.inputs['As Instance'].default_value = True
        n_inst = nodes.new('GeometryNodeInstanceOnPoints')
        # Realize Instances matters, not just tidiness: leaving the output as
        # unrealized instances (no Realize Instances node) was tried first,
        # and Blender's own "Apply Modifiers" export step then evaluates to
        # an EMPTY mesh -- the exported node carries no `mesh` at all, not
        # an error, just silently nothing. See
        # docs/agents/blender-pipeline.md's instancing findings.
        n_real = nodes.new('GeometryNodeRealizeInstances')

        links.new(n_in.outputs['Geometry'], n_pts.inputs['Mesh'])
        links.new(n_pts.outputs['Points'], n_inst.inputs['Points'])
        links.new(n_obj.outputs['Geometry'], n_inst.inputs['Instance'])
        links.new(n_inst.outputs['Instances'], n_real.inputs['Geometry'])
        links.new(n_real.outputs['Geometry'], n_out.inputs['Geometry'])

        modifier = source.modifiers.new("GNScatter", 'NODES')
        modifier.node_group = node_group
        return source, None
    except Exception as exc:  # noqa: BLE001 -- reported, not fatal to the fixture
        return None, str(exc)


# ---------------------------------------------------------------------------
# Assembly
# ---------------------------------------------------------------------------

def build_scene():
    bpy.ops.wm.read_factory_settings(use_empty=True)

    ground = build_ground()
    building, building_linked = build_building()
    glass, culled, open_panel = build_material_probes()
    spot, point = build_lights()
    spawn = build_spawn()
    mirrored = build_mirrored()
    shear_parent, shear_child = build_shear()
    prop, collection_instance, collection_instance2 = build_collection_instance()
    gn_source, gn_error = build_geometry_nodes_scatter(prop)

    report_lines = [
        "build_fixture: objects: %s" % ", ".join(sorted(o.name for o in bpy.data.objects)),
    ]
    if gn_error is not None:
        report_lines.append(
            "build_fixture: geometry-nodes scatter probe FAILED, left out of "
            "the fixture: %s" % gn_error
        )
    else:
        report_lines.append(
            "build_fixture: geometry-nodes scatter probe built as %r" % gn_source.name
        )
    for line in report_lines:
        print(line)

    return report_lines


def main(out_path):
    build_scene()
    export_level.export_level(out_path)


if __name__ == "__main__":
    argv = sys.argv
    if "--" not in argv:
        print(
            "build_fixture: run from Blender's Scripting tab -- call "
            "build_scene() to build it in this session without exporting, "
            "or main('/path/out.glb') to build and export. From a shell: "
            "blender -b --factory-startup --python tools/blender/build_fixture.py -- out.glb"
        )
    else:
        args = argv[argv.index("--") + 1:]
        if len(args) != 1:
            print(
                "usage: blender -b --factory-startup --python tools/blender/build_fixture.py -- out.glb",
                file=sys.stderr,
            )
            sys.exit(2)
        try:
            main(args[0])
        except Exception as exc:  # noqa: BLE001 -- turned into a process exit code
            print("build_fixture: FAILED: %s" % exc, file=sys.stderr)
            sys.exit(1)
