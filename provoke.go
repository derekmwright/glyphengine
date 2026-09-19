package glyphengine

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/derekmwright/glyphengine/renderer"
)

// Determinism provocations: environment variables that inject, on demand, the
// things a run normally suffers by accident.
//
// Issue #40 is an intermittent render difference that was sighted twice in
// months and could not be reproduced on purpose. Waiting for it is not a
// method, and neither is reading code: the candidates -- a frame the window
// system made the renderer drop, a swapchain rebuilt mid-run, a draw list that
// arrived in a different order -- all happen at times nobody chooses.
//
// So choose them. Each variable below makes one of those happen on a named
// frame, and a capture taken with it must be byte-identical to one taken
// without it under a fixed clock. If it is not, that is a determinism bug
// whether or not it is the sighted one, and `task determinism` gates on it.
//
// All of them are inert unless set, and parsed once at construction.
const (
	// provokeSkipEnv makes DrawFrame draw nothing on the listed loop frames,
	// taking the path an out-of-date acquire takes. Comma-separated, 1-based,
	// counting every iteration of the frame loop.
	provokeSkipEnv = "GLYPHENGINE_PROVOKE_SKIP_FRAMES"

	// provokeRecreateEnv forces a swapchain rebuild after the present on the
	// listed loop frames, which is what a resize, a suboptimal present or a
	// monitor change produces.
	provokeRecreateEnv = "GLYPHENGINE_PROVOKE_RECREATE_FRAMES"

	// provokeDrawOrderEnv permutes the draw list before it is sorted.
	// "reverse" is the value that means something: the list is assembled by an
	// ECS query that walks a Go map, so its order before the sort is already
	// arbitrary, and reversing it is a permutation that walk could legally
	// have produced. If the capture changes, the sort's tie-breaking reaches
	// the image and every run is a coin toss.
	provokeDrawOrderEnv = "GLYPHENGINE_PROVOKE_DRAW_ORDER"

	// provokeStallEnv sleeps inside the first N loop frames, as
	// "<frames>:<duration>" -- e.g. "20:40ms". It stands in for a cold GPU
	// clocking up, a compile still finishing, or another process holding the
	// machine: all the things the two sightings had in common and none of
	// which a fixed clock is allowed to notice.
	provokeStallEnv = "GLYPHENGINE_PROVOKE_STALL"
)

// provocations is the parsed form of the variables above. The zero value does
// nothing, which is what every normal run gets.
type provocations struct {
	skip     map[int]bool
	recreate map[int]bool
	reverse  bool

	stallFrames int
	stallFor    time.Duration
}

// active reports whether anything was asked for, so Run can log it once. A run
// whose capture is being compared against another needs to say out loud that
// it was provoked; a silent one invites a "these do not match" with no cause.
func (p *provocations) active() bool {
	return len(p.skip) > 0 || len(p.recreate) > 0 || p.reverse || p.stallFrames > 0
}

func readProvocations() provocations {
	var p provocations
	p.skip = parseFrameList(provokeSkipEnv)
	p.recreate = parseFrameList(provokeRecreateEnv)

	switch v := os.Getenv(provokeDrawOrderEnv); v {
	case "":
	case "reverse":
		p.reverse = true
	default:
		log.Printf("%s=%q is not a permutation (want \"reverse\"), ignoring", provokeDrawOrderEnv, v)
	}

	if v := os.Getenv(provokeStallEnv); v != "" {
		frames, dur, ok := strings.Cut(v, ":")
		n, err := strconv.Atoi(frames)
		d, derr := time.ParseDuration(dur)
		switch {
		case !ok || err != nil || derr != nil || n <= 0 || d <= 0:
			log.Printf("%s=%q is not <frames>:<duration> (want e.g. 20:40ms), ignoring", provokeStallEnv, v)
		default:
			p.stallFrames, p.stallFor = n, d
		}
	}
	return p
}

// parseFrameList reads a comma-separated list of 1-based loop frame numbers.
// An unparseable entry is reported rather than skipped: a provocation that
// quietly did not happen turns a passing gate into a lie.
func parseFrameList(env string) map[int]bool {
	v := os.Getenv(env)
	if v == "" {
		return nil
	}
	out := make(map[int]bool)
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			log.Printf("%s=%q: %q is not a positive frame number, ignoring it", env, v, part)
			continue
		}
		out[n] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// apply runs whatever this loop frame was asked to suffer, before the frame is
// rendered. loop is 1-based and counts every iteration, skipped ones included,
// so the numbers a caller gives line up with the state trace's loop= field.
func (p *provocations) apply(loop int, r *renderer.Renderer) {
	if loop <= p.stallFrames {
		time.Sleep(p.stallFor)
	}
	if p.skip[loop] {
		r.ProvokeSkipNextFrame()
	}
	if p.recreate[loop] {
		// The same flag a real resize sets, so the frame takes the real path:
		// drawn and presented, then the swapchain rebuilt underneath it.
		r.NotifyResize()
	}
}
