module github.com/derekmwright/glyphengine/examples

go 1.26.2

require github.com/derekmwright/glyphengine v0.0.0

// The examples are never published, so this replace is safe and keeps them
// building from a plain `git clone` even without the workspace. It is the
// opposite of a replace in the ENGINE's go.mod, which would be ignored by
// consumers and silently give them different code than we build against.
replace github.com/derekmwright/glyphengine => ../

require (
	github.com/CannibalVox/cgoparam v1.1.0 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20260707082822-2a407d02d01a // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/vkngwrapper/core/v3 v3.1.1 // indirect
	github.com/vkngwrapper/extensions/v3 v3.3.0 // indirect
)
