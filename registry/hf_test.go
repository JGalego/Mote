package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hfServer serves the shapes mote reads from the Hugging Face and GitHub
// APIs, so the HTTP client can be exercised without the network.
func hfServer(t *testing.T, routes map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		body, ok := routes[key]
		if !ok {
			if body, ok = routes[r.URL.Path]; !ok {
				http.Error(w, "not found: "+key, http.StatusNotFound)
				return
			}
		}
		if s, isString := body.(string); isString {
			w.Write([]byte(s))
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const fakeSHA = "0123456789abcdef0123456789abcdef01234567"

func TestHFRepoFiles(t *testing.T) {
	srv := hfServer(t, map[string]any{
		"/api/models/org/repo?expand[]=sha": map[string]any{"sha": fakeSHA},
		"/api/models/org/repo/tree/" + fakeSHA: []map[string]any{
			{"path": "model.gguf", "size": 123, "lfs": map[string]any{"oid": "abc"}},
			{"path": "README.md", "size": 5}, // no lfs: not a weight file
		},
	})
	h := &HF{HFBase: srv.URL}
	rev, files, err := h.RepoFiles(context.Background(), "org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if rev != fakeSHA {
		t.Errorf("revision %q", rev)
	}
	if len(files) != 1 {
		t.Fatalf("files: %+v", files)
	}
	if f := files["model.gguf"]; f.SHA256 != "abc" || f.Size != 123 {
		t.Errorf("model.gguf: %+v", f)
	}
}

func TestHFRepoFilesErrors(t *testing.T) {
	ctx := context.Background()
	// A repo with no usable revision is an error, not an empty answer.
	srv := hfServer(t, map[string]any{"/api/models/org/repo?expand[]=sha": map[string]any{"sha": "short"}})
	if _, _, err := (&HF{HFBase: srv.URL}).RepoFiles(ctx, "org/repo"); err == nil {
		t.Error("short revision accepted")
	}
	// A missing repo surfaces the status.
	empty := hfServer(t, map[string]any{})
	_, _, err := (&HF{HFBase: empty.URL}).RepoFiles(ctx, "org/gone")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing repo: %v", err)
	}
	// A reachable repo whose tree is missing also fails.
	partial := hfServer(t, map[string]any{"/api/models/org/repo?expand[]=sha": map[string]any{"sha": fakeSHA}})
	if _, _, err := (&HF{HFBase: partial.URL}).RepoFiles(ctx, "org/repo"); err == nil {
		t.Error("missing tree accepted")
	}
}

func TestHFUpstream(t *testing.T) {
	const q = "?expand[]=cardData&expand[]=safetensors&expand[]=evalResults"
	srv := hfServer(t, map[string]any{
		"/api/models/org/model" + q: map[string]any{
			"cardData":    map[string]any{"license": "mit"},
			"safetensors": map[string]any{"total": 1_500_000_000},
			"evalResults": []map[string]any{
				{
					"filename": "results.json", "verified": true,
					"data": map[string]any{
						"dataset": map[string]any{"id": "suite/bench", "task_id": "score"},
						"value":   61.5, "date": "2026-01-02T03:04:05Z",
						"source": map[string]any{"url": "https://example.org/run", "name": "Upstream"},
					},
				},
				{ // no value: skipped rather than recorded as zero
					"data": map[string]any{"dataset": map[string]any{"id": "suite/bench"}, "date": "2026-01-02"},
				},
				{ // no URL and only a user: falls back to the model's own file
					"filename": "other results.json",
					"data": map[string]any{
						"dataset": map[string]any{"id": "suite/two", "task_id": "score"},
						"value":   1.0, "date": "2026-02-03",
						"source": map[string]any{"user": "someone"},
					},
				},
			},
		},
	})
	info, err := (&HF{HFBase: srv.URL}).Upstream(context.Background(), "org/model")
	if err != nil {
		t.Fatal(err)
	}
	if info.License != "mit" || info.ParamsB != 1.5 {
		t.Errorf("license %q params %v", info.License, info.ParamsB)
	}
	if len(info.Evals) != 2 {
		t.Fatalf("evals: %+v", info.Evals)
	}
	e := info.Evals[0]
	if e.Dataset != "suite/bench" || e.Task != "score" || e.Value != 61.5 || !e.Verified {
		t.Errorf("first eval: %+v", e)
	}
	if e.Date != "2026-01-02" {
		t.Errorf("date not trimmed to a day: %q", e.Date)
	}
	if e.Source != "Upstream" || e.URL != "https://example.org/run" {
		t.Errorf("source: %q %q", e.Source, e.URL)
	}
	second := info.Evals[1]
	if second.Source != "someone" {
		t.Errorf("source fallback to user: %q", second.Source)
	}
	if !strings.HasPrefix(second.URL, "https://huggingface.co/org/model/blob/main/") || !strings.Contains(second.URL, "%20") {
		t.Errorf("url fallback not escaped: %q", second.URL)
	}
}

func TestHFUpstreamNeedsALicense(t *testing.T) {
	const q = "?expand[]=cardData&expand[]=safetensors&expand[]=evalResults"
	srv := hfServer(t, map[string]any{
		"/api/models/org/model" + q: map[string]any{"cardData": map[string]any{}},
	})
	_, err := (&HF{HFBase: srv.URL}).Upstream(context.Background(), "org/model")
	if err == nil || !strings.Contains(err.Error(), "license") {
		t.Errorf("undeclared license: %v", err)
	}
}

func TestHFLatestRelease(t *testing.T) {
	srv := hfServer(t, map[string]any{
		"/repos/ggml-org/llama.cpp/releases/latest": map[string]any{
			"tag_name":     "b9999",
			"published_at": "2026-09-01T00:00:00Z",
			"assets": []map[string]any{
				{"name": "llama-b9999-bin-ubuntu-x64.zip", "browser_download_url": "https://example.org/a.zip",
					"size": 42, "digest": "sha256:deadbeef"},
			},
		},
	})
	rel, err := (&HF{GitHubBase: srv.URL}).LatestRelease(context.Background(), "ggml-org/llama.cpp")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "b9999" || rel.Published.IsZero() {
		t.Errorf("release: %+v", rel)
	}
	a := rel.Assets["llama-b9999-bin-ubuntu-x64.zip"]
	if a.SHA256 != "deadbeef" || a.Size != 42 {
		t.Errorf("asset digest not unwrapped: %+v", a)
	}
}

func TestHFLeaderboardAndExists(t *testing.T) {
	srv := hfServer(t, map[string]any{
		"/api/datasets/suite/bench/leaderboard": []map[string]any{{"model": "org/m", "value": 1.5}},
		"/api/models/org/there":                 map[string]any{"id": "org/there"},
	})
	h := &HF{HFBase: srv.URL}
	rows, err := h.Leaderboard(context.Background(), "suite/bench")
	if err != nil || len(rows) != 1 {
		t.Fatalf("leaderboard: %v %+v", err, rows)
	}
	if !h.Exists(context.Background(), "org/there") {
		t.Error("existing repo reported missing")
	}
	if h.Exists(context.Background(), "org/gone") {
		t.Error("missing repo reported present")
	}
}

func TestHFDefaultsAndBadJSON(t *testing.T) {
	h := &HF{}
	if h.hf() != "https://huggingface.co" || h.gh() != "https://api.github.com" {
		t.Errorf("defaults: %s %s", h.hf(), h.gh())
	}
	// Malformed JSON is an error, not a half-filled struct.
	srv := hfServer(t, map[string]any{"/api/models/org/repo?expand[]=sha": "{not json"})
	if _, _, err := (&HF{HFBase: srv.URL}).RepoFiles(context.Background(), "org/repo"); err == nil {
		t.Error("bad JSON accepted")
	}
}
