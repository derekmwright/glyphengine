module github.com/derekmwright/glyphengine/x

go 1.27

// No `replace` here, deliberately. AGENTS.md rule 1 applies to this module for
// the same reason it applies to the engine's go.mod: `x` is published, a
// `replace` in a published module is ignored by consumers, and it would give
// them different code than we build against. go.work is what resolves the
// engine from the checkout, and go.work is ignored by consumers.
//
// The engine requirement is a pin, not a floating dependency: an x package
// compiles GLSL against the engine's include set and binds to its fixed shader
// layouts, so it is only correct against the engine version it names. v0.0.0
// stands in until the engine tags a release; in-repo the workspace resolves it
// from disk, and a build with GOWORK=off needs a -replace on the command line
// (see .github/workflows/ci.yml's consumer job).
//
// There is no go.sum here yet, and that is not an omission. In the workspace
// the engine and its dependencies resolve from disk, so nothing needs verifying
// -- confirmed by building and testing this module with the file deleted. Off
// the workspace, no go.sum could be complete either, because v0.0.0 of the
// engine has no hash to record. A hand-copied one would look authoritative and
// be neither. `go mod tidy` writes a real one the moment the pin becomes a
// tagged version.
require github.com/derekmwright/glyphengine v0.0.0

require (
	github.com/CannibalVox/cgoparam v1.1.0 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20260707082822-2a407d02d01a // indirect
	github.com/go-gl/mathgl v1.2.0 // indirect
	github.com/golang/geo v0.0.0-20260713102120-857a528af641 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/markus-wa/quickhull-go/v2 v2.2.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/qmuntal/gltf v0.28.0 // indirect
	github.com/vkngwrapper/core/v3 v3.1.3 // indirect
	github.com/vkngwrapper/extensions/v3 v3.3.2 // indirect
	golang.org/x/image v0.45.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
