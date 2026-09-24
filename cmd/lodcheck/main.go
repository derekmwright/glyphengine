// Command lodcheck verifies the forest's culling, transitions and far billboards.
// Captures and process output are retained under .task/lod for visual review.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var dir = ".task/lod"
var binary string
var gpu bool
var only = flag.String("only", "", "one check: cull, pop, impostor, determinism, lifetime or bench")
var reuse = flag.Bool("reuse", false, "check existing captures without rerunning the renderer")
var reuseBench = flag.Bool("reuse-bench", false, "reuse retained benchmark measurements while rerendering other checks")

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "LOD FAIL:", err)
		os.Exit(1)
	}
}

func run() error {
	if *only != "" && !strings.Contains(",cull,pop,impostor,determinism,lifetime,bench,", ","+*only+",") {
		return fmt.Errorf("unknown check %q", *only)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	binary, _ = filepath.Abs(filepath.Join(dir, "forest.exe"))
	if !*reuse {
		cmd := exec.Command("go", "build", "-C", "examples", "-o", binary, "./25-lod-forest")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("build: %w\n%s", err, out)
		}
	}
	for _, mode := range []string{"cpu", "gpu"} {
		gpu = mode == "gpu"
		dir = filepath.Join(".task/lod", mode)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		fmt.Printf("LOD mode=%s\n", mode)
		for _, check := range []struct {
			name string
			fn   func() error
		}{
			{"cull", cull}, {"pop", pop}, {"impostor", impostor}, {"determinism", determinism}, {"lifetime", lifetime},
		} {
			if *only == "" || *only == check.name {
				if err := check.fn(); err != nil {
					return err
				}
			}
		}
	}
	dir = ".task/lod"
	if *only == "" || *only == "determinism" {
		cmd := exec.Command("go", "run", "./cmd/pngsame", filepath.Join(dir, "cpu/repeat-a.png"), filepath.Join(dir, "gpu/repeat-a.png"))
		out, err := cmd.CombinedOutput()
		fmt.Print(string(out))
		if err != nil {
			return fmt.Errorf("CPU/GPU pixels: %w", err)
		}
		cmd = exec.Command("go", "run", "./cmd/pngsame", filepath.Join(dir, "cpu/indexed.png"), filepath.Join(dir, "gpu/indexed.png"))
		out, err = cmd.CombinedOutput()
		fmt.Print(string(out))
		if err != nil {
			return fmt.Errorf("CPU/GPU indexed pixels: %w", err)
		}
	}
	if *only == "" || *only == "bench" {
		if err := bench(); err != nil {
			return err
		}
	}

	return nil
}

func render(label string, env []string, args ...string) ([]byte, error) {
	path := filepath.Join(dir, label+".log")
	if *reuse {
		return os.ReadFile(path)
	}
	if gpu {
		args = append(args, "-gpu")
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "GLYPHENGINE_BACKGROUND=1", "GLYPHENGINE_FIXED_FRAME_TIME=16.667ms", "GLYPHENGINE_VALIDATION=1")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	os.WriteFile(path, out, 0644)
	if err != nil {
		return out, fmt.Errorf("%s: %w\n%s", label, err, out)
	}
	if bytes.Contains(out, []byte("VULKAN ERROR")) || bytes.Contains(out, []byte("VULKAN WARNING")) {
		return out, fmt.Errorf("%s: validation messages; see %s", label, path)
	}
	return out, nil
}

func capture(label string, frames int, args ...string) error {
	if !*reuse {
		if err := removeOld(filepath.Join(dir, label+".png")); err != nil {
			return err
		}
	}
	_, err := render(label, nil, append(args, "-frames", strconv.Itoa(frames), "-screenshot", filepath.Join(dir, label+".png"))...)
	if err != nil {
		return err
	}
	_, err = load(label)
	return err
}

func removeOld(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

var countPattern = regexp.MustCompile(`LOD counts=\[([^]]+)\] culled=(\d+) total=(\d+)`)

func count(log []byte) ([]int, int, int, error) {
	all := countPattern.FindAllSubmatch(log, -1)
	if len(all) == 0 {
		return nil, 0, 0, fmt.Errorf("missing LOD counts")
	}
	m := all[len(all)-1]
	var buckets []int
	for _, s := range strings.Fields(string(m[1])) {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, 0, 0, err
		}
		buckets = append(buckets, n)
	}
	culled, _ := strconv.Atoi(string(m[2]))
	total, _ := strconv.Atoi(string(m[3]))
	return buckets, culled, total, nil
}

