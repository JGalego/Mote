package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

// Turn is one message of an earlier exchange in a conversation.
type Turn struct {
	Role string // "user" or "assistant"
	Text string
}

// Request is a single generation request. Images and Audio are local paths.
type Request struct {
	System string
	// History is the conversation so far, oldest first, before Prompt.
	History     []Turn
	Prompt      string
	Images      []string
	Audio       []string
	MaxTokens   int
	Temperature float64
	JSONSchema  json.RawMessage
	// OnToken, when set, receives the reply incrementally as it is generated.
	OnToken func(string)
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

// Embedder is implemented by sessions whose model turns text into vectors
// instead of generating tokens. Callers type-assert for it, so backends that
// have no embedding models need not implement it.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Cosine is the similarity between two embeddings: 1 means the same
// direction, 0 unrelated. It is how callers compare what Embed returns.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Backend runs models of one runtime family. New runtimes implement this
// interface and register in Backends.
type Backend interface {
	Open(ctx context.Context, m *registry.Model, files map[string]string) (Session, error)
	Speak(ctx context.Context, m *registry.Model, files map[string]string, text, out string) (Stats, error)
}

// Llama runs GGUF models with llama.cpp's llama-server (chat, vision, audio
// input) and llama-tts (speech output). Inference runs on the CPU unless
// GPU is set.
type Llama struct {
	Dir     string // directory containing the binaries
	Threads int
	LogDir  string
	// StartTimeout bounds model loading; default two minutes.
	StartTimeout time.Duration
	// Repack reorders quantized weights at load for faster CPU kernels.
	// It costs about a second per GB of model and makes prompts read
	// 10-25% faster, which pays off for a server that stays loaded or a
	// long prompt, not for a short one-shot request.
	Repack bool
	// GPU offloads inference to a GPU when Dir's build can: llama.cpp
	// picks whichever device it was built with support for, so this only
	// helps when Dir holds a build with a GPU backend compiled in
	// (Vulkan on Linux and Windows, or the default macOS build, which
	// always includes Metal). It is silently a no-op on a CPU-only build.
	GPU bool
}

// RequiredFlags are llama-server flags mote relies on; `mote doctor` checks
// them so a runtime bump that renames one is caught early.
var RequiredFlags = []string{"--mmproj", "--ctx-size", "--device", "--gpu-layers", "--parallel", "--threads", "--host", "--port", "--fit", "--no-repack"}

func (l *Llama) bin(name string) string { return filepath.Join(l.Dir, exe(name)) }

func (l *Llama) logFile(name string) (*os.File, string) { return logFile(l.LogDir, name) }

// logFile creates name.log in dir, the system temporary directory when dir
// is empty. A log that cannot be created is not an error.
func logFile(dir, name string) (*os.File, string) {
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

func (l *Llama) commonArgs(files map[string]string) []string {
	args := []string{"-m", files["model"]}
	if l.GPU {
		// No --device: llama.cpp picks whichever GPU backend the build
		// has, falling back to the CPU when it has none. 999 offloads
		// every layer the model has; llama.cpp clamps to its actual count.
		args = append(args, "--gpu-layers", "999")
	} else {
		args = append(args, "--device", "none")
	}
	if p, ok := files["mmproj"]; ok {
		args = append(args, "--mmproj", p)
	}
	if l.Threads > 0 {
		args = append(args, "--threads", strconv.Itoa(l.Threads))
	}
	return args
}

// loadArgs skip load work mote does not need. --fit would read the whole
// model once only to size the context and device use, which mote sets
// itself. A model's own registry args come after these, so it can turn
// repacking back on.
func (l *Llama) loadArgs() []string {
	args := []string{"--fit", "off"}
	if !l.Repack {
		args = append(args, "--no-repack")
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
	args := l.commonArgs(files)
	args = append(args, "--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--ctx-size", strconv.Itoa(m.Context), "--parallel", "1")
	args = append(args, l.loadArgs()...)
	args = append(args, m.Args...)

	cmd := exec.Command(l.bin("llama-server"), args...)
	// One log per model: a resident server runs several at once.
	logf, logPath := l.logFile("llama-server-" + m.ID)
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
		if s.healthy(ctx) {
			s.startup = time.Since(start)
			return s, nil
		}
		if time.Now().After(deadline) {
			s.Close()
			return nil, fmt.Errorf("llama-server did not become ready within %s\n%s", timeout, tail(logPath, 15))
		}
	}
}

// URLer is implemented by sessions served over HTTP, so another process
// can pass requests to them.
type URLer interface{ URL() string }

type server struct {
	// name is sent as the request's model, which a server holding several
	// models needs; llama-server ignores it. cmd is nil for a session in
	// another process's server.
	name    string
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

func (s *server) URL() string { return s.base }

// healthy polls /health with a bounded request: llama-server can accept the
// connection but stall before answering (a wedged load, memory pressure),
// and an unbounded call here would defeat both ctx and Open's own deadline,
// which are only checked between calls to healthy, not inside one.
func (s *server) healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := s.client.Do(req)
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
	for _, t := range req.History {
		msgs = append(msgs, map[string]any{"role": t.Role, "content": t.Text})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": parts})
	body := map[string]any{"messages": msgs, "temperature": req.Temperature, "stream": req.OnToken != nil}
	if s.name != "" {
		body["model"] = s.name
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.JSONSchema) == 0 && len(req.Audio) == 0 {
		// DRY penalises repeated token sequences; small models otherwise
		// sometimes loop until the token limit.
		body["dry_multiplier"] = 0.8
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
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return Result{}, fmt.Errorf("llama-server: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if req.OnToken != nil {
		return s.readStream(resp.Body, req.OnToken)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
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

// readStream consumes a server-sent-events chat completion.
// Embed asks llama-server for one vector per text. The model must have been
// started with --embedding, which comes from its registry args.
func (s *server) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body := map[string]any{"input": texts}
	if s.name != "" {
		body["model"] = s.name
	}
	payload, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/v1/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("llama-server: %w\n%s", err, tail(s.logPath, 10))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("llama-server: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("llama-server: unexpected embeddings response: %.200s", raw)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("llama-server returned %d embeddings for %d texts", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("llama-server returned embedding index %d", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

func (s *server) readStream(r io.Reader, onToken func(string)) (Result, error) {
	var res Result
	var text strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Timings *struct {
				PromptN     int     `json:"prompt_n"`
				PromptMS    float64 `json:"prompt_ms"`
				PredictedN  int     `json:"predicted_n"`
				PredictedMS float64 `json:"predicted_ms"`
			} `json:"timings"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Result{}, fmt.Errorf("llama-server: bad stream chunk: %.200s", data)
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				text.WriteString(c.Delta.Content)
				onToken(c.Delta.Content)
			}
		}
		if t := chunk.Timings; t != nil {
			res.PromptTokens, res.PromptMS = t.PromptN, t.PromptMS
			res.OutputTokens, res.GenMS = t.PredictedN, t.PredictedMS
		}
	}
	if err := sc.Err(); err != nil {
		return Result{}, fmt.Errorf("llama-server: %w", err)
	}
	res.Text = Clean(text.String(), s.model.OutputAfter)
	return res, nil
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
	if s.cmd == nil {
		return s.stats
	}
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
	args := append(l.commonArgs(files), "--ctx-size", strconv.Itoa(m.Context), "-p", text, "--output", out)
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
