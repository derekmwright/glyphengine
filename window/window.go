package window

import (
	"fmt"
	"log"
	"sync"
	"unsafe"

	"github.com/go-gl/glfw/v3.3/glfw"
)

// Window wraps a GLFW window with Vulkan surface creation support.
type Window struct {
	handle      *glfw.Window
	resized     bool
	refreshRate int
	destroyed   bool
}

// GLFW is process-global: glfw.Terminate destroys every window and invalidates
// every callback, so whoever calls it must be the last one out. Rather than
// making that the caller's problem, this package reference-counts.
//
// Each live Window holds one reference, and so does each explicit Init. GLFW is
// initialized on the 0 -> 1 transition and terminated on 1 -> 0. That keeps the
// common single-window case a plain `defer win.Destroy()` while making two
// windows, and embedding in a host that owns GLFW, both correct.
var (
	glfwMu   sync.Mutex
	glfwRefs int
)

// Init initializes GLFW and takes a reference that Window.Destroy will not
// release. Call it when the host program owns the GLFW lifetime — for example
// when it creates its own windows, or when it opens and closes engine windows
// repeatedly and does not want GLFW torn down in between. Pair every Init with
// a Terminate.
//
// Programs that just open one window and exit do not need this: New initializes
// GLFW on demand.
func Init() error {
	glfwMu.Lock()
	defer glfwMu.Unlock()
	return acquireGLFW()
}

// Terminate releases a reference taken by Init, terminating GLFW once the last
// reference — including those held by live Windows — is gone.
func Terminate() {
	glfwMu.Lock()
	defer glfwMu.Unlock()
	releaseGLFW()
}

// acquireGLFW adds a reference, initializing GLFW on the first one.
// Callers must hold glfwMu.
func acquireGLFW() error {
	if glfwRefs == 0 {
		if err := glfw.Init(); err != nil {
			return fmt.Errorf("glfw init: %w", err)
		}
	}
	glfwRefs++
	return nil
}

// releaseGLFW drops a reference, terminating GLFW on the last one.
// Callers must hold glfwMu.
func releaseGLFW() {
	if glfwRefs == 0 {
		return
	}
	glfwRefs--
	if glfwRefs == 0 {
		glfw.Terminate()
	}
}

// Option configures optional Window parameters.
type Option func(*config)

type config struct {
	fullscreen bool
	resizable  bool
}

// WithFullscreen creates the window fullscreen on the primary monitor at its
// native resolution, ignoring the width and height passed to New.
func WithFullscreen() Option {
	return func(c *config) { c.fullscreen = true }
}

// WithResizable controls whether the user can resize a windowed-mode window.
// Ignored in fullscreen. Defaults to true.
func WithResizable(resizable bool) Option {
	return func(c *config) { c.resizable = resizable }
}

// New initializes GLFW if needed and creates a window with a Vulkan-capable
// surface. Call Destroy on the result.
//
// By default the window is created windowed at the requested width and height.
// Pass WithFullscreen to instead take the primary monitor at its native
// resolution, in which case width and height are ignored.
//
// GLFW must be used from the thread that initialized it, so the calling
// goroutine should be locked to its OS thread with runtime.LockOSThread.
func New(width, height int, title string, opts ...Option) (*Window, error) {
	cfg := config{resizable: true}
	for _, o := range opts {
		o(&cfg)
	}

	glfwMu.Lock()
	err := acquireGLFW()
	glfwMu.Unlock()
	if err != nil {
		return nil, err
	}

	glfw.WindowHint(glfw.ClientAPI, glfw.NoAPI)
	if cfg.resizable {
		glfw.WindowHint(glfw.Resizable, glfw.True)
	} else {
		glfw.WindowHint(glfw.Resizable, glfw.False)
	}

	// Refresh rate comes from the primary monitor either way -- the engine's
	// frame cap uses it even in windowed mode.
	monitor := glfw.GetPrimaryMonitor()
	mode := monitor.GetVideoMode()

	targetMonitor := (*glfw.Monitor)(nil)
	w, h := width, height
	if cfg.fullscreen {
		targetMonitor = monitor
		w, h = mode.Width, mode.Height
	}
	log.Printf("Monitor: %s %dx%d @ %dHz -- creating %dx%d (fullscreen=%v)",
		monitor.GetName(), mode.Width, mode.Height, mode.RefreshRate, w, h, cfg.fullscreen)

	handle, err := glfw.CreateWindow(w, h, title, targetMonitor, nil)
	if err != nil {
		// Give back the reference taken above rather than terminating
		// outright — another window, or a host that called Init, may still
		// be using GLFW.
		glfwMu.Lock()
		releaseGLFW()
		glfwMu.Unlock()
		return nil, fmt.Errorf("create window: %w", err)
	}

	win := &Window{handle: handle, refreshRate: mode.RefreshRate}
	handle.SetFramebufferSizeCallback(func(_ *glfw.Window, _, _ int) {
		win.resized = true
	})
	return win, nil
}