func cull() error {
	var submitted [2][2]int
	for _, mode := range []int{3, 1} {
		out, err := render(fmt.Sprintf("cull-%d", mode), nil, "-pose", "edge", "-levels", strconv.Itoa(mode), "-frames", "4", "-counts")
		if err != nil {
			return err
		}
		b, c, n, err := count(out)
		if err != nil {
			return err
		}
		fmt.Printf("Cull levels=%d: counts=%v culled=%d total=%d\n", mode, b, c, n)
		if n != 3600 {
			return fmt.Errorf("cull: expected 3600 placements")
		}
		if mode == 3 && (len(b) != 4 || b[0] != 1 || b[1] != 0 || b[2] != 0 || b[3] != 0 || c != 3599) {
			return fmt.Errorf("cull: expected exactly [1 0 0 0],3599")
		}
		if mode == 1 && (len(b) != 1 || b[0] != 3600 || c != 0) {
			return fmt.Errorf("cull: single-level control must submit all 3600")
		}
		m := regexp.MustCompile(`LOD stats draws=(\d+) instances=(\d+) triangles=(\d+)`).FindSubmatch(out)
		if m == nil {
			return fmt.Errorf("cull: missing submitted draw statistics")
		}
		i := 0
		if mode == 1 {
			i = 1
		}
		submitted[i][0], _ = strconv.Atoi(string(m[1]))
		submitted[i][1], _ = strconv.Atoi(string(m[2]))
		fmt.Printf("Cull levels=%d: submitted draws=%d instances=%d triangles=%s (all passes)\n", mode, submitted[i][0], submitted[i][1], m[3])
	}
	if (!gpu && submitted[0][0] >= submitted[1][0]) || submitted[0][1] >= submitted[1][1] {
		return fmt.Errorf("cull: LOD did not reduce submitted draws and instances at the edge pose")
	}
	return nil
}

func load(label string) (image.Image, error) {
	f, err := os.Open(filepath.Join(dir, label+".png"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	if im.Bounds() != image.Rect(0, 0, 1280, 720) {
		return nil, fmt.Errorf("%s: unexpected capture bounds %v", label, im.Bounds())
	}
	return im, nil
}

type metric struct {
	MAE, RMS float64
	Visible  int
	Max      int
}

func difference(a, b image.Image, region image.Rectangle) metric {
	var m metric
	var sum, square float64
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			largest := 0
			for _, d := range []int{int(ar>>8) - int(br>>8), int(ag>>8) - int(bg>>8), int(ab>>8) - int(bb>>8)} {
				if d < 0 {
					d = -d
				}
				largest = max(largest, d)
				sum += float64(d)
				square += float64(d * d)
			}
			m.Max = max(m.Max, largest)
			if largest >= 16 {
				m.Visible++
			}
		}
	}
	n := float64(region.Dx() * region.Dy() * 3)
	m.MAE = sum / n
	m.RMS = math.Sqrt(square / n)
	return m
}

func pop() error {
	for _, samples := range []int{4, 1} {
		var result [2]metric
		for j, fade := range []int{6, 0} {
			labels := [2]string{}
			for i, frame := range []int{60, 61} {
				labels[i] = fmt.Sprintf("pop-%d-%d-%d", samples, fade, frame)
				if err := capture(labels[i], frame, "-pose", "pop", "-fade", strconv.Itoa(fade), "-msaa", strconv.Itoa(samples)); err != nil {
					return err
				}
			}
			a, err := load(labels[0])
			if err != nil {
				return err
			}
			b, err := load(labels[1])
			if err != nil {
				return err
			}
			result[j] = difference(a, b, image.Rect(575, 280, 705, 440))
			fmt.Printf("Pop MSAA=%d fade=%d: MAE=%.6f/255 RMS=%.6f/255 pixels>=16=%d max=%d\n", samples, fade, result[j].MAE, result[j].RMS, result[j].Visible, result[j].Max)
		}
		// A fixed tree rectangle, not a whole-screen mean diluted by empty sky.
		// Both an absolute visibility floor and a ratio are required.
		if result[0].MAE >= 1 || result[1].MAE <= 1 || result[1].Visible < 100 || result[0].MAE*3 >= result[1].MAE {
			return fmt.Errorf("pop: fade must stay below 1/255 MAE, hard switch above it and 3x larger, with >=100 visible pixels")
		}
	}
	return nil
}

