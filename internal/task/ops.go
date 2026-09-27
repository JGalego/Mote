package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jgalego/mote/internal/runtime"
)

const (
	maxRead     = 1 << 20  // largest text input read into a prompt
	maxFileSize = 64 << 10 // per file when collecting a directory
	maxTree     = 16 << 10 // total when collecting a directory (fits an 8k context)
)

type op struct {
	fn    func(*run, Step) (Value, error)
	model bool     // needs a model capability
	tools []string // local executables it needs
}

// ops is the registry of step implementations. Adding a tool or modality
// means adding an entry here.
var ops = map[string]op{
	"read":     {fn: opRead},
	"generate": {fn: opGenerate, model: true},
	"speak":    {fn: opSpeak, model: true},
	"audio":    {fn: opAudio, tools: []string{"ffmpeg"}},
	"frames":   {fn: opFrames, tools: []string{"ffmpeg", "ffprobe"}},
	"convert":  {fn: opConvert, tools: []string{"ffmpeg"}},
	"collect":  {fn: opCollect},
	"rewrite":  {fn: opRewrite},
	"exec":     {fn: opExec}, // its tool is the step's own cmd[0]
}

func (r *run) input(s Step) (Value, error) {
	v, ok := r.vars[s.From]
	if !ok || v.empty() {
		return Value{}, fmt.Errorf("no input %q", s.From)
	}
	return v, nil
}

func (r *run) tempDir() (string, error) {
	base := r.env.TempDir
	if base == "" {
		base = os.TempDir()
	}
	os.MkdirAll(base, 0o755)
	return os.MkdirTemp(base, "mote-")
}

func readText(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if len(b) > maxRead {
		return "", fmt.Errorf("%s is larger than %d KiB", p, maxRead>>10)
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return "", fmt.Errorf("%s looks like a binary file", p)
	}
	return string(b), nil
}

func opRead(r *run, s Step) (Value, error) {
	in, err := r.input(s)
	if err != nil {
		return Value{}, err
	}
	txt, err := readText(in.Files[0])
	return Value{Text: txt}, err
}

func opGenerate(r *run, s Step) (Value, error) {
	req := runtime.Request{System: r.expand(s.System), Prompt: r.expand(s.Prompt), Temperature: 0.2, MaxTokens: 2048}
	if r.env.Memory != "" {
		req.System = strings.TrimSpace(r.env.Memory + "\n\n" + req.System)
	}
	var images []string
	if s.Images != "" {
		images = r.vars[s.Images].Files
		if len(images) == 0 {
			return Value{}, fmt.Errorf("no images in %q", s.Images)
		}
	}
	if s.Audio != "" {
		req.Audio = r.vars[s.Audio].Files
		if len(req.Audio) == 0 {
			return Value{}, fmt.Errorf("no audio in %q", s.Audio)
		}
		req.Temperature = 0
	}
	wantJSON := s.JSONSchema != ""
	if wantJSON {
		schema := strings.TrimSpace(r.vars[s.JSONSchema].Text)
		if schema == "" {
			schema = `{"type":"object"}`
		}
		if !json.Valid([]byte(schema)) {
			return Value{}, errors.New("schema is not valid JSON")
		}
		req.JSONSchema = json.RawMessage(schema)
		req.Temperature = 0
	}
	m, sess, err := r.session(s.Cap)
	if err != nil {
		return Value{}, err
	}
	stream := r.env.Stream != nil && s.As == "out" && !s.Each
	call := func(req runtime.Request) (string, error) {
		done := r.status("thinking with " + m.ID)
		var sf *streamFilter
		if stream {
			sf = &streamFilter{after: m.OutputAfter, fences: s.Fences == "strip", lang: r.env.Lang, emit: func(t string) {
				if done != nil {
					done(true)
					done = nil
				}
				r.env.Stream(t)
			}}
			req.OnToken = sf.write
		}
		res, err := sess.Generate(r.ctx, req)
		if sf != nil {
			sf.flush()
		}
		if done != nil {
			done(err == nil)
		}
		if stream && err == nil {
			r.streamed = true
		}
		if err != nil {
			return "", err
		}
		r.calls = append(r.calls, Call{Model: m.ID, Cap: s.Cap, Result: res})
		return res.Text, nil
	}

	var text string
	if s.Each && len(images) > 1 {
		var lines []string
		for i, img := range images {
			req.Images = []string{img}
			t, err := call(req)
			if err != nil {
				return Value{}, err
			}
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, strings.ReplaceAll(t, "\n", " ")))
		}
		text = strings.Join(lines, "\n")
	} else {
		req.Images = images
		if text, err = call(req); err != nil {
			return Value{}, err
		}
	}
	if s.Fences == "strip" {
		if s.As == "out" {
			r.lang = FenceLang(text)
		}
		text = StripFences(text)
	}
	if wantJSON {
		var v any
		if err := json.Unmarshal([]byte(StripFences(text)), &v); err != nil {
			return Value{}, fmt.Errorf("model did not return valid JSON: %.200s", text)
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		text = string(b)
	}
	return Value{Text: text}, nil
}

