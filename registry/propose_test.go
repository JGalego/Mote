package registry

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestProposeKeepsOnlyModelsThatChangeADefault(t *testing.T) {
	prev, pol, cands, src := refreshFixture()
	now := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)
	cur, _, err := Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	file := func(size int64) RemoteFile { return RemoteFile{SHA256: sha('f'), Size: size} }
	src.files["ggml-org/New-1B-GGUF"] = map[string]RemoteFile{
		"New-1B-Q4_K_M.gguf": file(500 << 20),
		"New-1B-Q8_0.gguf":   file(900 << 20),
		"mmproj-F16.gguf":    file(10 << 20),
	}
	src.up["org/New-1B"] = UpstreamInfo{License: "mit", ParamsB: 1.0, Evals: []Eval{{Dataset: "d/mmlu", Task: "acc", Value: 40, Date: "2026-01-01", URL: "https://x/n"}}}
	src.files["org/Bigger-3B-GGUF"] = map[string]RemoteFile{"Bigger-3B-Q4_K_M.gguf": file(1800 << 20)}
	src.up["org/Bigger-3B"] = UpstreamInfo{License: "mit", ParamsB: 3.0, Evals: []Eval{{Dataset: "d/mmlu", Task: "acc", Value: 61, Date: "2026-01-01", URL: "https://x/b"}}}
	src.files["org/Worse-2B-GGUF"] = map[string]RemoteFile{"Worse-2B-Q4_K_M.gguf": file(1200 << 20)}
	src.up["org/Worse-2B"] = UpstreamInfo{License: "mit", ParamsB: 2.0, Evals: []Eval{{Dataset: "d/mmlu", Task: "acc", Value: 30, Date: "2026-01-01", URL: "https://x/w"}}}
	src.files["org/Split-1B-GGUF"] = map[string]RemoteFile{"Split-1B-Q8_0.gguf": file(900 << 20)}

	found := []Discovered{
		{Upstream: "org/Bigger-3B", ParamsB: 3, Dataset: "d/mmlu", Value: 61, GGUF: "org/Bigger-3B-GGUF"},
		{Upstream: "org/New-1B", ParamsB: 1, Dataset: "d/mmlu", Value: 40, GGUF: "ggml-org/New-1B-GGUF"},
		{Upstream: "org/Worse-2B", ParamsB: 2, Dataset: "d/mmlu", Value: 30, GGUF: "org/Worse-2B-GGUF"},
		{Upstream: "org/Split-1B", ParamsB: 1, Dataset: "d/mmlu", Value: 50, GGUF: "org/Split-1B-GGUF"},
		{Upstream: "org/NoGGUF-1B", ParamsB: 1, Dataset: "d/mmlu", Value: 55},
		{Upstream: "org/Speech", ParamsB: 1, Dataset: "d/asr", Value: 3, GGUF: "org/Speech-GGUF"},
	}
	props, warns := Propose(context.Background(), cur, pol, found, src)

	var ids []string
	for _, p := range props {
		ids = append(ids, p.Candidate.ID)
	}
	if strings.Join(ids, ",") != "new-1b,bigger-3b" {
		t.Fatalf("proposed %v, want new-1b then bigger-3b", ids)
	}
	p := props[0]
	if p.Candidate.Files["model"].File != "New-1B-Q4_K_M.gguf" || p.Candidate.Repo != "ggml-org/New-1B-GGUF" {
		t.Errorf("draft picked %+v", p.Candidate)
	}
	var small *Change
	for i, ch := range p.Changes {
		if ch.Profile == "small" && ch.Capability == "text" {
			small = &p.Changes[i]
		}
	}
	if small == nil || small.From != "up" {
		t.Errorf("new-1b should take small text from up: %+v", p.Changes)
	}
	var quality *Change
	for i, ch := range props[1].Changes {
		if ch.Profile == "quality" && ch.Capability == "text" {
			quality = &props[1].Changes[i]
		}
	}
	if quality == nil || quality.From != "new-1b" {
		t.Errorf("bigger-3b should take quality text from new-1b, so it was judged after it: %+v", props[1].Changes)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "Split-1B") {
		t.Errorf("warnings %v", warns)
	}
	if _, ok := cur.Model("new-1b"); ok {
		t.Error("Propose changed the registry it was given")
	}
}