func impostor() error {
	for _, enabled := range []string{"true", "false"} {
		if err := capture("far-"+enabled, 60, "-pose", "far", "-impostor="+enabled); err != nil {
			return err
		}
	}
	a, err := load("far-true")
	if err != nil {
		return err
	}
	b, err := load("far-false")
	if err != nil {
		return err
	}
	far := difference(a, b, image.Rect(0, 260, 1280, 540))
	near := difference(a, b, image.Rect(0, 600, 1280, 720))
	sky := difference(a, b, image.Rect(0, 0, 1280, 200))
	fmt.Printf("Impostor far: MAE=%.6f/255 RMS=%.6f/255 pixels>=16=%d; near MAE=%.6f sky MAE=%.6f\n", far.MAE, far.RMS, far.Visible, near.MAE, sky.MAE)
	if far.MAE < 0.5 || far.Visible < 100 || near.MAE > 0.1 || sky.MAE > 0.1 {
		return fmt.Errorf("impostor: expected visible change confined to the far tree band")
	}
	return nil
}

func determinism() error {
	for _, label := range []string{"repeat-a", "repeat-b"} {
		if err := capture(label, 61); err != nil {
			return err
		}
	}
	a, err := os.ReadFile(filepath.Join(dir, "repeat-a.png"))
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, "repeat-b.png"))
	if err != nil {
		return err
	}
	if _, err = load("repeat-a"); err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return fmt.Errorf("determinism: same frame differs")
	}
	fmt.Printf("Determinism: %d PNG bytes identical\n", len(a))
	if err := capture("indexed", 61, "-indexed"); err != nil {
		return err
	}
	return nil
}

func lifetime() error {
	out, err := render("lifetime", []string{"GLYPHENGINE_PROVOKE_RECREATE_FRAMES=15"}, "-replace", "-frames", "100", "-counts")
	if err != nil {
		return err
	}
	line := regexp.MustCompile(`LOD replacements=4 resources=.*`).Find(out)
	if line == nil {
		return fmt.Errorf("lifetime: missing replacement assertions")
	}
	if !bytes.Contains(out, []byte("LOD scene-swaps=2")) {
		return fmt.Errorf("lifetime: missing scene swaps")
	}
	fmt.Printf("Lifetime: resize, 2 scene swaps, 4 replacements and shutdown validation silent\n%s\n", line)
	return nil
}

// Benchmarks are sequential A/B/A/B, after the render gates have exited. The
// application still uses the fixed clock, but validation is disabled for timing.
func bench() error {
	if *reuseBench {
		fmt.Println("Bench: checking retained measurements; no benchmark runs")
	}
	var sums, cpu [2]float64
	fmt.Println("Bench (ms/frame): run mode GPU CPU-cull CPU-upload GPU-select")
	for run := 1; run <= 2; run++ {
		for mode, scene := range []string{"lod", "lod-gpu"} {
			label := fmt.Sprintf("bench-%s-%d", scene, run)
			path := filepath.Join(dir, label+".json")
			if !*reuse && !*reuseBench {
				if err := removeOld(path); err != nil {
					return err
				}
				cmd := exec.Command("task", "bench", "--", "-scene", scene, "-json", path)
				cmd.Env = append(os.Environ(), "GLYPHENGINE_VALIDATION=0")
				out, err := cmd.CombinedOutput()
				os.WriteFile(filepath.Join(dir, label+".log"), out, 0644)
				if err != nil {
					return fmt.Errorf("%s: %w\n%s", label, err, out)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var results []struct {
				Values map[string]float64 `json:"values"`
			}
			if err = json.Unmarshal(data, &results); err != nil {
				return err
			}
			if len(results) != 1 || results[0].Values["gpu_total"] <= 0 {
				return fmt.Errorf("bench: missing positive GPU measurement")
			}
			v := results[0].Values
			sums[mode] += v["gpu_total"]
			cpu[mode] += v["cpu_lodcull"] + v["cpu_lodupload"]
			if mode == 1 && v["gpu_lodselect"] <= 0 {
				return fmt.Errorf("bench: missing GPU selection measurement")
			}
			if mode == 1 && v["cpu_lodcull"]+v["cpu_lodupload"] <= 0 {
				return fmt.Errorf("bench: missing CPU LOD measurement")
			}
			fmt.Printf("Bench %d %-8s %.3f %.3f %.3f %.3f\n", run, scene, v["gpu_total"], v["cpu_lodcull"], v["cpu_lodupload"], v["gpu_lodselect"])
		}
	}
	if cpu[1] >= cpu[0]*0.5 {
		return fmt.Errorf("bench: GPU selection must at least halve CPU selection/upload cost (%.3f >= %.3f)", cpu[1]/2, cpu[0]/2)
	}
	fmt.Printf("Bench mean: GPU %.3f -> %.3f ms; CPU selection/upload %.3f -> %.3f ms\n", sums[0]/2, sums[1]/2, cpu[0]/2, cpu[1]/2)
	return nil
}