// streamFilter shapes streamed tokens for display: it waits for a model's
// output marker (e.g. ASR "<asr_text>"), drops leading blank space and,
// for code, hides Markdown fence lines. The final value is still computed
// from the full reply, so this only affects what the terminal shows.
type streamFilter struct {
	emit    func(string)
	lang    func(string)
	after   string
	fences  bool
	acc     strings.Builder
	passed  bool
	started bool
	line    strings.Builder
}

func (f *streamFilter) write(tok string) {
	if f.after != "" && !f.passed {
		f.acc.WriteString(tok)
		i := strings.Index(f.acc.String(), f.after)
		if i < 0 {
			return
		}
		f.passed = true
		tok = f.acc.String()[i+len(f.after):]
	}
	if !f.started {
		tok = strings.TrimLeft(tok, " \n")
		if tok == "" {
			return
		}
		f.started = true
	}
	if !f.fences {
		f.emit(tok)
		return
	}
	f.line.WriteString(tok)
	for {
		buf := f.line.String()
		nl := strings.IndexByte(buf, '\n')
		if nl < 0 {
			return
		}
		f.line.Reset()
		f.line.WriteString(buf[nl+1:])
		if line := strings.TrimSpace(buf[:nl]); strings.HasPrefix(line, "```") {
			if f.lang != nil {
				if info := strings.TrimSpace(strings.TrimPrefix(line, "```")); info != "" {
					f.lang(info)
					f.lang = nil // only the opening fence names a language
				}
			}
		} else {
			f.emit(buf[:nl+1])
		}
	}
}

func (f *streamFilter) flush() {
	if rest := f.line.String(); rest != "" && !strings.HasPrefix(strings.TrimSpace(rest), "```") {
		f.emit(rest)
	}
	f.line.Reset()
}

// FenceLang returns the language named by the first Markdown fence
// ("```python"), or "" when the text has no fence or the fence is bare.
func FenceLang(text string) string {
	start := strings.Index(text, "```")
	if start < 0 {
		return ""
	}
	info := text[start+3:]
	if nl := strings.IndexByte(info, '\n'); nl >= 0 {
		info = info[:nl]
	}
	return strings.TrimSpace(info)
}

// StripFences returns the body of the first fenced code block, or the text
// unchanged when there is none.
func StripFences(text string) string {
	start := strings.Index(text, "```")
	if start < 0 {
		return strings.TrimSpace(text)
	}
	body := text[start+3:]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	}
	if end := strings.Index(body, "```"); end >= 0 {
		body = body[:end]
	}
	return strings.TrimRight(body, "\n ") + "\n"
}

func (r *run) outputPath() string {
	if r.opt.Output != "" {
		return r.opt.Output
	}
	return r.task.Output
}

func opSpeak(r *run, s Step) (Value, error) {
	text := strings.TrimSpace(r.expand(s.Prompt))
	if text == "" {
		return Value{}, errors.New("nothing to say")
	}
	m, files, err := r.env.Resolve(r.ctx, s.Cap)
	if err != nil {
		return Value{}, err
	}
	b, err := r.env.Backend(m)
	if err != nil {
		return Value{}, err
	}
	out := r.outputPath()
	if out == "" {
		out = "speech.wav"
	}
	done := r.status("speaking with " + m.ID)
	_, err = b.Speak(r.ctx, m, files, text, out)
	done(err == nil)
	if err != nil {
		return Value{}, err
	}
	return Value{Text: out, Files: []string{out}}, nil
}

