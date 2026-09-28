package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/serve"
	"github.com/jgalego/mote/registry"
)

// serveState is what a running mote serve records in the data directory,
// so commands can find it.
type serveState struct {
	PID         int    `json:"pid"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"`
	// Background marks a server a command started, which a command may
	// replace; one someone started by hand is left alone.
	Background bool      `json:"background"`
	Started    time.Time `json:"started"`
}

func (a *app) statePath() string { return filepath.Join(a.dataDir(), "serve.json") }

func (a *app) readState() (serveState, bool) {
	var st serveState
	b, err := os.ReadFile(a.statePath())
	if err != nil || json.Unmarshal(b, &st) != nil || st.URL == "" {
		return st, false
	}
	return st, true
}

// keepAlive is how long models stay loaded between commands; zero means
// commands never start a server, though they use one that is running.
// MOTE_KEEP_ALIVE overrides the configuration.
func (a *app) keepAlive() time.Duration {
	if v := os.Getenv("MOTE_KEEP_ALIVE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return a.cfg.KeepAliveDuration()
}

// fingerprint identifies what decides how models are loaded: this build,
// the registry, the runtime and the settings passed to llama-server. A
// server with another fingerprint would load models differently from how
// this command would.
func (a *app) fingerprint() string {
	h := sha256.New()
	fmt.Fprintln(h, version(), a.registry().Version, a.dataDir(), a.cfg.LlamaDir, a.cfg.Threads, a.cfg.Repack, a.cfg.GPU)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

var serveClient = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}

// health asks a server who it is.
func health(url string) (fingerprint string, ok bool) {
	resp, err := serveClient.Get(url + "/health")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var h struct {
		Fingerprint string `json:"fingerprint"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&h) != nil {
		return "", false
	}
	return h.Fingerprint, true
}

// stopServer asks a server to stop and waits until it has.
func stopServer(url string) bool {
	resp, err := serveClient.Post(url+"/mote/stop", "application/json", nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	for i := 0; i < 100; i++ {
		if _, ok := health(url); !ok {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// serverURL returns the URL of a resident server this command can use,
// starting one when keep_alive allows, or "" to load models in-process.
// It is worked out once per command.
func (a *app) serverURL() string {
	if a.serveURL != nil {
		return *a.serveURL
	}
	url := a.findServer()
	a.serveURL = &url
	return url
}

func (a *app) findServer() string {
	if a.inServe {
		return ""
	}
	fp := a.fingerprint()
	if st, ok := a.readState(); ok {
		got, alive := health(st.URL)
		switch {
		case alive && got == fp:
			return st.URL
		case alive && st.Background:
			// Started by a command under another configuration or build:
			// replace it, which unloads what it holds.
			a.logServe("replacing the resident server, which was started with different settings")
			stopServer(st.URL)
		case alive:
			a.logServe("not using mote serve at " + st.URL + ": it runs with different settings; restart it to use it")
			return ""
		}
	}
	if a.keepAlive() == 0 {
		return ""
	}
	url, err := a.startServer(fp)
	if err != nil {
		a.logServe(fmt.Sprintf("could not start a resident server (%v); loading models in this process", err))
		return ""
	}
	return url
}

// logServe notes something about the resident server in its log, and on
// stderr in a terminal, where it explains a slower command.
func (a *app) logServe(msg string) {
	if a.ue.Live() {
		fmt.Fprintln(a.err, a.ue.Dim("mote: "+msg))
	}
	if f, err := os.OpenFile(filepath.Join(a.dataDir(), "logs", "serve.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "%s client: %s\n", time.Now().Format("15:04:05"), msg)
		f.Close()
	}
}

// startServer starts `mote serve --background` detached from this command
// and waits until it answers. A lock file keeps two commands started at
// once from starting two servers; the second waits for the first's.
func (a *app) startServer(fp string) (string, error) {
	lock := a.statePath() + ".lock"
	os.MkdirAll(filepath.Dir(lock), 0o755)
	var f *os.File
	for i := 0; ; i++ {
		var err error
		f, err = os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			break
		}
		if st, serr := os.Stat(lock); serr == nil && time.Since(st.ModTime()) > 30*time.Second {
			os.Remove(lock) // left behind by a command that died
			continue
		}
		if url, ok := a.awaitServer(fp, 20*time.Second); ok {
			return url, nil
		}
		return "", errors.New("another command is starting it")
	}
	defer os.Remove(lock)
	f.Close()

	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	logs := filepath.Join(a.dataDir(), "logs")
	os.MkdirAll(logs, 0o755)
	logf, err := os.OpenFile(filepath.Join(logs, "serve.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "serve", "--background")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Dir = a.dataDir()
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return "", fmt.Errorf("it exited: %v; see %s", err, filepath.Join(logs, "serve.log"))
		case <-time.After(50 * time.Millisecond):
		}
		if st, ok := a.readState(); ok && st.PID == cmd.Process.Pid {
			if got, alive := health(st.URL); alive && got == fp {
				return st.URL, nil
			}
		}
	}
	cmd.Process.Kill()
	return "", errors.New("it did not answer within 15s")
}

// awaitServer waits for a server with fingerprint fp to appear.
func (a *app) awaitServer(fp string, wait time.Duration) (string, bool) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if st, ok := a.readState(); ok {
			if got, alive := health(st.URL); alive && got == fp {
				return st.URL, true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", false
}

// servedCaps are the capabilities whose models run in llama-server, which
// is what a resident server can hold.
func servedCaps() []string {
	var out []string
	for c := range registry.Capabilities {
		if c != "tts" && c != "image" {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// newServer builds the resident server over this configuration.
func (a *app) newServer(keep time.Duration) *serve.Server {
	return &serve.Server{
		Resolve: func(name string) (*registry.Model, error) {
			if name == "" || name == "mote" {
				name = "text"
			}
			var m *registry.Model
			if _, isCap := registry.Capabilities[name]; isCap {
				var err error
				if m, _, err = a.choose(name, a.cfg.Profile); err != nil {
					return nil, err
				}
			} else if found, ok := a.registry().Model(name); ok {
				m = found
			} else {
				return nil, fmt.Errorf("unknown model %q; GET /v1/models lists them", name)
			}
			if m.Backend != "llama.cpp" {
				return nil, fmt.Errorf("%s cannot be served; use `mote run`", m.ID)
			}
			if !a.store().Installed(m) {
				return nil, fmt.Errorf("model %s is not downloaded; run `mote models pull %s`", m.ID, m.ID)
			}
			return m, nil
		},
		Open: func(ctx context.Context, m *registry.Model) (mrt.Session, error) {
			l, err := a.llama()
			if err != nil {
				return nil, err
			}
			// A model that stays loaded reads every prompt faster for the
			// load time repacking costs once.
			l.Repack = a.cfg.Repack != "off"
			return l.Open(ctx, m, a.store().Files(m))
		},
		Names: func() []string {
			names := []string{"mote"}
			names = append(names, servedCaps()...)
			reg := a.registry()
			for i := range reg.Models {
				m := &reg.Models[i]
				if m.Backend == "llama.cpp" && a.store().Installed(m) {
					names = append(names, m.ID)
				}
			}
			return names
		},
		KeepAlive:   keep,
		BudgetMB:    a.ramMB() * 3 / 4,
		Fingerprint: a.fingerprint(),
	}
}

// serveCmd implements `mote serve`.
func (a *app) serveCmd(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return a.serveStatus()
		case "stop":
			st, ok := a.readState()
			if _, alive := health(st.URL); !ok || !alive {
				fmt.Fprintln(a.out, "mote serve is not running")
				return nil
			}
			if !stopServer(st.URL) {
				return fmt.Errorf("mote serve (pid %d) did not stop", st.PID)
			}
			fmt.Fprintf(a.out, "%s stopped mote serve\n", a.uo.OK())
			return nil
		}
	}
	vals, pos, err := flags(args, []string{"--port", "--keep-alive"}, []string{"--background"})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: mote serve [--port N] [--keep-alive DURATION] | status | stop")
	}
	background := vals["--background"] == "true"
	keep := a.keepAlive()
	if v := vals["--keep-alive"]; v != "" {
		if keep, err = time.ParseDuration(v); err != nil || keep < 0 {
			return usagef("--keep-alive expects a duration such as 5m")
		}
	}
	if keep == 0 && background {
		return usagef("keep_alive is 0, so there is nothing to keep loaded")
	}
	port := a.cfg.Port()
	if v := vals["--port"]; v != "" {
		if port, err = strconv.Atoi(v); err != nil || port < 1 || port > 65535 {
			return usagef("--port expects a port number")
		}
	}
	a.inServe = true
	if st, ok := a.readState(); ok {
		if _, alive := health(st.URL); alive {
			if background || !st.Background {
				return fmt.Errorf("mote serve is already running at %s (pid %d)", st.URL, st.PID)
			}
			stopServer(st.URL) // one a command started; this one replaces it
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil && background {
		// The usual port belongs to something else; any will do for
		// commands, which find it in serve.json.
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return fmt.Errorf("cannot listen on port %d: %v; choose another with --port", port, err)
	}
	url := "http://" + ln.Addr().String()
	srv := a.newServer(keep)
	srv.Log = a.err
	if background {
		srv.ExitIdle = keep
	}
	st := serveState{PID: os.Getpid(), URL: url, Fingerprint: srv.Fingerprint, Background: background, Started: time.Now().UTC()}
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(a.statePath(), append(b, '\n'), 0o644); err != nil {
		ln.Close()
		return err
	}
	defer func() {
		if cur, ok := a.readState(); ok && cur.PID == st.PID {
			os.Remove(a.statePath())
		}
	}()
	if !background {
		fmt.Fprintf(a.out, "%s serving an OpenAI-compatible API at %s\n", a.uo.OK(), a.uo.Bold(url+"/v1"))
		fmt.Fprintln(a.out, a.uo.Dim(fmt.Sprintf("models load on first use and unload after %s idle; mote commands use them too; Ctrl-C stops", keep)))
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM)
	defer stop()
	return srv.Serve(ctx, ln)
}

func (a *app) serveStatus() error {
	st, ok := a.readState()
	if ok {
		if _, alive := health(st.URL); !alive {
			ok = false
		}
	}
	if !ok {
		msg := "mote serve is not running"
		if k := a.keepAlive(); k > 0 {
			msg += fmt.Sprintf("; the next command starts it and keeps models loaded for %s", k)
		}
		fmt.Fprintln(a.out, msg)
		return nil
	}
	resp, err := serveClient.Get(st.URL + "/mote/status")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var s struct {
		KeepAlive string         `json:"keep_alive"`
		Loaded    []serve.Status `json:"loaded"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return err
	}
	how := "started by hand"
	if st.Background {
		how = "started by a command, exits when idle"
	}
	fmt.Fprintf(a.out, "%s mote serve at %s %s\n", a.uo.OK(), a.uo.Bold(st.URL+"/v1"), a.uo.Dim(fmt.Sprintf("(pid %d, %s, keep-alive %s)", st.PID, how, s.KeepAlive)))
	if len(s.Loaded) == 0 {
		fmt.Fprintln(a.out, a.uo.Dim("  no models loaded"))
	}
	for _, l := range s.Loaded {
		state := "idle " + time.Since(l.LastUsed).Round(time.Second).String()
		if l.Busy > 0 {
			state = a.uo.Green("busy")
		}
		fmt.Fprintf(a.out, "  %s %s\n", a.uo.Bold(pad(l.Model, 18)), a.uo.Dim(fmt.Sprintf("~%d MB, loaded in %.1fs, %s", l.RAMMB, l.StartupMS/1000, state)))
	}
	return nil
}

// llama returns the local llama.cpp backend. When gpu is on, it prefers a
// build that offloads to the GPU: a separate one on the platforms that need
// it (Vulkan, on Linux and Windows), or, on one that does not (macOS, whose
// default build already includes Metal), the same build under different
// runtime flags.
func (a *app) llama() (*mrt.Llama, error) {
	gpu := a.cfg.GPU == "on"
	variant := ""
	if gpu {
		info := a.platform()
		if _, ok := a.registry().Runtime.GPUAsset(info.OS, info.Arch); ok {
			variant = "gpu"
		}
	}
	dir, err := mrt.FindLlama(a.cfg.LlamaDir, a.store(), a.registry().Runtime, variant)
	if err != nil {
		return nil, err
	}
	return &mrt.Llama{Dir: dir, Threads: a.cfg.Threads, LogDir: filepath.Join(a.dataDir(), "logs"), Repack: a.cfg.Repack == "on", GPU: gpu}, nil
}

// servedBackend wraps the local backend so models load in the resident
// server when there is one, and in this process when it cannot be reached.
func (a *app) servedBackend(local *mrt.Llama) mrt.Backend {
	url := a.serverURL()
	if url == "" {
		return local
	}
	return &mrt.Remote{URL: url, Local: local, Fallback: func(err error) {
		a.logServe(err.Error() + "; loading the model in this process")
	}}
}
