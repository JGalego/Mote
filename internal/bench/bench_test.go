package bench

import (
	"context"
	"os"
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
