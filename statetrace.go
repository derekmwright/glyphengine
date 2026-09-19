package glyphengine

import (
	"log"
	"os"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/renderer"
)

// GLYPHENGINE_STATE_TRACE names a file to write one line per frame-loop
// iteration to. See renderer.StateTrace for the format and the workflow; this
// is the engine's half of it, and docs/agents/statetrace.md is the page.
//
// Read from the environment rather than exposed as an Option for the same
// reason GLYPHENGINE_FIXED_FRAME_TIME and GLYPHENGINE_VALIDATION are: the run
// that needs tracing is almost always an example someone else built, and
// recompiling it to add a flag is the step that does not happen.
const stateTraceEnv = "GLYPHENGINE_STATE_TRACE"

// openStateTrace returns the trace asked for by the environment, or nil.
// A trace that cannot be created is reported and skipped: losing a diagnostic
// should not stop the run that was going to produce the evidence.
func openStateTrace() *renderer.StateTrace {
	path := os.Getenv(stateTraceEnv)
	if path == "" {
		return nil
	}
	t, err := renderer.OpenStateTrace(path)
	if err != nil {
		log.Printf("glyphengine: %s: %v", stateTraceEnv, err)
		return nil
	}
	log.Printf("glyphengine: state trace -> %s", path)
	return t
}

// traceSimulation records the simulation state this frame will be rendered
// from, split into the three groups that fail independently.
//
// Split rather than one hash because the first question a diff raises is which
// half moved. A run whose clock= matches but whose cam= does not is a camera
// that drifted under an identical clock -- interpolation alpha, a follow that
// read a different transform -- and a run whose clock= moved is a frame loop
// that ticked a different number of times. Those are opposite investigations,
// and one combined hash would start both of them at the same place.
func (e *Engine) traceSimulation(t *renderer.StateTrace, view, proj, vp mgl32.Mat4, env EnvironmentState) {
	t.Int("ticks", e.tickCount)

	clock := renderer.NewHash.
		Float32(e.elapsed).
		Float32(e.alpha).
		Float32(e.timeScale).
		Uint64(uint64(e.accumulator))
	t.Hash("clock", clock)

	cam := renderer.NewHash
	cam = hashVec3(cam, e.cameraEye)
	cam = hashVec3(cam, e.cameraCenter)
	cam = hashVec3(cam, e.cameraUp)
	cam = hashMat4(cam, view)
	cam = hashMat4(cam, proj)
	cam = hashMat4(cam, vp)
	t.Hash("cam", cam)

	sky := renderer.NewHash.
		Float32(e.Scene.TimeOfDay()).
		Vec3(env.SunDir).
		Vec3(env.SunColor).
		Vec3(env.SunDiscDir).
		Float32(env.SunElevation).
		Vec3(env.Ambient).
		Float32(env.StarFade).
		Float32(env.FogDensity).
		Bool(env.CastShadows)
	t.Hash("sky", sky)
}

// traceDrawList records the draw list in the order it was handed to the
// recorder.
//
// Order is part of the hash on purpose. Opaque draws are depth-tested so their
// order cannot change the image, but the blended tail's order IS the image, and
// the list arrives from an ECS query that walks a Go map -- so its order is
// randomised on every frame of every run before it is sorted. Whether that
// randomness survives the sort is exactly what this field answers.
//
// Nothing here hashes a pointer. Model matrices and material constants identify
// a draw well enough to tell two of them apart, and two draws that hash the
// same are two draws that render the same, so a swap between them could not
// have changed the picture either.
// The companion <key>set= field XORs the same per-draw hashes, so it does not
// depend on order. Two runs whose draws= differ and whose drawsset= agree were
// handed the same geometry in a different sequence, which is the whole of the
// question; without the second field a reordering and a moved object read the
// same, and they are not the same bug.
func traceDrawList(t *renderer.StateTrace, key string, draws []renderer.RenderObject) {
	seq := renderer.NewHash
	var set renderer.Hasher
	for i := range draws {
		d := &draws[i]
		h := renderer.NewHash.Mat4(d.Model).Mat4(d.MVP).Vec3(d.Color).
			Float32(d.Metallic).Float32(d.Roughness).Float32(d.Alpha).
			Bool(d.Emissive).Bool(d.DoubleSided).Bool(d.NoCastShadow).Bool(d.ShadowOnly).
			Bool(d.Instances != nil).Bool(d.Joints != nil).Bool(d.TerrainMat != nil).
			Bool(d.Water != nil).Bool(d.Material != nil).Bool(d.Texture != nil)
		seq = seq.Uint64(uint64(h))
		set ^= h
	}
	t.CountHash(key, len(draws), seq)
	t.Hash(key+"set", set)
}

func hashVec3(h renderer.Hasher, v mgl32.Vec3) renderer.Hasher {
	return h.Float32(v[0]).Float32(v[1]).Float32(v[2])
}

func hashMat4(h renderer.Hasher, m mgl32.Mat4) renderer.Hasher {
	for i := 0; i < 16; i++ {
		h = h.Float32(m[i])
	}
	return h
}
