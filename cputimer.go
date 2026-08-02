package glyphengine

import "time"

// CPUPhase names a slice of the frame's CPU work.
//
// The split is chosen so each entry points at something a game can actually
// change. "The frame takes 8 ms" is not actionable; "6 ms of it is waiting for
// the GPU" and "4 ms of it is building the draw list" lead to opposite fixes.
type CPUPhase int

const (
	// CPUPoll is input sampling and window event pumping.
	CPUPoll CPUPhase = iota
	// CPUUpdate is Game.Update.
	CPUUpdate
	// CPUTick is Scene.Tick plus FixedUpdate, summed over however many ticks
	// the frame ran — which is zero on most frames at a high refresh rate.
	CPUTick
	// CPUAnimate is skeletal animation sampling.
	CPUAnimate
	// CPULateUpdate is Game.LateUpdate.
	CPULateUpdate
	// CPUDrawList is building, culling and sorting the draw list.
	CPUDrawList
	// CPUGPUWait is time blocked on the previous frame's fence and on acquiring
	// a swapchain image.
	//
	// The most useful number here. Large means the CPU is ahead and the GPU is
	// the limit — or, with vsync on, simply that the frame is being paced, which
	// is what a healthy capped frame looks like. Near zero while the frame is
	// long means the CPU is the limit and the rest of this table says where.
	CPUGPUWait
	// CPUSubmit is whatever else DrawFrame does: staged uploads, descriptor
	// writes, the submit call itself. Small when healthy; a large value means
	// per-frame work has crept in somewhere unattributed.
	CPUSubmit
	// CPURecord is building and submitting the command buffer. Genuine CPU work,
	// unlike the waits either side of it.
	CPURecord
	// CPUPresent is time inside QueuePresent, which with vsync on can block for
	// most of a frame. Split out from record because lumping the two together
	// reports a busy CPU on a frame that is only being paced.
	CPUPresent

	cpuPhaseCount
)

// String is the label used in reports.
func (p CPUPhase) String() string {
	switch p {
	case CPUPoll:
		return "poll"
	case CPUUpdate:
		return "update"
	case CPUTick:
		return "tick"
	case CPUAnimate:
		return "animate"
	case CPULateUpdate:
		return "lateupdate"
	case CPUDrawList:
		return "drawlist"
	case CPUGPUWait:
		return "gpuwait"
	case CPUSubmit:
		return "submit"
	case CPURecord:
		return "record"
	case CPUPresent:
		return "present"
	default:
		return "cpu?"
	}
}

// CPUTimings is one frame's CPU cost in milliseconds, per phase.
//
// Total is the whole loop body measured directly, so it can be compared against
// the sum the same way GPUTimings.Total can. Unlike the GPU passes these run
// strictly in sequence, so a gap between total and sum is unmeasured work rather
// than overlap — which makes it a useful check that the phases still cover the
// loop after it is edited.
type CPUTimings struct {
	Phase [cpuPhaseCount]float32
	Total float32
	Valid bool
}

// cpuTimer accumulates per-phase durations and reports their mean.
//
// A mean rather than the last frame, for the same reason the GPU timer uses one:
// a single frame's numbers swing with whatever the scheduler was doing, and the
// tick phase in particular is zero on most frames and large on the ones that run
// two ticks. Averaging is the only reading that means anything.
type cpuTimer struct {
	frame  [cpuPhaseCount]time.Duration
	sum    [cpuPhaseCount]time.Duration
	total  time.Duration
	frames int

	// start is when the currently open phase began. Only one phase is open at a
	// time — the loop is sequential, and nesting would make the sum meaningless.
	start  time.Time
	open   CPUPhase
	isOpen bool
}

// begin opens a phase, closing any phase still open.
func (t *cpuTimer) begin(p CPUPhase) {
	now := time.Now()
	if t.isOpen {
		t.frame[t.open] += now.Sub(t.start)
	}
	t.start, t.open, t.isOpen = now, p, true
}

// stop closes the open phase without opening another.
func (t *cpuTimer) stop() {
	if !t.isOpen {
		return
	}
	t.frame[t.open] += time.Since(t.start)
	t.isOpen = false
}

// add folds in a duration measured elsewhere, for phases the loop cannot time
// itself — the fence wait happens inside the renderer.
func (t *cpuTimer) add(p CPUPhase, d time.Duration) { t.frame[p] += d }

// endFrame folds this frame into the running mean and clears it.
func (t *cpuTimer) endFrame(total time.Duration) {
	t.stop()
	for i, d := range t.frame {
		t.sum[i] += d
		t.frame[i] = 0
	}
	t.total += total
	t.frames++
}

// mean returns the average over every frame recorded since the last reset.
func (t *cpuTimer) mean() CPUTimings {
	if t.frames == 0 {
		return CPUTimings{}
	}
	n := float32(t.frames)
	out := CPUTimings{
		Valid: true,
		Total: float32(t.total.Seconds()*1000) / n,
	}
	for i, d := range t.sum {
		out.Phase[i] = float32(d.Seconds()*1000) / n
	}
	return out
}

// reset discards the accumulated mean, so a run can exclude its warm-up frames.
func (t *cpuTimer) reset() {
	t.sum = [cpuPhaseCount]time.Duration{}
	t.total, t.frames = 0, 0
}

// CPUTimings returns the mean per-phase CPU cost in milliseconds.
//
// Pair it with GPUTimings: together they say whether a frame is CPU-bound,
// GPU-bound, or simply paced by vsync, which the three look identical without.
func (e *Engine) CPUTimings() CPUTimings { return e.cpu.mean() }

// ResetTimings discards both the CPU and GPU accumulated means, for measuring
// one phase of a run without its startup frames.
func (e *Engine) ResetTimings() {
	e.cpu.reset()
	e.renderer.ResetGPUTimings()
}
