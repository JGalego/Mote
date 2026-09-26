package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func sha(c byte) string { return strings.Repeat(string(c), 64) }

func fixture() *Registry {
	file := func(name string, size int64) File {
		return File{Role: "model", Name: name, URL: "https://example.org/" + name, SHA256: sha('a'), Size: size}
	}
	proj := File{Role: "mmproj", Name: "p.gguf", URL: "https://example.org/p.gguf", SHA256: sha('b'), Size: 100 << 20}
	up := func(ds, task string, v float64) Benchmark {
		return Benchmark{Kind: KindUpstream, Dataset: ds, Task: task, Value: v, URL: "https://example.org/eval", Date: "2026-01-01"}
	}
	r := &Registry{
		Schema: Schema, Version: "test",
		Runtime: Runtime{Name: "llama.cpp", Version: "b1", Assets: []Asset{
			{OS: "linux", Arch: "amd64", URL: "https://example.org/l.tar.gz", SHA256: sha('c'), Size: 1},
		}},
		Policy: Policy{
			UsableRAMFraction: 0.5, RAMOverheadMB: 512,
			Profiles: map[string]Profile{
				"small":   {MaxRAMMB: 2048, MinTokensPerSec: 8},
				"quality": {MaxRAMMB: 8192, MinTokensPerSec: 2},
			},
			Gates: map[string]Gate{
				"text": {Dataset: "d/mmlu", Task: "acc", Higher: true, Min: map[string]float64{"small": 25, "quality": 60}},
				"asr":  {Dataset: "d/asr", Task: "wer", Higher: false, Min: map[string]float64{"small": 8, "quality": 6}},
			},
		},
		Models: []Model{
			{ID: "tiny", Backend: "llama.cpp", License: "apache-2.0", ParamsB: 0.3, Caps: []string{"text"}, Context: 2048,
				Files: []File{file("tiny.gguf", 300<<20)}, RAMEstimate: 800, Benchmarks: []Benchmark{up("d/mmlu", "acc", 12)}},
			{ID: "small", Backend: "llama.cpp", License: "apache-2.0", ParamsB: 0.8, Caps: []string{"text", "vision"}, Context: 4096,
				Files: []File{file("small.gguf", 500<<20), proj}, RAMEstimate: 1200, Benchmarks: []Benchmark{up("d/mmlu", "acc", 30)}},
			{ID: "big", Backend: "llama.cpp", License: "apache-2.0", ParamsB: 4, Caps: []string{"text"}, Context: 4096,
				Files: []File{file("big.gguf", 2500<<20)}, RAMEstimate: 3000, Benchmarks: []Benchmark{up("d/mmlu", "acc", 70)}},
			{ID: "unmeasured", Backend: "llama.cpp", License: "mit", ParamsB: 0.5, Caps: []string{"text"}, Context: 4096,
				Files: []File{file("u.gguf", 400<<20)}, RAMEstimate: 900},
			{ID: "asr-a", Backend: "llama.cpp", License: "apache-2.0", ParamsB: 0.6, Caps: []string{"asr"}, Context: 4096,
				Files: []File{file("a.gguf", 800<<20), proj}, RAMEstimate: 1500, Benchmarks: []Benchmark{up("d/asr", "wer", 6.4)}},
			{ID: "asr-b", Backend: "llama.cpp", License: "apache-2.0", ParamsB: 1.7, Caps: []string{"asr"}, Context: 4096,
				Files: []File{file("b.gguf", 2000<<20), proj}, RAMEstimate: 2900, Benchmarks: []Benchmark{up("d/asr", "wer", 5.7)}},
		},
	}
	return r
}

