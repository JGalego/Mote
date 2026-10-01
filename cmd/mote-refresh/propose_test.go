package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jgalego/mote/registry"
)

func TestAppendCandidatesKeepsTheFileAndStaysValid(t *testing.T) {
	raw := []byte("{\n  \"runtime\": {},\n  \"candidates\": [\n    {\"id\": \"a\"}\n  ]\n}\n")
	out, err := appendCandidates(raw, []registry.Candidate{{
		ID: "b", Name: "B", Upstream: "o/B", Repo: "o/B-GGUF", Quant: "Q4_K_M",
		Files: map[string]registry.FileSpec{"model": {File: "b.gguf"}}, Caps: []string{"text"}, Context: 8192,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), string(raw[:len(raw)-len("\n  ]\n}\n")])) {
		t.Errorf("existing entries were rewritten:\n%s", out)
	}
	var c registry.Candidates
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(c.Candidates) != 2 || c.Candidates[1].Files["model"].File != "b.gguf" {
		t.Errorf("candidates %+v", c.Candidates)
	}
	if _, err := appendCandidates([]byte(`{"candidates": [], "x": 1}`), nil); err == nil {
		t.Error("a file whose array is not last was accepted")
	}
}
