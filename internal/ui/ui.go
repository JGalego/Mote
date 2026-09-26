// Package ui renders terminal output: colour, the banner, spinners and
// progress bars. Everything degrades to plain text when the writer is not a
// terminal, when NO_COLOR is set, or when TERM=dumb, so piped output stays
// clean and scripts see the same text as before.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// UI writes decorated output to one stream.
type UI struct {
	w     io.Writer
	color bool
	live  bool // may redraw lines (spinners, bars)
	file  *os.File
	mu    sync.Mutex
}

// Width returns the terminal width in columns (80 when unknown).
func (u *UI) Width() int {
	if u.file != nil {
		if w := termWidth(u.file); w > 0 {
			return w
		}
	}
	return 80
}

// fit shortens s to at most n runes, marking the cut with an ellipsis.
func fit(s string, n int) string {
	r := []rune(s)
	if n <= 1 || len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// New inspects w and the environment to decide what decoration is safe.
func New(w io.Writer) *UI {
	u := &UI{w: w}
	f, ok := w.(*os.File)
	if !ok {
		return u
	}
	st, err := f.Stat()
	tty := err == nil && st.Mode()&os.ModeCharDevice != 0
	force := os.Getenv("CLICOLOR_FORCE") != "" && os.Getenv("CLICOLOR_FORCE") != "0"
	if (!tty && !force) || os.Getenv("TERM") == "dumb" {
		return u
	}
	if !enableVT(f) {
		return u
	}
	u.live = tty
	u.file = f
	u.color = os.Getenv("NO_COLOR") == ""
	return u
}

// Plain returns a UI that never decorates (tests, pipes).
func Plain(w io.Writer) *UI { return &UI{w: w} }

// Color reports whether ANSI colour is enabled.
func (u *UI) Color() bool { return u.color }

// Live reports whether lines can be redrawn in place.
func (u *UI) Live() bool { return u.live }

func (u *UI) paint(code, s string) string {
	if !u.color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (u *UI) Bold(s string) string   { return u.paint("1", s) }
func (u *UI) Dim(s string) string    { return u.paint("2", s) }
func (u *UI) Green(s string) string  { return u.paint("32", s) }
func (u *UI) Yellow(s string) string { return u.paint("33", s) }
func (u *UI) Red(s string) string    { return u.paint("31", s) }
func (u *UI) Cyan(s string) string   { return u.paint("36", s) }
func (u *UI) Accent(s string) string { return u.paint("38;5;141", s) }

// Symbols with plain fallbacks.
func (u *UI) OK() string   { return u.pick(u.Green("✓"), "ok") }
func (u *UI) Warn() string { return u.pick(u.Yellow("!"), "warn") }
func (u *UI) Fail() string { return u.pick(u.Red("✗"), "FAIL") }
func (u *UI) Ask() string  { return u.pick(u.Cyan("?"), "?") }
func (u *UI) Arrow() string {
	return u.pick(u.Accent("→"), "->")
}

func (u *UI) pick(fancy, plain string) string {
	if u.color {
		return fancy
	}
	return plain
}

// Printf writes formatted text, clearing any live line first.
func (u *UI) Printf(format string, a ...any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	fmt.Fprintf(u.w, format, a...)
}

var bannerLines = []string{
	"                  _",
	"  _ __ ___   ___ | |_ ___",
	" | '_ ` _ \\ / _ \\| __/ _ \\",
	" | | | | | | (_) | ||  __/",
	" |_| |_| |_|\\___/ \\__\\___|",
}

// Banner returns the mote logo with a tagline. Without colour it is a single
// plain line, so logs stay compact.
func (u *UI) Banner(tagline string) string {
	if !u.color {
		return "mote - " + tagline + "\n"
	}
	shades := []string{"38;5;117", "38;5;117", "38;5;111", "38;5;141", "38;5;177"}
	motes := []string{"        ·", "      ˙    ·", "    ·", "  ˙     ·", "     ·"}
	var b strings.Builder
	b.WriteString("\n")
	width := 0
	for _, l := range bannerLines {
		width = max(width, len(l))
	}
	for i, l := range bannerLines {
		l += strings.Repeat(" ", width-len(l))
		b.WriteString(u.paint("1;"+shades[i], l) + u.paint("38;5;222", motes[i]) + "\n")
	}
	b.WriteString("  " + u.Dim(tagline) + "\n\n")
	return b.String()
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner shows an animated status line until stopped.
type Spinner struct {
	u     *UI
	msg   string
	start time.Time
	stop  chan struct{}
	done  chan struct{}
}

// Spin starts a spinner. On non-live outputs it prints nothing until Stop.
func (u *UI) Spin(msg string) *Spinner {
	s := &Spinner{u: u, msg: msg, start: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	if !u.live {
		close(s.done)
		return s
	}
	go func() {
		defer close(s.done)
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			u.mu.Lock()
			el := fmt.Sprintf("%.1fs", time.Since(s.start).Seconds())
			msg := fit(s.msg, u.Width()-len(el)-4)
			fmt.Fprintf(u.w, "\r\x1b[2K%s %s %s", u.Accent(spinFrames[i%len(spinFrames)]), msg, u.Dim(el))
			u.mu.Unlock()
			select {
			case <-s.stop:
				u.mu.Lock()
				fmt.Fprint(u.w, "\r\x1b[2K")
				u.mu.Unlock()
				return
			case <-t.C:
			}
		}
	}()
	return s
}

// Elapsed is the time since the spinner started.
func (s *Spinner) Elapsed() time.Duration { return time.Since(s.start) }

// Stop ends the spinner and prints final (if not empty) on its own line.
func (s *Spinner) Stop(final string) {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	if final != "" {
		s.u.Printf("%s\n", final)
	}
}

// Bar is a download progress bar.
type Bar struct {
	u     *UI
	label string
	total int64
	done  int64
	start time.Time
	last  time.Time
}

// NewBar starts a progress bar for total bytes, of which done are present.
func (u *UI) NewBar(label string, done, total int64) *Bar {
	return &Bar{u: u, label: label, done: done, total: total, start: time.Now()}
}

// Set updates the byte count and redraws at most ten times a second.
func (b *Bar) Set(done int64) {
	b.done = done
	if !b.u.live || time.Since(b.last) < 100*time.Millisecond {
		return
	}
	b.last = time.Now()
	b.u.Printf("\r\x1b[2K%s", b.line())
}

func (b *Bar) line() string {
	frac := 0.0
	if b.total > 0 {
		frac = float64(b.done) / float64(b.total)
	}
	if frac > 1 {
		frac = 1
	}
	count := fmt.Sprintf("%d/%d MB", b.done>>20, b.total>>20)
	speed := ""
	if secs := time.Since(b.start).Seconds(); secs > 0.3 {
		rate := float64(b.done>>20) / secs
		speed = fmt.Sprintf("  %.0f MB/s", rate)
		if rate > 0 && b.done < b.total {
			speed += fmt.Sprintf("  %ds left", int(float64((b.total-b.done)>>20)/rate))
		}
	}
	// Keep the line narrower than the terminal: a wrapped line cannot be
	// redrawn in place. Shrink the bar first, then the label, then drop speed.
	cols := b.u.Width() - 1
	label := b.label
	width := 28
	used := func() int { return 2 + len([]rune(label)) + 1 + width + 1 + len(count) + len(speed) }
	for used() > cols && width > 10 {
		width--
	}
	if used() > cols {
		label = fit(label, len([]rune(label))-(used()-cols))
	}
	if used() > cols {
		speed = ""
	}
	fill := int(frac * float64(width))
	bar := strings.Repeat("━", fill)
	rest := ""
	if fill < width {
		rest = "╸" + strings.Repeat("━", width-fill-1)
	}
	return fmt.Sprintf("  %s %s%s %s%s", label, b.u.Accent(bar), b.u.Dim(rest), count, b.u.Dim(speed))
}

// Finish prints the final state of the bar.
func (b *Bar) Finish(err error) {
	mark := b.u.OK()
	if err != nil {
		mark = b.u.Fail()
	}
	prefix := ""
	if b.u.live {
		prefix = "\r\x1b[2K"
	}
	b.u.Printf("%s  %s %s %s\n", prefix, mark, b.label, b.u.Dim(fmt.Sprintf("%d MB in %.1fs", b.total>>20, time.Since(b.start).Seconds())))
}
