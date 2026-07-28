---
id: client-prediction
title: Snapshot and replay a character for client-side prediction
summary: >
  Rewind a character to a server-confirmed state and replay unacknowledged
  inputs, using the fixed tick and CharacterState to make the result exact.
capability: physics
status: stable
since: v0.2.0
api:
  - glyphengine.CharacterState
  - glyphengine.Scene.SnapshotCharacter
  - glyphengine.Scene.RestoreCharacter
  - glyphengine.Scene.TickCount
  - glyphengine.Scene.MoveCharacter
  - glyphengine.MoveIntent
requires:
  - cgo
assets: none
verified: 2026-07-28
---

# Snapshot and replay a character for client-side prediction

The engine provides three things and stops: a deterministic controller, a tick
number to correlate on, and a way to save and restore one character's movement
state. Everything above that — the input buffer, the correction threshold,
whether a correction snaps or eases — is game policy and lives in the game.

```go
type CharacterState struct {
	Position mgl32.Vec3
	Rotation mgl32.Vec3
	Velocity mgl32.Vec3
	Grounded bool
}

st, ok := scene.SnapshotCharacter(player)
ok = scene.RestoreCharacter(player, st)
```

## The loop

```go
type pending struct {
	tick   uint64
	intent glyphengine.MoveIntent
}

// Client, every tick inside FixedUpdate:
func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	intent := g.consumeIntent()
	tick := e.TickCount()

	e.MoveCharacter(g.player, intent, dt)   // predict immediately
	g.unacked = append(g.unacked, pending{tick, intent})
	g.net.Send(tick, intent)
}

// When the server confirms tick N with authoritative state S:
func (g *game) reconcile(e *glyph.Engine, n uint64, s glyphengine.CharacterState, dt float32) {
	// Drop everything the server has now accounted for.
	i := 0
	for i < len(g.unacked) && g.unacked[i].tick <= n {
		i++
	}
	g.unacked = g.unacked[i:]

	predicted, _ := e.SnapshotCharacter(g.player)
	if closeEnough(predicted.Position, s.Position) {
		return // prediction held; do nothing
	}

	// Rewind and replay what the server has not seen yet.
	e.RestoreCharacter(g.player, s)
	for _, p := range g.unacked {
		e.MoveCharacter(g.player, p.intent, dt)
	}
}
```

`closeEnough` is deliberately yours. A footrace game corrects at a centimetre;
a slower game tolerates more to avoid visible snapping.

## Compare at the matching tick, not against the present

The mistake that looks correct and is not:

```go
predicted, _ := e.SnapshotCharacter(player)   // WRONG: this is "now"
if far(predicted.Position, authoritative.Position) { correct() }
```

By the time an authoritative update for tick N arrives, the client is already a
round trip past N. Comparing its current state against the server's state for a
tick in the past always mismatches, so this corrects on *every packet* — the
character is permanently being yanked backwards, and it looks exactly like
network jitter.

Keep a history of your own predictions keyed by tick, and compare like for
like:

```go
predicted, ok := g.predictedAt[msg.tick]
if !ok || close(predicted.Position, msg.state.Position) {
	return // prediction held
}
e.RestoreCharacter(g.player, msg.state)
for _, u := range g.unacked {
	e.MoveCharacter(g.player, u.intent, dt)
}
```

Prune the history as inputs are acknowledged, the same way you prune the
unacknowledged input buffer.

## Why replay is exact

`MoveCharacter` is a pure function of `(CharacterState, MoveIntent, dt)` plus
the static world. Given a fixed `dt` — which is what `FixedUpdate` guarantees —
replaying the same intents from the same state produces bit-identical results.
`prediction_test.go` asserts exactly that over 180 ticks against real collision
geometry, including a mid-stream rewind, and re-runs it 50 times to rule out
map-iteration order mattering.

`reconcile_test.go` goes further and runs the whole loop: two Scenes, one
authoritative and one predicting, with simulated latency between them. With
matching tick stamps and no divergence the client's prediction equals the
server's authoritative state at **every** tick and zero corrections are ever
needed — at 1, 6, and 20 ticks of latency. When the server applies an impulse
the client could not predict, the client notices, rewinds, replays, and
reconverges. Introducing an off-by-one in the buffer trim, or skipping the
rewind, makes those tests fail.

That exactness is the entire reason movement is on the fixed tick. On the frame
clock, jump apex alone varies ~5% between 30fps and 300fps, so a client and
server would disagree constantly and no reconciliation scheme could tell a real
correction from a timestep artifact.

## What it does not cover

- **Only the local player.** Replaying every networked character means
  re-simulating every one of them per correction. Remote characters are
  normally interpolated between received states instead, which is a different
  technique and a game-side one.
- **Static geometry only.** Replay assumes the world the character collides
  with is the same on both passes. Colliders that moved between the original
  simulation and the replay are not rewound, so prediction against moving
  platforms will drift. Keep predicted movement against `Static` geometry.
- **`RestoreCharacter` teleports.** Nothing is swept, so a state that is no
  longer legal leaves the character intersecting geometry. `Unstick` recovers
  if a game wants to be defensive.
- **Not a save format.** `CharacterState` is what `MoveCharacter` touches and
  nothing else — no health, no inventory, no animation state. It is
  deliberately too narrow to be misused as scene serialization.

## Keeping the contract honest

If `MoveCharacter` ever grows new persistent state, `CharacterState` must grow
with it or replay silently drifts.
`TestSnapshotCapturesEverythingMoveCharacterWrites` guards this: it drives a
character into a non-trivial state, diverges hard, restores, and asserts that
continuing from the restored state is reproducible. Adding controller state
without adding it to the snapshot fails that test.

## Failure modes

- **Prediction drifts steadily.** `dt` differs between client and server. Both
  must run the same `WithTickRate`, and both must call `MoveCharacter` from the
  fixed tick, never from a frame update.
- **Corrections fight the player.** The unacknowledged buffer is not being
  trimmed, so every correction replays inputs the server already applied.
- **Character jitters on correction.** Expected with a snap. Apply the
  correction to the simulation and let the *camera* ease toward it, rather than
  easing the simulated position, which desyncs collision.
- **Replay diverges only when other players are nearby.** Predicting against
  non-`Static` entities. See above.
