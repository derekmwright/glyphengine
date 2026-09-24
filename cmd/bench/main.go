// Command bench runs a fixed set of example scenes and prints one comparable
// table of CPU and GPU cost.
//
// The point is answering "did that change help" without hand-running examples
// and eyeballing log lines. Every scene runs a fixed frame count, so the
// workload is the same between runs; the numbers are means over those frames,
// so they do not depend on which frame the run happened to end on.
//
//	task bench                    # every scene
//	task bench -- -scene grass    # one of them
//	task bench -- -json out.json  # for diffing between commits
//
// What it does not do is compare against a stored baseline. Frame cost depends
// on the GPU, the driver, the display mode and what else is running, so a
// committed baseline would be a number from someone else's machine. Run it
// before and after a change on the same machine instead.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// scene is one benchmark entry: an example and the arguments that make it
// render a representative, repeatable workload.
type scene struct {
	name  string
	dir   string
	args  []string
	blurb string
}

// The set covers the passes that actually cost something, plus a minimal scene
// as a control: a change that slows 02-cube down is a change to something
// fundamental.
//
// 01-triangle is deliberately absent. It drives the renderer directly with its
// own loop rather than going through Engine.Run, which is the point of it, and
// that means there is no frame loop to instrument and no BENCH line to parse.
var scenes = []scene{
	{"lod-full", "25-lod-forest", []string{"-frames", "200", "-levels", "1"}, "3600 full-detail trees, per-set culling"},
	{"lod", "25-lod-forest", []string{"-frames", "200"}, "same placements, per-instance culling and four LOD bands"},
	{"cube", "02-cube", []string{"-frames", "200"}, "control: minimal lit scene"},
	{"terrain", "07-terrain", []string{"-frames", "200"}, "heightmap terrain, no flora"},
	{"grass", "08-grass", []string{"-frames", "200"}, "instanced flora, the heaviest pass"},
	{"water", "09-water", []string{"-frames", "200"}, "refraction pass and god rays"},
	{"clouds-sunset", "09-water", []string{"-frames", "200", "-time", "0.755", "-pitch", "-0.38", "-yaw", "1.771"}, "sunset cumulus"},
	{"clouds-cirrus", "09-water", []string{"-frames", "200", "-time", "0.755", "-pitch", "-0.38", "-yaw", "1.771", "-cirrus", "1"}, "same sunset with high cirrus"},
	{"lights", "11-lights", []string{"-frames", "200"}, "one shadowed point light plus fills"},
	{"particles", "12-particles", []string{"-frames", "200"}, "three additive emitters"},
	{"materials", "16-materials", []string{"-frames", "200"}, "the material pipeline"},
	{"translucent", "18-translucent", []string{"-frames", "200"}, "the blended pass and its sort"},

	// The same 900 props drawn two ways. Paired deliberately: instancing trades
	// CPU command recording for GPU vertex work that culling no longer removes,
	// and the only honest way to say whether that is a win is to run both
	// against an identical field.
	{"instanced", "19-instanced", []string{"-count", "900", "-instanced=true", "-frames", "200"}, "900 props, one draw call"},
	{"individual", "19-instanced", []string{"-count", "900", "-instanced=false", "-frames", "200"}, "the same 900, one draw each"},

	// Clustered vs. brute force at three light counts, all on -lamps -- a
	// spread-out grid of short-range lights, not the default ring, where
	// every light reaches most of the scene and clustering has nothing to
	// remove (see the -lamps doc comment in 11-lights). Paired so the same
	// scene runs both ways: a difference here is what clustering costs or
	// saves on real work, not two different workloads.
	{"lights32-clustered", "11-lights", []string{"-lamps", "32", "-frames", "200"}, "32 lamps, clustered"},
	{"lights32-brute", "11-lights", []string{"-lamps", "32", "-lightdebug", "bruteforce", "-frames", "200"}, "32 lamps, brute force"},
	{"lights256-clustered", "11-lights", []string{"-lamps", "256", "-frames", "200"}, "256 lamps, clustered"},
	{"lights256-brute", "11-lights", []string{"-lamps", "256", "-lightdebug", "bruteforce", "-frames", "200"}, "256 lamps, brute force"},
	{"lights1024-clustered", "11-lights", []string{"-lamps", "1024", "-frames", "200"}, "1024 lamps (the cap), clustered"},
	{"lights1024-brute", "11-lights", []string{"-lamps", "1024", "-lightdebug", "bruteforce", "-frames", "200"}, "1024 lamps (the cap), brute force"},

	{"streetlights", "21-streetlights", []string{"-frames", "200"}, "a settlement: PBR walls, door spots, lamp posts"},

	// The same settlement with every light asking for volumetric
	// in-scattering, against the row above as its control. Paired for the
	// reason the clustered/brute-force rows are: the only honest way to say
	// what the march costs is the same scene with and without it, and the
	// cost lands INSIDE the passes that were already there -- opaque, terrain
	// and sky -- because the march lives in applyFog and in sky.frag rather
	// than in a pass of its own. There is no gpu_volumetric column to read
	// and there cannot be one.
	//
	// -skylamp is in here on purpose: it is the only light in the set whose
	// cone crosses sky pixels, and sky.frag's march is a different shader
	// from the lit one.
	{"streetlights-volumetric", "21-streetlights", []string{"-volumetric", "1", "-lampvolumetric", "1", "-skylamp", "-frames", "200"}, "the same settlement, every light scattering"},

	// Lit water, which is a different shape of light cost from the scenes
	// above: the surface is one near-horizontal draw covering most of the
	// lower frame, so a light over the lake lands on a great many fragments
	// at once. -lampposts=false keeps the three rows drawing identical
	// geometry, so the difference between them is the light loop and nothing
	// else. `water` above stays the unlit control.
	{"waterlights32", "09-water", []string{"-time", "0.02", "-lamps", "32", "-spots", "0", "-lampposts=false", "-frames", "200"}, "32 lamps over a lake"},
	{"waterlights400", "09-water", []string{"-time", "0.02", "-lamps", "400", "-spots", "0", "-lampposts=false", "-frames", "200"}, "400 lamps over the same lake"},

	// The same 32 lamps with every one of them scattering, against
	// waterlights32 as its control -- the SAME arguments plus -volumetric 1,
	// so the two rows differ by the march and by nothing else. The first
	// version of this row added two spot lights as well, which would have
	// made a third of the difference something other than what it claimed to
	// measure.
	//
	// Water is its own shape of cost here: the surface is one near-horizontal
	// draw covering most of the lower frame, so every one of those fragments
	// runs a march, and it runs it in the water pass on top of the opaque
	// pass having already run one for the bed underneath. That double count
	// is measured in docs/agents/lights.md and is not a bug -- see the note
	// there about the shoreline.
	{"waterlights-volumetric", "09-water", []string{"-time", "0.02", "-lamps", "32", "-spots", "0", "-volumetric", "1", "-lampposts=false", "-frames", "200"}, "the same 32 lamps, all scattering"},

	// The screen-space UI, three ways over one scene. Paired for the same
	// reason the instanced and clustered rows are: the only honest way to say
	// what a second layer costs is to render the same frame with and without
	// it, and the only honest way to separate the layer from the glow is a row
	// where the layer is on and nothing is asking to emit.
	//
	// gpu_composite is where the difference shows up twice over: with the
	// layer on it stops holding the UI quads and holds one fullscreen triangle
	// instead, because the composite REPLACES the direct draw. gpu_uilayer and
	// gpu_uiglow are the added cost.
	{"ui", "13-ui", []string{"-frames", "200"}, "HUD drawn straight onto the swapchain"},
	{"ui-layer", "13-ui", []string{"-glow", "layer", "-frames", "200"}, "the same HUD through its own HDR layer, nothing glowing"},
	{"ui-glow", "13-ui", []string{"-glow", "on", "-frames", "200"}, "the same HUD again, three elements emitting"},

	{"kitchensink", "15-kitchen-sink", []string{"-demo", "-frames", "240"}, "everything at once"},
	{"lod-gpu", "25-lod-forest", []string{"-frames", "200", "-gpu"}, "same placements, GPU selection and indirect draws"},
}