func (r *run) tool(name string) (string, error) {
	if r.env.Tool == nil {
		return "", fmt.Errorf("%s is required", name)
	}
	return r.env.Tool(name)
}

func runTool(path string, args ...string) (string, error) {
	out, err := exec.Command(path, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %v: %s", filepath.Base(path), err, strings.TrimSpace(lastLines(string(out), 5)))
	}
	return string(out), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// opAudio passes WAV/MP3 through and converts anything else (other audio
// formats, video) to 16 kHz mono WAV with ffmpeg.
func opAudio(r *run, s Step) (Value, error) {
	in, err := r.input(s)
	if err != nil {
		return Value{}, err
	}
	src := in.Files[0]
	switch strings.ToLower(filepath.Ext(src)) {
	case ".wav", ".mp3":
		return Value{Text: src, Files: []string{src}}, nil
	}
	ffmpeg, err := r.tool("ffmpeg")
	if err != nil {
		return Value{}, err
	}
	dir, err := r.tempDir()
	if err != nil {
		return Value{}, err
	}
	out := filepath.Join(dir, "audio.wav")
	done := r.status("extracting audio with ffmpeg")
	_, err = runTool(ffmpeg, "-v", "error", "-y", "-i", src, "-vn", "-ac", "1", "-ar", "16000", out)
	done(err == nil)
	if err != nil {
		return Value{}, err
	}
	if st, err := os.Stat(out); err != nil || st.Size() <= 44 {
		return Value{}, errors.New("no audio stream")
	}
	return Value{Text: out, Files: []string{out}}, nil
}

func opFrames(r *run, s Step) (Value, error) {
	in, err := r.input(s)
	if err != nil {
		return Value{}, err
	}
	n, err := strconv.Atoi(r.ref(s.Count))
	if err != nil || n < 1 || n > 1000 {
		return Value{}, fmt.Errorf("%w: frame count must be 1-1000", ErrUsage)
	}
	ffprobe, err := r.tool("ffprobe")
	if err != nil {
		return Value{}, err
	}
	ffmpeg, err := r.tool("ffmpeg")
	if err != nil {
		return Value{}, err
	}
	out, err := runTool(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", in.Files[0])
	if err != nil {
		return Value{}, err
	}
	dur, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil || dur <= 0 {
		return Value{}, fmt.Errorf("cannot read video duration of %s", in.Files[0])
	}
	var dir string
	if s.As == "out" {
		dir = r.outputPath()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Value{}, err
		}
	} else if dir, err = r.tempDir(); err != nil {
		return Value{}, err
	}
	rate := strconv.FormatFloat(float64(n)/dur, 'f', 6, 64)
	pattern := filepath.Join(dir, "frame_%03d.png")
	done := r.status(fmt.Sprintf("extracting %d frames with ffmpeg", n))
	_, err = runTool(ffmpeg, "-v", "error", "-y", "-i", in.Files[0], "-vf", "fps="+rate, "-frames:v", strconv.Itoa(n), pattern)
	done(err == nil)
	if err != nil {
		return Value{}, err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "frame_*.png"))
	sort.Strings(files)
	if len(files) == 0 {
		return Value{}, errors.New("no frames extracted")
	}
	return Value{Text: strings.Join(files, "\n"), Files: files}, nil
}

func opConvert(r *run, s Step) (Value, error) {
	in, err := r.input(s)
	if err != nil {
		return Value{}, err
	}
	out := r.outputPath()
	if a, _ := filepath.Abs(out); a != "" {
		if b, _ := filepath.Abs(in.Files[0]); a == b {
			return Value{}, fmt.Errorf("%w: output would overwrite the input", ErrUsage)
		}
	}
	ffmpeg, err := r.tool("ffmpeg")
	if err != nil {
		return Value{}, err
	}
	args := []string{"-v", "error", "-y", "-i", in.Files[0]}
	if w := r.ref(s.Width); w != "" {
		if n, err := strconv.Atoi(w); err != nil || n <= 0 {
			return Value{}, fmt.Errorf("%w: width must be a positive integer", ErrUsage)
		}
		args = append(args, "-vf", "scale="+w+":-2")
	}
	if _, err := runTool(ffmpeg, append(args, out)...); err != nil {
		return Value{}, err
	}
	return Value{Text: out, Files: []string{out}}, nil
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "target": true, "dist": true, "build": true, "__pycache__": true}

