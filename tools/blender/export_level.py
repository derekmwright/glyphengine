"""export_level.py -- export the current Blender scene as a level glTF/GLB
with the options a level needs, so they cannot be forgotten one at a time.

Why this exists: measured on Blender 5.0.1 (see
docs/agents/blender-pipeline.md), `export_extras` (Include > Custom
Properties), `export_lights` (Include > Punctual Lights) and
`export_gpu_instances` (Include > GPU Instances) are all OFF in the
exporter's own defaults. A plain File > Export > glTF 2.0 therefore still
succeeds, still opens in the engine, and silently has no lamps and no
`static`/`collider`/`spawn` tags on any node -- see docs/agents/models.md's
"A Blender export needs two boxes ticked". That reads as "the level has no
lights", not "the export dropped them", which is the failure mode this
script removes.

Two ways to run it:

  Headless, from a shell -- this is how tools/blender/build_fixture.py and
  the fixture regeneration in renderer/testdata/blender/README.md both use
  it:

    blender -b level.blend --python tools/blender/export_level.py -- out.glb

  From Blender's own Scripting tab: open this file and hit Run Script, then
  call export_level("/path/to/out.glb") from the Python console. Running the
  file with no `-- out.glb` on the command line (which is what the
  Scripting tab does) only prints usage -- it never exports on its own,
  because a script that fires an export as a side effect of being loaded is
  a worse surprise than one that does nothing until asked.

Also importable as a module -- `from export_level import export_level` --
which is how build_fixture.py drives it without shelling back out to a
second Blender process.

The exporter's keyword arguments are not identical across Blender versions
(4.2, 4.3, 4.4 and 5.0 are the ones this recipe is checked against -- see
docs/agents/blender-pipeline.md's per-version table). Rather than guess with
try/except, this asks the operator's own RNA for the properties that exist
on the Blender actually running (`bpy.ops.export_scene.gltf.get_rna_type()
.properties`) and applies only those that do, printing what it applied and
what it could not find. That turns "exported with an older version's
defaults, silently" into a line in the log saying exactly that.

No add-on, no UI panel, no engine-specific property schema: plain Python
against Blender's own glTF exporter operator. What the keys in a node's
`extras` MEAN is the consuming game's business (AGENTS.md rule 14), not
this script's -- it only makes sure they survive the export.
"""

import os
import sys

import bpy

# The settings this recipe insists on, and why each one earns its place
# rather than being left at the exporter's own default. Printed in this
# order by export_level() so the report reads as a checklist.
#
# export_yup and export_import_convert_lighting_mode are already the
# exporter's own defaults on every version this has been checked against --
# they are set explicitly anyway so this recipe does not silently change
# behaviour if a future Blender version ever changes either default out from
# under it, and so the report below says what was actually asked for rather
# than leaving a reader to assume the default was good enough.
LEVEL_EXPORT_OPTIONS = [
    ("export_extras", True,
     "Include > Custom Properties -- OFF by default. A node's gameplay "
     "tags (collider, static, floors, spawn) live in glTF `extras`; "
     "without this every ModelNode.Extras in the engine comes back nil."),
    ("export_lights", True,
     "Include > Punctual Lights -- OFF by default. Without this every "
     "lamp in the scene is simply absent from the file -- Model.Lights "
     "comes back empty, which is not an error the exporter reports."),
    ("export_gpu_instances", True,
     "Include > GPU Instances -- OFF by default. Lets Blender's own "
     "instancing (a collection instance, a duplicator the exporter "
     "recognises as instanced) reach the file as EXT_mesh_gpu_instancing "
     "on the doc mesh rather than being silently baked into N separate "
     "meshes. The engine does not read the extension yet (issue #71) --"
     "this only stops the exporter from baking, so #71 has something to "
     "read."),
    ("export_yup", True,
     "+Y up -- already the exporter's default; see the module comment "
     "above for why it is still set here."),
    ("export_import_convert_lighting_mode", "SPEC",
     "Physical light units (SPEC) -- already the exporter's default; see "
     "the module comment above. This is what makes a 1000 W Blender spot "
     "arrive as 54351.4 candela (docs/agents/blender-pipeline.md); the "
     "fixture and its Go test are calibrated to that number."),
    ("export_apply", True,
     "Apply Modifiers -- OFF by default. An unapplied modifier (a "
     "mirror, a subdivision) exports the modifier's CAGE rather than its "
     "result, so the file stops matching the viewport the first time "
     "someone adds one."),
]


def _rna_properties():
    """Returns the export_scene.gltf operator's own RNA properties for the
    Blender that is actually running.

    Read fresh each call rather than cached at import time: this file is
    imported once per Blender process either way, but keeping it a function
    rather than a module-level constant means a test can call it after
    forcing a reload and get the live answer, not a stale one.
    """
    return bpy.ops.export_scene.gltf.get_rna_type().properties


