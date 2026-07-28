package window

import (
	"runtime"
	"testing"
)

// These tests create real GLFW windows, so they need a display. They skip when
// GLFW cannot initialize (headless CI) and on macOS, where GLFW requires the
// actual main thread rather than merely a locked one.
func requireGLFW(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("GLFW requires the macOS main thread; not available to the test runner")
	}
	runtime.LockOSThread()

	glfwMu.Lock()
	err := acquireGLFW()
	glfwMu.Unlock()
	if err != nil {
		t.Skipf("GLFW unavailable: %v", err)
	}
	t.Cleanup(func() {
		glfwMu.Lock()
		releaseGLFW()
		glfwMu.Unlock()
	})
}

// refs reads the reference count under the lock.
func refs() int {
	glfwMu.Lock()
	defer glfwMu.Unlock()
	return glfwRefs
}

// TestDestroyingOneWindowKeepsGLFWAliveForOthers is the regression this
// reference counting exists for: Destroy used to call glfw.Terminate
// unconditionally, which destroys *every* window and invalidates every
// callback, so closing one window silently broke the rest.
func TestDestroyingOneWindowKeepsGLFWAliveForOthers(t *testing.T) {
	requireGLFW(t) // holds one reference for the duration
	base := refs()

	a, err := New(320, 240, "glyphengine test a")
	if err != nil {
		t.Fatalf("create first window: %v", err)
	}
	b, err := New(320, 240, "glyphengine test b")
	if err != nil {
		a.Destroy()
		t.Fatalf("create second window: %v", err)
	}
	if got, want := refs(), base+2; got != want {
		t.Errorf("refs after two windows = %d, want %d", got, want)
	}

	a.Destroy()
	if got, want := refs(), base+1; got != want {
		t.Errorf("refs after destroying one window = %d, want %d", got, want)
	}

	// GLFW must still be live: this call would be undefined behavior after a
	// stray Terminate.
	b.PollEvents()
	if b.ShouldClose() {
		t.Error("surviving window reports ShouldClose after its sibling was destroyed")
	}

	b.Destroy()
	if got, want := refs(), base; got != want {
		t.Errorf("refs after destroying both windows = %d, want %d", got, want)
	}
}

// TestDestroyIsIdempotent guards against a double release dropping the count
// below what live windows and Init callers actually hold — which would
// terminate GLFW out from under them.
func TestDestroyIsIdempotent(t *testing.T) {
	requireGLFW(t)
	base := refs()

	w, err := New(320, 240, "glyphengine test idempotent")
	if err != nil {
		t.Fatalf("create window: %v", err)
	}

	w.Destroy()
	w.Destroy()
	w.Destroy()

	if got := refs(); got != base {
		t.Errorf("refs after three Destroy calls = %d, want %d", got, base)
	}
}

// TestInitHoldsAReferenceAcrossWindows covers the embedding case: a host that
// owns the GLFW lifetime calls Init once, and opening and closing engine
// windows in between must not tear GLFW down.
func TestInitHoldsAReferenceAcrossWindows(t *testing.T) {
	requireGLFW(t)
	base := refs()

	if err := Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got, want := refs(), base+1; got != want {
		t.Fatalf("refs after Init = %d, want %d", got, want)
	}

	w, err := New(320, 240, "glyphengine test init")
	if err != nil {
		Terminate()
		t.Fatalf("create window: %v", err)
	}
	w.Destroy()

	// The host's reference survives the window's whole lifetime.
	if got, want := refs(), base+1; got != want {
		t.Errorf("refs after window destroyed = %d, want %d (Init's reference must survive)", got, want)
	}

	Terminate()
	if got := refs(); got != base {
		t.Errorf("refs after Terminate = %d, want %d", got, base)
	}
}

// TestTerminateWithoutInitDoesNotUnderflow keeps a stray Terminate from
// pushing the count negative and terminating GLFW while windows are live.
func TestTerminateWithoutInitDoesNotUnderflow(t *testing.T) {
	glfwMu.Lock()
	saved := glfwRefs
	glfwRefs = 0
	glfwMu.Unlock()

	Terminate() // must be a no-op, not a decrement past zero

	got := refs()

	glfwMu.Lock()
	glfwRefs = saved
	glfwMu.Unlock()

	if got != 0 {
		t.Errorf("refs after Terminate at zero = %d, want 0", got)
	}
}
