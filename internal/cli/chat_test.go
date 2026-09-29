package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/ui"
)

// fakeLog returns the requests the fake llama-server was sent.
func fakeLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(path)
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func TestChatKeepsTheConversation(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	log := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("MOTE_FAKE_LOG", log)
	e.mote("", "remember", "I write Go")
	in := "hello\nwhat did I say\n\n/new\nfirst \\\nsecond\n/exit\nnever sent\n"
	code, out, errs := e.mote(in, "chat", "--system", "Be brief.")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	want := "echo: hello\necho: what did I say\necho: first \nsecond\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	reqs := fakeLog(t, log)
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	// system + question, then system + one exchange + question, then after
	// /new system + question again.
	for i, n := range []float64{2, 4, 2} {
		if reqs[i]["messages"] != n {
			t.Errorf("request %d had %v messages, want %v", i+1, reqs[i]["messages"], n)
		}
	}
	if sys, _ := reqs[0]["system"].(string); !strings.Contains(sys, "I write Go") || !strings.HasSuffix(sys, "Be brief.") {
		t.Errorf("system prompt %q", sys)
	}
	if code, _, _ := e.mote("", "chat", "stray"); code != ExitUsage {
		t.Errorf("positional argument: exit %d", code)
	}
}

func TestReadTurn(t *testing.T) {
	a := &app{ue: ui.New(io.Discard)}

	// A line ending in backslash continues onto the next, trimmed once complete.
	sc := bufio.NewScanner(strings.NewReader("first \\\nsecond\n"))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	if line, ok := a.readTurn(sc, false); !ok || line != "first \nsecond" {
		t.Errorf("continuation: got %q %v", line, ok)
	}

	// EOF while a continuation is still open returns what was gathered so far.
	sc = bufio.NewScanner(strings.NewReader("first \\\n"))
	if line, ok := a.readTurn(sc, false); !ok || line != "first " {
		t.Errorf("continuation cut short by EOF: got %q %v", line, ok)
	}

	// Plain EOF with nothing typed reports there is no turn.
	sc = bufio.NewScanner(strings.NewReader(""))
	if _, ok := a.readTurn(sc, false); ok {
		t.Error("empty input should report no turn")
	}

	// Live mode prompts for each line, marks a continuation differently,
	// and ends the prompt's line once input runs out mid-continuation.
	var errBuf bytes.Buffer
	a.err = &errBuf
	sc = bufio.NewScanner(strings.NewReader("first \\\n"))
	if line, ok := a.readTurn(sc, true); !ok || line != "first " {
		t.Errorf("live continuation: got %q %v", line, ok)
	}
	if s := errBuf.String(); !strings.Contains(s, "›") || !strings.Contains(s, "…") {
		t.Errorf("live prompts missing: %q", s)
	}
}

func TestFitTurnsDropsTheOldestPairs(t *testing.T) {
	turn := func(n int) mrt.Turn { return mrt.Turn{Role: "user", Text: strings.Repeat("x", n)} }
	turns := []mrt.Turn{turn(3000), turn(3000), turn(100), turn(100), turn(100), turn(100)}
	// (1500 - 500 - 256) * 3 = 2232 characters: the two short pairs fit.
	if got := fitTurns(turns, 1500, 500, 0); len(got) != 4 || len(got[0].Text) != 100 {
		t.Errorf("kept %d turns", len(got))
	}
	if got := fitTurns(turns, 0, 500, 0); len(got) != 6 {
		t.Error("an unknown context should keep everything")
	}
	if got := fitTurns(turns, 1500, 500, 5000); len(got) != 0 {
		t.Errorf("a long question leaves no room, kept %d", len(got))
	}
}
