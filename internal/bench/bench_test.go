package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/config"
	"github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

func TestCasesAndCheck(t *testing.T) {
	cases, err := Cases()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, c := range cases {
		have[c.Cap] = true
	}
	for c := range registry.Capabilities {
		if !have[c] {
			t.Errorf("no benchmark case for %s", c)
		}
	}
	c := Case{Expect: []string{"paris"}}
	if ok, _ := Check(c, "It is Paris."); !ok {
		t.Error("case-insensitive match failed")
	}
	if ok, why := Check(c, "London"); ok || !strings.Contains(why, "paris") {
		t.Error("mismatch passed")
	}
	if ok, _ := Check(Case{Regex: `def\s+add`}, "def add(a, b):"); !ok {
		t.Error("regex failed")
	}
	if ok, _ := Check(Case{Schema: []byte(`{}`)}, "{broken"); ok {
		t.Error("invalid json passed")
	}
}

type fake struct{ answer string }

func (f fake) Open(context.Context, *registry.Model, map[string]string) (runtime.Session, error) {
	return f, nil
}
func (f fake) Speak(_ context.Context, _ *registry.Model, _ map[string]string, _, out string) (runtime.Stats, error) {
	return runtime.Stats{PeakRSSMB: 900}, os.WriteFile(out, make([]byte, 100), 0o644)
}
func (f fake) Generate(_ context.Context, req runtime.Request) (runtime.Result, error) {
	a := f.answer
	if len(req.Audio) > 0 {
		a = "the quick brown fox jumps over the lazy dog"
	}
	return runtime.Result{Text: a, OutputTokens: 10, GenMS: 500, PromptTokens: 20, PromptMS: 100}, nil
}
func (fake) Close() runtime.Stats { return runtime.Stats{StartupMS: 1200, PeakRSSMB: 1500} }

func TestRun(t *testing.T) {
	models := []*registry.Model{
		{ID: "txt", Caps: []string{"text", "code"}},
		{ID: "ear", Caps: []string{"asr"}},
		{ID: "voice", Caps: []string{"tts"}},
	}
	opt := Options{
		Models: models,
		Files:  func(*registry.Model) map[string]string { return nil },
		Backend: func(m *registry.Model) (runtime.Backend, error) {
			return fake{answer: "Paris. def add(a, b): return a+b"}, nil
		},
		TempDir: t.TempDir(),
	}
	entries, err := Run(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries %+v", entries)
	}
	txt := entries[0]
	if txt.Cases != 2 || txt.Passed != 2 || txt.TokensPerSec != 20 || txt.PeakRSSMB != 1500 || txt.Kind != "local" {
		t.Errorf("text entry %+v", txt)
	}
	if entries[1].Passed != 1 {
		t.Errorf("asr round trip %+v", entries[1])
	}
	if entries[2].Passed != 1 || entries[2].PeakRSSMB != 900 {
		t.Errorf("tts %+v", entries[2])
	}
	opt.Full = true
	entries, _ = Run(context.Background(), opt)
	if entries[0].Cases != 5 || entries[0].Passed != 2 {
		t.Errorf("full run %+v", entries[0])
	}
	// ASR without a TTS model is skipped, not failed.
	opt.Models = models[:2]
	entries, _ = Run(context.Background(), opt)
	if entries[1].Cases != 0 || len(entries[1].Skipped) != 1 {
		t.Errorf("asr without tts %+v", entries[1])
	}
}

func TestSaveLoadPropose(t *testing.T) {
	dir := t.TempDir()
	if r, err := Load(dir); err != nil || len(r.Entries) != 0 {
		t.Fatal("empty load failed")
	}
	reg := registry.Default()
	cfg := config.Default()
	small, _ := reg.Select("text", "small", registry.Env{RAMMB: 16384})
	// The default text model is too slow on this machine.
	if err := Save(dir, "test", []Entry{{Model: small.Model, TokensPerSec: 1, Cases: 3, Passed: 3, PeakRSSMB: 1000}}); err != nil {
		t.Fatal(err)
	}
	res, _ := Load(dir)
	changes := Propose(reg, cfg, res, 16384)
	var text *Change
	for i := range changes {
		if changes[i].Cap == "text" {
			text = &changes[i]
		}
	}
	if text == nil || text.From != small.Model || text.To == small.Model || !text.Pin {
		t.Fatalf("changes %+v", changes)
	}
	next := Apply(cfg, changes)
	if next.Models["text"] != text.To {
		t.Errorf("apply %+v", next.Models)
	}
	// Once measurements are fine again, the pin is proposed for removal.
	Save(dir, "test", []Entry{{Model: small.Model, TokensPerSec: 50, Cases: 3, Passed: 3}})
	res, _ = Load(dir)
	back := Propose(reg, next, res, 16384)
	found := false
	for _, ch := range back {
		if ch.Cap == "text" && !ch.Pin && ch.To == small.Model {
			found = true
		}
	}
	if !found || Apply(next, back).Models["text"] != "" {
		t.Errorf("unpin not proposed: %+v", back)
	}
	if _, err := os.Stat(dir + "/bench/history.jsonl"); err != nil {
		t.Error("history not written")
	}
}

