// Package bench measures installed models on this machine: startup time,
// generation throughput, peak memory and pass/fail on small task checks.
// Results are local measurements; they are stored in the user's data
// directory and never written into the shared registry.
package bench

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

//go:embed cases.json
var casesJSON []byte

type Case struct {
	ID     string          `json:"id"`
	Cap    string          `json:"cap"`
	Full   bool            `json:"full,omitempty"`
	Prompt string          `json:"prompt,omitempty"`
	Image  string          `json:"image,omitempty"`  // solid color: red, green, blue
	Speech string          `json:"speech,omitempty"` // synthesized with a tts model, then transcribed
	Schema json.RawMessage `json:"schema,omitempty"`
	Expect []string        `json:"expect,omitempty"`
	Regex  string          `json:"regex,omitempty"`
	// Embedding cases have no reply to check: the model passes when Prompt
	// lands closer to Similar than to Different.
	Similar   string `json:"similar,omitempty"`
	Different string `json:"different,omitempty"`
}

// Cases returns the built-in benchmark cases.
func Cases() ([]Case, error) {
	var doc struct {
		Cases []Case `json:"cases"`
	}
	if err := json.Unmarshal(casesJSON, &doc); err != nil {
		return nil, err
	}
	for _, c := range doc.Cases {
		if _, ok := registry.Capabilities[c.Cap]; !ok {
			return nil, fmt.Errorf("case %s: unknown capability %q", c.ID, c.Cap)
		}
		if c.Regex != "" {
			if _, err := regexp.Compile(c.Regex); err != nil {
				return nil, fmt.Errorf("case %s: %w", c.ID, err)
			}
		}
	}
	return doc.Cases, nil
}

// Entry is the local measurement of one model.
type Entry struct {
	Model        string    `json:"model"`
	Kind         string    `json:"kind"`
	Time         time.Time `json:"time"`
	Runtime      string    `json:"runtime"`
	Mode         string    `json:"mode"`
	StartupMS    float64   `json:"startup_ms"`
	PeakRSSMB    int       `json:"peak_rss_mb"`
	TokensPerSec float64   `json:"tokens_per_sec"`
	PromptTPS    float64   `json:"prompt_tokens_per_sec"`
	LatencyMS    float64   `json:"latency_ms"`
	Cases        int       `json:"cases"`
	Passed       int       `json:"passed"`
	Failures     []string  `json:"failures,omitempty"`
	Skipped      []string  `json:"skipped,omitempty"`
}

// Options select what to run.
type Options struct {
	Full    bool
	Models  []*registry.Model // models to measure (must be installed)
	Files   func(m *registry.Model) map[string]string
	Backend func(m *registry.Model) (runtime.Backend, error)
	Runtime string
	TempDir string
	Log     io.Writer
}

