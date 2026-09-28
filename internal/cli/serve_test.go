package cli

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

// state reads serve.json from the test's data directory.
func (e *env) state() (serveState, bool) {
	var st serveState
	b, err := os.ReadFile(filepath.Join(e.home, "serve.json"))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return st, false
	}
	return st, true
}

func (e *env) waitState(t *testing.T, want func(serveState) bool) serveState {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := e.state(); ok && want(st) {
			if _, alive := health(st.URL); alive {
				return st
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("mote serve did not come up")
	return serveState{}
}

func loaded(t *testing.T, url string) []string {
	t.Helper()
	resp, err := http.Get(url + "/mote/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s struct {
		Loaded []struct {
			Model string `json:"model"`
		} `json:"loaded"`
	}
	json.NewDecoder(resp.Body).Decode(&s)
	var ids []string
	for _, l := range s.Loaded {
		ids = append(ids, l.Model)
	}
	return ids
}

func TestCommandsUseARunningServer(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	port := freeLoopbackPort(t)
	done := make(chan string)
	go func() {
		_, out, errs := e.mote("", "serve", "--port", port, "--keep-alive", "1m")
		done <- out + errs
	}()
	st := e.waitState(t, func(s serveState) bool { return !s.Background })
	if st.URL != "http://127.0.0.1:"+port {
		t.Errorf("url %s", st.URL)
	}

	// keep_alive is 0 in tests, so the command starts nothing, but it uses
	// the server that is running.
	code, out, errs := e.mote("", "run", "chat", "hello")
	if code != 0 || strings.TrimSpace(out) != "echo: hello" {
		t.Fatalf("chat: %d %q %s", code, out, errs)
	}
	if ids := loaded(t, st.URL); len(ids) != 1 || ids[0] != "qwen3.5-0.8b" {
		t.Errorf("loaded %v", ids)
	}
	// Streaming, as in a terminal, and a second command, reusing the model.
	if code, out, _ := e.mote("", "pipe", "chat hi | chat 'again: {}'"); code != 0 || !strings.Contains(out, "echo: again: echo: hi") {
		t.Errorf("pipe: %d %q", code, out)
	}

	// Editors reach the same models through the OpenAI API.
	resp, err := http.Post(st.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"text","messages":[{"role":"user","content":[{"type":"text","text":"from an editor"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "echo: from an editor") {
		t.Errorf("api: %s", b)
	}
	resp, _ = http.Get(st.URL + "/v1/models")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{`"id":"mote"`, `"id":"text"`, `"id":"qwen3.5-0.8b"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("models lack %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), `"id":"tts"`) {
		t.Errorf("offers tts, which it cannot serve: %s", b)
	}

	_, out, _ = e.mote("", "serve", "status")
	if !strings.Contains(out, "qwen3.5-0.8b") || !strings.Contains(out, "started by hand") {
		t.Errorf("status: %s", out)
	}
	if code, out, _ := e.mote("", "serve", "stop"); code != 0 || !strings.Contains(out, "stopped") {
		t.Errorf("stop: %d %s", code, out)
	}
	select {
	case out := <-done:
		if !strings.Contains(out, "OpenAI-compatible API") {
			t.Errorf("serve said: %s", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after stop")
	}
	if _, ok := e.state(); ok {
		t.Error("serve.json left behind")
	}
	if _, out, _ := e.mote("", "serve", "status"); !strings.Contains(out, "not running") {
		t.Errorf("status after stop: %s", out)
	}
}

func TestACommandStartsTheServerAndLaterOnesReuseIt(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	t.Setenv("MOTE_TEST_AS_MOTE", "1")
	t.Setenv("MOTE_KEEP_ALIVE", "30s")
	t.Cleanup(func() {
		if st, ok := e.state(); ok {
			stopServer(st.URL)
		}
	})
	if code, out, errs := e.mote("", "run", "chat", "first"); code != 0 || strings.TrimSpace(out) != "echo: first" {
		t.Fatalf("first: %d %q %s", code, out, errs)
	}
	st, ok := e.state()
	if !ok || !st.Background || st.PID == os.Getpid() {
		t.Fatalf("no background server: %+v %v", st, ok)
	}
	if code, out, _ := e.mote("", "run", "chat", "second"); code != 0 || strings.TrimSpace(out) != "echo: second" {
		t.Fatalf("second: %d %q", code, out)
	}
	if again, _ := e.state(); again.PID != st.PID {
		t.Errorf("a second server was started: %d then %d", st.PID, again.PID)
	}
	if ids := loaded(t, st.URL); len(ids) != 1 {
		t.Errorf("loaded %v", ids)
	}

	// A change in how models load replaces the server a command started.
	if code, _, errs := e.mote("", "config", "set", "threads", "3"); code != 0 {
		t.Fatal(errs)
	}
	if code, out, _ := e.mote("", "run", "chat", "third"); code != 0 || strings.TrimSpace(out) != "echo: third" {
		t.Fatalf("third: %d %q", code, out)
	}
	next, _ := e.state()
	if next.PID == st.PID || next.Fingerprint == st.Fingerprint {
		t.Errorf("the old server was kept: %+v", next)
	}
	if _, alive := health(st.URL); alive && st.URL != next.URL {
		t.Error("the old server still runs")
	}

	// Speech runs in the command, with or without a server.
	e.install("qwen3-tts-1.7b")
	wav := filepath.Join(t.TempDir(), "s.wav")
	if code, _, errs := e.mote("", "run", "speak", "hi", "-o", wav); code != 0 {
		t.Errorf("speak: %s", errs)
	}
}

func TestAServerThatIsGoneMeansLoadingLocally(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	// A stale record of a server that no longer answers.
	b, _ := json.Marshal(serveState{PID: 1, URL: "http://127.0.0.1:" + freeLoopbackPort(t), Fingerprint: "x"})
	os.WriteFile(filepath.Join(e.home, "serve.json"), b, 0o644)
	if code, out, errs := e.mote("", "run", "chat", "hello"); code != 0 || strings.TrimSpace(out) != "echo: hello" {
		t.Fatalf("%d %q %s", code, out, errs)
	}
}

func TestKeepAliveSettings(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for _, bad := range []string{"soon", "-5m"} {
		if code, _, _ := e.mote("", "config", "set", "keep_alive", bad); code == 0 {
			t.Errorf("keep_alive %q accepted", bad)
		}
	}
	for _, good := range []string{"0", "10m", ""} {
		if code, _, errs := e.mote("", "config", "set", "keep_alive", good); code != 0 {
			t.Errorf("keep_alive %q: %s", good, errs)
		}
	}
	if code, _, _ := e.mote("", "config", "set", "serve_port", "70000"); code == 0 {
		t.Error("port 70000 accepted")
	}
	if code, _, _ := e.mote("", "serve", "--background"); code != ExitUsage {
		t.Errorf("a background server with keep_alive 0: exit %d", code)
	}
}
