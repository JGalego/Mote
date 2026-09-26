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
			reply = `{"ok": true}`
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
