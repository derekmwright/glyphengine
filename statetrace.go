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

	// Everything the environment decided this frame, and nothing it did not.
	//
	// This replaced a `sky=` that hashed nine of the state's fields plus
	// Scene.TimeOfDay, and both halves of that were wrong for what the field is
	// for. The nine were a hand-picked subset, so a source that moved Cirrus,
	// the Milky Way, a disc colour or the palette moved the capture and left the
	// trace agreeing -- which is the one thing a trace field must not do. And
	// TimeOfDay reached past the seam into the built-in cycle, so the field would
	// have changed meaning the moment a replacement source was plugged in, which
	// is exactly when the two are being compared. Nothing is lost by dropping
	// it: every value the clock determines is derived below.
	envHash := hashEnvironment(env).
		// The scattering medium rides with the environment because it IS the
		// fog's: the density and height profile above are what a beam scatters
		// off, and these two are the only part of the medium the fog does not
		// already say. They are Scene state rather than EnvironmentState (see
		// Scene.SetVolumetrics), so they are folded in here rather than inside
		// hashEnvironment. A game that animates either -- dust settling, a step
		// count dropped under load -- moves every beam in the frame, and without
		// this the trace would show a capture that changed with an `env=` that
		// did not.
		Float32(e.Scene.Volumetrics().Anisotropy).
		Uint64(uint64(e.Scene.Volumetrics().Steps))
	t.Hash("env", envHash)
}

// hashEnvironment hashes every field of an EnvironmentState.
//
// Every field, with no judgement about which ones matter: a field in the state
// is a field some shader reads, and the point of the carve is that one source
// decides all of them. TestEnvTraceCoversEveryField walks the struct by
// reflection and fails if any field can be changed without changing this hash,
// so adding a field to EnvironmentState and forgetting this function is a test
// failure rather than a quietly narrower trace.
func hashEnvironment(env EnvironmentState) renderer.Hasher {
	h := renderer.NewHash.
		Vec3(env.SunDir).
		Vec3(env.SunColor).
		Vec3(env.RealSunDir).
		Float32(env.SunElevation).
		Vec3(env.Ambient).
		Float32(env.FogDensity).
		Float32(env.FogHeight).
		Float32(env.FogBaseHeight).
		Vec3(env.ClearColor).
		Float32(env.StarFade).
		Float32(env.MilkyWay).
		Float32(env.StarDensity).
		Bool(env.DrawSky).
		Bool(env.DrawStars).
		Bool(env.DrawSun).
		Bool(env.DrawMoon).
		Vec3(env.SunDiscDir).
		Vec3(env.SunDiscColor).
		Vec3(env.MoonDiscDir).
		Vec3(env.MoonDiscColor).
		Uint64(uint64(env.CloudSteps)).
		Float32(env.Cirrus).
		Float32(env.LightShafts).
		Float32(env.LightShaftShape.Radius).
		Float32(env.LightShaftShape.Decay).
		Float32(env.LightShaftShape.Threshold[0]).
		Float32(env.LightShaftShape.Threshold[1]).
		Bool(env.CastShadows).
		Float32(env.NightGrade.Strength)
	h = hashVec3(h, env.NightGrade.Tint)
	for _, c := range [...]mgl32.Vec3{
		env.SkyPalette.ZenithDay, env.SkyPalette.HorizonDay,
		env.SkyPalette.ZenithTwilight, env.SkyPalette.HorizonTwilight,
		env.SkyPalette.ZenithNight, env.SkyPalette.HorizonNight,
	} {
		h = hashVec3(h, c)
	}
	return h
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
