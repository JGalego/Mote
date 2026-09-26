package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jgalego/mote/registry"
)

// Request is a single generation request. Images and Audio are local paths.
type Request struct {
	System      string
	Prompt      string
	Images      []string
	Audio       []string
	MaxTokens   int
	Temperature float64
	JSONSchema  json.RawMessage
}

// Result is the model output plus timing reported by the runtime.
type Result struct {
	Text         string
	PromptTokens int
	OutputTokens int
	PromptMS     float64
	GenMS        float64
}

// Stats describes a finished session.
type Stats struct {
	StartupMS float64
	PeakRSSMB int
}

// Session is a loaded model that can serve several requests.
type Session interface {
	Generate(ctx context.Context, req Request) (Result, error)
	Close() Stats
}

// Backend runs models of one runtime family. New runtimes implement this
// interface and register in Backends.
type Backend interface {
	Open(ctx context.Context, m *registry.Model, files map[string]string) (Session, error)
	Speak(ctx context.Context, m *registry.Model, files map[string]string, text, out string) (Stats, error)
}

// Llama runs GGUF models with llama.cpp's llama-server (chat, vision, audio
// input) and llama-tts (speech output). Inference is pinned to the CPU.
type Llama struct {
	Dir     string // directory containing the binaries
	Threads int
	LogDir  string
	// StartTimeout bounds model loading; default two minutes.
	StartTimeout time.Duration
}

// RequiredFlags are llama-server flags mote relies on; `mote doctor` checks
// them so a runtime bump that renames one is caught early.
var RequiredFlags = []string{"--mmproj", "--ctx-size", "--device", "--parallel", "--threads", "--host", "--port"}

func (l *Llama) bin(name string) string { return filepath.Join(l.Dir, exe(name)) }

func (l *Llama) logFile(name string) (*os.File, string) {
	dir := l.LogDir
	if dir == "" {
		dir = os.TempDir()
	}
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, name+".log")
	f, err := os.Create(p)
	if err != nil {
		return nil, ""
	}
	return f, p
}

func (l *Llama) commonArgs(m *registry.Model, files map[string]string) []string {
	args := []string{"-m", files["model"], "--device", "none"}
	if p, ok := files["mmproj"]; ok {
		args = append(args, "--mmproj", p)
	}
	if l.Threads > 0 {
		args = append(args, "--threads", strconv.Itoa(l.Threads))
	}
	return args
}

// Open starts llama-server on a free loopback port and waits until the model
// is loaded.
func (l *Llama) Open(ctx context.Context, m *registry.Model, files map[string]string) (Session, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	args := l.commonArgs(m, files)
	args = append(args, "--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--ctx-size", strconv.Itoa(m.Context), "--parallel", "1")
	args = append(args, m.Args...)

	cmd := exec.Command(l.bin("llama-server"), args...)
	logf, logPath := l.logFile("llama-server")
	if logf != nil {
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		if logf != nil {
			logf.Close()
		}
		return nil, fmt.Errorf("start llama-server: %w", err)
	}
	s := &server{cmd: cmd, log: logf, logPath: logPath, model: m,
		base:   fmt.Sprintf("http://127.0.0.1:%d", port),
		client: &http.Client{Transport: &http.Transport{Proxy: nil}},
		exited: make(chan struct{})}
	go func() { s.waitErr = cmd.Wait(); close(s.exited) }()

	timeout := l.StartTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-s.exited:
			s.Close()
			return nil, fmt.Errorf("llama-server exited while loading %s: %v\n%s", m.ID, s.waitErr, tail(logPath, 15))
		case <-ctx.Done():
			s.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if s.healthy() {
			s.startup = time.Since(start)
			return s, nil
		}
		if time.Now().After(deadline) {
			s.Close()
			return nil, fmt.Errorf("llama-server did not become ready within %s\n%s", timeout, tail(logPath, 15))
		}
	}
}

type server struct {
	cmd     *exec.Cmd
	log     *os.File
	logPath string
	model   *registry.Model
	base    string
	client  *http.Client
	startup time.Duration
	exited  chan struct{}
	waitErr error
	closed  bool
	stats   Stats
}

