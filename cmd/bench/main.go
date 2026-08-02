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
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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
	{"cube", "02-cube", []string{"-frames", "200"}, "control: minimal lit scene"},
	{"terrain", "07-terrain", []string{"-frames", "200"}, "heightmap terrain, no flora"},
	{"grass", "08-grass", []string{"-frames", "200"}, "instanced flora, the heaviest pass"},
	{"water", "09-water", []string{"-frames", "200"}, "refraction pass and god rays"},
	{"lights", "11-lights", []string{"-frames", "200"}, "one shadowed point light plus fills"},
	{"particles", "12-particles", []string{"-frames", "200"}, "three additive emitters"},
	{"materials", "16-materials", []string{"-frames", "200"}, "the material pipeline"},
	{"kitchensink", "15-kitchen-sink", []string{"-demo", "-frames", "240"}, "everything at once"},
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
	repeat := flag.Int("repeat", 1, "run each scene N times and keep the fastest")
	flag.Parse()

	var results []result
	for _, sc := range scenes {
		if *only != "" && sc.name != *only {
			continue
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
		"GLYPHENGINE_TIMING=tsv",
		"GLYPHENGINE_BENCH_LABEL="+sc.name,
	)

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
	return r, nil
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