// benchLine parses the tab-separated line LogTimingsTSV emits.
var benchLine = regexp.MustCompile(`BENCH\t(.+)`)

type result struct {
	Scene  string             `json:"scene"`
	Frames int                `json:"frames"`
	Values map[string]float64 `json:"values"`
}

func main() {
	only := flag.String("scene", "", "run only the named scene")
	jsonOut := flag.String("json", "", "also write results as JSON to this path")
	repeat := flag.Int("repeat", 1, "run N times; patches retains interleaved samples, other scenes keep the fastest")
	// extra exists so a sweep -- a resolution, a step count, a light count --
	// can be measured with this tool's parsing and this tool's table instead
	// of a one-off script that reports something subtly different. It is
	// deliberately only useful with -scene: appending "-volsteps 32" to
	// 02-cube would fail, and failing loudly on one named scene is clearer
	// than silently skipping it in a run of twenty.
	extra := flag.String("extra", "", "extra arguments appended to the scene's own, e.g. -extra \"-width 1920 -height 1080\"; requires -scene")
	flag.Parse()
	if *only == "patches" {
		if err := runPatches(*repeat, *jsonOut, *extra); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if *extra != "" && *only == "" {
		fmt.Fprintln(os.Stderr, "bench: -extra needs -scene: the arguments are not valid for every scene")
		os.Exit(2)
	}

	var results []result
	for _, sc := range scenes {
		if *only != "" && sc.name != *only {
			continue
		}

		if *extra != "" {
			sc.args = append(append([]string{}, sc.args...), strings.Fields(*extra)...)
		}

		var best *result
		for i := 0; i < *repeat; i++ {
			r, err := run(sc)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", sc.name, err)
				break
			}
			// Fastest of N: a slow run means something else on the machine
			// interfered, and there is no such thing as a spuriously fast one.
			if best == nil || r.Values["cpu_total"] < best.Values["cpu_total"] {
				best = r
			}
		}
		if best != nil {
			results = append(results, *best)
			printRow(*best)
		}
	}

	if *jsonOut != "" && len(results) > 0 {
		writeJSON(*jsonOut, results)
	}
}