func (s *server) healthy() bool {
	resp, err := s.client.Get(s.base + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type chatPart struct {
	Type       string            `json:"type"`
	Text       string            `json:"text,omitempty"`
	ImageURL   map[string]string `json:"image_url,omitempty"`
	InputAudio map[string]string `json:"input_audio,omitempty"`
}

func (s *server) Generate(ctx context.Context, req Request) (Result, error) {
	var parts []chatPart
	for _, p := range req.Images {
		b, err := os.ReadFile(p)
		if err != nil {
			return Result{}, err
		}
		parts = append(parts, chatPart{Type: "image_url", ImageURL: map[string]string{
			"url": "data:" + imageMIME(p) + ";base64," + base64.StdEncoding.EncodeToString(b)}})
	}
	for _, p := range req.Audio {
		b, err := os.ReadFile(p)
		if err != nil {
			return Result{}, err
		}
		format := strings.TrimPrefix(strings.ToLower(filepath.Ext(p)), ".")
		parts = append(parts, chatPart{Type: "input_audio", InputAudio: map[string]string{
			"data": base64.StdEncoding.EncodeToString(b), "format": format}})
	}
	if req.Prompt != "" {
		parts = append(parts, chatPart{Type: "text", Text: req.Prompt})
	}
	var msgs []map[string]any
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": parts})
	body := map[string]any{"messages": msgs, "temperature": req.Temperature, "stream": false}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.JSONSchema) > 0 {
		body["response_format"] = map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"name": "output", "schema": req.JSONSchema}}
	}
	payload, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(hreq)
	if err != nil {
		return Result{}, fmt.Errorf("llama-server: %w\n%s", err, tail(s.logPath, 10))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("llama-server: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Timings struct {
			PromptN     int     `json:"prompt_n"`
			PromptMS    float64 `json:"prompt_ms"`
			PredictedN  int     `json:"predicted_n"`
			PredictedMS float64 `json:"predicted_ms"`
		} `json:"timings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return Result{}, fmt.Errorf("llama-server: unexpected response: %.200s", raw)
	}
	return Result{
		Text:         Clean(out.Choices[0].Message.Content, s.model.OutputAfter),
		PromptTokens: out.Timings.PromptN, OutputTokens: out.Timings.PredictedN,
		PromptMS: out.Timings.PromptMS, GenMS: out.Timings.PredictedMS,
	}, nil
}

// Clean strips reasoning blocks and model-specific prefixes from output.
func Clean(text, after string) string {
	for {
		i := strings.Index(text, "<think>")
		j := strings.Index(text, "</think>")
		if i < 0 || j < i {
			break
		}
		text = text[:i] + text[j+len("</think>"):]
	}
	if after != "" {
		if i := strings.LastIndex(text, after); i >= 0 {
			text = text[i+len(after):]
		}
	}
	return strings.TrimSpace(text)
}

func (s *server) Close() Stats {
	if s.closed {
		return s.stats
	}
	s.closed = true
	s.stats.StartupMS = float64(s.startup.Microseconds()) / 1000
	s.stats.PeakRSSMB = peakRSSMB(s.cmd.Process.Pid)
	stop(s.cmd.Process)
	select {
	case <-s.exited:
	case <-time.After(5 * time.Second):
		s.cmd.Process.Kill()
		<-s.exited
	}
	if s.stats.PeakRSSMB == 0 {
		s.stats.PeakRSSMB = exitRSSMB(s.cmd.ProcessState)
	}
	if s.log != nil {
		s.log.Close()
	}
	return s.stats
}

// Speak synthesizes text to a WAV file with llama-tts.
func (l *Llama) Speak(ctx context.Context, m *registry.Model, files map[string]string, text, out string) (Stats, error) {
	if _, err := os.Stat(l.bin("llama-tts")); err != nil {
		return Stats{}, fmt.Errorf("llama-tts not found in %s", l.Dir)
	}
	args := append(l.commonArgs(m, files), "-p", text, "--output", out)
	args = append(args, m.Args...)
	cmd := exec.CommandContext(ctx, l.bin("llama-tts"), args...)
	logf, logPath := l.logFile("llama-tts")
	if logf != nil {
		cmd.Stdout, cmd.Stderr = logf, logf
		defer logf.Close()
	}
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return Stats{}, fmt.Errorf("llama-tts: %w\n%s", err, tail(logPath, 15))
	}
	return Stats{StartupMS: float64(time.Since(start).Milliseconds()), PeakRSSMB: exitRSSMB(cmd.ProcessState)}, nil
}

// CheckFlags runs `llama-server --help` and reports required flags that are
// missing from this runtime build.
func (l *Llama) CheckFlags() ([]string, error) {
	out, err := exec.Command(l.bin("llama-server"), "--help").CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("llama-server --help: %w", err)
	}
	var missing []string
	for _, f := range RequiredFlags {
		if !bytes.Contains(out, []byte(f)) {
			missing = append(missing, f)
		}
	}
	return missing, nil
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func imageMIME(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	}
	return "image/png"
}

func tail(p string, n int) string {
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "  " + strings.Join(lines, "\n  ") + "\n  (full log: " + p + ")"
}

// ErrUnsupported is returned for backends that are not available.
var ErrUnsupported = errors.New("unsupported backend")
