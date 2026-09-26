package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func store(t *testing.T) Store {
	t.Helper()
	return New(t.TempDir(), t.TempDir())
}

func TestFactsRoundTrip(t *testing.T) {
	s := store(t)
	if facts, err := s.Facts(); err != nil || len(facts) != 0 {
		t.Fatalf("empty store: %q %v", facts, err)
	}
	for _, f := range []string{"I write Go", "  spaced  out  ", "I write go"} {
		if err := s.Remember(f); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := s.Facts()
	if err != nil {
		t.Fatal(err)
	}
	// Whitespace is folded and a repeat in another case is not stored twice.
	want := []string{"I write Go", "spaced out"}
	if strings.Join(facts, "|") != strings.Join(want, "|") {
		t.Errorf("got %q want %q", facts, want)
	}
	if err := s.Remember("   "); err == nil {
		t.Error("empty fact accepted")
	}
	// Multi-line input stays one fact, so the file keeps one per line.
	if err := s.Remember("two\nlines"); err != nil {
		t.Fatal(err)
	}
	if facts, _ := s.Facts(); len(facts) != 3 || facts[2] != "two lines" {
		t.Errorf("multi-line fact: %q", facts)
	}
}

func TestForget(t *testing.T) {
	s := store(t)
	for _, f := range []string{"one", "two", "three"} {
		s.Remember(f)
	}
	if err := s.Forget(2); err != nil {
		t.Fatal(err)
	}
	if facts, _ := s.Facts(); strings.Join(facts, ",") != "one,three" {
		t.Errorf("after forget: %q", facts)
	}
	if err := s.Forget(9); err == nil {
		t.Error("out of range accepted")
	}
	if err := s.Forget(0); err == nil {
		t.Error("zero accepted")
	}
	if err := s.ForgetAll(); err != nil {
		t.Fatal(err)
	}
	if facts, _ := s.Facts(); len(facts) != 0 {
		t.Errorf("facts survived: %q", facts)
	}
	// Forgetting nothing is not an error.
	if err := s.ForgetAll(); err != nil {
		t.Errorf("second ForgetAll: %v", err)
	}
}

func TestHistory(t *testing.T) {
	s := store(t)
	if h, err := s.History(0); err != nil || len(h) != 0 {
		t.Fatalf("empty history: %v %v", h, err)
	}
	for i, q := range []string{"first", "second", "third"} {
		if err := s.Record(Entry{Task: "chat", Request: q, Response: "answer " + q,
			Time: time.Now().Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	// Nothing to remember is silently skipped rather than stored blank.
	if err := s.Record(Entry{Task: "chat", Request: "", Response: "x"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d entries", len(all))
	}
	if all[0].Request != "first" || all[2].Request != "third" {
		t.Errorf("wrong order: %+v", all)
	}
	last, _ := s.History(2)
	if len(last) != 2 || last[0].Request != "second" {
		t.Errorf("limit: %+v", last)
	}

	// A damaged line must not lose the rest of the file.
	f, _ := os.OpenFile(s.HistoryPath, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("{not json\n")
	f.Close()
	s.Record(Entry{Task: "chat", Request: "fourth", Response: "answer"})
	if all, _ := s.History(0); len(all) != 4 || all[3].Request != "fourth" {
		t.Errorf("after a bad line: %d entries", len(all))
	}

	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.History(0); len(all) != 0 {
		t.Errorf("history survived clear: %d", len(all))
	}
}

func TestRecordClipsLongText(t *testing.T) {
	s := store(t)
	long := strings.Repeat("é", maxLine) // multi-byte, to catch a bad cut
	if err := s.Record(Entry{Task: "chat", Request: "q", Response: long}); err != nil {
		t.Fatal(err)
	}
	all, err := s.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d entries", len(all))
	}
	if n := len(all[0].Response); n > maxLine/2+4 {
		t.Errorf("response kept %d bytes", n)
	}
	if !strings.HasSuffix(all[0].Response, "…") {
		t.Error("clipped text is not marked")
	}
	if !utf8Valid(all[0].Response) {
		t.Error("clipping split a rune")
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

func TestPrompt(t *testing.T) {
	if got := Prompt(nil, nil); got != "" {
		t.Errorf("empty memory produced %q", got)
	}
	got := Prompt([]string{"I write Go"}, []Entry{{Request: "what is a mutex", Response: "a lock"}})
	for _, want := range []string{"I write Go", "what is a mutex", "a lock"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt omits %q:\n%s", want, got)
		}
	}
	// The recalled exchange must be labelled, or the model reads it as now.
	if !strings.Contains(got, "Earlier") {
		t.Errorf("recalled exchange is not marked as past:\n%s", got)
	}
}

// bagOfWords stands in for an encoder: vectors share direction when texts
// share words, which is enough to check the ordering of search results.
type bagOfWords struct{ err error }

func (b bagOfWords) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if b.err != nil {
		return nil, b.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 64)
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

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

func sqrt(f float64) float64 {
	if f <= 0 {
		return 0
	}
	x := f
	for i := 0; i < 40; i++ {
		x = (x + f/x) / 2
	}
	return x
}

func TestSearchRanksByMeaning(t *testing.T) {
	s := store(t)
	s.Record(Entry{Task: "chat", Request: "how do I pickle a cucumber", Response: "in brine"})
	s.Record(Entry{Task: "code", Request: "write a go mutex example", Response: "sync.Mutex"})
	s.Record(Entry{Task: "chat", Request: "what is the weather", Response: "rain"})

	hits, err := s.Search(context.Background(), bagOfWords{}, "go mutex", "", 2, cosine)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits", len(hits))
	}
	if !strings.Contains(hits[0].Request, "mutex") {
		t.Errorf("best hit was %q", hits[0].Request)
	}
	if hits[0].Score < hits[1].Score {
		t.Errorf("hits are not ordered: %.3f then %.3f", hits[0].Score, hits[1].Score)
	}

	// Searching an empty history is not an error.
	empty := store(t)
	if hits, err := empty.Search(context.Background(), bagOfWords{}, "anything", "", 3, cosine); err != nil || len(hits) != 0 {
		t.Errorf("empty search: %v %v", hits, err)
	}
	// An embedding failure is reported rather than silently returning none.
	if _, err := s.Search(context.Background(), bagOfWords{err: errors.New("no model")}, "x", "", 1, cosine); err == nil {
		t.Error("embedding error swallowed")
	}
}

func TestNewPutsFilesWhereTheyBelong(t *testing.T) {
	cfg, data := t.TempDir(), t.TempDir()
	s := New(cfg, data)
	if filepath.Dir(s.FactsPath) != cfg {
		t.Errorf("facts outside the config dir: %s", s.FactsPath)
	}
	if filepath.Dir(s.HistoryPath) != data {
		t.Errorf("history outside the data dir: %s", s.HistoryPath)
	}
}
