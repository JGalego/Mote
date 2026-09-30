package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// lockedBuffer is written by a running command and read by the test.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// serving is `mote nb serve` running, and a client that has opened a session.
type serving struct {
	t      *testing.T
	base   string
	client *http.Client
	stderr *lockedBuffer
	stop   context.CancelFunc
	code   chan int
}

var addrRe = regexp.MustCompile(`http://(127\.0\.0\.1:\d+)/\?token=([0-9a-f]+)`)

func startServing(t *testing.T, e *env, args ...string) *serving {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &serving{t: t, stderr: &lockedBuffer{}, stop: cancel, code: make(chan int, 1)}
	go func() {
		s.code <- mainContext(ctx, append([]string{"nb", "serve"}, args...), strings.NewReader(""), io.Discard, s.stderr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.code:
		case <-time.After(10 * time.Second):
			t.Error("mote nb serve did not stop")
		}
	})
	var m []string
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if m = addrRe.FindStringSubmatch(s.stderr.String()); m != nil {
			break
		}
		select {
		case code := <-s.code:
			t.Fatalf("mote nb serve exited %d: %s", code, s.stderr.String())
		default:
		}
	}
	if m == nil {
		t.Fatalf("no address printed:\n%s", s.stderr.String())
	}
	jar, _ := cookiejar.New(nil)
	s.base = "http://" + m[1]
	s.client = &http.Client{Jar: jar}
	resp, err := s.client.Get(m[0])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("opening the session: %d", resp.StatusCode)
	}
	return s
}

func (s *serving) api(method, path string, body any) (int, map[string]any) {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.base+path, r)
	req.Header.Set("X-Mote", "1")
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func rev(m map[string]any) float64 {
	if nb, ok := m["notebook"].(map[string]any); ok {
		return nb["rev"].(float64)
	}
	return m["rev"].(float64)
}

func TestNbServeRunsCellsWithMotesTasks(t *testing.T) {
	e, path := nbSessionEnv(t)
	os.WriteFile(path, []byte("# Notes\n\n```mote as=a\nchat one\n```\n\n```mote\nchat \"got {{a}}\"\n```\n"), 0o644)
	s := startServing(t, e, path, "--port", "0")

	// It listens for this machine only.
	if !strings.HasPrefix(s.base, "http://127.0.0.1:") {
		t.Errorf("address %q", s.base)
	}
	code, nb := s.api("GET", "/api/notebook", nil)
	if code != 200 || len(nb["segments"].([]any)) != 3 {
		t.Fatalf("notebook: %d %v", code, nb)
	}
	code, r := s.api("POST", "/api/run", map[string]any{"rev": rev(nb), "index": 1})
	if code != 200 || r["ran"] != true {
		t.Fatalf("first cell: %d %v", code, r)
	}
	code, r = s.api("POST", "/api/run", map[string]any{"rev": rev(r), "index": 2})
	if code != 200 {
		t.Fatalf("second cell: %d %v", code, r)
	}
	// chat echoes its prompt, so the second cell shows it read the first.
	file := readFile(t, path)
	if !strings.Contains(file, "echo: got echo: one") || !strings.HasPrefix(file, "# Notes\n") {
		t.Errorf("the notebook:\n%s", file)
	}
	// What ran went to the terminal too, timed.
	if !strings.Contains(s.stderr.String(), "chat one") {
		t.Errorf("stderr:\n%s", s.stderr.String())
	}
}

func TestNbServeAsksBeforeACellThatCanChangeThings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stages use cmd /c on Windows; the sh syntax here does not apply")
	}
	e, path := nbSessionEnv(t)
	os.WriteFile(path, []byte("```mote\nchat hi | sh: read l; echo \"saw $l\"\n```\n"), 0o644)
	s := startServing(t, e, path)
	_, nb := s.api("GET", "/api/notebook", nil)
	if seg := nb["segments"].([]any)[0].(map[string]any); seg["changes"] != true {
		t.Errorf("the page is not told the cell can change things: %v", seg)
	}
	code, r := s.api("POST", "/api/run", map[string]any{"rev": rev(nb), "index": 0})
	if code != 409 || r["approval"] != true {
		t.Fatalf("without approval: %d %v", code, r)
	}
	code, r = s.api("POST", "/api/run", map[string]any{"rev": rev(nb), "index": 0, "approve": true})
	if code != 200 || !strings.Contains(readFile(t, path), "saw echo: hi") {
		t.Errorf("approved: %d %v", code, r)
	}

	// --yes is approval given in advance.
	yes := startServing(t, e, path, "--yes")
	_, nb = yes.api("GET", "/api/notebook", nil)
	if code, _ := yes.api("POST", "/api/run", map[string]any{"rev": rev(nb), "index": 0}); code != 200 {
		t.Errorf("--yes: %d", code)
	}
}

