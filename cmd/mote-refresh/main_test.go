package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const rev = "0123456789abcdef0123456789abcdef01234567"

// stubHF serves the handful of Hugging Face endpoints a refresh reads.
func stubHF(t *testing.T) string {
	t.Helper()
	routes := map[string]any{
		"/api/models/org/gguf?expand[]=sha": map[string]any{"sha": rev},
		"/api/models/org/gguf/tree/" + rev + "?recursive=true": []map[string]any{
			{"path": "m-Q4_K_M.gguf", "size": 1 << 20, "lfs": map[string]any{"oid": strings.Repeat("a", 64)}},
		},
		"/api/models/org/upstream?expand[]=cardData&expand[]=safetensors&expand[]=evalResults": map[string]any{
			"cardData":    map[string]any{"license": "mit"},
			"safetensors": map[string]any{"total": 800_000_000},
			"evalResults": []map[string]any{{
				"filename": "results.json", "verified": true,
				"data": map[string]any{
					"dataset": map[string]any{"id": "suite/bench", "task_id": "score"},
					"value":   80.0, "date": "2026-01-01",
					"source": map[string]any{"url": "https://example.org/run", "name": "Upstream"},
				},
			}},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		body, ok := routes[key]
		if !ok {
			http.Error(w, "not found: "+key, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// seedRegistry writes the three input files a refresh reads.
func seedRegistry(t *testing.T, models string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("models.json", models)
	write("policy.json", `{
      "priorities": ["size"], "usable_ram_fraction": 0.6, "ram_overhead_mb": 1024,
      "profiles": {"small": {"description": "d", "max_ram_mb": 2048, "min_tokens_per_sec": 8}},
      "gates": {"text": {"dataset": "suite/bench", "task": "score", "higher_is_better": true,
                         "threshold": {"small": 25}}}
    }`)
	write("candidates.json", `{
      "runtime": {"repo": "ggml-org/llama.cpp"},
      "discover": {"max_params_b": 5, "gguf_orgs": ["org"]},
      "candidates": [
        {"id": "m", "name": "M", "upstream": "org/upstream", "repo": "org/gguf",
         "quant": "Q4_K_M", "files": {"model": "m-Q4_K_M.gguf"}, "caps": ["text"], "context": 4096}
      ]
    }`)
	return dir
}

const emptyModels = `{"schema":1,"version":"2026.01.01","generated":"2026-01-01T00:00:00Z",` +
	`"runtime":{"name":"llama.cpp","version":"b1","published":"2026-01-01","license":"MIT",` +
	`"source":"https://example.org/b1","assets":[]},"policy":{},"models":[],"defaults":{}}`

func TestRunWritesTheRegistry(t *testing.T) {
	t.Setenv("MOTE_HF_BASE", stubHF(t))
	dir := seedRegistry(t, emptyModels)

	if err := run(dir, false, false, false); err != nil {
		t.Fatalf("run: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Models []struct {
			ID       string `json:"id"`
			License  string `json:"license"`
			Revision string `json:"revision"`
			Files    []struct {
				SHA256 string `json:"sha256"`
			} `json:"files"`
			Benchmarks []struct {
				Kind  string  `json:"kind"`
				Value float64 `json:"value"`
			} `json:"benchmarks"`
		} `json:"models"`
		Defaults map[string]map[string]struct {
			Model string `json:"model"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 1 {
		t.Fatalf("models: %s", b)
	}
	m := out.Models[0]
	if m.ID != "m" || m.License != "mit" || m.Revision != rev {
		t.Errorf("model: %+v", m)
	}
	if len(m.Files) != 1 || m.Files[0].SHA256 == "" {
		t.Errorf("files not pinned by hash: %+v", m.Files)
	}
	if len(m.Benchmarks) == 0 || m.Benchmarks[0].Value != 80 {
		t.Errorf("upstream benchmark not recorded: %+v", m.Benchmarks)
	}
	if out.Defaults["small"]["text"].Model != "m" {
		t.Errorf("defaults not computed: %+v", out.Defaults)
	}

	// Running again changes nothing, so -check passes.
	if err := run(dir, false, false, true); err != nil {
		t.Errorf("check after write: %v", err)
	}
	if err := run(dir, false, false, false); err != nil {
		t.Errorf("second run: %v", err)
	}
}

func TestRunCheckReportsDrift(t *testing.T) {
	t.Setenv("MOTE_HF_BASE", stubHF(t))
	dir := seedRegistry(t, emptyModels)
	// The seeded registry has no models, so a refresh would change it.
	err := run(dir, false, false, true)
	if err != errChanged {
		t.Fatalf("check on a stale registry: %v", err)
	}
	// -check must not write.
	b, _ := os.ReadFile(filepath.Join(dir, "models.json"))
	if strings.Contains(string(b), `"id": "m"`) {
		t.Error("check wrote the registry")
	}
}

func TestRunReportsBadInput(t *testing.T) {
	t.Setenv("MOTE_HF_BASE", stubHF(t))
	if err := run(t.TempDir(), false, false, false); err == nil {
		t.Error("missing models.json accepted")
	}
	dir := seedRegistry(t, `{not json`)
	if err := run(dir, false, false, false); err == nil || !strings.Contains(err.Error(), "models.json") {
		t.Errorf("bad models.json: %v", err)
	}
	dir = seedRegistry(t, emptyModels)
	os.WriteFile(filepath.Join(dir, "policy.json"), []byte("{oops"), 0o644)
	if err := run(dir, false, false, false); err == nil || !strings.Contains(err.Error(), "policy.json") {
		t.Errorf("bad policy.json: %v", err)
	}
	dir = seedRegistry(t, emptyModels)
	os.WriteFile(filepath.Join(dir, "candidates.json"), []byte("{oops"), 0o644)
	if err := run(dir, false, false, false); err == nil || !strings.Contains(err.Error(), "candidates.json") {
		t.Errorf("bad candidates.json: %v", err)
	}
}

func TestRunRefusesToRewriteTheRegistryWithNothing(t *testing.T) {
	// No stub at all: every fetch fails. Refresh must refuse rather than
	// write a registry it could not build, leaving the file as it was.
	t.Setenv("MOTE_HF_BASE", "http://127.0.0.1:1")
	prev := `{"schema":1,"version":"2026.01.01","generated":"2026-01-01T00:00:00Z",
	  "runtime":{"name":"llama.cpp","version":"b1","published":"2026-01-01","license":"MIT",
	  "source":"https://example.org/b1","assets":[]},"policy":{},
	  "models":[{"id":"m","name":"M","backend":"llama.cpp","upstream":"org/upstream","repo":"org/gguf",
	    "revision":"` + rev + `","license":"mit","quant":"Q4_K_M","params_b":0.8,"caps":["text"],
	    "context":4096,"files":[{"role":"model","name":"m-Q4_K_M.gguf",
	    "url":"https://huggingface.co/org/gguf/resolve/` + rev + `/m-Q4_K_M.gguf",
	    "sha256":"` + strings.Repeat("a", 64) + `","size":1048576}],
	    "ram_mb_estimate":1024,"min_ram_mb":2048,"benchmarks":[]}],
	  "defaults":{}}`
	dir := seedRegistry(t, prev)
	before, _ := os.ReadFile(filepath.Join(dir, "models.json"))
	err := run(dir, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "upstream unavailable") {
		t.Fatalf("run with no upstream: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "models.json"))
	if string(before) != string(after) {
		t.Error("the registry was rewritten despite the failure")
	}
}

func TestWriteAtomicAndReadJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	if err := writeAtomic(p, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	var v struct{ A int }
	if err := readJSON(p, &v); err != nil || v.A != 1 {
		t.Errorf("read back: %v %+v", err, v)
	}
	if err := readJSON(filepath.Join(dir, "missing.json"), &v); err == nil {
		t.Error("missing file accepted")
	}
	os.WriteFile(p, []byte("{bad"), 0o644)
	if err := readJSON(p, &v); err == nil || !strings.Contains(err.Error(), "x.json") {
		t.Errorf("bad JSON error should name the file: %v", err)
	}
	if err := writeAtomic(filepath.Join(dir, "nope", "x.json"), []byte("{}")); err == nil {
		t.Error("unwritable path accepted")
	}
}

func TestRunDiscoversCandidates(t *testing.T) {
	base := stubHF(t)
	t.Setenv("MOTE_HF_BASE", base)
	dir := seedRegistry(t, emptyModels)
	// Discovery reads a leaderboard; the stub has none, so the run must
	// warn and carry on rather than fail.
	if err := run(dir, false, true, false); err != nil {
		t.Fatalf("discover: %v", err)
	}
}

func TestRunWithTheRuntimePin(t *testing.T) {
	t.Setenv("MOTE_HF_BASE", stubHF(t))
	// A GitHub stub offering a newer llama.cpp release.
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name":     "b9999",
			"published_at": "2026-09-01T00:00:00Z",
			"assets": []map[string]any{
				{"name": "llama-b9999-bin-ubuntu-x64.tar.gz", "browser_download_url": "https://example.org/l.tar.gz",
					"size": 1024, "digest": "sha256:" + strings.Repeat("c", 64)},
			},
		}})
	}))
	defer gh.Close()
	t.Setenv("MOTE_GITHUB_BASE", gh.URL)

	dir := seedRegistry(t, emptyModels)
	if err := run(dir, true, false, false); err != nil {
		t.Fatalf("runtime refresh: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "models.json"))
	if !strings.Contains(string(b), "b9999") {
		t.Errorf("runtime pin not updated: %s", b)
	}
}

func TestRunCannotWriteTheRegistry(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions do not stop writes here")
	}
	t.Setenv("MOTE_HF_BASE", stubHF(t))
	dir := seedRegistry(t, emptyModels)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := run(dir, false, false, false); err == nil {
		t.Error("writing into a read-only directory reported success")
	}
}