// run executes one scene and parses its bench line.
func run(sc scene) (*result, error) {
	args := append([]string{"run", "./" + sc.dir}, sc.args...)
	cmd := exec.Command("go", args...)
	cmd.Dir = "examples"
	cmd.Env = append(os.Environ(),
		"GLYPHENGINE_BACKGROUND=1",
		"GLYPHENGINE_TIMING=tsv",
		"GLYPHENGINE_BENCH_LABEL="+sc.name,
	)

	if strings.HasPrefix(sc.name, "lod") || strings.HasPrefix(sc.name, "patches") {
		cmd.Env = append(cmd.Env, "GLYPHENGINE_FIXED_FRAME_TIME=16.667ms")
	}

	start := time.Now()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run failed after %s: %w\n%s", time.Since(start).Round(time.Millisecond), err, out)
	}

	m := benchLine.FindSubmatch(out)
	if m == nil {
		return nil, fmt.Errorf("no BENCH line in output; is timing supported on this device?")
	}

	fields := strings.Split(strings.TrimRight(string(m[1]), "\r\n"), "\t")
	if len(fields) < 2 {
		return nil, fmt.Errorf("malformed BENCH line")
	}
	r := &result{Scene: fields[0], Values: map[string]float64{}}
	r.Frames, _ = strconv.Atoi(fields[1])
	for i := 2; i+1 < len(fields); i += 2 {
		v, err := strconv.ParseFloat(fields[i+1], 64)
		if err != nil {
			continue
		}
		r.Values[fields[i]] = v
	}
	if p := regexp.MustCompile(`PATCHES\t([^\r\n]+)`).FindSubmatch(out); p != nil {
		fields := strings.Split(string(p[1]), "\t")
		for i := 0; i+1 < len(fields); i += 2 {
			if v, err := strconv.ParseFloat(fields[i+1], 64); err == nil {
				r.Values[fields[i]] = v
			}
		}
	}
	return r, nil
}