// embedFake answers embedding requests with bag-of-words vectors, so the
// embedding case has a real comparison to make.
type embedFake struct{ fake }

// Open must return the embedding session itself: the promoted method would
// hand back the plain fake, which cannot embed.
func (f embedFake) Open(context.Context, *registry.Model, map[string]string) (runtime.Session, error) {
	return f, nil
}

func (embedFake) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 32)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := 0
			for _, c := range w {
				h = (h*31 + int(c)) % len(v)
			}
			v[h]++
		}
		out[i] = v
	}
	return out, nil
}

func TestEmbeddingCase(t *testing.T) {
	c := Case{
		ID: "embed-routing", Cap: "embed",
		Prompt:    "turn this recording into text",
		Similar:   "transcribe a recording into text",
		Different: "resize an image with ffmpeg",
	}
	ok, why, err := embedCase(context.Background(), embedFake{}, c)
	if err != nil || !ok {
		t.Errorf("similar text should win: ok=%v why=%q err=%v", ok, why, err)
	}
	// Swapped, the case must fail and say what it found.
	c.Similar, c.Different = c.Different, c.Similar
	ok, why, err = embedCase(context.Background(), embedFake{}, c)
	if err != nil || ok {
		t.Errorf("expected a failure: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(why, "closer to") {
		t.Errorf("unhelpful explanation: %q", why)
	}
	// A model that cannot embed is an error, not a silent pass.
	if _, _, err := embedCase(context.Background(), fake{}, c); err == nil {
		t.Error("a non-embedding model was accepted")
	}
}

func TestRunBenchesAnEmbeddingModel(t *testing.T) {
	opt := Options{
		Models:  []*registry.Model{{ID: "vec", Caps: []string{"embed"}}},
		Files:   func(*registry.Model) map[string]string { return nil },
		Backend: func(*registry.Model) (runtime.Backend, error) { return embedFake{}, nil },
		TempDir: t.TempDir(),
	}
	entries, err := Run(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries: %+v", entries)
	}
	e := entries[0]
	// The built-in case is scored with a bag-of-words stand-in rather than
	// a real encoder, so it may or may not pass; what matters is that the
	// embedding path ran, timed the model and reached a verdict.
	if e.Cases != 1 {
		t.Errorf("embedding case did not run: %+v", e)
	}
	if e.Passed == 0 && len(e.Failures) == 0 {
		t.Errorf("no verdict recorded: %+v", e)
	}
	if e.LatencyMS <= 0 {
		t.Errorf("embedding was not timed: %+v", e)
	}
	// Nothing is generated, so there is no token rate to report.
	if e.TokensPerSec != 0 {
		t.Errorf("tokens per second for an encoder: %v", e.TokensPerSec)
	}
}

func TestResultsSortedAndLoadErrors(t *testing.T) {
	r := Results{Entries: map[string]Entry{
		"z": {Model: "z"}, "a": {Model: "a"}, "m": {Model: "m"},
	}}
	got := r.Sorted()
	if len(got) != 3 || got[0].Model != "a" || got[2].Model != "z" {
		t.Errorf("sorted: %+v", got)
	}
	if len(Results{}.Sorted()) != 0 {
		t.Error("empty results should sort to nothing")
	}

	dir := t.TempDir()
	// No results yet is not an error.
	if r, err := Load(dir); err != nil || len(r.Entries) != 0 {
		t.Errorf("empty dir: %+v %v", r, err)
	}
	// A damaged file is reported, naming the path.
	os.MkdirAll(filepath.Dir(resultsPath(dir)), 0o755)
	os.WriteFile(resultsPath(dir), []byte("{not json"), 0o644)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "results") {
		t.Errorf("broken results: %v", err)
	}
	// A file without an entries map still loads usably.
	os.WriteFile(resultsPath(dir), []byte(`{"machine":"x"}`), 0o644)
	r2, err := Load(dir)
	if err != nil || r2.Entries == nil {
		t.Errorf("results without entries: %+v %v", r2, err)
	}
	if err := Save(dir, "machine", []Entry{{Model: "m"}}); err != nil {
		t.Fatal(err)
	}
	if r3, _ := Load(dir); r3.Machine != "machine" || r3.Entries["m"].Model != "m" {
		t.Errorf("saved results: %+v", r3)
	}
}

