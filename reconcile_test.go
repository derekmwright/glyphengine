package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// This file drives the whole client-side prediction loop against a simulated
// authoritative server, in one process and with no networking.
//
// The unit tests in prediction_test.go prove that snapshot, restore, and
// replay work in isolation. They cannot catch the bugs that actually break
// prediction in practice, which are bookkeeping: stamping an input with the
// wrong tick, trimming the unacknowledged buffer off by one, or replaying from
// the wrong point. Those only appear once two simulations run at an offset
// from each other, which is what this builds.

const netTickDt = float32(1.0 / 60.0)

// inputMsg is one frame of player intent, stamped with the client tick it was
// produced on. The stamp is the whole point: wall-clock time and arrival order
// are both unreliable, but both sides agree on what tick 4213 means.
type inputMsg struct {
	tick   uint64
	intent MoveIntent
}

// stateMsg is the server's authoritative answer: where the character actually
// ended up, and the last input tick that result accounts for.
type stateMsg struct {
	tick  uint64
	state CharacterState
}

// link is a one-way channel with fixed latency measured in ticks.
type link[T any] struct {
	delay int
	queue []pending[T]
}

type pending[T any] struct {
	arriveAt uint64
	msg      T
}

func (l *link[T]) send(now uint64, msg T) {
	l.queue = append(l.queue, pending[T]{arriveAt: now + uint64(l.delay), msg: msg})
}

// deliver returns everything that has arrived by now, in send order.
func (l *link[T]) deliver(now uint64) []T {
	var out []T
	kept := l.queue[:0]
	for _, p := range l.queue {
		if p.arriveAt <= now {
			out = append(out, p.msg)
		} else {
			kept = append(kept, p)
		}
	}
	l.queue = kept
	return out
}

