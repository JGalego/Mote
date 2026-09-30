package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// driver starts the mote kernel through jupyter_client, the way Jupyter and
// VS Code do, runs the cells it is given and prints what each one showed.
const kernelDriver = `
import json, sys
from jupyter_client.manager import start_new_kernel

km, kc = start_new_kernel(kernel_name="mote", startup_timeout=60)
results = []
for code in json.loads(sys.argv[1]):
    msg_id = kc.execute(code)
    out = {"stream": "", "images": 0, "error": "", "status": ""}
    while True:
        msg = kc.get_iopub_msg(timeout=60)
        if msg["parent_header"].get("msg_id") != msg_id:
            continue
        kind, content = msg["msg_type"], msg["content"]
        if kind == "stream":
            out["stream"] += content["text"]
        elif kind == "display_data" and "image/png" in content["data"]:
            out["images"] += 1
        elif kind == "error":
            out["error"] = content["evalue"]
        elif kind == "status" and content["execution_state"] == "idle":
            break
    # The reply to this request, not one to another the client made.
    while True:
        reply = kc.get_shell_msg(timeout=60)
        if reply["parent_header"].get("msg_id") == msg_id:
            break
    out["status"] = reply["content"]["status"]
    results.append(out)
kc.stop_channels()
km.shutdown_kernel(now=True)
print(json.dumps(results))
`

// TestKernelWithAJupyterClient runs the real kernel: the built mote, the
// installed kernelspec and a live ipykernel. It needs a Python that has
// ipykernel and jupyter_client, so it runs only when MOTE_TEST_JUPYTER names
// one, as in
//
//	MOTE_TEST_JUPYTER=$(which python3) go test -run KernelWithAJupyterClient ./internal/cli
func TestKernelWithAJupyterClient(t *testing.T) {
	python := os.Getenv("MOTE_TEST_JUPYTER")
	if python == "" {
		t.Skip("set MOTE_TEST_JUPYTER to a Python with ipykernel and jupyter_client to run this")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on the PATH to build mote with")
	}
	bin := filepath.Join(t.TempDir(), "mote")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command(goBin, "build", "-o", bin, "../../cmd/mote").CombinedOutput(); err != nil {
		t.Fatalf("building mote: %v\n%s", err, out)
	}

	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	jupyter := t.TempDir()
	t.Setenv("JUPYTER_DATA_DIR", jupyter)
	if out, err := exec.Command(bin, "kernel", "install", "--python", python).CombinedOutput(); err != nil {
		t.Fatalf("kernel install: %v\n%s", err, out)
	}

	cells := []string{
		"city = chat capital",
		`chat "got {{city}}"`,
		"nosuchtask x",
		"chat {{unbound}}",
	}
	if runtime.GOOS != "windows" {
		// A task that writes a picture prints its path, and the kernel shows
		// the picture.
		png := filepath.Join(t.TempDir(), "fox.png")
		os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n"), 0o644)
		cells = append(cells, "chat x | sh: echo "+png)
	}
	arg, _ := json.Marshal(cells)
	out, err := exec.Command(python, "-c", kernelDriver, string(arg)).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("driver: %v\n%s", err, ee.Stderr)
		}
		t.Fatal(err)
	}
	var got []struct {
		Stream string
		Images int
		Error  string
		Status string
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(cells) {
		t.Fatalf("driver printed %q (%v)", out, err)
	}
	if got[0].Status != "ok" || got[0].Stream != "echo: capital\n" {
		t.Errorf("cell 1: %+v", got[0])
	}
	if got[1].Status != "ok" || got[1].Stream != "echo: got echo: capital\n" {
		t.Errorf("cell 2 did not read what cell 1 bound: %+v", got[1])
	}
	if got[2].Status != "error" || !strings.Contains(got[2].Error, "nosuchtask") {
		t.Errorf("cell 3: %+v", got[2])
	}
	if got[3].Status != "error" || !strings.Contains(got[3].Error, "has no value") {
		t.Errorf("cell 4: %+v", got[3])
	}
	if len(got) == 5 && (got[4].Status != "ok" || got[4].Images != 1 || got[4].Stream != "") {
		t.Errorf("cell 5 should show one image: %+v", got[4])
	}
}