func TestCasesRejectsBadDefinitions(t *testing.T) {
	// The built-in cases must always parse and name real capabilities.
	if _, err := Cases(); err != nil {
		t.Fatalf("built-in cases: %v", err)
	}
	// Check's regex path is compiled per call, so a bad one must not panic
	// the run; Cases is what rejects it up front.
	if ok, why := Check(Case{Regex: `def\s+add`}, "nothing here"); ok || why == "" {
		t.Errorf("regex mismatch: %v %q", ok, why)
	}
}

// failing backends, to check that one bad model does not sink a run.
type openFails struct{ fake }

func (openFails) Open(context.Context, *registry.Model, map[string]string) (runtime.Session, error) {
	return nil, errors.New("model will not load")
}

type genFails struct{ fake }

func (g genFails) Open(context.Context, *registry.Model, map[string]string) (runtime.Session, error) {
	return g, nil
}
func (genFails) Generate(context.Context, runtime.Request) (runtime.Result, error) {
	return runtime.Result{}, errors.New("generation failed")
}

type speakFails struct{ fake }

func (s speakFails) Open(context.Context, *registry.Model, map[string]string) (runtime.Session, error) {
	return s, nil
}
func (speakFails) Speak(context.Context, *registry.Model, map[string]string, string, string) (runtime.Stats, error) {
	return runtime.Stats{}, errors.New("no voice")
}

func TestRunReportsModelFailures(t *testing.T) {
	base := func(b runtime.Backend, caps ...string) Options {
		return Options{
			Models:  []*registry.Model{{ID: "m", Caps: caps}},
			Files:   func(*registry.Model) map[string]string { return nil },
			Backend: func(*registry.Model) (runtime.Backend, error) { return b, nil },
			TempDir: t.TempDir(),
		}
	}
	// A model that will not load stops the run and says which one.
	if _, err := Run(context.Background(), base(openFails{}, "text")); err == nil || !strings.Contains(err.Error(), "m:") {
		t.Errorf("open failure: %v", err)
	}
	// A model that loads but cannot answer records failures instead.
	entries, err := Run(context.Background(), base(genFails{}, "text"))
	if err != nil {
		t.Fatalf("generate failure: %v", err)
	}
	if len(entries) != 1 || entries[0].Passed != 0 || len(entries[0].Failures) == 0 {
		t.Errorf("failures not recorded: %+v", entries)
	}
	// A tts model whose synthesis fails is reported, not silently skipped.
	opt := base(speakFails{}, "tts")
	entries, err = Run(context.Background(), opt)
	if err != nil {
		t.Fatalf("speak failure: %v", err)
	}
	if len(entries[0].Failures) == 0 {
		t.Errorf("tts failure not recorded: %+v", entries[0])
	}
	// A backend that cannot be built at all is an error.
	bad := base(fake{}, "text")
	bad.Backend = func(*registry.Model) (runtime.Backend, error) { return nil, errors.New("unknown backend") }
	if _, err := Run(context.Background(), bad); err == nil {
		t.Error("a backend failure was accepted")
	}
	// A model with no cases for its capability is skipped quietly.
	none := base(fake{}, "vision")
	none.Models[0].Caps = []string{"telepathy"}
	if entries, err := Run(context.Background(), none); err != nil || len(entries) != 0 {
		t.Errorf("unknown capability: %+v %v", entries, err)
	}
}

func TestSaveRefusesAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	os.Mkdir(locked, 0o500)
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	if err := Save(locked, "machine", []Entry{{Model: "m"}}); err == nil {
		t.Error("saving into a read-only directory reported success")
	}
}
