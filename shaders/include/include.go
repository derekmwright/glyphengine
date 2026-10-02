// Package include exports the engine's shared GLSL include set.
//
// The engine's own shaders are compiled ahead of time and committed as SPIR-V
// (see AGENTS.md rule 2), so for them these files are build-time inputs that
// never need to ship. A package outside the engine module -- anything under
// github.com/derekmwright/glyphengine/x -- is in the opposite position: it
// writes GLSL that calls applyFog, calcShadow or srgbToLinear, and it has to
// compile that GLSL against the include set belonging to the engine version it
// will actually run against. Without an export it would have to vendor a copy,
// and a copy drifts silently: the fragment shader still compiles, the
// descriptor layouts still match, and the lighting is simply from the wrong
// version of the engine.
//
// Hence FS. The files are embedded rather than read off disk because a consumer
// has the module, not the repository.
//
// # Build step
//
// glslc resolves #include through its own file system, not through an io/FS, so
// the set has to exist as real files for the length of one compile. WriteTo
// does that; the caller owns the directory and normally passes a temp dir:
//
//	dir, err := os.MkdirTemp("", "glyphinc")
//	if err != nil { ... }
//	defer os.RemoveAll(dir)
//	if err := include.WriteTo(dir); err != nil { ... }
//	cmd := exec.Command(glslc, "-I", dir, "water.frag", "-o", "water.frag.spv")
//
// Write the shader with a bare name -- #include "lighting.inc" -- so -I is what
// resolves it. Relative paths into the engine's source tree do not survive
// `go get`, and that is the failure this package exists to remove.
//
// Run that compile from a test or a generator, not at startup: a package that
// shells out to glslc when the game launches has made an authoring-only tool
// into a runtime dependency. Compiling in a test is also what makes a changed
// include fail at build rather than at draw, which is the point.
//
// # What the includes expect
//
// These are fragments, not standalone shaders. lighting.inc needs the including
// file to have already declared the shadow descriptor set, the push-constant
// block and LIGHT_SET; the layouts are in docs/agents/render-targets.md under
// "Fixed shader layouts", and shaders/terrain.frag is a worked example of the
// preamble. Getting it wrong is a compile error, which is the good case.
package include

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FS holds every shared GLSL include in the engine, keyed by bare file name
// ("lighting.inc"). The includes reference each other by bare name too, so the
// whole set has to be materialized together even when a shader names only one.
//
//go:embed *.inc
var FS embed.FS

// WriteTo writes the whole include set into dir, which must already exist.
// Files are written fresh each call so a stale copy from an earlier engine
// version cannot survive in a reused directory.
func WriteTo(dir string) error {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		return fmt.Errorf("read embedded includes: %w", err)
	}
	for _, e := range entries {
		data, err := FS.ReadFile(e.Name())
		if err != nil {
			return fmt.Errorf("read embedded include %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			return fmt.Errorf("write include %s: %w", e.Name(), err)
		}
	}
	return nil
}

// Names lists the include files, sorted, so a caller can report which set it
// compiled against.
func Names() []string {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		// fs.ReadDir on an embed.FS root cannot fail; a panic here would mean
		// the embed itself is broken, which is a build problem, not a runtime
		// one.
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
