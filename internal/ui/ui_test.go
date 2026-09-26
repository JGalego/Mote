package ui

import (
	"bytes"
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
