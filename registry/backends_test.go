package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// withImages registers the image capability for a test when the registry
// does not have it yet.
func withImages(t *testing.T) {
	if _, ok := Capabilities["image"]; ok {
		return
	}
	Capabilities["image"] = "text -> image"
	t.Cleanup(func() { delete(Capabilities, "image") })
}

// sdFixture adds an sd.cpp runtime and an image candidate whose files come
// from three repositories, the way FLUX.2-klein's do.
func sdFixture() (*Registry, Policy, Candidates, *fakeSource) {
	prev, pol, c, src := refreshFixture()
	c.Runtimes = map[string]RuntimeSource{"sd.cpp": {
		Repo: "leejet/stable-diffusion.cpp", License: "MIT",
		Assets: map[string]string{
			"linux/amd64":  "sd-*-bin-Linux-Ubuntu-*-x86_64.zip",
			"darwin/arm64": "sd-*-bin-Darwin-macOS-*-arm64.zip",
		},
	}}
	c.Candidates = append(c.Candidates, Candidate{
		ID: "painter", Name: "Painter", Backend: "sd.cpp", Upstream: "org/Painter", Repo: "org/Painter-GGUF", Quant: "Q4_0",
		Files: map[string]FileSpec{
			"model": {File: "painter.gguf"},
			"vae":   {Repo: "org/Painter", File: "vae/diffusion_pytorch_model.safetensors"},
			"llm":   {Repo: "org/Encoder-GGUF", File: "encoder.gguf"},
		},
		Caps: []string{"image"}, Args: []string{"--steps", "4"},
	})
	src.files["org/Painter-GGUF"] = map[string]RemoteFile{"painter.gguf": {SHA256: sha('1'), Size: 2 << 30}}
	src.files["org/Painter"] = map[string]RemoteFile{"vae/diffusion_pytorch_model.safetensors": {SHA256: sha('2'), Size: 160 << 20}}
	src.files["org/Encoder-GGUF"] = map[string]RemoteFile{"encoder.gguf": {SHA256: sha('3'), Size: 2 << 30}}
	src.up["org/Painter"] = UpstreamInfo{License: "apache-2.0", ParamsB: 3.9}
	src.revs = map[string]string{
		"org/Painter-GGUF": strings.Repeat("a", 40),
		"org/Painter":      strings.Repeat("b", 40),
		"org/Encoder-GGUF": strings.Repeat("c", 40),
	}
	src.releases = map[string]Release{"leejet/stable-diffusion.cpp": {
		Tag: "master-9-abc", Published: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		Assets: map[string]RemoteAsset{
			"sd-master-abc-bin-Linux-Ubuntu-24.04-x86_64.zip":        {URL: "https://example.org/sd-linux.zip", SHA256: sha('4'), Size: 9},
			"sd-master-abc-bin-Linux-Ubuntu-24.04-x86_64-vulkan.zip": {URL: "https://example.org/sd-vulkan.zip", SHA256: sha('5'), Size: 9},
			"sd-master-abc-bin-Darwin-macOS-26.6.2-arm64.zip":        {URL: "https://example.org/sd-mac.zip", SHA256: sha('6'), Size: 9},
		},
	}}
	return prev, pol, c, src
}

func TestRefreshBuildsModelsFromSeveralRepos(t *testing.T) {
	withImages(t)
	prev, pol, cands, src := sdFixture()
	next, warns, err := Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: time.Now()})
	if err != nil {
		t.Fatal(err, warns)
	}
	// The sd.cpp runtime is pinned even without runtime bumps: its models
	// cannot run without it.
	rt, ok := next.RuntimeFor("sd.cpp")
	if !ok || rt.Version != "master-9-abc" || rt.License != "MIT" {
		t.Fatalf("runtime %+v %v", rt, ok)
	}
	// * spans the macOS version, and the plain Linux build is chosen over
	// the Vulkan one.
	if a, ok := rt.Asset("linux", "amd64"); !ok || a.URL != "https://example.org/sd-linux.zip" {
		t.Errorf("linux asset %+v", a)
	}
	if a, ok := rt.Asset("darwin", "arm64"); !ok || a.URL != "https://example.org/sd-mac.zip" {
		t.Errorf("mac asset %+v", a)
	}
	if _, ok := rt.Asset("linux", "arm64"); ok {
		t.Error("an unlisted platform got an asset")
	}

	m, ok := next.Model("painter")
	if !ok {
		t.Fatalf("model missing: %v", warns)
	}
	if m.Backend != "sd.cpp" || m.Context != 0 {
		t.Errorf("model %+v", m)
	}
	want := map[string]string{
		"model": "https://huggingface.co/org/Painter-GGUF/resolve/" + strings.Repeat("a", 40) + "/painter.gguf",
		"vae":   "https://huggingface.co/org/Painter/resolve/" + strings.Repeat("b", 40) + "/vae/diffusion_pytorch_model.safetensors",
		"llm":   "https://huggingface.co/org/Encoder-GGUF/resolve/" + strings.Repeat("c", 40) + "/encoder.gguf",
	}
	for role, url := range want {
		f, ok := m.File(role)
		if !ok || f.URL != url {
			t.Errorf("%s: %+v, want %s", role, f, url)
		}
	}
	// A file in a folder is stored under its base name.
	if f, _ := m.File("vae"); f.Name != "diffusion_pytorch_model.safetensors" {
		t.Errorf("vae name %q", f.Name)
	}
	if err := next.Validate(); err != nil {
		t.Error(err)
	}
	// The encoded registry reads back, schema and all.
	if _, err := Parse(Encode(next)); err != nil {
		t.Error(err)
	}
}

