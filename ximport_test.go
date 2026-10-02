package glyphengine

import (
	"os/exec"
	"strings"
	"testing"
)

// xModulePath is the module the engine must never depend on. Written out rather
// than derived, so a rename of the x module shows up here as a deliberate edit.
const xModulePath = "github.com/derekmwright/glyphengine/x"

// TestEngineDoesNotImportX asserts the dependency direction ADR 0012 records:
// the engine depends on nothing of ours, x depends on the engine, the examples
// depend on both.
//
// The module boundary does not enforce this on its own. go.work makes every
// module in the workspace importable from every other, so an engine file can
// `import ".../x/terrainfield"` and build, vet and test clean locally -- it is
// only a consumer running `go get` who discovers that the engine now pulls in
// its own opinions, and by then the import is load-bearing. The failure is a
// cycle in intent rather than in the compiler: x pins an engine version, so an
// engine that imports x pins a version of a module that pins a version of it.
//
// `go list -deps` is the check rather than a source scan because it sees
// transitive imports. An engine package reaching x through two intermediate
// packages is the same violation and is much harder to spot by reading.
//
// The compiler catches only a subset of this on its own, and the subset is the
// easy half. An x package that imports the engine ROOT -- x/terrainfield does --
// makes any engine import of it a package cycle, so a blank import of
// x/terrainfield in renderer/shadow.go fails `go build` outright with "import
// cycle not allowed". An x package that imports a leaf instead, say ecs or
// renderer alone, has no cycle to find, and that is the case this test is for.
//
// Verified by breaking it, 2026-10-02: a throwaway x/probe importing only
// glyphengine/ecs, blank-imported from renderer/shadow.go, left both
// `go build ./...` and `go build -C examples ./...` at exit 0 and failed this
// test on both subtests:
//
//	ximport_test.go:67: ./renderer/... depends on
//	github.com/derekmwright/glyphengine/x/probe, which is in the x module
//
// A green build next to a red test is exactly the signal this is here to add.
func TestEngineDoesNotImportX(t *testing.T) {
	// The engine root transitively covers the scene, terrain, audio, ui and
	// input packages. renderer/... is listed separately because several of its
	// packages are reachable only from a game, not from the root, and the
	// renderer is where a seam is most likely to be "completed" by reaching
	// back into an x package that already solved the problem.
	for _, pattern := range []string{".", "./renderer/..."} {
		t.Run(pattern, func(t *testing.T) {
			cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", pattern)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go list -deps %s: %v\n%s", pattern, err, out)
			}

			deps := strings.Fields(string(out))
			if len(deps) == 0 {
				t.Fatalf("go list -deps %s returned nothing; this test would prove nothing", pattern)
			}
			// A floor rather than an exact count: the point is that the walk
			// actually happened. The first version of a check like this in this
			// repository passed while hashing nothing at all.
			if len(deps) < 20 {
				t.Fatalf("go list -deps %s returned only %d packages, so the walk is not reaching the engine",
					pattern, len(deps))
			}

			for _, dep := range deps {
				if dep == xModulePath || strings.HasPrefix(dep, xModulePath+"/") {
					t.Errorf("%s depends on %s, which is in the x module; an opinion has been imported back into the engine (ADR 0012)",
						pattern, dep)
				}
			}
		})
	}
}