// ShouldClose returns true when the window has been requested to close.
func (w *Window) ShouldClose() bool {
	return w.handle.ShouldClose()
}

// Close signals the window to close at the end of the current frame.
func (w *Window) Close() {
	w.handle.SetShouldClose(true)
}

// PollEvents processes pending GLFW events (input, resize, etc.).
func (w *Window) PollEvents() {
	glfw.PollEvents()
}

// Destroy closes the window and releases its GLFW reference, terminating GLFW
// if this was the last one. It is safe to call more than once.
func (w *Window) Destroy() {
	if w.destroyed {
		return
	}
	w.destroyed = true

	w.handle.Destroy()

	glfwMu.Lock()
	releaseGLFW()
	glfwMu.Unlock()
}

// WasResized returns true if the framebuffer was resized since the last call and resets the flag.
func (w *Window) WasResized() bool {
	r := w.resized
	w.resized = false
	return r
}

// WaitEvents blocks until at least one GLFW event is available, then processes it.
func (w *Window) WaitEvents() {
	glfw.WaitEvents()
}

// Handle returns the underlying GLFW window for direct access (e.g., input callbacks).
func (w *Window) Handle() *glfw.Window {
	return w.handle
}

// GetFramebufferSize returns the current framebuffer dimensions in pixels.
func (w *Window) GetFramebufferSize() (int, int) {
	return w.handle.GetFramebufferSize()
}

// RefreshRate returns the primary monitor's refresh rate in Hz, as reported
// when the window was created.
//
// Three caveats, all of which have bitten: it is the *primary* monitor's rate
// regardless of which display the window is on, it is never updated after
// creation, and some drivers report 0. Treat it as a hint for display, not as
// something to compute a frame budget from — frame pacing belongs to the
// swapchain present mode (renderer.WithVSync), which paces against the display
// the window actually occupies.
func (w *Window) RefreshRate() int {
	return w.refreshRate
}

// GetRequiredInstanceExtensions returns extensions needed by GLFW for Vulkan surface creation.
func (w *Window) GetRequiredInstanceExtensions() []string {
	return w.handle.GetRequiredInstanceExtensions()
}

// GetVulkanProcAddr returns GLFW's Vulkan instance proc address loader.
func GetVulkanProcAddr() unsafe.Pointer {
	return glfw.GetVulkanGetInstanceProcAddress()
}

// CreateVulkanSurface creates a VkSurfaceKHR for this window.
// instanceHandle is the raw VkInstance passed as unsafe.Pointer.
// Returns the raw VkSurfaceKHR handle as a uintptr.
func (w *Window) CreateVulkanSurface(instanceHandle unsafe.Pointer) (uintptr, error) {
	// GLFW's CreateWindowSurface uses reflect and expects a Ptr-kind value.
	// unsafe.Pointer has Kind==UnsafePointer which is not Ptr, so wrap as *byte.
	instancePtr := (*byte)(instanceHandle)

	// CreateWindowSurface returns a pointer TO the VkSurfaceKHR local, not the handle itself.
	surfaceAddr, err := w.handle.CreateWindowSurface(instancePtr, nil)
	if err != nil {
		return 0, fmt.Errorf("create vulkan surface: %w", err)
	}

	// Dereference to get the actual VkSurfaceKHR handle value.
	// This is safe because the GC won't run between CreateWindowSurface returning
	// and this dereference (no allocation in between).
	surfaceHandle := *(*uintptr)(unsafe.Pointer(surfaceAddr)) //nolint:govet // Bridge between GLFW C surface handle and Go
	return surfaceHandle, nil
}
