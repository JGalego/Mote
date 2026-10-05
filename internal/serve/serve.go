// Package serve keeps models loaded between commands. One process owns
// every llama-server it starts: it loads a model on first use, hands each
// request to it, and unloads it after it has been idle for a while, or
// sooner to make room in memory for another. It speaks OpenAI and Anthropic
// APIs on a loopback port, which is how mote's own commands, editors and
// other tools use mote's models.
package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

// Server holds the loaded models and serves requests for them.
type Server struct {
	// Resolve maps a request's model name, a model id or a capability
	// such as "text", to a model it may load. An empty name means text.
	Resolve func(name string) (*registry.Model, error)
	// Open starts a model; the session must implement runtime.URLer.
	Open func(ctx context.Context, m *registry.Model) (runtime.Session, error)
	// Names lists what /v1/models offers.
	Names func() []string
	// KeepAlive is how long a model stays loaded after its last request.
	KeepAlive time.Duration
	// BudgetMB is the memory loaded models may use together, by their
	// registry estimates; the least recently used idle model is unloaded
	// to make room. Zero means no limit.
	BudgetMB int
	// ExitIdle, when set, stops the server once nothing has been loaded
	// for that long, which is how a server started by a command ends.
	ExitIdle time.Duration
	// Fingerprint identifies the build and configuration the server runs
	// with, so a command can tell a server that would load models
	// differently from how it would.
	Fingerprint string
	Log         io.Writer

	mu      sync.Mutex
	slots   map[string]*slot
	idle    time.Time // when the last model was unloaded, or the start
	stop    chan struct{}
	stopped sync.Once
	client  *http.Client
}

type slot struct {
	m     *registry.Model
	sess  runtime.Session
	url   string
	ready chan struct{}
	err   error
	busy  int
	last  time.Time
	start time.Duration
}

// Status describes one loaded model.
type Status struct {
	Model     string    `json:"model"`
	Busy      int       `json:"busy"`
	LastUsed  time.Time `json:"last_used"`
	RAMMB     int       `json:"ram_mb"`
	StartupMS float64   `json:"startup_ms"`
}

func (s *Server) init() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slots == nil {
		s.slots = map[string]*slot{}
		s.idle = time.Now()
		s.stop = make(chan struct{})
		s.client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
}

func (s *Server) logf(format string, a ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, time.Now().Format("15:04:05 ")+format+"\n", a...)
	}
}

// acquire returns the loaded model, loading it first if need be, and marks
// it busy until release is called.
func (s *Server) acquire(ctx context.Context, m *registry.Model) (*slot, func(), error) {
	s.mu.Lock()
	sl, ok := s.slots[m.ID]
	if !ok {
		s.makeRoom(m)
		sl = &slot{m: m, ready: make(chan struct{})}
		s.slots[m.ID] = sl
		sl.busy++
		s.mu.Unlock()
		s.logf("loading %s", m.ID)
		start := time.Now()
		sess, err := s.Open(context.WithoutCancel(ctx), m)
		s.mu.Lock()
		sl.sess, sl.err, sl.start = sess, err, time.Since(start)
		if err == nil {
			if u, ok := sess.(runtime.URLer); ok {
				sl.url = u.URL()
			} else {
				sess.Close()
				sl.err = fmt.Errorf("%s cannot be served", m.ID)
			}
		}
		if sl.err != nil {
			delete(s.slots, m.ID)
			s.logf("loading %s failed: %v", m.ID, sl.err)
		} else {
			s.logf("loaded %s in %s", m.ID, sl.start.Round(time.Millisecond))
		}
		close(sl.ready)
		s.mu.Unlock()
		if sl.err != nil {
			return nil, nil, sl.err
		}
		return sl, s.releaser(sl), nil
	}
	sl.busy++
	s.mu.Unlock()
	select {
	case <-sl.ready:
	case <-ctx.Done():
		s.releaser(sl)()
		return nil, nil, ctx.Err()
	}
	if sl.err != nil {
		return nil, nil, sl.err
	}
	return sl, s.releaser(sl), nil
}

func (s *Server) releaser(sl *slot) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			sl.busy--
			sl.last = time.Now()
			s.mu.Unlock()
		})
	}
}

// makeRoom unloads idle models, least recently used first, until m fits
// the budget. Busy models stay: running over the budget for a while beats
// failing a request. The caller holds s.mu.
func (s *Server) makeRoom(m *registry.Model) {
	if s.BudgetMB <= 0 {
		return
	}
	used := m.RAMEstimate
	var idle []*slot
	for _, sl := range s.slots {
		used += sl.m.RAMEstimate
		if sl.busy == 0 && sl.sess != nil {
			idle = append(idle, sl)
		}
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].last.Before(idle[j].last) })
	for _, sl := range idle {
		if used <= s.BudgetMB {
			return
		}
		s.logf("unloading %s to make room for %s", sl.m.ID, m.ID)
		s.unload(sl)
		used -= sl.m.RAMEstimate
	}
}

