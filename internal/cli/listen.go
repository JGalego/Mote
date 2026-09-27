package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
)

// Listening is never on by default: nothing records until `mote listen` is
// run. It captures short chunks from the microphone with ffmpeg, transcribes
// each with the asr model already installed, and when the wake word appears
// runs the rest of the sentence as a task. The asr model stays loaded between
// chunks, so only the first one waits for it.

// defaultWake is used when the config says nothing.
const defaultWake = "hey mote"

// normalise lowercases text and reduces anything that is not a letter or
// digit to a single space, so "Hey, mote!" and "hey mote" match. Speech
// recognition punctuates unpredictably, which is exactly what this removes.
func normalise(s string) string {
	var b strings.Builder
	space := true // trims leading space
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// wake reports whether a transcript contains the wake word and returns
// whatever was said after it, which is the request.
func wake(transcript, phrase string) (string, bool) {
	t, p := normalise(transcript), normalise(phrase)
	if p == "" {
		return "", false
	}
	i := strings.Index(t, p)
	if i < 0 {
		return "", false
	}
	return strings.TrimSpace(t[i+len(p):]), true
}

// captureArgs builds the ffmpeg command that records seconds of mono 16 kHz
// audio (what the asr models expect) from device into dst. An empty device
// means the platform's default input, which Windows does not have: there,
// dshow needs the device's name.
func captureArgs(device string, seconds int, dst string) ([]string, error) {
	return captureArgsOn(runtime.GOOS, device, seconds, dst)
}

// captureArgsOn takes the operating system as an argument so each
// platform's recording command can be checked from any of them.
func captureArgsOn(goos, device string, seconds int, dst string) ([]string, error) {
	format := ""
	switch goos {
	case "linux":
		format = "pulse"
		if device == "" {
			device = "default"
		}
	case "darwin":
		format = "avfoundation"
		if device == "" {
			device = ":default"
		}
	case "windows":
		format = "dshow"
		if device == "" {
			return nil, usagef("on Windows, name the microphone with --device, " +
				`e.g. --device "audio=Microphone (Realtek)"; list them with ` +
				`ffmpeg -list_devices true -f dshow -i dummy`)
		}
		if !strings.HasPrefix(device, "audio=") {
			device = "audio=" + device
		}
	default:
		return nil, fmt.Errorf("recording is not supported on %s", goos)
	}
	return []string{
		"-v", "error", "-y", "-f", format, "-i", device,
		"-t", strconv.Itoa(seconds), "-ac", "1", "-ar", "16000", dst,
	}, nil
}

// listener holds what the loop needs, so tests can supply their own
// recording and transcription instead of a microphone and a model.
type listener struct {
	phrase string
	once   bool
	record func(ctx context.Context, dst string) error
	hear   func(ctx context.Context, wav string) (string, error)
	act    func(ctx context.Context, request string) error
	log    func(format string, a ...any)
}

// loop records, transcribes and acts until the context is cancelled, or
// until one request has been handled when once is set.
func (l listener) loop(ctx context.Context, dir string) error {
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return nil // Ctrl-C is how listening ends
		}
		wav := filepath.Join(dir, fmt.Sprintf("chunk%d.wav", i%2))
		if err := l.record(ctx, wav); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		heard, err := l.hear(ctx, wav)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A chunk of silence or noise is not a reason to stop.
			l.log("could not transcribe that chunk: %v", err)
			continue
		}
		request, ok := wake(heard, l.phrase)
		if !ok {
			continue
		}
		if request == "" {
			l.log("heard %q, but nothing followed it", l.phrase)
			continue
		}
		if err := l.act(ctx, request); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if l.once {
			return nil
		}
	}
}

// listen implements `mote listen`.
func (a *app) listen(ctx context.Context, args []string) error {
	vals, pos, err := flags(args,
		[]string{"--wake", "--device", "--chunk", "--model", "--profile"},
		[]string{"--once"})
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("usage: mote listen [TASK] [--wake PHRASE] [--device D] [--chunk SECONDS] [--once]")
	}
	id := "chat"
	if len(pos) == 1 {
		id = pos[0]
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	t, ok := task.Find(tasks, id)
	if !ok {
		return usagef("unknown task %q; see `mote tasks`", id)
	}
	if len(t.Params) == 0 || t.Params[0].Kind != "text" {
		return usagef("%s does not take spoken text as its first argument", id)
	}
	transcribe, ok := task.Find(tasks, "transcribe")
	if !ok {
		return errors.New("the built-in transcribe task is missing")
	}
	seconds := 5
	if v := vals["--chunk"]; v != "" {
		if seconds, err = strconv.Atoi(v); err != nil || seconds < 1 {
			return usagef("--chunk expects whole seconds")
		}
	}
	phrase := firstNonEmpty(vals["--wake"], a.cfg.WakeWord, defaultWake)
	ffmpeg, err := a.tool("ffmpeg")
	if err != nil {
		return err
	}
	capture, err := captureArgs(vals["--device"], seconds, "")
	if err != nil {
		return err
	}
	profile, err := a.selectModels(vals)
	if err != nil {
		return err
	}

	sessions := map[string]mrt.Session{}
	defer task.CloseSessions(sessions)
	dir, err := os.MkdirTemp(a.dataDir(), "listen-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	l := listener{
		phrase: phrase,
		once:   vals["--once"] == "true",
		log:    func(f string, v ...any) { fmt.Fprintf(a.err, a.ue.Dim(f)+"\n", v...) },
		record: func(ctx context.Context, dst string) error {
			args, err := captureArgs(vals["--device"], seconds, dst)
			if err != nil {
				return err
			}
			out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
			if err != nil && ctx.Err() == nil {
				return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
			}
			return err
		},
		hear: func(ctx context.Context, wav string) (string, error) {
			res, err := transcribe.Run(ctx, a.env(profile, sessions), []string{wav}, task.Options{})
			if err != nil {
				return "", err
			}
			return res.Text, nil
		},
		act: func(ctx context.Context, request string) error {
			fmt.Fprintf(a.err, "%s %s\n", a.ue.Arrow(), a.ue.Bold(request))
			env := a.env(profile, sessions)
			if env.Memory, err = a.memoryFor(ctx, request, nil, profile, sessions); err != nil {
				return err
			}
			res, err := t.Run(ctx, env, []string{request}, task.Options{})
			if err != nil {
				return err
			}
			a.record(t, request, res)
			return a.emit(t, res, "", nil)
		},
	}
	fmt.Fprintf(a.err, "%s listening for %q; Ctrl-C to stop (%s)\n",
		a.ue.OK(), phrase, a.ue.Dim(strings.Join(capture[:6], " ")))
	return l.loop(ctx, dir)
}
