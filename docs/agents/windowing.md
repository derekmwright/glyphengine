---
id: create-window
title: Create a window
summary: >
  Open a windowed or fullscreen Vulkan-capable window, handle resize, and
  read keyboard and mouse input.
capability: windowing
status: stable
since: v0.1.0
api:
  - window.New
  - window.Init
  - window.Terminate
  - window.WithFullscreen
  - window.WithResizable
  - window.Window.ShouldClose
  - window.Window.PollEvents
  - window.Window.WasResized
  - window.Window.Close
  - window.Window.Destroy
  - window.Window.Handle
  - input.New
  - input.Input.Update
  - input.Input.KeyPressed
example: examples/01-triangle
run: task example:01-triangle
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-07-28
---

# Create a window

```go
package main

import (
	"log"
	"runtime"

	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/window"
)

func init() {
	// Required. GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

func main() {
	win, err := window.New(800, 600, "My Game")
	if err != nil {
		log.Fatal(err)
	}
	defer win.Destroy()

	in := input.New(win.Handle())

	for !win.ShouldClose() {
		in.Update()      // must come BEFORE PollEvents
		win.PollEvents()

		if in.KeyPressed(input.KeyEscape) {
			win.Close()
		}
	}
}
```

## Windowed vs fullscreen

`New` is windowed at the requested size by default. Pass `WithFullscreen()` to
take the primary monitor at its native resolution, in which case the `width` and
`height` arguments are ignored.

```go
win, err := window.New(1280, 720, "My Game", window.WithFullscreen())
win, err := window.New(1280, 720, "My Game", window.WithResizable(false))
```

## Two ordering rules that bite

**`Input.Update()` must be called before `Window.PollEvents()`**, not after.
`Update` snapshots the previous frame's key state so that `KeyPressed` and
`KeyReleased` can report edges; `PollEvents` then writes the new state. Reversing
them makes edge-triggered input fire on the wrong frame or not at all.

**`runtime.LockOSThread()` in `init()` is not optional.** GLFW requires all its
calls to come from the thread that initialized it. Without the lock the Go
runtime may migrate the goroutine to another OS thread and window or input calls
will fail in ways that look random.

## Resize

The framebuffer size changes independently of anything you control. Poll it and
tell the renderer:

```go
if win.WasResized() {
	rend.NotifyResize()
}
```

`WasResized` consumes the flag — it returns true once per resize and then resets.

Also skip drawing while minimized, since the swapchain has zero area:

```go
if rend.Minimized() {
	continue
}
```

## GLFW lifetime

GLFW is process-global, and `glfw.Terminate` destroys *every* window and
invalidates every callback. So the package reference-counts instead of making
that the caller's problem: each live `Window` holds a reference, GLFW is
initialized on the first and terminated when the last is released.

For a single window that means the obvious code is already correct:

```go
win, err := window.New(800, 600, "My Game")
if err != nil {
	log.Fatal(err)
}
defer win.Destroy() // terminates GLFW, because it was the last reference
```

Two windows are also correct — destroying one leaves the other working.

Call `window.Init()` when the *host program* owns the GLFW lifetime: it creates
its own GLFW windows, or it opens and closes engine windows repeatedly and does
not want GLFW torn down in between. `Init` takes a reference that `Destroy`
never releases; pair it with `Terminate`.

```go
if err := window.Init(); err != nil {
	log.Fatal(err)
}
defer window.Terminate()

// Windows can now come and go without GLFW being terminated underneath.
```

`Destroy` is idempotent, so a `defer win.Destroy()` alongside an explicit close
path is safe.

## Refresh rate

`RefreshRate()` reports the **primary** monitor's rate, sampled once when the
window was created. It does not follow the window between displays and does not
update if the display mode changes, and some drivers report `0`.

Do not use it for frame pacing — divide by it and a `0` is an immediate panic.
Frame pacing belongs to the swapchain present mode (`renderer.WithVSync`),
which paces to whichever display the window is actually on.

## Known limitations

- There is no monitor selection; fullscreen always uses the primary monitor.
  A game on a 144Hz secondary display can be windowed there, but cannot go
  fullscreen there.
- GLFW still requires all calls from the thread that initialized it, so a
  program that opens windows from more than one goroutine must lock them all to
  the same OS thread. The reference count is mutex-guarded, but GLFW itself is
  not made thread-safe by it.