// unload stops a model. The caller holds s.mu.
func (s *Server) unload(sl *slot) {
	delete(s.slots, sl.m.ID)
	go sl.sess.Close()
	if len(s.slots) == 0 {
		s.idle = time.Now()
	}
}

// reap unloads models idle for longer than KeepAlive and reports whether
// the server has had nothing loaded for ExitIdle.
func (s *Server) reap(now time.Time) (exit bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sl := range s.slots {
		if sl.sess != nil && sl.busy == 0 && now.Sub(sl.last) >= s.KeepAlive {
			s.logf("unloading %s after %s idle", sl.m.ID, s.KeepAlive)
			s.unload(sl)
		}
	}
	return s.ExitIdle > 0 && len(s.slots) == 0 && now.Sub(s.idle) >= s.ExitIdle
}

// Loaded lists the models in memory.
func (s *Server) Loaded() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Status
	for _, sl := range s.slots {
		if sl.sess == nil {
			continue
		}
		out = append(out, Status{Model: sl.m.ID, Busy: sl.busy, LastUsed: sl.last, RAMMB: sl.m.RAMEstimate,
			StartupMS: float64(sl.start.Microseconds()) / 1000})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// Stop asks Serve to return.
func (s *Server) Stop() {
	s.init()
	s.stopped.Do(func() { close(s.stop) })
}

// Serve answers requests on ln until ctx ends, Stop is called or the
// server has been idle for ExitIdle, then unloads every model.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.init()
	hs := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	tick := s.KeepAlive / 4
	if tick < 50*time.Millisecond {
		tick = 50 * time.Millisecond
	}
	if tick > 5*time.Second {
		tick = 5 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-s.stop:
			break loop
		case err = <-errc:
			break loop
		case now := <-t.C:
			if s.reap(now) {
				s.logf("nothing loaded for %s; exiting", s.ExitIdle)
				break loop
			}
		}
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hs.Shutdown(shut)
	s.mu.Lock()
	for _, sl := range s.slots {
		if sl.sess != nil {
			sl.sess.Close()
		}
	}
	s.slots = map[string]*slot{}
	s.mu.Unlock()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// maxBody bounds a request, which may carry images or audio as base64.
const maxBody = 64 << 20

// Handler serves the OpenAI endpoints and mote's own.
func (s *Server) Handler() http.Handler {
	s.init()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "pid": os.Getpid(), "fingerprint": s.Fingerprint})
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		var data []any
		for _, n := range s.Names() {
			data = append(data, map[string]any{"id": n, "object": "model", "owned_by": "mote"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	})
	for _, p := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings"} {
		mux.HandleFunc("POST "+p, s.proxy)
	}
	mux.HandleFunc("POST /v1/messages", s.anthropicMessages)
	mux.HandleFunc("POST /v1/messages/count_tokens", s.anthropicCountTokens)
	mux.HandleFunc("POST /mote/load", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			apiError(w, http.StatusBadRequest, "bad request: "+err.Error())
			return
		}
		m, err := s.Resolve(req.Model)
		if err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		sl, release, err := s.acquire(r.Context(), m)
		if err != nil {
			apiError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		release()
		writeJSON(w, http.StatusOK, map[string]any{"model": m.ID, "startup_ms": float64(sl.start.Microseconds()) / 1000})
	})
	mux.HandleFunc("GET /mote/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"pid": os.Getpid(), "keep_alive": s.KeepAlive.String(),
			"budget_mb": s.BudgetMB, "loaded": s.Loaded()})
	})
	mux.HandleFunc("POST /mote/stop", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"stopping": true})
		s.Stop()
	})
	return local(mux)
}

// local refuses requests that did not come from this machine's own tools:
// a Host other than loopback is how a web page would reach the server
// through DNS rebinding, and an Origin other than loopback is a web page
// asking directly.
func local(h http.Handler) http.Handler {
	loopback := func(hostport string) bool {
		host := hostport
		if h, _, err := net.SplitHostPort(hostport); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if host == "localhost" {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopback(r.Host) {
			apiError(w, http.StatusForbidden, "only local clients may use this server")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			host := o
			if i := strings.Index(host, "://"); i >= 0 {
				host = host[i+3:]
			}
			if !loopback(host) {
				apiError(w, http.StatusForbidden, "cross-origin requests are not allowed")
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// proxy passes an OpenAI request to the llama-server of the model it
// names, streaming the reply back as it comes.
func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		apiError(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		apiError(w, http.StatusBadRequest, "request is not JSON: "+err.Error())
		return
	}
	name := req.Model
	if name == "" && r.URL.Path == "/v1/embeddings" {
		name = "embed"
	}
	m, err := s.Resolve(name)
	if err != nil {
		apiError(w, http.StatusNotFound, err.Error())
		return
	}
	sl, release, err := s.acquire(r.Context(), m)
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer release()
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, sl.url+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	up.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(up)
	if err != nil {
		apiError(w, http.StatusBadGateway, fmt.Sprintf("%s: %v", m.ID, err))
		return
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// apiError answers in the OpenAI error shape, which clients know to show.
func apiError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"message": msg, "type": "mote_error", "code": code}})
}
