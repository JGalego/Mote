package cli

import (
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/fakellama"
	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

const imageModel = "flux2-klein-4b"

// sdAvailable reports whether the pinned sd.cpp has a build for the
// machine running the tests; where it has none, drawing is unavailable.
func sdAvailable() bool {
	rt, ok := registry.Default().RuntimeFor("sd.cpp")
	if !ok {
		return false
	}
	_, ok = rt.Asset(runtime.GOOS, runtime.GOARCH)
	return ok
}

// installSD puts the fake sd-cli where the pinned runtime would be
// unpacked, and returns a file that records its arguments.
func (e *env) installSD() string {
	e.t.Helper()
	rt, _ := registry.Default().RuntimeFor("sd.cpp")
	fakellama.InstallSD(e.t, mrt.Store{Dir: e.home}.RuntimeDir(rt, ""))
	log := filepath.Join(e.t.TempDir(), "sd-args")
	e.t.Setenv("MOTE_FAKE_SD_LOG", log)
	return log
}

func readPNG(t *testing.T, p string) (int, int) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return img.Bounds().Dx(), img.Bounds().Dy()
}

func TestDrawIsOptIn(t *testing.T) {
	if !sdAvailable() {
		t.Skip("no sd.cpp build for this platform")
	}
	e := newEnv(t)
	e.setup()
	// Nothing is downloaded until asked for: the error says how.
	code, _, errs := e.mote("", "run", "draw", "a fox")
	if code != ExitMissing || !strings.Contains(errs, "mote models pull "+imageModel) {
		t.Errorf("not downloaded: %d %s", code, errs)
	}
	// doctor treats that as normal, not as a problem.
	_, out, _ := e.mote("", "doctor")
	if !strings.Contains(out, imageModel+", fetched on first use") {
		t.Errorf("doctor:\n%s", out)
	}
	// The model's files alone are not enough: its runtime is needed too.
	e.install(imageModel)
	if code, _, errs := e.mote("", "run", "draw", "a fox"); code != ExitMissing || !strings.Contains(errs, "stable-diffusion.cpp") {
		t.Errorf("runtime missing: %d %s", code, errs)
	}
}

func TestDrawWritesAnImage(t *testing.T) {
	if !sdAvailable() {
		t.Skip("no sd.cpp build for this platform")
	}
	e := newEnv(t)
	e.setup()
	e.install(imageModel)
	log := e.installSD()
	out := filepath.Join(t.TempDir(), "fox.png")

	code, stdout, errs := e.mote("", "run", "draw", "a fox in the snow", "256", "7", "-o", out)
	if code != 0 {
		t.Fatalf("draw: %d %s", code, errs)
	}
	if w, h := readPNG(t, out); w != 256 || h != 256 {
		t.Errorf("size %dx%d", w, h)
	}
	if !strings.Contains(stdout, out) {
		t.Errorf("the path was not printed: %q", stdout)
	}
	if !strings.Contains(errs, "seed 7") {
		t.Errorf("the seed was not reported: %s", errs)
	}
	b, _ := os.ReadFile(log)
	args := "\n" + string(b)
	for _, want := range []string{"\n-p\na fox in the snow\n", "\n-W\n256\n", "\n--seed\n7\n", "\n--mmap\n", "\n--steps\n4\n", "\n--cfg-scale\n1.0\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("sd-cli arguments lack %q:%s", strings.TrimSpace(want), args)
		}
	}
	if strings.Contains(args, "--vae-tiling") {
		t.Error("a small image was decoded in tiles")
	}

	// A wide picture is rounded to the size step, and big enough to tile.
	if code, _, errs := e.mote("", "run", "draw", "a lighthouse", "770x500", "-o", out); code != 0 {
		t.Fatalf("wide: %d %s", code, errs)
	}
	if w, h := readPNG(t, out); w != 768 || h != 512 {
		t.Errorf("wide size %dx%d", w, h)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "--vae-tiling") {
		t.Error("a large image was not decoded in tiles")
	}
	// Without a seed, one is picked and reported.
	if _, _, errs := e.mote("", "run", "draw", "a lighthouse", "128", "-o", out); !strings.Contains(errs, "seed ") {
		t.Errorf("no seed reported: %s", errs)
	}
}

func TestDrawRejectsBadArguments(t *testing.T) {
	if !sdAvailable() {
		t.Skip("no sd.cpp build for this platform")
	}
	e := newEnv(t)
	e.setup()
	e.install(imageModel)
	e.installSD()
	out := filepath.Join(t.TempDir(), "x.png")
	for _, args := range [][]string{
		{"run", "draw", "a fox", "huge", "-o", out},
		{"run", "draw", "a fox", "64", "-o", out},
		{"run", "draw", "a fox", "2048x512", "-o", out},
		{"run", "draw", "a fox", "256", "-3", "-o", out},
		{"run", "draw", "a fox", "256", "lucky", "-o", out},
		{"run", "draw", "   ", "-o", out},
	} {
		if code, _, errs := e.mote("", args...); code == 0 {
			t.Errorf("%v accepted: %s", args, errs)
		}
	}
}

func TestDrawReportsAFailedRun(t *testing.T) {
	if !sdAvailable() {
		t.Skip("no sd.cpp build for this platform")
	}
	e := newEnv(t)
	e.setup()
	e.install(imageModel)
	e.installSD()
	t.Setenv("MOTE_FAKE_SD_FAIL", "failed to allocate compute buffer")
	code, _, errs := e.mote("", "run", "draw", "a fox", "-o", filepath.Join(t.TempDir(), "x.png"))
	if code == 0 || !strings.Contains(errs, "failed to allocate compute buffer") {
		t.Errorf("failure: %d %s", code, errs)
	}
}

func TestDrawFeedsAPipeline(t *testing.T) {
	if !sdAvailable() {
		t.Skip("no sd.cpp build for this platform")
	}
	e := newEnv(t)
	e.setup()
	e.install(imageModel)
	e.install("qwen3.5-0.8b")
	e.installSD()
	t.Chdir(t.TempDir()) // draw writes image.png here
	// The picture is a file, so it can go straight to a task that looks
	// at images.
	code, out, errs := e.mote("", "pipe", "draw 'a fox' 128 | describe")
	if code != 0 || !strings.Contains(out, "[image_url]") {
		t.Fatalf("pipe: %d %q %s", code, out, errs)
	}
	if w, _ := readPNG(t, "image.png"); w != 128 {
		t.Errorf("width %d", w)
	}
}

func TestBenchChecksTheImageModel(t *testing.T) {
	if !sdAvailable() {
		t.Skip("no sd.cpp build for this platform")
	}
	e := newEnv(t)
	e.setup()
	e.install(imageModel)
	e.installSD()
	// Drawing takes minutes, so only the full benchmark does it.
	if _, out, _ := e.mote("", "bench", "--model", imageModel); strings.Contains(out, "image-square") {
		t.Errorf("the quick benchmark drew: %s", out)
	}
	code, out, errs := e.mote("", "bench", "--full", "--model", imageModel)
	if code != 0 || !strings.Contains(out, imageModel) || !strings.Contains(out, "1/1") {
		t.Errorf("bench: %d %s %s", code, out, errs)
	}
}