func TestRefreshKeepsAPinnedRuntimeUnlessAsked(t *testing.T) {
	withImages(t)
	prev, pol, cands, src := sdFixture()
	first, _, err := Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	newer := src.releases["leejet/stable-diffusion.cpp"]
	newer.Tag = "master-10-def"
	src.releases["leejet/stable-diffusion.cpp"] = newer
	second, _, err := Refresh(context.Background(), first, pol, cands, src, RefreshOptions{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rt, _ := second.RuntimeFor("sd.cpp"); rt.Version != "master-9-abc" {
		t.Errorf("pinned runtime moved without --runtime: %s", rt.Version)
	}
	third, _, err := Refresh(context.Background(), first, pol, cands, src, RefreshOptions{Now: time.Now(), Runtime: true})
	if err != nil {
		t.Fatal(err)
	}
	if rt, _ := third.RuntimeFor("sd.cpp"); rt.Version != "master-10-def" {
		t.Errorf("--runtime did not bump sd.cpp: %s", rt.Version)
	}
}

func TestMatchAsset(t *testing.T) {
	rel := Release{Tag: "t", Assets: map[string]RemoteAsset{
		"a-1.zip": {SHA256: sha('1')}, "a-2.zip": {SHA256: sha('2')}, "b.zip": {}}}
	if _, err := matchAsset(rel, "a-*.zip"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Errorf("two matches: %v", err)
	}
	if _, err := matchAsset(rel, "c-*.zip"); err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Errorf("no match: %v", err)
	}
	if _, err := matchAsset(rel, "b.zip"); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("no digest: %v", err)
	}
	if a, err := matchAsset(rel, "a-1.zip"); err != nil || a.SHA256 != sha('1') {
		t.Errorf("exact: %v", err)
	}
}

func TestRefreshSkipsModelsWhoseRuntimeIsMissing(t *testing.T) {
	withImages(t)
	prev, pol, cands, src := sdFixture()
	cands.Runtimes = nil
	next, warns, err := Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.Model("painter"); ok {
		t.Error("a model without its runtime was kept")
	}
	if !strings.Contains(strings.Join(warns, "\n"), "painter: no sd.cpp runtime") {
		t.Errorf("warnings %v", warns)
	}
}

func TestValidateBackends(t *testing.T) {
	withImages(t)
	prev, pol, cands, src := sdFixture()
	good, _, err := Refresh(context.Background(), prev, pol, cands, src, RefreshOptions{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	painter := func(r *Registry) *Model {
		for i := range r.Models {
			if r.Models[i].ID == "painter" {
				return &r.Models[i]
			}
		}
		t.Fatal("no painter")
		return nil
	}
	bad := map[string]func(r *Registry){
		"missing vae":        func(r *Registry) { m := painter(r); m.Files = m.Files[:1] },
		"image on llama":     func(r *Registry) { painter(r).Backend = "llama.cpp" },
		"text on sd":         func(r *Registry) { painter(r).Caps = []string{"image", "text"} },
		"unknown backend":    func(r *Registry) { painter(r).Backend = "comfy" },
		"unpinned backend":   func(r *Registry) { r.Runtimes = nil },
		"llama role on sd":   func(r *Registry) { painter(r).Files[1].Role = "mmproj" },
		"runtime twice":      func(r *Registry) { r.Runtimes = append(r.Runtimes, r.Runtimes[0]) },
		"runtime unlicensed": func(r *Registry) { r.Runtimes[0].License = "" },
		"llama in runtimes": func(r *Registry) {
			r.Runtimes = append(r.Runtimes, Runtime{Name: "llama.cpp", Version: "x", License: "MIT"})
		},
	}
	for name, mutate := range bad {
		var r Registry
		b, _ := json.Marshal(good)
		json.Unmarshal(b, &r)
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseNamesANewerSchema(t *testing.T) {
	_, err := Parse([]byte(`{"schema": 99, "some_future_field": true}`))
	if err == nil || !strings.Contains(err.Error(), "needs a newer mote") {
		t.Errorf("newer schema: %v", err)
	}
}

func TestParseSetsOlderSchemasApart(t *testing.T) {
	_, err := Parse([]byte(`{"schema": 1}`))
	if !errors.Is(err, ErrOldSchema) {
		t.Errorf("older schema: %v", err)
	}
}

func TestFileSpecAcceptsBothForms(t *testing.T) {
	var c Candidate
	err := json.Unmarshal([]byte(`{"files": {"model": "m.gguf", "vae": {"repo": "o/r", "file": "vae/x.safetensors"}}}`), &c)
	if err != nil {
		t.Fatal(err)
	}
	if c.Files["model"] != (FileSpec{File: "m.gguf"}) || c.Files["vae"] != (FileSpec{Repo: "o/r", File: "vae/x.safetensors"}) {
		t.Errorf("files %+v", c.Files)
	}
	for _, bad := range []string{`{"files": {"vae": {"repo": "o/r"}}}`, `{"files": {"vae": {"file": "x", "extra": 1}}}`} {
		if err := json.Unmarshal([]byte(bad), &c); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}