// Run measures each model on the cases for its capabilities.
func Run(ctx context.Context, opt Options) ([]Entry, error) {
	cases, err := Cases()
	if err != nil {
		return nil, err
	}
	mode := "quick"
	if opt.Full {
		mode = "full"
	}
	tmp, err := os.MkdirTemp(opt.TempDir, "mote-bench-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	var tts *registry.Model
	for _, m := range opt.Models {
		if m.Has("tts") {
			tts = m
		}
	}
	var out []Entry
	for _, m := range opt.Models {
		var mine []Case
		seen := map[string]bool{}
		for _, c := range cases {
			if !m.Has(c.Cap) || (c.Full && !opt.Full) {
				continue
			}
			if !opt.Full && seen[c.Cap] {
				continue
			}
			seen[c.Cap] = true
			mine = append(mine, c)
		}
		if len(mine) == 0 {
			continue
		}
		logf(opt.Log, "bench %s (%d cases)", m.ID, len(mine))
		e, err := runModel(ctx, opt, m, mine, tts, tmp)
		if err != nil {
			return out, fmt.Errorf("%s: %w", m.ID, err)
		}
		e.Mode, e.Runtime, e.Kind, e.Time = mode, opt.Runtime, registry.KindLocal, time.Now().UTC()
		out = append(out, e)
	}
	return out, nil
}

func runModel(ctx context.Context, opt Options, m *registry.Model, cases []Case, tts *registry.Model, tmp string) (Entry, error) {
	e := Entry{Model: m.ID}
	b, err := opt.Backend(m)
	if err != nil {
		return e, err
	}
	files := opt.Files(m)
	if m.Has("image") {
		return drawCases(ctx, b, m, files, cases, tmp)
	}
	if m.Has("tts") {
		for _, c := range cases {
			wav := filepath.Join(tmp, c.ID+".wav")
			start := time.Now()
			st, err := b.Speak(ctx, m, files, c.Prompt, wav)
			e.LatencyMS += float64(time.Since(start).Milliseconds())
			e.Cases++
			if fi, serr := os.Stat(wav); err == nil && serr == nil && fi.Size() > 44 {
				e.Passed++
			} else {
				e.Failures = append(e.Failures, fmt.Sprintf("%s: %v", c.ID, err))
			}
			if st.PeakRSSMB > e.PeakRSSMB {
				e.PeakRSSMB = st.PeakRSSMB
			}
		}
		e.LatencyMS /= float64(e.Cases)
		return e, nil
	}

	sess, err := b.Open(ctx, m, files)
	if err != nil {
		return e, err
	}
	var genTok, promptTok int
	var genMS, promptMS, total float64
	for _, c := range cases {
		if c.Similar != "" {
			start := time.Now()
			ok, why, err := embedCase(ctx, sess, c)
			total += float64(time.Since(start).Microseconds()) / 1000
			e.Cases++
			switch {
			case err != nil:
				e.Failures = append(e.Failures, fmt.Sprintf("%s: %v", c.ID, err))
			case ok:
				e.Passed++
			default:
				e.Failures = append(e.Failures, c.ID+": "+why)
			}
			continue
		}
		req := runtime.Request{Prompt: c.Prompt, JSONSchema: c.Schema, MaxTokens: 256}
		if c.Image != "" {
			p := filepath.Join(tmp, c.ID+".png")
			if err := solidPNG(p, c.Image); err != nil {
				sess.Close()
				return e, err
			}
			req.Images = []string{p}
		}
		if c.Speech != "" {
			if tts == nil {
				e.Skipped = append(e.Skipped, c.ID+": needs an installed tts model to synthesize input")
				continue
			}
			wav := filepath.Join(tmp, c.ID+".wav")
			tb, err := opt.Backend(tts)
			if err == nil {
				_, err = tb.Speak(ctx, tts, opt.Files(tts), c.Speech, wav)
			}
			if err != nil {
				e.Skipped = append(e.Skipped, c.ID+": speech synthesis failed: "+err.Error())
				continue
			}
			req.Audio, req.Prompt = []string{wav}, ""
		}
		start := time.Now()
		res, err := sess.Generate(ctx, req)
		total += float64(time.Since(start).Microseconds()) / 1000
		e.Cases++
		if err != nil {
			e.Failures = append(e.Failures, fmt.Sprintf("%s: %v", c.ID, err))
			continue
		}
		genTok += res.OutputTokens
		genMS += res.GenMS
		promptTok += res.PromptTokens
		promptMS += res.PromptMS
		if ok, why := Check(c, res.Text); ok {
			e.Passed++
		} else {
			e.Failures = append(e.Failures, c.ID+": "+why)
		}
	}
	st := sess.Close()
	e.StartupMS, e.PeakRSSMB = st.StartupMS, st.PeakRSSMB
	if e.Cases > 0 {
		e.LatencyMS = total / float64(e.Cases)
	}
	if genMS > 0 {
		e.TokensPerSec = float64(genTok) / genMS * 1000
	}
	if promptMS > 0 {
		e.PromptTPS = float64(promptTok) / promptMS * 1000
	}
	return e, nil
}

// benchSide is the image size benchmarks draw at: large enough to judge,
// small enough to finish in a few minutes on a laptop CPU.
const benchSide = 256

// drawCases runs image cases. A picture passes when it is a PNG of the
// requested size that is not one flat colour; judging what it shows would
// need another model.
func drawCases(ctx context.Context, b runtime.Backend, m *registry.Model, files map[string]string, cases []Case, tmp string) (Entry, error) {
	e := Entry{Model: m.ID}
	d, ok := b.(runtime.Drawer)
	if !ok {
		return e, fmt.Errorf("%s cannot generate images", m.ID)
	}
	for _, c := range cases {
		out := filepath.Join(tmp, c.ID+".png")
		start := time.Now()
		st, err := d.Draw(ctx, m, files, runtime.ImageRequest{Prompt: c.Prompt, Width: benchSide, Height: benchSide, Seed: 42, Out: out})
		e.LatencyMS += float64(time.Since(start).Milliseconds())
		e.Cases++
		if err == nil {
			err = checkImage(out, benchSide, benchSide)
		}
		if err != nil {
			e.Failures = append(e.Failures, fmt.Sprintf("%s: %v", c.ID, err))
		} else {
			e.Passed++
		}
		if st.PeakRSSMB > e.PeakRSSMB {
			e.PeakRSSMB = st.PeakRSSMB
		}
	}
	if e.Cases > 0 {
		e.LatencyMS /= float64(e.Cases)
	}
	return e, nil
}

// checkImage verifies a generated picture's format, size and that it has
// more than one colour.
func checkImage(p string, w, h int) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return fmt.Errorf("not a PNG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != w || bounds.Dy() != h {
		return fmt.Errorf("size %dx%d, want %dx%d", bounds.Dx(), bounds.Dy(), w, h)
	}
	first := img.At(bounds.Min.X, bounds.Min.Y)
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 4 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 4 {
			if img.At(x, y) != first {
				return nil
			}
		}
	}
	return errors.New("the image is one flat colour")
}

