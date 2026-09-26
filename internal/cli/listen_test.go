package cli

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestNormaliseAndWake(t *testing.T) {
	if got := normalise("  Hey, MOTE!  What's up? "); got != "hey mote what s up" {
		t.Errorf("normalise: %q", got)
	}
	cases := []struct {
		transcript, phrase, want string
		ok                       bool
	}{
		{"Hey, mote! summarise my notes", "hey mote", "summarise my notes", true},
		{"hey mote", "hey mote", "", true},
		{"and then he said hey mote, stop", "hey mote", "stop", true},
		{"nothing to do with it", "hey mote", "", false},
		{"hey mote do it", "", "", false},
		{"HEY   MOTE   do it", "hey mote", "do it", true},
	}
	for _, c := range cases {
		got, ok := wake(c.transcript, c.phrase)
		if got != c.want || ok != c.ok {
			t.Errorf("wake(%q, %q) = %q,%v want %q,%v", c.transcript, c.phrase, got, ok, c.want, c.ok)
		}
	}
}

func TestCaptureArgs(t *testing.T) {
	args, err := captureArgs("", 5, "/tmp/x.wav")
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("Windows needs an explicit device")
		}
		if args, err = captureArgs("Microphone", 5, "/tmp/x.wav"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(args, " "), "audio=Microphone") {
			t.Errorf("dshow device not named: %q", args)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	// The asr models want mono 16 kHz, and the chunk must be time-limited.
	for _, want := range []string{"-ac 1", "-ar 16000", "-t 5", "/tmp/x.wav"} {
		if !strings.Contains(line, want) {
			t.Errorf("ffmpeg args miss %q: %s", want, line)
		}
	}
}

// fakeEars drives the loop without a microphone or a model.
type fakeEars struct {
	heard   []string // one transcript per chunk, in order
	n       int
	acted   []string
	hearErr error
}

func (f *fakeEars) listener(phrase string, once bool) listener {
	return listener{
		phrase: phrase,
		once:   once,
		log:    func(string, ...any) {},
		record: func(context.Context, string) error { return nil },
		hear: func(context.Context, string) (string, error) {
			if f.n >= len(f.heard) {
				return "", errors.New("no more audio")
			}
			s := f.heard[f.n]
			f.n++
			if s == "!err" {
				return "", f.hearErr
			}
			return s, nil
		},
		act: func(_ context.Context, request string) error {
			f.acted = append(f.acted, request)
			return nil
		},
	}
}

func TestListenLoopActsOnlyAfterTheWakeWord(t *testing.T) {
	f := &fakeEars{heard: []string{"just talking", "hey mote what time is it", "more talking"}}
	if err := f.listener("hey mote", true).loop(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(f.acted) != 1 || f.acted[0] != "what time is it" {
		t.Errorf("acted on %q", f.acted)
	}
	if f.n != 2 {
		t.Errorf("stopped after %d chunks, want 2", f.n)
	}
}

func TestListenLoopSurvivesTranscriptionErrors(t *testing.T) {
	f := &fakeEars{heard: []string{"!err", "hey mote carry on"}, hearErr: errors.New("silence")}
	if err := f.listener("hey mote", true).loop(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(f.acted) != 1 || f.acted[0] != "carry on" {
		t.Errorf("acted on %q after an unreadable chunk", f.acted)
	}
}

func TestListenLoopIgnoresAWakeWordWithNoRequest(t *testing.T) {
	f := &fakeEars{heard: []string{"hey mote", "hey mote and now do this"}}
	if err := f.listener("hey mote", true).loop(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(f.acted) != 1 || f.acted[0] != "and now do this" {
		t.Errorf("acted on %q", f.acted)
	}
}

func TestListenLoopStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeEars{heard: []string{"hey mote go"}}
	l := f.listener("hey mote", false)
	l.act = func(context.Context, string) error {
		cancel() // as Ctrl-C would, while the task runs
		return context.Canceled
	}
	if err := l.loop(ctx, t.TempDir()); err != nil {
		t.Errorf("cancellation should end the loop quietly: %v", err)
	}
}

func TestListenChecksItsArgumentsBeforeRecording(t *testing.T) {
	e := newEnv(t)
	e.setup()

	if code, _, errs := e.mote("", "listen", "nosuchtask"); code != ExitUsage || !strings.Contains(errs, "nosuchtask") {
		t.Errorf("unknown task: %d %s", code, errs)
	}
	// describe wants an image first, so spoken text cannot drive it.
	if code, _, errs := e.mote("", "listen", "describe"); code != ExitUsage || !strings.Contains(errs, "spoken text") {
		t.Errorf("wrong first parameter: %d %s", code, errs)
	}
	if code, _, errs := e.mote("", "listen", "--chunk", "nope"); code != ExitUsage || !strings.Contains(errs, "--chunk") {
		t.Errorf("bad chunk: %d %s", code, errs)
	}
	// PATH is empty in tests, so ffmpeg is missing and that is the error.
	if code, _, errs := e.mote("", "listen"); code != ExitMissing || !strings.Contains(errs, "ffmpeg") {
		t.Errorf("missing ffmpeg: %d %s", code, errs)
	}
}
