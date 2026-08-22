module github.com/derekmwright/glyphengine/examples

go 1.26.2

require (
	github.com/derekmwright/glyphengine v0.0.0
	github.com/go-gl/mathgl v1.2.0
)

// The examples are never published, so this replace is safe and keeps them
// building from a plain `git clone` even without the workspace. It is the
// opposite of a replace in the ENGINE's go.mod, which would be ignored by
// consumers and silently give them different code than we build against.
replace github.com/derekmwright/glyphengine => ../

require (
	github.com/CannibalVox/cgoparam v1.1.0 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20260707082822-2a407d02d01a // indirect
	github.com/golang/geo v0.0.0-20260713102120-857a528af641 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/markus-wa/quickhull-go/v2 v2.2.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/qmuntal/gltf v0.28.0 // indirect
	github.com/vkngwrapper/core/v3 v3.1.2-0.20260223161040-227274a7d9ae // indirect
	github.com/vkngwrapper/extensions/v3 v3.3.0 // indirect
	golang.org/x/image v0.44.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)