func TestNbServeReportsWhatAFailedCellSays(t *testing.T) {
	e, path := nbSessionEnv(t)
	os.WriteFile(path, []byte("```mote\nnosuchtask x\n```\n"), 0o644)
	s := startServing(t, e, path)
	_, nb := s.api("GET", "/api/notebook", nil)
	seg := nb["segments"].([]any)[0].(map[string]any)
	if !strings.Contains(seg["problem"].(string), "nosuchtask") {
		t.Errorf("the page is not told the task does not exist: %v", seg)
	}
	code, r := s.api("POST", "/api/run", map[string]any{"rev": rev(nb), "index": 0})
	if code != 400 || !strings.Contains(r["error"].(string), "nosuchtask") {
		t.Errorf("%d %v", code, r)
	}
}

func TestNbServeShowsMediaAndStopsWithAPageListening(t *testing.T) {
	e, path := nbSessionEnv(t)
	dir := filepath.Dir(path)
	os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("video"), 0o644)
	os.WriteFile(path, []byte("Watch:\n\n<video controls src=\"clip.mp4\"></video>\n"), 0o644)
	s := startServing(t, e, path)

	_, nb := s.api("GET", "/api/notebook", nil)
	html := nb["segments"].([]any)[0].(map[string]any)["html"].(string)
	if !strings.Contains(html, `<video controls preload="metadata" src="/file?p=clip.mp4">`) {
		t.Errorf("the player: %s", html)
	}
	resp, err := s.client.Get(s.base + "/file?p=clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(resp.Body); resp.StatusCode != 200 || string(body) != "video" {
		t.Errorf("the file: %d %q", resp.StatusCode, body)
	}
	resp.Body.Close()

	// A page is listening for events when Ctrl-C comes.
	events, err := s.client.Get(s.base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	s.stop()
	select {
	case code := <-s.code:
		if code != 0 {
			t.Errorf("exit %d after Ctrl-C: %s", code, s.stderr.String())
		}
		s.code <- code // for the cleanup
	case <-time.After(10 * time.Second):
		t.Fatal("did not stop with a page listening")
	}
}

func TestNbServeRefusesWhatItCannotServe(t *testing.T) {
	e, path := nbSessionEnv(t)
	bad := filepath.Join(t.TempDir(), "bad.mote.md")
	os.WriteFile(bad, []byte("```mote\nchat {{nope}}\n```\n"), 0o644)
	for name, args := range map[string][]string{
		"no file":        {"nb", "serve"},
		"a bad port":     {"nb", "serve", path, "--port", "http"},
		"a huge port":    {"nb", "serve", path, "--port", "70000"},
		"a bad notebook": {"nb", "serve", bad},
		"a missing root": {"nb", "serve", path, "--root", filepath.Join(t.TempDir(), "gone")},
		"a run flag":     {"nb", "serve", path, "--force"},
		"--port on run":  {"nb", "run", path, "--port", "1"},
		"--open on run":  {"nb", "run", path, "--open"},
	} {
		if code, _, errs := e.mote("", args...); code != ExitUsage {
			t.Errorf("%s: exit %d, want usage\n%s", name, code, errs)
		}
	}
	// A port in use is an ordinary error, not a usage one.
	s := startServing(t, e, path)
	port := strings.TrimPrefix(s.base, "http://127.0.0.1:")
	if code, _, errs := e.mote("", "nb", "serve", path, "--port", port); code == 0 || code == ExitUsage {
		t.Errorf("a port in use: exit %d\n%s", code, errs)
	}
}

func TestOpenBrowserReportsWhatItCannotStart(t *testing.T) {
	t.Setenv("PATH", "")
	if err := openBrowser("http://127.0.0.1:1/"); err == nil && runtime.GOOS != "windows" {
		t.Error("a browser was started with no PATH")
	}
}

func TestNbServeStopsARunningCellOnCtrlC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sh syntax here does not apply to cmd")
	}
	e, path := nbSessionEnv(t)
	pidfile := filepath.Join(t.TempDir(), "pid")
	os.WriteFile(path, []byte("```mote\nchat hi | sh: echo $$ > "+pidfile+"; exec /bin/sleep 60\n```\n"), 0o644)
	s := startServing(t, e, path, "--yes")
	_, nb := s.api("GET", "/api/notebook", nil)
	go s.api("POST", "/api/run", map[string]any{"rev": rev(nb), "index": 0})
	var pid int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && pid == 0; time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(pidfile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
	if pid == 0 {
		t.Fatal("the cell did not start")
	}
	s.stop()
	select {
	case code := <-s.code:
		s.code <- code
	case <-time.After(10 * time.Second):
		t.Fatal("did not stop")
	}
	// By the time the command returns, the cell's process is gone.
	if p, err := os.FindProcess(pid); err == nil && p.Signal(syscall.Signal(0)) == nil {
		out, _ := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if st := strings.TrimSpace(string(out)); st != "" && !strings.HasPrefix(st, "Z") {
			p.Kill()
			t.Errorf("the cell's process %d outlived the server (%s)", pid, st)
		}
	}
}
