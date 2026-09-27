// Package fakellama lets tests stand in for llama-server and llama-tts by
// re-executing the test binary. Call MaybeRun at the top of TestMain and
// Install to place fake binaries in a directory.
package fakellama

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const envRole = "MOTE_FAKE_LLAMA_ROLE"

// MaybeRun turns the process into a fake runtime binary when the fake role is
// set in the environment, and exits.
func MaybeRun() {
	role := os.Getenv(envRole)
	if role == "" {
		base := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
		if base == "llama-server" || base == "llama-tts" {
			role = base
		}
	}
	switch role {
	case "llama-server":
		os.Exit(server(os.Args[1:]))
	case "llama-tts":
		os.Exit(tts(os.Args[1:]))
	}
}

// Install copies the running test binary into dir as llama-server and
// llama-tts.
func Install(t testing.TB, dir string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"llama-server", "llama-tts"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func flag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func server(args []string) int {
	if len(args) > 0 && args[0] == "--help" {
		fmt.Println("-m --mmproj --ctx-size --device --parallel --threads --host --port")
		return 0
	}
	if _, err := os.Stat(flag(args, "-m")); err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to load model:", err)
		return 1
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+flag(args, "--port"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"ok"}`) })
	// Embeddings: a deterministic bag-of-words vector, so similarity
	// between texts sharing words is real without loading a model.
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		data := make([]any, len(req.Input))
		for i, text := range req.Input {
			vec := make([]float64, 64)
			for _, word := range strings.Fields(strings.ToLower(text)) {
				h := 0
				for _, c := range word {
					h = (h*31 + int(c)) % len(vec)
				}
				vec[h]++
			}
			data[i] = map[string]any{"index": i, "embedding": vec, "object": "embedding"}
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			ResponseFormat json.RawMessage `json:"response_format"`
			Stream         bool            `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		last := req.Messages[len(req.Messages)-1]
		json.Unmarshal(last.Content, &parts)
		var text, kinds []string
		for _, p := range parts {
			if p.Type == "text" {
				text = append(text, p.Text)
			} else {
				kinds = append(kinds, p.Type)
			}
		}
		reply := "echo: " + strings.Join(text, " ")
		if len(kinds) > 0 {
			reply += " [" + strings.Join(kinds, ",") + "]"
		}
		if strings.Contains(strings.Join(text, " "), "Project files:") {
			reply = "=== hello.txt ===\nhello, world\n"
		}
		if len(req.ResponseFormat) > 0 {
			reply = schemaReply(req.ResponseFormat, strings.Join(text, " "))
			if r, ok := scripted(); ok {
				reply = r
			}
		}
		logRequest(req.Messages, strings.Join(text, " "), req.ResponseFormat)
		// MOTE_FAKE_REPLY lets a test force an awkward answer, such as a
		// router reply that is not the JSON the caller expects.
		if v := os.Getenv("MOTE_FAKE_REPLY"); v != "" {
			reply = v
		}
		if os.Getenv("MOTE_FAKE_ASR") != "" {
			reply = "language English<asr_text>" + reply
		}
		timings := map[string]any{"prompt_n": 10, "prompt_ms": 5.0, "predicted_n": 20, "predicted_ms": 100.0}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, word := range strings.SplitAfter(reply, " ") {
				b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": word}}}})
				fmt.Fprintf(w, "data: %s\n\n", b)
			}
			b, _ := json.Marshal(map[string]any{"choices": []any{}, "timings": timings})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}}},
			"timings": map[string]any{"prompt_n": 10, "prompt_ms": 5.0, "predicted_n": 20, "predicted_ms": 100.0},
		})
	})
	http.Serve(ln, mux)
	return 0
}

// MOTE_FAKE_SCRIPT names a file of replies, one per line, given in turn to
// schema-constrained requests: a planner's plan, or an agent's steps. The
// last line repeats once the script runs out. The count lives in the
// server, which serves one mote process.
var (
	scriptMu   sync.Mutex
	scriptNext int
)

func scripted() (string, bool) {
	path := os.Getenv("MOTE_FAKE_SCRIPT")
	if path == "" {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "", false
	}
	scriptMu.Lock()
	defer scriptMu.Unlock()
	i := scriptNext
	if i >= len(lines) {
		i = len(lines) - 1
	}
	scriptNext++
	return lines[i], true
}

// MOTE_FAKE_LOG names a file that gets one JSON line per chat request, so a
// test can check what a model was shown.
func logRequest(msgs []struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}, prompt string, format json.RawMessage) {
	path := os.Getenv("MOTE_FAKE_LOG")
	if path == "" {
		return
	}
	var system string
	if len(msgs) > 1 && msgs[0].Role == "system" {
		json.Unmarshal(msgs[0].Content, &system)
	}
	entry := map[string]any{"system": system, "prompt": prompt}
	if len(format) > 0 {
		entry["format"] = format
	}
	b, _ := json.Marshal(entry)
	scriptMu.Lock()
	defer scriptMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// schemaReply answers a schema-constrained request. Properties with an enum
// are answered with whichever value the prompt mentions, falling back to the
// first: enough for routing, where the reply must be one of the task ids.
func schemaReply(format json.RawMessage, prompt string) string {
	var f struct {
		JSONSchema struct {
			Schema struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(format, &f); err != nil {
		return `{"ok": true}`
	}
	out := map[string]any{}
	for name, p := range f.JSONSchema.Schema.Properties {
		if len(p.Enum) == 0 {
			continue
		}
		pick := p.Enum[0]
		for _, v := range p.Enum {
			if strings.Contains(prompt, v) {
				pick = v
				break
			}
		}
		out[name] = pick
	}
	if len(out) == 0 {
		return `{"ok": true}`
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func tts(args []string) int {
	out := flag(args, "--output")
	if out == "" {
		return 2
	}
	// A valid 44-byte WAV header with no samples.
	hdr := []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\xc0\x5d\x00\x00\x80\xbb\x00\x00\x02\x00\x10\x00data\x00\x00\x00\x00")
	if err := os.WriteFile(out, hdr, 0o644); err != nil {
		return 1
	}
	return 0
}