def level_export_kwargs(overrides=None):
    """Builds the bpy.ops.export_scene.gltf(...) keyword arguments for
    LEVEL_EXPORT_OPTIONS, restricted to whatever properties this Blender
    version's exporter actually has.

    Returns (kwargs, applied, unavailable): applied and unavailable are the
    option NAMES (not the tuples), in LEVEL_EXPORT_OPTIONS's order, so a
    caller -- export_level below, or a test driving this directly -- can
    report or assert on exactly what happened on this version without
    re-deriving it.
    """
    props = _rna_properties()
    kwargs = {}
    applied = []
    unavailable = []
    for name, value, _why in LEVEL_EXPORT_OPTIONS:
        if name in props:
            kwargs[name] = value
            applied.append(name)
        else:
            unavailable.append(name)
    if overrides:
        kwargs.update(overrides)
    return kwargs, applied, unavailable


def _export_format_for(filepath):
    """Picks GLB or GLTF_SEPARATE from filepath's extension.

    export_format's ENUM items are populated dynamically by the operator
    (bpy.ops.export_scene.gltf.get_rna_type().properties["export_format"]
    .enum_items comes back empty outside of an active invocation on every
    version checked here), so this is not introspected the way the other
    options are -- GLB/GLTF_SEPARATE/GLTF_EMBEDDED are the exporter's own
    documented identifiers and have been stable across 4.2-5.0.
    """
    ext = os.path.splitext(filepath)[1].lower()
    if ext == ".glb":
        return "GLB"
    if ext == ".gltf":
        return "GLTF_SEPARATE"
    raise ValueError(
        "export_level: %r has no .glb or .gltf extension -- name the "
        "output file one or the other" % (filepath,)
    )


def export_level(filepath, overrides=None, report=print):
    """Exports the current scene to filepath with the level recipe's
    options applied on top of whatever the caller already set on the
    scene/objects, and returns the set bpy.ops.export_scene.gltf itself
    returned (e.g. {'FINISHED'}).

    overrides, if given, is a dict merged into the recipe's own kwargs
    AFTER LEVEL_EXPORT_OPTIONS is applied -- build_fixture.py uses this for
    nothing today, but it is the seam a caller reaches for instead of
    duplicating this function to change one setting.

    Raises RuntimeError on anything other than a clean {'FINISHED'} plus a
    file that actually exists afterward. Printing a warning and returning
    is exactly the silent-failure shape this script exists to remove from
    the pipeline -- a caller (this file's own __main__ below, or
    build_fixture.py) turns the exception into a non-zero process exit.
    """
    filepath = os.path.abspath(filepath)
    out_dir = os.path.dirname(filepath)
    if out_dir and not os.path.isdir(out_dir):
        os.makedirs(out_dir, exist_ok=True)

    kwargs, applied, unavailable = level_export_kwargs(overrides)
    kwargs["filepath"] = filepath
    kwargs.setdefault("export_format", _export_format_for(filepath))

    version = ".".join(str(v) for v in bpy.app.version)
    report("export_level: Blender %s -> %s" % (version, filepath))
    report("export_level: applied: %s" % ", ".join(applied))
    if unavailable:
        report(
            "export_level: NOT AVAILABLE on this Blender version, exported "
            "with the exporter's own default instead: %s" % ", ".join(unavailable)
        )

    result = bpy.ops.export_scene.gltf(**kwargs)
    if result != {"FINISHED"}:
        raise RuntimeError(
            "bpy.ops.export_scene.gltf(...) returned %r, not {'FINISHED'}" % (result,)
        )
    if not os.path.isfile(filepath):
        raise RuntimeError(
            "export_scene.gltf reported FINISHED but %s does not exist" % filepath
        )
    report("export_level: wrote %s (%d bytes)" % (filepath, os.path.getsize(filepath)))
    return result


def _main():
    """Command-line entry point: `blender -b file.blend --python
    export_level.py -- out.glb`.

    Blender consumes every argument up to and including its own `--`; what
    is left in sys.argv after that is this script's. No `--` at all (this
    is what running the file from the Scripting tab looks like -- there is
    no command line to speak of) prints usage and returns rather than
    raising, since loading the file into the text editor is not a request
    to export anything.
    """
    argv = sys.argv
    if "--" not in argv:
        print(
            "export_level: run from Blender's Scripting tab -- nothing to "
            "do until you call export_level(path). From a shell, pass the "
            "output path after `--`: blender -b level.blend --python "
            "tools/blender/export_level.py -- out.glb"
        )
        return
    args = argv[argv.index("--") + 1:]
    if len(args) != 1:
        print(
            "usage: blender -b level.blend --python tools/blender/export_level.py -- out.glb",
            file=sys.stderr,
        )
        sys.exit(2)
    try:
        export_level(args[0])
    except Exception as exc:  # noqa: BLE001 -- turned into a process exit code on purpose
        print("export_level: FAILED: %s" % exc, file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    _main()
