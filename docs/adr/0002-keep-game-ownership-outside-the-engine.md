# 0002: Keep game ownership outside the engine

- Status: Accepted
- Recorded: 2026-09-22

Retrospective record of the existing library boundary. The original decision
date is not recorded here; the rationale below comes from the repository's
current guides and implementation.

## Context

GlyphEngine was extracted from a working game so other games could use the
engine without inheriting that game's content, components, or application
structure. Moving those concepts into the engine would recreate that coupling.

## Decision

Ship GlyphEngine as a Go library. The consuming game owns `main()`, content,
gameplay, and application structure. Engine code contains no game-specific
asset paths or gameplay concepts.

`Scene.C` contains only component stores the engine reads. A game registers its
own stores on the same `ecs.World`, using the same entity IDs without adding
game concepts to the engine's `Components` type.

Add engine capabilities when the game cannot reach them from outside the
engine, or when completing an extension point or toolkit the engine already
provides. Convenience alone does not justify making the engine own application
structure. For example, the exported `Engine.Scene` already allows a game to
swap scenes while retaining its renderer; scene-stack policy belongs in the
game.

## Alternatives

- An engine-owned application runtime or scene manager would prescribe game
  structure where the existing library already allows composition.
- Adding game components to `Components` would make the engine depend on
  gameplay it does not need to read.
- Requiring games to work around every missing capability would also break the
  boundary's purpose: renderer internals and engine-owned simulation need
  explicit extension points when game code cannot otherwise reach them.

## Consequences

- Games can share engine capabilities while choosing their own architecture
  and gameplay model.
- Games implement their own scene transitions and other application policies.
- New engine features must identify the inaccessible capability they expose or
  the existing contract they complete. Review must distinguish that need from
  a convenience abstraction.

## References and evidence

These are existing sources for this retrospective record; no runtime behavior
changes with this ADR.

- [Agent guide](../../AGENTS.md), rules 4, 8, and 14
- [Library entry point and embedded Scene](../../app.go)
- [Engine component stores](../../components.go)
- [Registering game-owned component stores](../agents/scene-entities.md)
- [Game loop and extension points](../agents/game-loop.md)
