---
id: input
title: Keyboard, mouse and gamepad input
summary: >
  Bind named actions to any device and read them without knowing which one
  answered, including analog sticks, triggers, and runtime rebinding.
capability: input
status: stable
since: v0.4.0
api:
  - input.Input
  - input.Bindings
  - input.NewBindings
  - input.Bindings.Action
  - input.Bindings.Axis
  - input.Bindings.Vector
  - input.Bindings.CapturePressed
  - input.Bindings.Rebind
  - input.Source
  - input.Keyboard
  - input.Mouse
  - input.PadButton
  - input.SourceLabel
  - input.Input.PadStick
  - input.Input.PadAxis
  - input.Input.FirstPad
  - input.AddPadMapping
  - glyphengine.FPCamera.LookStick
  - glyphengine.Camera.LookStick
  - glyphengine.Camera.ZoomBy
assets: none
example: examples/17-input
run: go run ./17-input
verified: 2026-07-29
---

# Input

```go
binds := input.NewBindings(e.Input())

move := binds.Vector("move",
    input.Keyboard(input.KeyW), input.Keyboard(input.KeyS),
    input.Keyboard(input.KeyA), input.Keyboard(input.KeyD))
binds.SetVectorStick(move, input.StickLeft)

jump := binds.Action("jump",
    input.Keyboard(input.KeySpace), input.PadButton(input.ButtonA))
```

Then read them without naming a device:

```go
x, y := binds.Direction(move)   // magnitude survives: half-pushed stick, half speed
if binds.Pressed(jump) { ... }
```

Raw per-device polling (`KeyDown`, `MouseDelta`, `PadStick`) stays available and
supported. The bindings layer sits on top of it, not in front of it.

## Handles, not strings

`Action`, `Axis` and `Vector` return handles you keep. That is deliberate: a
string-keyed API reads nicely right up until `Down("jmp")` returns false forever
with nothing anywhere saying why. Holding a handle makes a typo a compile error.

Names are still carried, for `ActionName` and rebinding screens.

## Sampling and the fixed timestep

`Input.Update` snapshots every device — keyboard, mouse, and all four gamepads —
once per frame, before `PollEvents`. `Pressed` therefore means "this frame" for
every device equally.

That interacts with the engine's fixed timestep, and the rule is unchanged by
this layer: **sample in `Update`, latch the edge, consume it in `FixedUpdate`.**
A tick can run zero or several times per frame, so an edge read directly inside
`FixedUpdate` is dropped on the frames that run no tick and fired twice on the
frames that run two. Going through `Bindings` does not rescue that.

```go
func (g *game) Update(e *glyph.Engine, dt float32) {
    if g.binds.Pressed(g.jump) {
        g.jumpQueued = true // latch
    }
}

func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
    g.intent.Jump = g.jumpQueued
    g.jumpQueued = false // consume
}
```

## Analog reaches the character controller

`MoveIntent.Forward` and `.Right` scale speed by their magnitude, so a stick at a
third of its travel walks at a third of `WalkSpeed`. Digital sources pass ±1 and
get full speed; diagonals are clamped to unit length so holding two keys is not
faster than holding one.

This was not always true. Until v0.4.0 the controller reduced both to their signs
and normalized, so any deflection produced full speed and the engine had no
analog movement at all.

## Gamepad conventions are normalized once

Three things are flipped relative to what SDL and GLFW report, because every one
of them is a silent bug when a caller gets it wrong:

| Value | Driver reports | `input` reports |
|---|---|---|
| Stick Y | +1 **down** | +1 **up** |
| Triggers | −1 at rest, +1 pressed | 0 at rest, 1 pressed |
| Unmapped joystick | present, scrambled buttons | **absent** |

An unmapped pad reading as absent is the one worth explaining. Without a mapping
its button indices mean something different on every model, so a binding against
them is a guess. Teach GLFW about unusual hardware with `AddPadMapping`, which
takes an `SDL_GameControllerDB` line.

`PadPresent` reporting false therefore means either "not plugged in" or "plugged
in but unrecognised", and those want different fixes. `PadName` distinguishes
them — `17-input` logs it on connect for exactly this reason.

## Dead zones are radial, not per axis

Sticks do not return to exactly zero; a worn one can rest at 0.15 or more.
`DefaultDeadzone` is 0.18, overridable with `SetPadDeadzone`.

The dead zone applies to the stick **vector**, not to each axis separately. A
per-axis dead zone leaves a square hole around centre, and the diagonal is
exactly where both axes are individually small — so it becomes impossible to walk
slowly on a diagonal, while every cardinal direction tests fine. That asymmetry
is why the bug survives casual testing.

Past the threshold the magnitude is rescaled from zero, so the stick does not
jump to a fifth of full travel the instant it responds, and the direction
survives the rescale.

## Mouse and stick are different quantities

This is the trap worth internalising. A mouse reports **displacement since the
last frame**; a stick reports **how far it is pushed**, which says nothing about
elapsed time. They are both "two floats from an input device", which is why they
get conflated.

Feeding stick deflection through a mouse path produces a camera that turns once
per *frame* instead of once per *second* — twice as fast at 120fps as at 60.

So the cameras keep them separate, and a game calls both:

```go
g.camera.Update(e.Input())            // mouse: displacement, no dt
lx, ly := binds.Direction(look)
g.camera.LookStick(lx, ly, dt)        // stick: rate, needs dt
```

`LookSensitivity` is radians per pixel. `StickLookRate` is radians per second at
full deflection. `Camera.ZoomBy` is the same distinction for a held trigger
versus the scroll wheel, which already arrives pre-quantised per frame.

The two cameras also disagree on pitch sign — `FPCamera`'s positive pitch looks
down, `Camera`'s looks up — which their field comments record and both stick
paths honour.

## Rebinding

Three calls:

```go
if armed {
    if src, ok := binds.CapturePressed(); ok {
        binds.Rebind(jump, src)
        armed = false
    }
}
```

`CapturePressed` scans keyboard, then mouse, then the active pad, returning the
first fresh press. `SourceLabel` renders a source for display, with face buttons
carrying both labels ("A / Cross") because the same position is A on one pad and
cross on another. `ActionCount`, `ActionName` and `ActionSources` let a screen
enumerate every binding without the game keeping its own list.

Rebinding to no sources at all is allowed — a player may want an action unbound,
and silently keeping the old key would be worse than an empty row.

There is deliberately **no bindings file format**. Persistence is the game's
business; the engine would only be guessing at where and in what shape.

## Multiple pads

Bindings follow the first connected pad, resolved per frame, so a single-player
game keeps working when a replug lands the controller in slot 1. `SetPad` pins
one for local multiplayer. `MaxPads` is 4.

## Testing without hardware

The gamepad read sits behind an interface, so the package's own tests drive
synthetic device state — dead-zone geometry, axis normalization, button and
trigger edges, and the absent-pad path all run with no controller attached.

A game can do the same for its own input logic by binding actions and asserting
on the resulting `MoveIntent` rather than on device state.

What this does **not** cover is feel: sensitivity, dead zone width, and whether a
particular pad maps the way its owner expects all need the hardware.
