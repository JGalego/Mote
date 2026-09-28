package ui

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPlainOutputHasNoEscapes(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	if u.Color() || u.Live() {
		t.Fatal("buffer treated as a terminal")
	}
	if got := u.Green("ok") + u.Bold("x"); got != "okx" {
		t.Errorf("plain paint: %q", got)
	}
	if b := u.Banner("tag"); strings.Contains(b, "\x1b") || !strings.HasPrefix(b, "mote") {
		t.Errorf("banner: %q", b)
	}
	s := u.Spin("working")
	s.Stop("done")
	bar := u.NewBar("file", 0, 10<<20)
	bar.Set(5 << 20)
	bar.Finish(nil)
	if strings.Contains(buf.String(), "\x1b") || !strings.Contains(buf.String(), "done") {
		t.Errorf("output %q", buf.String())
	}
}

func TestBarFitsNarrowTerminal(t *testing.T) {
	u := &UI{color: true}
	b := &Bar{u: u, label: "qwen3.5-0.8b/Qwen3.5-0.8B-Q4_K_M.gguf", total: 507 << 20, done: 100 << 20, start: time.Now().Add(-2 * time.Second)}
	plain := strings.NewReplacer("\x1b[38;5;141m", "", "\x1b[2m", "", "\x1b[0m", "").Replace(b.line())
	if n := utf8.RuneCountInString(plain); n >= 80 {
		t.Errorf("bar line is %d columns: %q", n, plain)
	}
}

func TestFit(t *testing.T) {
	if fit("abcdef", 4) != "abc…" || fit("abc", 4) != "abc" {
		t.Error("fit")
	}
}

// liveUI returns a UI that believes it is a terminal, so spinners and bars
// draw and colour is on.
func liveUI(buf *bytes.Buffer) *UI { return &UI{w: buf, color: true, live: true} }

func TestBannerPaintsWhenColoured(t *testing.T) {
	var buf bytes.Buffer
	b := liveUI(&buf).Banner("small models")
	if !strings.Contains(b, "\x1b[") {
		t.Errorf("coloured banner has no escapes: %q", b)
	}
	if !strings.Contains(b, "small models") {
		t.Errorf("banner omits the tagline: %q", b)
	}
	// Every line of the logo is drawn.
	if n := strings.Count(b, "\n"); n < len(bannerLines) {
		t.Errorf("banner has %d lines, want at least %d", n, len(bannerLines))
	}
}

func TestSpinnerDrawsAndStops(t *testing.T) {
	var buf bytes.Buffer
	u := liveUI(&buf)
	// The spinner goroutine writes to buf under u.mu, so every read of it
	// here must take the same lock or race with those writes.
	snapshot := func() string {
		u.mu.Lock()
		defer u.mu.Unlock()
		return buf.String()
	}
	s := u.Spin("thinking")
	// Give the animation a moment to draw at least one frame.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(snapshot(), "thinking") {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(snapshot(), "thinking") {
		t.Error("spinner never drew its message")
	}
	if s.Elapsed() <= 0 {
		t.Error("spinner reports no elapsed time")
	}
	s.Stop("done")
	out := snapshot()
	if !strings.Contains(out, "done") {
		t.Errorf("final message missing: %q", out)
	}
	// Stopping twice must not panic or double-print.
	s.Stop("done")
}

func TestSpinnerOnAPipePrintsOnlyTheResult(t *testing.T) {
	var buf bytes.Buffer
	u := Plain(&buf)
	s := u.Spin("working")
	if buf.Len() != 0 {
		t.Errorf("non-live spinner drew %q", buf.String())
	}
	s.Stop("finished")
	if !strings.Contains(buf.String(), "finished") {
		t.Errorf("result not printed: %q", buf.String())
	}
}

func TestProgressBar(t *testing.T) {
	var buf bytes.Buffer
	u := liveUI(&buf)
	b := u.NewBar("model.gguf", 0, 100<<20)
	b.Set(50 << 20)
	if !strings.Contains(buf.String(), "model.gguf") {
		t.Errorf("bar did not draw: %q", buf.String())
	}
	// Redraws are throttled, so an immediate second Set changes nothing.
	n := buf.Len()
	b.Set(60 << 20)
	if buf.Len() != n {
		t.Error("bar redrew inside the throttle window")
	}
	b.Finish(nil)
	out := buf.String()
	if !strings.Contains(out, "100 MB in") {
		t.Errorf("finished bar: %q", out)
	}
	// A failed download is marked, not silently finished.
	var buf2 bytes.Buffer
	b2 := liveUI(&buf2).NewBar("x", 0, 1<<20)
	b2.Finish(errors.New("boom"))
	if !strings.Contains(buf2.String(), "x") {
		t.Errorf("failed bar: %q", buf2.String())
	}
}

func TestBarWithUnknownTotal(t *testing.T) {
	var buf bytes.Buffer
	b := liveUI(&buf).NewBar("unknown", 0, 0)
	b.Set(1 << 20)
	b.Finish(nil)
	if strings.Contains(buf.String(), "NaN") || strings.Contains(buf.String(), "+Inf") {
		t.Errorf("zero total produced %q", buf.String())
	}
}

func TestAskAndWidth(t *testing.T) {
	var buf bytes.Buffer
	u := liveUI(&buf)
	if a := u.Ask(); !strings.Contains(a, "?") {
		t.Errorf("Ask: %q", a)
	}
	if a := Plain(&buf).Ask(); a != "?" {
		t.Errorf("plain Ask: %q", a)
	}
	// A buffer is not a terminal, so the width falls back to 80.
	if w := Plain(&buf).Width(); w != 80 {
		t.Errorf("width %d", w)
	}
	// A real file is not a terminal either, which exercises termWidth.
	f, err := os.CreateTemp(t.TempDir(), "w")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if w := (&UI{w: f, file: f}).Width(); w != 80 {
		t.Errorf("file width %d", w)
	}
}