// embedCase checks that an embedding model places Prompt nearer to Similar
// than to Different, which is all a router needs of it. There are no tokens
// to count, so only latency and memory are measured.
func embedCase(ctx context.Context, sess runtime.Session, c Case) (bool, string, error) {
	em, ok := sess.(runtime.Embedder)
	if !ok {
		return false, "", fmt.Errorf("model cannot produce embeddings")
	}
	v, err := em.Embed(ctx, []string{c.Prompt, c.Similar, c.Different})
	if err != nil {
		return false, "", err
	}
	near, far := runtime.Cosine(v[0], v[1]), runtime.Cosine(v[0], v[2])
	if near > far {
		return true, "", nil
	}
	return false, fmt.Sprintf("closer to %q (%.3f) than to %q (%.3f)", c.Different, far, c.Similar, near), nil
}

// Check scores a model reply against a case.
func Check(c Case, text string) (bool, string) {
	low := strings.ToLower(text)
	for _, want := range c.Expect {
		if !strings.Contains(low, strings.ToLower(want)) {
			return false, fmt.Sprintf("missing %q in %.80q", want, text)
		}
	}
	if c.Regex != "" && !regexp.MustCompile(c.Regex).MatchString(text) {
		return false, fmt.Sprintf("no match for /%s/ in %.80q", c.Regex, text)
	}
	if len(c.Schema) > 0 && !json.Valid([]byte(strings.TrimSpace(text))) {
		return false, "invalid JSON"
	}
	return true, ""
}

func solidPNG(p, name string) error {
	colors := map[string]color.RGBA{"red": {255, 0, 0, 255}, "green": {0, 170, 0, 255}, "blue": {0, 0, 255, 255}}
	c, ok := colors[name]
	if !ok {
		return fmt.Errorf("unknown image color %q", name)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(p, buf.Bytes(), 0o644)
}

func logf(w io.Writer, format string, a ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", a...)
	}
}

// Results is the stored set of latest local measurements.
type Results struct {
	Machine string           `json:"machine"`
	Entries map[string]Entry `json:"entries"`
}

func resultsPath(dir string) string { return filepath.Join(dir, "bench", "results.json") }

// Load reads stored results; a missing file yields empty results.
func Load(dataDir string) (Results, error) {
	r := Results{Entries: map[string]Entry{}}
	b, err := os.ReadFile(resultsPath(dataDir))
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", resultsPath(dataDir), err)
	}
	if r.Entries == nil {
		r.Entries = map[string]Entry{}
	}
	return r, nil
}

// Save merges entries into the stored results and appends them to the
// history log.
func Save(dataDir, machine string, entries []Entry) error {
	r, err := Load(dataDir)
	if err != nil {
		return err
	}
	r.Machine = machine
	for _, e := range entries {
		r.Entries[e.Model] = e
	}
	if err := os.MkdirAll(filepath.Dir(resultsPath(dataDir)), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(resultsPath(dataDir), append(b, '\n'), 0o644); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "bench", "history.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range entries {
		line, _ := json.Marshal(e)
		f.Write(append(line, '\n'))
	}
	return nil
}

// Measured converts results into selection input.
func (r Results) Measured() map[string]registry.Measured {
	out := map[string]registry.Measured{}
	for id, e := range r.Entries {
		out[id] = registry.Measured{PeakRSSMB: e.PeakRSSMB, TokensPerSec: e.TokensPerSec, Cases: e.Cases, Passed: e.Passed}
	}
	return out
}

// Sorted returns entries ordered by model id.
func (r Results) Sorted() []Entry {
	var out []Entry
	for _, e := range r.Entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}