// opCollect renders a directory's text files into one prompt-sized string.
// Inside a git work tree it uses `git ls-files` so ignored files are skipped.
func opCollect(r *run, s Step) (Value, error) {
	in, err := r.input(s)
	if err != nil {
		return Value{}, err
	}
	root := in.Files[0]
	var files []string
	if git, err := r.tool("git"); err == nil {
		if out, err := exec.Command(git, "-C", root, "ls-files", "--cached", "--others", "--exclude-standard").Output(); err == nil {
			for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if f != "" {
					files = append(files, filepath.FromSlash(f))
				}
			}
		}
	}
	if files == nil {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && p != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				rel, _ := filepath.Rel(root, p)
				files = append(files, rel)
			}
			return nil
		})
	}
	sort.Strings(files)
	var b strings.Builder
	var skipped int
	for _, f := range files {
		p := filepath.Join(root, f)
		st, err := os.Stat(p)
		if err != nil || st.Size() > maxFileSize {
			skipped++
			continue
		}
		txt, err := readText(p)
		if err != nil {
			skipped++
			continue
		}
		entry := "=== " + filepath.ToSlash(f) + " ===\n" + txt + "\n"
		if b.Len()+len(entry) > maxTree {
			skipped++
			continue
		}
		b.WriteString(entry)
	}
	if b.Len() == 0 {
		return Value{}, fmt.Errorf("no readable text files in %s", root)
	}
	if skipped > 0 {
		r.logf("collect: %d files skipped (binary, too large, or over the %d KiB budget)", skipped, maxTree>>10)
	}
	return Value{Text: b.String()}, nil
}

// ParseFiles splits model output into "=== path ===" blocks. Code fences
// around a block's content are removed.
func ParseFiles(text string) (map[string]string, []string) {
	files := map[string]string{}
	var order []string
	var cur string
	var buf []string
	flush := func() {
		if cur == "" {
			return
		}
		body := strings.Join(buf, "\n")
		if strings.HasPrefix(strings.TrimSpace(body), "```") {
			body = StripFences(body)
		}
		body = strings.TrimRight(body, "\n") + "\n"
		if _, seen := files[cur]; !seen {
			order = append(order, cur)
		}
		files[cur] = body
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "=== ") && strings.HasSuffix(t, " ===") && len(t) > 8 {
			flush()
			cur, buf = strings.TrimSpace(t[4:len(t)-4]), nil
			continue
		}
		if cur != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return files, order
}

// opRewrite turns whole-file rewrites from the model into a unified diff and
// writes the files only when the user passed --apply.
func opRewrite(r *run, s Step) (Value, error) {
	root := r.vars[s.Dir].Text
	files, order := ParseFiles(r.vars[s.From].Text)
	if len(order) == 0 {
		return Value{}, fmt.Errorf("model reply contained no \"=== path ===\" file blocks:\n%.600s", r.vars[s.From].Text)
	}
	var diff strings.Builder
	var changed []string
	content := map[string]string{}
	for _, rel := range order {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
		if filepath.IsAbs(rel) || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, ".git/") {
			return Value{}, fmt.Errorf("model tried to write outside the directory: %s", rel)
		}
		before := ""
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(clean))); err == nil {
			before = strings.ReplaceAll(string(b), "\r\n", "\n")
		}
		if d := Unified(clean, before, files[rel]); d != "" {
			diff.WriteString(d)
			changed = append(changed, clean)
			content[clean] = files[rel]
		}
	}
	if len(changed) == 0 {
		return Value{}, errors.New("model proposed no changes")
	}
	if r.opt.Apply {
		for _, rel := range changed {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return Value{}, err
			}
			if err := os.WriteFile(p, []byte(content[rel]), 0o644); err != nil {
				return Value{}, err
			}
		}
		r.logf("applied changes to %s", strings.Join(changed, ", "))
	}
	return Value{Text: diff.String()}, nil
}
