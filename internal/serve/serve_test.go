package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

// upstream stands in for a llama-server: it echoes the request's model and
// streams when asked to.
type upstream struct {
	*httptest.Server
	closed atomic.Bool
}

func (u *upstream) URL() string { return u.Server.URL }
func (u *upstream) Generate(context.Context, runtime.Request) (runtime.Result, error) {
	return runtime.Result{}, nil
}
func (u *upstream) Close() runtime.Stats {
	u.closed.Store(true)
	u.Server.Close()
	return runtime.Stats{}
}

func newUpstream(id string) *upstream {
	return &upstream{Server: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, part := range []string{"one", "two"} {
				fmt.Fprintf(w, "data: {\"part\":%q}\n\n", part)
				w.(http.Flusher).Flush()
			}
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"served_by": id, "path": r.URL.Path, "asked": req.Model})
	}))}
}

type harness struct {
	srv    *Server
	url    string
	mu     sync.Mutex
	opened map[string]int
	ups    map[string]*upstream
	fail   error
}

func newHarness(t *testing.T, keep time.Duration, budget int) *harness {
	t.Helper()
	h := &harness{opened: map[string]int{}, ups: map[string]*upstream{}}
	models := map[string]*registry.Model{
		"small": {ID: "small", Caps: []string{"text"}, RAMEstimate: 1000},
		"big":   {ID: "big", Caps: []string{"vision"}, RAMEstimate: 3000},
		"vec":   {ID: "vec", Caps: []string{"embed"}, RAMEstimate: 100},
	}
	h.srv = &Server{
		Resolve: func(name string) (*registry.Model, error) {
			switch name {
			case "", "text":
				name = "small"
			case "embed":
				name = "vec"
			}
			if m, ok := models[name]; ok {
				return m, nil
			}
			return nil, errors.New("unknown model " + name)
		},
		Open: func(ctx context.Context, m *registry.Model) (runtime.Session, error) {
			time.Sleep(20 * time.Millisecond) // loading takes a while
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.fail != nil {
				return nil, h.fail
			}
			h.opened[m.ID]++
			u := newUpstream(m.ID)
			h.ups[m.ID] = u
			return u, nil
		},
		Names:       func() []string { return []string{"mote", "text", "small"} },
		KeepAlive:   keep,
		BudgetMB:    budget,
		Fingerprint: "fp1",
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.url = "http://" + ln.Addr().String()
	done := make(chan struct{})
	go func() { h.srv.Serve(context.Background(), ln); close(done) }()
	t.Cleanup(func() { h.srv.Stop(); <-done })
	return h
}

func (h *harness) post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(h.url+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (h *harness) openedCount(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.opened[id]
}

func TestServesEachModelFromOneLoad(t *testing.T) {
	h := newHarness(t, time.Minute, 0)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, body := h.post(t, "/v1/chat/completions", `{"model":"text","messages":[]}`); code != 200 || !strings.Contains(body, `"served_by":"small"`) {
				t.Errorf("%d %s", code, body)
			}
		}()
	}
	wg.Wait()
	if n := h.openedCount("small"); n != 1 {
		t.Errorf("loaded %d times for concurrent requests", n)
	}
	// The request goes through as sent, to the same path.
	if _, body := h.post(t, "/v1/embeddings", `{"input":["a"]}`); !strings.Contains(body, `"served_by":"vec"`) || !strings.Contains(body, `"path":"/v1/embeddings"`) {
		t.Errorf("embeddings default to the embed model: %s", body)
	}
	if code, body := h.post(t, "/v1/chat/completions", `{"model":"nope"}`); code != 404 || !strings.Contains(body, `"message":"unknown model nope"`) {
		t.Errorf("unknown model: %d %s", code, body)
	}
	if code, _ := h.post(t, "/v1/chat/completions", `not json`); code != 400 {
		t.Errorf("bad body: %d", code)
	}
}

func TestStreamsRepliesAsTheyCome(t *testing.T) {
	h := newHarness(t, time.Minute, 0)
	resp, err := http.Post(h.url+"/v1/chat/completions", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type %q", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"part":"one"`) || !strings.Contains(string(b), `"part":"two"`) {
		t.Errorf("stream: %s", b)
	}
}

func TestUnloadsIdleModelsAndMakesRoom(t *testing.T) {
	h := newHarness(t, 150*time.Millisecond, 3500)
	h.post(t, "/mote/load", `{"model":"small"}`)
	if l := h.srv.Loaded(); len(l) != 1 || l[0].Model != "small" {
		t.Fatalf("loaded %+v", l)
	}
	// big does not fit beside small, which is idle, so small goes.
	h.post(t, "/mote/load", `{"model":"big"}`)
	if l := h.srv.Loaded(); len(l) != 1 || l[0].Model != "big" {
		t.Errorf("after loading big: %+v", l)
	}
	time.Sleep(20 * time.Millisecond)
	if !h.ups["small"].closed.Load() {
		t.Error("small was not stopped")
	}
	// After keep-alive nothing is left.
	deadline := time.Now().Add(3 * time.Second)
	for len(h.srv.Loaded()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if l := h.srv.Loaded(); len(l) != 0 {
		t.Errorf("still loaded after keep-alive: %+v", l)
	}
}

func TestExitsWhenIdle(t *testing.T) {
	s := &Server{Resolve: func(string) (*registry.Model, error) { return nil, errors.New("x") },
		Names: func() []string { return nil }, KeepAlive: 50 * time.Millisecond, ExitIdle: 100 * time.Millisecond}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	done := make(chan error)
	go func() { done <- s.Serve(context.Background(), ln) }()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(3 * time.Second):
		s.Stop()
		t.Fatal("did not exit when idle")
	}
}

func TestReportsLoadFailures(t *testing.T) {
	h := newHarness(t, time.Minute, 0)
	h.fail = errors.New("model file is corrupt")
	code, body := h.post(t, "/mote/load", `{"model":"small"}`)
	if code != 503 || !strings.Contains(body, "model file is corrupt") {
		t.Errorf("%d %s", code, body)
	}
	// A failed load is not remembered: the next request tries again.
	h.fail = nil
	if code, body := h.post(t, "/mote/load", `{"model":"small"}`); code != 200 || !strings.Contains(body, `"model":"small"`) {
		t.Errorf("retry: %d %s", code, body)
	}
}

func TestOnlyLocalClients(t *testing.T) {
	h := newHarness(t, time.Minute, 0)
	for name, set := range map[string]func(*http.Request){
		"rebound host":   func(r *http.Request) { r.Host = "evil.example:11435" },
		"foreign origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
	} {
		req, _ := http.NewRequest(http.MethodGet, h.url+"/v1/models", nil)
		set(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, h.url+"/v1/models", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	if resp.StatusCode != 200 || len(list.Data) != 3 || list.Data[0].ID != "mote" {
		t.Errorf("models: %d %+v", resp.StatusCode, list)
	}
}
