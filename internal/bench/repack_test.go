package bench

import (
	"context"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/config"
	"github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

// loadFake loads and reads prompts at the speeds given, as a backend that
// repacks weights or not would.
type loadFake struct {
	fake
	startupMS, promptTPS float64
}

func (f loadFake) Open(context.Context, *registry.Model, map[string]string) (runtime.Session, error) {
	return f, nil
}
func (f loadFake) Generate(_ context.Context, req runtime.Request) (runtime.Result, error) {
	return runtime.Result{Text: "ok", PromptTokens: 600, PromptMS: 600 / f.promptTPS * 1000}, nil
}
func (f loadFake) Close() runtime.Stats { return runtime.Stats{StartupMS: f.startupMS} }

func TestFullRunTimesLoadingWithAndWithoutRepacking(t *testing.T) {
	var asked []bool
	opt := Options{
		Full:   true,
		Models: []*registry.Model{{ID: "txt", Backend: "llama.cpp", Caps: []string{"text"}}, {ID: "ear", Backend: "llama.cpp", Caps: []string{"asr"}}},
		Files:  func(*registry.Model) map[string]string { return nil },
		Backend: func(m *registry.Model) (runtime.Backend, error) {
			return fake{answer: "Paris 42 red, green, blue"}, nil
		},
		Loader: func(m *registry.Model, repack bool) (runtime.Backend, error) {
			asked = append(asked, repack)
			if repack {
				return loadFake{startupMS: 2500, promptTPS: 127}, nil
			}
			return loadFake{startupMS: 1000, promptTPS: 101}, nil
		},
		TempDir: t.TempDir(),
	}
	entries, err := Run(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if e.NoRepack == nil || e.Repack == nil || e.NoRepack.StartupMS != 1000 || e.Repack.PromptTPS != 127 {
		t.Fatalf("load times: %+v %+v", e.NoRepack, e.Repack)
	}
	if len(asked) != 2 || asked[0] || !asked[1] {
		t.Errorf("loaded with repack %v; only the text model, both ways", asked)
	}
	// 1.5 s more load against 1/101 - 1/127 s saved per token.
	if be, ok := e.RepackBreakEven(); !ok || be < 700 || be > 780 {
		t.Errorf("break-even %d %v", be, ok)
	}
	if entries[1].NoRepack != nil {
		t.Error("an asr model was load-timed")
	}
	// Quick mode leaves it out.
	opt.Full, asked = false, nil
	if entries, _ = Run(context.Background(), opt); entries[0].NoRepack != nil || len(asked) != 0 {
		t.Error("quick mode timed loading")
	}
}

func TestBreakEvenEdges(t *testing.T) {
	for _, c := range []struct {
		no, re LoadTime
		want   int
	}{
		{LoadTime{1000, 100}, LoadTime{900, 120}, 0},   // repacking costs nothing
		{LoadTime{1000, 100}, LoadTime{2000, 100}, -1}, // and gains nothing
		{LoadTime{1000, 100}, LoadTime{2000, 200}, 200},
	} {
		e := Entry{NoRepack: &c.no, Repack: &c.re}
		if got, ok := e.RepackBreakEven(); !ok || got != c.want {
			t.Errorf("%+v %+v: %d, want %d", c.no, c.re, got, c.want)
		}
	}
	if _, ok := (Entry{}).RepackBreakEven(); ok {
		t.Error("break-even without measurements")
	}
}

func TestProposeRepack(t *testing.T) {
	reg := registry.Default()
	cfg := config.Default()
	text, _ := reg.Select("text", cfg.Profile, registry.Env{RAMMB: 16384})
	res := func(no, re LoadTime) Results {
		return Results{Entries: map[string]Entry{text.Model: {Model: text.Model, TokensPerSec: 50, Cases: 1, Passed: 1, NoRepack: &no, Repack: &re}}}
	}
	find := func(changes []Change) *Change {
		for i := range changes {
			if changes[i].Key == "repack" {
				return &changes[i]
			}
		}
		return nil
	}
	// Pays off early: turn it on.
	ch := find(Propose(reg, cfg, res(LoadTime{1000, 101}, LoadTime{2500, 127}), 16384))
	if ch == nil || ch.From != "auto" || ch.To != "on" || !strings.Contains(ch.Reason, "from about") {
		t.Fatalf("change %+v", ch)
	}
	next := Apply(cfg, []Change{*ch})
	if next.Repack != "on" {
		t.Errorf("apply: %q", next.Repack)
	}
	// Pays off late: auto already skips it, so nothing to change...
	late := res(LoadTime{1000, 101}, LoadTime{4000, 110})
	if ch := find(Propose(reg, cfg, late, 16384)); ch != nil {
		t.Errorf("proposed %+v", ch)
	}
	// ...unless it was turned on.
	if ch := find(Propose(reg, next, late, 16384)); ch == nil || ch.To != "off" {
		t.Errorf("did not propose turning it off: %+v", ch)
	}
}