func TestEmbeddedRegistryIsValid(t *testing.T) {
	r := Default()
	if len(r.Models) == 0 {
		t.Fatal("embedded registry has no models")
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			if _, ok := r.Asset(goos, arch); !ok {
				t.Errorf("no runtime asset for %s/%s", goos, arch)
			}
		}
	}
	for c := range Capabilities {
		if _, err := r.Select(c, "small", Env{RAMMB: 8192}); err != nil {
			t.Errorf("capability %s has no small model on 8 GiB: %v", c, err)
		}
	}
	// The stored defaults must be reproducible from the stored models and policy.
	got := r.ComputeDefaults()
	for p, caps := range r.Defaults {
		for c, ch := range caps {
			if got[p][c] != ch {
				t.Errorf("defaults %s/%s: stored %+v, computed %+v", p, c, ch, got[p][c])
			}
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(r *Registry){
		"schema":        func(r *Registry) { r.Schema = 99 },
		"http url":      func(r *Registry) { r.Models[0].Files[0].URL = "http://example.org/x" },
		"bad sha":       func(r *Registry) { r.Models[0].Files[0].SHA256 = "abc" },
		"path in name":  func(r *Registry) { r.Models[0].Files[0].Name = "../evil" },
		"dup id":        func(r *Registry) { r.Models[1].ID = "tiny" },
		"unknown cap":   func(r *Registry) { r.Models[0].Caps = []string{"telepathy"} },
		"no mmproj":     func(r *Registry) { r.Models[1].Files = r.Models[1].Files[:1] },
		"local bench":   func(r *Registry) { r.Models[0].Benchmarks[0].Kind = KindLocal },
		"unsourced":     func(r *Registry) { r.Models[0].Benchmarks[0].URL = "" },
		"no context":    func(r *Registry) { r.Models[0].Context = 0 },
		"shell arg":     func(r *Registry) { r.Models[0].Args = []string{"; rm -rf /"} },
		"gate profile":  func(r *Registry) { delete(r.Policy.Gates["text"].Min, "quality") },
		"bad default":   func(r *Registry) { r.Defaults = map[string]map[string]Choice{"small": {"text": {Model: "nope"}}} },
		"asset archive": func(r *Registry) { r.Runtime.Assets[0].URL = "https://example.org/x.exe" },
	}
	for name, mutate := range cases {
		r := fixture()
		if err := r.Validate(); err != nil {
			t.Fatalf("fixture invalid: %v", err)
		}
		mutate(r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	if _, err := Parse([]byte(`{"schema":1,"surprise":true}`)); err == nil {
		t.Error("unknown fields must be rejected")
	}
}

func TestSelectSmallestPassing(t *testing.T) {
	r := fixture()
	ch, err := r.Select("text", "small", Env{})
	if err != nil || ch.Model != "small" || !ch.Meets {
		t.Fatalf("got %+v, %v", ch, err)
	}
	if !strings.Contains(ch.Reason, ">= 25") {
		t.Errorf("reason lacks threshold: %s", ch.Reason)
	}
	ch, _ = r.Select("text", "quality", Env{})
	if ch.Model != "big" {
		t.Errorf("quality picked %s", ch.Model)
	}
}

func TestSelectLowerIsBetter(t *testing.T) {
	r := fixture()
	if ch, _ := r.Select("asr", "small", Env{}); ch.Model != "asr-a" {
		t.Errorf("small asr picked %s", ch.Model)
	}
	if ch, _ := r.Select("asr", "quality", Env{}); ch.Model != "asr-b" {
		t.Errorf("quality asr picked %s", ch.Model)
	}
}

func TestSelectRespectsRAM(t *testing.T) {
	r := fixture()
	// 4 GiB * 0.5 = 2 GiB usable: "big" (3000) does not fit, best fitting is "small" with 30 < 60.
	ch, err := r.Select("text", "quality", Env{RAMMB: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Model != "small" || ch.Meets {
		t.Errorf("got %+v", ch)
	}
	if !strings.Contains(ch.Reason, "no fitting model meets") {
		t.Errorf("reason: %s", ch.Reason)
	}
	_, err = r.Select("text", "small", Env{RAMMB: 512})
	if !errors.Is(err, ErrNoModel) && (err == nil || !strings.Contains(err.Error(), "no model")) {
		t.Errorf("expected no-model error, got %v", err)
	}
}

func TestSelectProfileCeilingIsSoft(t *testing.T) {
	r := fixture()
	var only []Model
	for _, m := range r.Models {
		if m.ID == "big" {
			only = append(only, m)
		}
	}
	r.Models = only
	ch, err := r.Select("text", "small", Env{RAMMB: 16384})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Model != "big" || ch.Meets || !strings.Contains(ch.Reason, "exceeds the small profile ceiling") {
		t.Errorf("got %+v", ch)
	}
}

func TestSelectUsesLocalMeasurements(t *testing.T) {
	r := fixture()
	env := Env{Measured: map[string]Measured{"small": {TokensPerSec: 3}}}
	ch, err := r.Select("text", "small", env)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Model == "small" {
		t.Error("model below measured throughput floor was selected")
	}
	env = Env{Measured: map[string]Measured{"small": {Cases: 4, Passed: 1}}}
	if ch, _ := r.Select("text", "small", env); ch.Model == "small" {
		t.Error("model failing local checks was selected")
	}
	env = Env{RAMMB: 4096, Measured: map[string]Measured{"big": {PeakRSSMB: 1800, TokensPerSec: 4}}}
	ch, _ = r.Select("text", "quality", env)
	if ch.Model != "big" || !strings.Contains(ch.Reason, "measured RAM 1800") {
		t.Errorf("measured RAM not used: %+v", ch)
	}
}

func TestSelectErrors(t *testing.T) {
	r := fixture()
	if _, err := r.Select("text", "turbo", Env{}); err == nil {
		t.Error("unknown profile accepted")
	}
	if _, err := r.Select("smell", "small", Env{}); err == nil {
		t.Error("unknown capability accepted")
	}
	if _, err := r.Select("tts", "small", Env{}); err == nil {
		t.Error("capability without models should fail")
	}
}

type fakeSource struct {
	files   map[string]map[string]RemoteFile
	up      map[string]UpstreamInfo
	release Release
	fail    map[string]bool
}

func (f *fakeSource) RepoFiles(_ context.Context, repo string) (string, map[string]RemoteFile, error) {
	if f.fail[repo] {
		return "", nil, errors.New("503")
	}
	return strings.Repeat("1", 40), f.files[repo], nil
}
func (f *fakeSource) Upstream(_ context.Context, id string) (UpstreamInfo, error) {
	if f.fail[id] {
		return UpstreamInfo{}, errors.New("503")
	}
	return f.up[id], nil
}
func (f *fakeSource) LatestRelease(context.Context, string) (Release, error) { return f.release, nil }
func (f *fakeSource) Leaderboard(_ context.Context, ds string) ([]LeaderboardEntry, error) {
	return []LeaderboardEntry{{ModelID: "org/New-1B", Value: 40, Params: 1e9}, {ModelID: "org/Up", Value: 50, Params: 1e9}, {ModelID: "org/Huge", Value: 90, Params: 70e9}}, nil
}
func (f *fakeSource) Exists(_ context.Context, repo string) bool {
	return repo == "ggml-org/New-1B-GGUF"
}

func refreshFixture() (*Registry, Policy, Candidates, *fakeSource) {
	prev := fixture()
	prev.Models = nil
	prev.Defaults = nil
	pol := prev.Policy
	var c Candidates
	c.Runtime.Repo = "ggml-org/llama.cpp"
	c.Runtime.License = "MIT"
	c.Runtime.Assets = map[string]string{"linux/amd64": "llama-{tag}-linux.tar.gz"}
	c.Discover.MaxParamsB = 5
	c.Discover.GGUFOrgs = []string{"ggml-org"}
	c.Candidates = []Candidate{{ID: "up", Name: "Up", Upstream: "org/Up", Repo: "org/Up-GGUF", Quant: "Q4_K_M",
		Files: map[string]string{"model": "up.gguf"}, Caps: []string{"text"}, Context: 4096}}
	src := &fakeSource{
		files: map[string]map[string]RemoteFile{"org/Up-GGUF": {"up.gguf": {SHA256: sha('d'), Size: 600 << 20}}},
		up: map[string]UpstreamInfo{"org/Up": {License: "apache-2.0", ParamsB: 1.234, Evals: []Eval{
			{Dataset: "d/mmlu", Task: "acc", Value: 40, Date: "2026-01-01", Source: "b", URL: "https://x/1"},
			{Dataset: "d/mmlu", Task: "acc", Value: 41, Date: "2026-02-01", Source: "a", URL: "https://x/2"},
			{Dataset: "d/mmlu", Task: "acc", Value: 39, Date: "2025-01-01", Verified: true, Source: "c", URL: "https://x/3"},
			{Dataset: "d/other", Task: "x", Value: 1, Date: "2026-01-01", URL: "https://x/4"},
		}}},
		release: Release{Tag: "b2", Published: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			Assets: map[string]RemoteAsset{"llama-b2-linux.tar.gz": {URL: "https://example.org/b2.tar.gz", SHA256: sha('e'), Size: 5}}},
		fail: map[string]bool{},
	}
	return prev, pol, c, src
}

func TestRefreshBuildsModels(t *testing.T) {
	prev, pol, cands, src := refreshFixture()
	now := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)
	next, _, err := Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := next.Model("up")
	if !ok {
		t.Fatal("model missing")
	}
	if len(m.Benchmarks) != 1 || m.Benchmarks[0].Value != 39 || !m.Benchmarks[0].Verified {
		t.Errorf("verified result should win and ungated metrics be dropped: %+v", m.Benchmarks)
	}
	if m.RAMEstimate != 600+512 || m.ParamsB != 1.23 {
		t.Errorf("estimate %d params %v", m.RAMEstimate, m.ParamsB)
	}
	if !strings.Contains(m.Files[0].URL, "/resolve/"+strings.Repeat("1", 40)+"/") {
		t.Errorf("url not pinned to revision: %s", m.Files[0].URL)
	}
	if next.Version != "2026.09.26" || next.Runtime.Version != "b1" {
		t.Errorf("version %s runtime %s", next.Version, next.Runtime.Version)
	}
	if next.Defaults["small"]["text"].Model != "up" {
		t.Errorf("defaults %+v", next.Defaults)
	}

	again, _, err := Refresh(context.Background(), next, pol, cands, src, RefreshOptions{Now: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if string(Encode(again)) != string(Encode(next)) {
		t.Error("refresh without upstream change is not byte-identical")
	}

	src.up["org/Up"].Evals[2].Value = 45
	third, _, _ := Refresh(context.Background(), next, pol, cands, src, RefreshOptions{Now: now.Add(2 * time.Hour)})
	if third.Version != "2026.09.26.1" {
		t.Errorf("same-day change version = %s", third.Version)
	}
}

func TestRefreshKeepsPreviousOnFailure(t *testing.T) {
	prev, pol, cands, src := refreshFixture()
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	good, _, err := Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	cands.Candidates = append(cands.Candidates, Candidate{ID: "gone", Upstream: "org/Gone", Repo: "org/Gone-GGUF",
		Files: map[string]string{"model": "g.gguf"}, Caps: []string{"text"}, Context: 1})
	src.fail["org/Up-GGUF"] = true
	src.fail["org/Gone-GGUF"] = true
	_, warns, err := Refresh(context.Background(), good, pol, cands, src, RefreshOptions{Now: now})
	if err == nil {
		t.Fatal("all-candidates-failed must be an error")
	}
	if len(warns) != 2 {
		t.Errorf("warnings: %v", warns)
	}
	delete(src.fail, "org/Gone-GGUF")
	src.files["org/Gone-GGUF"] = map[string]RemoteFile{"g.gguf": {SHA256: sha('f'), Size: 1}}
	src.up["org/Gone"] = UpstreamInfo{License: "mit"}
	next, _, err := Refresh(context.Background(), good, pol, cands, src, RefreshOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := good.Model("up")
	kept, _ := next.Model("up")
	if kept.Files[0].SHA256 != old.Files[0].SHA256 {
		t.Error("failed candidate did not keep previous entry")
	}
}

func TestRefreshRuntime(t *testing.T) {
	prev, pol, cands, src := refreshFixture()
	prev.Runtime.Published = "2026-08-01"
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	next, _, err := Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: now, Runtime: true})
	if err != nil {
		t.Fatal(err)
	}
	if next.Runtime.Version != "b2" || next.Runtime.Published != "2026-09-01" {
		t.Errorf("runtime %+v", next.Runtime)
	}

	cands.Runtime.RefreshAfterDays = 90
	next, _, _ = Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: now, Runtime: true})
	if next.Runtime.Version != "b1" {
		t.Error("runtime bumped before refresh_after_days")
	}

	cands.Runtime.RefreshAfterDays = 0
	src.release.Assets = map[string]RemoteAsset{"llama-b2-linux.tar.gz": {URL: "https://example.org/b2.tar.gz", Size: 5}}
	next, warns, _ := Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: now, Runtime: true})
	if next.Runtime.Version != "b1" || len(warns) == 0 {
		t.Error("runtime without digest must not be pinned")
	}
}

func TestDiscover(t *testing.T) {
	_, pol, cands, src := refreshFixture()
	got, err := Discover(context.Background(), pol, cands, src)
	if err != nil {
		t.Fatal(err)
	}
	// Two gate datasets each list New-1B; it must appear once, with its GGUF.
	if len(got) != 1 || got[0].Upstream != "org/New-1B" || got[0].GGUF != "ggml-org/New-1B-GGUF" {
		t.Errorf("discovered %+v", got)
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2026.09.26.10", "2026.09.26.9", true},
		{"2026.09.26.1", "2026.09.26", true},
		{"2026.09.26", "2026.09.26", false},
		{"2026.09.25", "2026.09.26", false},
		{"2027.01.01", "2026.12.31.4", true},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