// world builds a scene with terrain and a wall, plus one character.
func world(t *testing.T) (*Scene, Entity) {
	t.Helper()
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	wall := s.Spawn()
	s.C.Transform.Set(wall, &Transform{Position: mgl32.Vec3{0, 2, -8}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(wall, &Collider{HalfExtents: mgl32.Vec3{6, 2, 0.5}})
	s.C.Static.Set(wall, &Static{})
	s.RebuildStatics()

	return s, spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
}

// scriptedInput is a deterministic input stream with turning, strafing,
// sprinting, and a jump, so replay covers more than walking in a line.
func scriptedInput(tick uint64) MoveIntent {
	return MoveIntent{
		Forward: 1,
		Right:   float32((tick/13)%3) - 1,
		Yaw:     float32(tick) * 0.02,
		Sprint:  tick%17 < 8,
		Jump:    tick%31 == 0,
	}
}

// session runs a client and an authoritative server in lockstep-with-latency
// and records what each believed the character's state was at every tick.
//
// serverImpulse, if non-nil, is applied only on the server — it stands in for
// anything the client could not have predicted (a knockback, a teleport, a
// correction from another player's action). It is what forces reconciliation
// to do real work rather than confirming an already-correct guess.
type session struct {
	ticks         int
	latency       int
	serverImpulse func(s *Scene, ch Entity, tick uint64)

	clientAt    map[uint64]CharacterState
	serverAt    map[uint64]CharacterState
	corrections int
}

func (sess *session) run(t *testing.T) {
	t.Helper()

	clientScene, clientCh := world(t)
	serverScene, serverCh := world(t)

	sess.clientAt = map[uint64]CharacterState{}
	sess.serverAt = map[uint64]CharacterState{}

	toServer := &link[inputMsg]{delay: sess.latency}
	toClient := &link[stateMsg]{delay: sess.latency}

	var unacked []inputMsg
	var serverTick uint64

	// What the client predicted for each tick, recorded at prediction time and
	// never rewritten. Reconciliation compares against this rather than
	// against the client's present state — by the time an authoritative update
	// for tick N arrives, the client is already a round trip past N, so
	// comparing "now" to "then" always mismatches and would correct on every
	// single packet.
	predictedAt := map[uint64]CharacterState{}

	for i := 0; i < sess.ticks; i++ {
		// ── client ──
		clientScene.UpdateSpatialGrid()
		clientScene.Tick(netTickDt)
		clientTick := clientScene.TickCount()

		intent := scriptedInput(clientTick)

		// Predict immediately: the local player must not wait a round trip to
		// see their own movement.
		clientScene.MoveCharacter(clientCh, intent, netTickDt)
		unacked = append(unacked, inputMsg{tick: clientTick, intent: intent})
		toServer.send(clientTick, inputMsg{tick: clientTick, intent: intent})

		st, _ := clientScene.SnapshotCharacter(clientCh)
		sess.clientAt[clientTick] = st
		predictedAt[clientTick] = st

		// ── server ──
		// Applies inputs in stamped order, one per tick, so its tick counter
		// stays aligned with the client's stamps rather than with arrival.
		for _, in := range toServer.deliver(clientTick) {
			serverScene.UpdateSpatialGrid()
			serverScene.Tick(netTickDt)
			serverTick = in.tick

			serverScene.MoveCharacter(serverCh, in.intent, netTickDt)
			if sess.serverImpulse != nil {
				sess.serverImpulse(serverScene, serverCh, serverTick)
			}

			auth, _ := serverScene.SnapshotCharacter(serverCh)
			sess.serverAt[serverTick] = auth
			toClient.send(clientTick, stateMsg{tick: serverTick, state: auth})
		}

		// ── reconciliation ──
		for _, msg := range toClient.deliver(clientTick) {
			// Drop everything the server has now accounted for. An off-by-one
			// here replays an input the server already applied, or drops one
			// it never saw — both show up as permanent drift.
			keep := unacked[:0]
			for _, u := range unacked {
				if u.tick > msg.tick {
					keep = append(keep, u)
				}
			}
			unacked = keep

			// Compare like for like: what did the client think the character's
			// state was at exactly this tick?
			predicted, ok := predictedAt[msg.tick]
			if !ok {
				continue
			}
			for tick := range predictedAt {
				if tick < msg.tick {
					delete(predictedAt, tick) // acknowledged; no longer needed
				}
			}
			if predicted.Position.Sub(msg.state.Position).Len() < 1e-6 {
				continue // prediction held; nothing to do
			}
			sess.corrections++

			// Rewind to authoritative truth, then replay what the server has
			// not seen yet.
			clientScene.RestoreCharacter(clientCh, msg.state)
			for _, u := range unacked {
				clientScene.UpdateSpatialGrid()
				clientScene.MoveCharacter(clientCh, u.intent, netTickDt)
			}
			if cur, ok := clientScene.SnapshotCharacter(clientCh); ok {
				sess.clientAt[clientTick] = cur
				predictedAt[clientTick] = cur
			}
		}
	}
}

// agreementFrom reports the first tick at or after `from` where the client and
// server disagree about position, or -1 if they agree everywhere both know.
func (sess *session) firstDisagreement(from uint64, tol float32) int {
	for tick := from; tick <= uint64(sess.ticks); tick++ {
		c, okc := sess.clientAt[tick]
		s, oks := sess.serverAt[tick]
		if !okc || !oks {
			continue
		}
		if c.Position.Sub(s.Position).Len() > tol {
			return int(tick)
		}
	}
	return -1
}

// TestPredictionMatchesServerWithoutDivergence is the bookkeeping test.
//
// Client and server run identical deterministic code, so if inputs are stamped
// and applied at matching ticks, every predicted state must equal the
// authoritative one exactly — no corrections should ever be needed. Any
// mismatch means a tick got misaligned, not that physics is wrong.
func TestPredictionMatchesServerWithoutDivergence(t *testing.T) {
	sess := &session{ticks: 240, latency: 6}
	sess.run(t)

	if len(sess.serverAt) == 0 {
		t.Fatal("server never processed any input; the harness is not exercising anything")
	}
	if bad := sess.firstDisagreement(0, 1e-6); bad >= 0 {
		c := sess.clientAt[uint64(bad)]
		s := sess.serverAt[uint64(bad)]
		t.Errorf("client and server disagree at tick %d:\n  predicted %v\n  authoritative %v",
			bad, c.Position, s.Position)
	}
	if sess.corrections != 0 {
		t.Errorf("%d corrections applied with nothing to correct; prediction should have held exactly",
			sess.corrections)
	}
	t.Logf("240 ticks at %d-tick latency: %d server states, zero corrections needed",
		sess.latency, len(sess.serverAt))
}

// TestReconciliationConvergesAfterServerImpulse is the correction test.
//
// The server shoves the character sideways at a tick the client had no way to
// predict. The client's guess is wrong from that point, and reconciliation has
// to notice, rewind, and replay — the path RestoreCharacter exists for.
func TestReconciliationConvergesAfterServerImpulse(t *testing.T) {
	const impulseAt = 60

	sess := &session{
		ticks:   240,
		latency: 6,
		serverImpulse: func(s *Scene, ch Entity, tick uint64) {
			if tick != impulseAt {
				return
			}
			// A knockback the client could not have known about.
			if tr, ok := s.C.Transform.Get(ch); ok {
				tr.Position[0] += 2.5
			}
		},
	}
	sess.run(t)

	if sess.corrections == 0 {
		t.Fatal("no corrections applied despite an unpredictable server impulse; the harness is not testing reconciliation")
	}

	// The client cannot know about the impulse until the state carrying it
	// arrives, which takes a round trip. After that it must agree again.
	settleBy := uint64(impulseAt + 2*sess.latency + 2)
	if bad := sess.firstDisagreement(settleBy, 1e-4); bad >= 0 {
		c := sess.clientAt[uint64(bad)]
		s := sess.serverAt[uint64(bad)]
		t.Errorf("client never reconverged: still disagrees at tick %d (impulse at %d)\n  predicted %v\n  authoritative %v",
			bad, impulseAt, c.Position, s.Position)
	}
	t.Logf("recovered from an unpredictable server impulse: %d corrections, converged by tick %d",
		sess.corrections, settleBy)
}

// TestReconciliationSurvivesHighLatency checks the buffer keeps its shape when
// there are many unacknowledged inputs in flight at once — 20 ticks each way
// is a third of a second, worse than most real connections.
func TestReconciliationSurvivesHighLatency(t *testing.T) {
	for _, latency := range []int{1, 6, 20} {
		sess := &session{ticks: 240, latency: latency}
		sess.run(t)

		if bad := sess.firstDisagreement(0, 1e-6); bad >= 0 {
			t.Errorf("latency %d: disagreement at tick %d", latency, bad)
		}
		if sess.corrections != 0 {
			t.Errorf("latency %d: %d corrections with nothing to correct", latency, sess.corrections)
		}
	}
}
