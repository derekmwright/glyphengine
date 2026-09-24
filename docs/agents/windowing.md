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
verified: 2026-09-24 # triangle under dynamic rendering on Windows
---

# Create a window

The renderer requires `VK_KHR_dynamic_rendering`, its `dynamicRendering`
feature and extension dependencies. Startup names the driver and any missing
capability. Vulkan 1.0 plus these extensions is sufficient; a Vulkan 1.3 core
API is not required. MoltenVK portability handling remains enabled when
advertised, but the same capabilities are required there; macOS is untested.
See [ADR 0009](../adr/0009-execute-render-passes-with-dynamic-rendering.md).

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

## Intermittent startup swapchain failure

[Issue #125](https://github.com/derekmwright/glyphengine/issues/125) reports
`vkCreateSwapchainKHR` returning `vulkan error: unknown` before any pipeline or
frame, with no validation diagnostic. The four main sightings were two
`15-kitchen-sink` launches late in `task smoke`, `09-water -time 0.72 ...` in
`task validate`, and `25-lod-forest -levels 1` in `task lod`. The issue also
mentions two earlier `08-grass` launches during a validation rerun. Each
passed immediately on retry. The historical batch sizes are unknown, so these
sightings do not establish a failure rate. Some predate hidden windows (#107).

Run the creation-only harness from the repository root:

```sh
go run ./cmd/swapchainchurn -n 200
go run ./cmd/swapchainchurn -n 200 -validate
go run ./cmd/swapchainchurn -n 200 -delay 10
go run ./cmd/swapchainchurn -n 200 -visible
go run ./cmd/swapchainchurn -n 200 -shared
go run ./cmd/swapchainchurn -n 200 -shared -validate
```

Defaults are 200 hidden 1280x720 windows, no validation, no inter-iteration
delay, and a new instance/device per window. `-delay` is milliseconds **after
teardown**, not a wait for surface readiness. `-visible` overrides
`GLYPHENGINE_BACKGROUND`; `-validate` requires the layer rather than silently
running without it. `-shared` keeps GLFW, one Vulkan instance and one device
alive, but recreates every window, surface, swapchain and image view. The
instance/device extension baseline and swapchain choices mirror the renderer:
Vulkan 1.0 plus dynamic-rendering dependencies, sRGB preference, FIFO, and
TRANSFER_SRC when supported. It neither builds pipelines nor presents frames.

Every iteration logs the surface-capability query result and complete
capabilities before creation, then the swapchain's numeric Vulkan result.
Failures name the stage and iteration; the final summary includes the first
failure, failures/iterations, swapchain attempts/failures, capability failures,
and validation message count. It continues after an iteration failure to
measure the whole run and exits nonzero if any iteration or validation fails.

Measured on 2026-09-24, Windows 11 Pro 10.0.26200 / RX 7900 XTX / Vulkan SDK
1.3.268.0 / Go 1.27.0, starting at `daff3b7` (after the dynamic-rendering
migration). Each variant ran three times in separate processes of 200
iterations, interleaved by pass:

| Variant | Run 1 failures | Run 2 failures | Run 3 failures | Total / observed rate |
|---|---:|---:|---:|---:|
| Hidden, fresh instance/device | 0/200 | 0/200 | 0/200 | 0/600 (0%) |
| Hidden, `-validate` | 0/200 | 0/200 | 0/200 | 0/600 (0%) |
| Hidden, `-delay 10` | 0/200 | 0/200 | 0/200 | 0/600 (0%) |
| `-visible` | 0/200 | 0/200 | 0/200 | 0/600 (0%) |
| Hidden, `-shared` | 0/200 | 0/200 | 0/200 | 0/600 (0%) |
| Hidden, `-shared -validate` | 0/200 | 0/200 | 0/200 | 0/600 (0%) |

The failure **did not reproduce in isolation**: 0/3,600 creations, zero
capability failures and zero validation messages. Every capability query and
creation returned VkResult 0. All surfaces reported 1280x720 current/min/max
extent, image-count limits 2..16, identity transform, opaque alpha and
color-attachment/TRANSFER_SRC support. The logs were checked for exactly
iterations 1..200 of both CAPS and CREATE per run. No zero extent or transient
unusable capability set was observed. Zero observed failures does not prove a
zero underlying rate, and these measurements cannot identify the loader,
compositor or driver as the cause. **The renderer adds no startup retry or
readiness wait.**

Unlike the historical batches, the harness loops inside one process and does
not build/render each example between creations. Separate `go run` processes,
pipeline/asset allocation, presentation, validation settings, and the time
between process teardown and the next launch differ. Concurrent GPU use by
another worktree was permitted during measurement; the GPU was not isolated.

The renderer logs the failing Vulkan call, numeric result and wrapped error.
The `smoke`/`validate` checker labels a failed initial creation
`STARTUP FAILURE (swapchain)` and restarts that example once, retaining both
attempt logs. A second failure fails the gate. Surface-query, image-view,
rebuild, rendering and validation failures do not take that retry path. See
[validation](validation.md) for the gate's checks.

## Known limitations

- There is no monitor selection; fullscreen always uses the primary monitor.
  A game on a 144Hz secondary display can be windowed there, but cannot go
  fullscreen there.
- GLFW still requires all calls from the thread that initialized it, so a
  program that opens windows from more than one goroutine must lock them all to
  the same OS thread. The reference count is mutex-guarded, but GLFW itself is
  not made thread-safe by it.
