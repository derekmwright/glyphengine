---
id: environment
title: Environment — sky, light, fog
summary: >
  Compose a scene's sky, directional light, ambient and fog from independent
  optional pieces, or replace the whole model with your own implementation.
capability: environment
status: stable
since: v0.3.0
api:
  - glyphengine.EnvironmentSource
  - glyphengine.EnvironmentState
  - glyphengine.Environment
  - glyphengine.DefaultEnvironment
  - glyphengine.Sky
  - glyphengine.DefaultSky
  - glyphengine.DirectionalLight
  - glyphengine.AmbientLight
  - glyphengine.Fog
  - glyphengine.Scene.Env
  - glyphengine.Scene.Environment
requires: []
assets: none
example: examples/09-water
run: go run ./09-water
verified: 2026-07-28
---

# Environment

`Scene.Env` holds the sky, the light and the air. It is an interface, so it can
be composed from the engine's pieces or replaced entirely.

```go
// The default: full sky, a cycle frozen at sunrise, light haze.
scene.Env = glyph.DefaultEnvironment()

// An interior: no sky, no sun, no weather.
scene.Env = &glyph.Environment{
    Ambient:    &glyph.AmbientLight{Color: [3]float32{0.18, 0.17, 0.20}},
    Sun:        &glyph.DirectionalLight{
        Direction: [3]float32{0.4, 0.8, 0.3},
        Color:     [3]float32{0.7, 0.68, 0.62},
    },
    ClearColor: [3]float32{0.03, 0.03, 0.045},
}

// Nothing. Black background, and only the lights you place yourself.
scene.Env = nil
```

A new `Scene` gets `DefaultEnvironment()`, so a game that says nothing still
opens onto a lit world. Everything past that is a decision.

## The pieces are independent

| Piece | Nil means |
|---|---|
| `Cycle` | Time does not pass. `Sun` and `Ambient` supply the light instead |
| `Sky` | No dome, no discs, no stars. The frame clears to `ClearColor` |
| `Sun` | No directional light (unless `Cycle` provides one) |
| `Ambient` | No fill light (unless `Cycle` provides one) |
| `Fog` | No distance fog |

They combine in the ways you would expect, with one rule worth stating: **a
`Cycle` overrides `Sun` and `Ambient`.** A cycle already knows where the sun is
and what colour the sky is casting; a fixed light alongside it would be a second
answer to the same question.

Useful combinations:

- `Sky` without `Cycle` — a static sky at a fixed hour. Set
  `Sky.FixedSunElevation` to pick which one.
- `Cycle` without `Sky` — the engine's sun, moon and ambient driving *your*
  skybox. The light works; nothing is drawn.
- `Sky` with `SunDisc: false` — sky colour and light without a visible sun.

## Clouds are a graphics setting

`Sky.CloudSteps` controls the volumetric cloud raymarch and is the most
expensive thing the engine draws per pixel. It exists to be wired to a settings
menu, not left at a constant. Measured at 1280x720, MSAA 4x, on a Radeon RX
7900 XTX, whole frame:

| Setting | Frame | FPS |
|---|---|---|
| `CloudsOff` | 0.28 ms | 3593 |
| `CloudsLow` (16) | 0.76 ms | 1323 |
| `CloudsHigh` (32) | 1.11 ms | 898 |

Those are one GPU's numbers; the ratios transfer better than the absolutes.
Any integer works, not just the presets.

It is safe to change every frame — the value is read when the environment
resolves, so a slider takes effect on the next frame with nothing to rebuild.

The sky is drawn **after** opaque geometry and depth-tested against the far
plane, so none of this runs for a pixel the terrain covers. That reordering is
worth about 12% on its own and is what makes a raymarched sky affordable at
all; before it, a fullscreen sky shaded every pixel and the world painted over
most of them.

## Replacing it entirely

Implement `EnvironmentSource`:

```go
type EnvironmentSource interface {
    Advance(dt float32)          // on the fixed tick
    State() EnvironmentState     // once per frame, no mutation
}
```

`Advance` runs at the simulation rate, so anything driven from it is frame-rate
independent. `State` runs once per rendered frame; the engine resolves it a
single time and uses that for the whole frame, so a stateful implementation
cannot light half a frame one way and half another.

```go
type weather struct{ storm float32 }

func (w *weather) Advance(dt float32) { w.storm = ... }

func (w *weather) State() glyph.EnvironmentState {
    return glyph.EnvironmentState{
        SunDir:     [3]float32{0.3, 0.5, 0.2},
        SunColor:   [3]float32{0.35, 0.36, 0.40},
        Ambient:    [3]float32{0.10, 0.11, 0.13},
        FogDensity: 0.02 + w.storm*0.05,
        DrawSky:    true,
    }
}

scene.Env = &weather{}
```

**Values and pixels are separate concerns.** `EnvironmentSource` decides the
numbers. To change how the sky is *drawn*, replace `sky.frag` through
`renderer.WithShaders`. Neither forces the other.

## SunDir is not the sun

`EnvironmentState` has both `SunDir` and `SunElevation`, and they are not the
same thing.

`SunDir` is whichever body is currently lighting the scene — at night that is
the moon. `SunElevation` is the real sun's height, and it is what the
atmosphere derives its palette from.

Feeding the sky `SunDir.y` paints a noon sky at midnight, because the moon
rides highest exactly when the sky should be darkest. A custom implementation
that only sets `SunDir` gets `SunElevation` of 0 — permanent sunset. Set both.

## Convenience methods

`Scene.SetTimeOfDay`, `SetDayCycleSpeed` and `Engine.SetFogDensity` reach
through to the built-in `Environment`. They are **no-ops** under a custom
`EnvironmentSource`, which owns its own state — `Scene.DayNight()` returns nil
there, and that is the signal to configure your own type directly.

## Failure modes

- **A sky appears in an interior scene.** Something is still using
  `DefaultEnvironment()`. Set `Sky: nil`.
- **Everything is black.** `Env` is nil, or has no `Cycle`, `Sun` or `Ambient`.
  That is the documented meaning of an empty environment, not a bug.
- **`SetTimeOfDay` does nothing.** The scene has a custom `EnvironmentSource`
  or no `Cycle`.
- **A custom source gives a permanent sunset sky.** `SunElevation` was left at
  zero. See above.
- **Lighting flickers between frames.** A `State()` implementation is mutating.
  Move the change into `Advance`.