// A/B/C/A/B/C preserves all samples instead of selecting a favourable fastest
// run. Compare the mean saving to the within-mode range at 400 patches; accept
// batching only when CPU recording improves beyond scatter without GPU cost.
func runPatches(repeat int, jsonOut, extra string) error {
	if repeat < 2 {
		repeat = 2
	}
	var results []result
	for _, n := range []int{100, 400, 1600} {
		for trial := 0; trial < repeat; trial++ {
			for _, mode := range []string{"separate", "ranges", "indirect"} {
				if err := patchesGPUIdle(); err != nil {
					return err
				}
				sc := scene{name: fmt.Sprintf("patches-%d-%s-%d", n, mode, trial+1), dir: "26-mesh-ranges", args: []string{"-count", strconv.Itoa(n), "-mode", mode, "-frames", "200"}}
				sc.args = append(sc.args, strings.Fields(extra)...)
				r, err := run(sc)
				if err != nil {
					return err
				}
				if err := patchesGPUIdle(); err != nil {
					return fmt.Errorf("discard %s: %w", sc.name, err)
				}
				results = append(results, *r)
				fmt.Printf("%s: record %.3f ms CPU %.3f ms GPU %.3f ms draws %.0f instances %.0f geometry buffers %.0f bytes %.0f Alloc %.6f ms\n", r.Scene, r.Values["cpu_record"], r.Values["cpu_total"], r.Values["gpu_total"], r.Values["n_draws"], r.Values["n_instances"], r.Values["n_geometry_buffers"], r.Values["geometry_bytes"], r.Values["alloc_ms"])
			}
		}
	}
	if jsonOut != "" {
		writeJSON(jsonOut, results)
	}
	return nil
}

// Validate the Windows process-isolation precondition on each side of every
// sample, so a second engine run cannot quietly become performance evidence.
func patchesGPUIdle() error {
	if runtime.GOOS != "windows" {
		return nil
	}
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return fmt.Errorf("patches: tasklist: %w", err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		return err
	}
	example := regexp.MustCompile(`^[0-9]{2}-.*\.exe$`)
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		pid, _ := strconv.Atoi(row[1])
		if pid == os.Getpid() {
			continue
		}
		name := strings.ToLower(row[0])
		if example.MatchString(name) || name == "bench.exe" || name == "universebuild.exe" || name == "churn.exe" || strings.HasSuffix(name, "check.exe") {
			return fmt.Errorf("patches: GPU busy: %s PID %d; retry when no other engine jobs are running", name, pid)
		}
	}
	return nil
}

// printRow prints one scene: the totals, then whichever passes are actually
// costing something. Printing all sixteen columns for every scene buries the one
// that matters under a row of zeros.
func printRow(r result) {
	cpu, gpu := r.Values["cpu_total"], r.Values["gpu_total"]
	wait := r.Values["cpu_gpuwait"] + r.Values["cpu_present"]

	fmt.Printf("\n%-12s  %d frames\n", r.Scene, r.Frames)
	fmt.Printf("  frame %7.3f ms    gpu %7.3f ms    waiting %7.3f ms    cpu work %6.3f ms\n",
		cpu, gpu, wait, cpu-wait)

	if c, u := r.Values["cpu_lodcull"], r.Values["cpu_lodupload"]; c+u > 0 {
		fmt.Printf("    cpu LOD cull %.3f ms  upload %.3f ms (per frame)\n", c, u)
	}
	type kv struct {
		k string
		v float64
	}
	var hot []kv
	for k, v := range r.Values {
		if !strings.HasPrefix(k, "gpu_") || k == "gpu_total" || v < 0.01 {
			continue
		}
		hot = append(hot, kv{strings.TrimPrefix(k, "gpu_"), v})
	}
	sort.Slice(hot, func(i, j int) bool { return hot[i].v > hot[j].v })
	for _, h := range hot {
		share := 0.0
		if gpu > 0 {
			share = h.v / gpu * 100
		}
		fmt.Printf("    gpu %-10s %6.3f ms  %4.1f%%\n", h.k, h.v, share)
	}
}

func writeJSON(path string, results []result) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
		return
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		fmt.Fprintf(os.Stderr, "encode %s: %v\n", path, err)
		return
	}
	fmt.Printf("\nwrote %s\n", path)
}
