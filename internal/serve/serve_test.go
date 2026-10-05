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

func TestAnthropicMessagesTranslatesToolCalls(t *testing.T) {
	var upstreamRequest map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-1",
			"model": "small",
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": "I will read it.",
					"tool_calls": []any{map[string]any{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "Read", "arguments": `{"file_path":"x.go"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 7},
		})
	}))
	defer up.Close()

	m := &registry.Model{ID: "small", Caps: []string{"code"}}
	s := &Server{
		Resolve: func(string) (*registry.Model, error) { return m, nil },
		Open: func(context.Context, *registry.Model) (runtime.Session, error) {
			return &upstream{Server: up}, nil
		},
		Names:     func() []string { return []string{"code"} },
		KeepAlive: time.Minute,
	}
	api := httptest.NewServer(s.Handler())
	defer api.Close()

	body := `{
		"model":"code",
		"max_tokens":512,
		"system":"Be precise.",
		"messages":[{"role":"user","content":"Read x.go"}],
		"tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}]
	}`
	resp, err := http.Post(api.URL+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Model      string           `json:"model"`
		StopReason string           `json:"stop_reason"`
		Content    []map[string]any `json:"content"`
		Usage      struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %+v", resp.StatusCode, got)
	}
	if got.Model != "code" || got.StopReason != "tool_use" || got.Usage.Input != 12 || got.Usage.Output != 7 {
		t.Errorf("response metadata: %+v", got)
	}
	if len(got.Content) != 2 || got.Content[0]["type"] != "text" || got.Content[1]["type"] != "tool_use" || got.Content[1]["name"] != "Read" {
		t.Fatalf("response content: %+v", got.Content)
	}
	input, _ := got.Content[1]["input"].(map[string]any)
	if input["file_path"] != "x.go" {
		t.Errorf("tool input: %+v", input)
	}

	messages, _ := upstreamRequest["messages"].([]any)
	tools, _ := upstreamRequest["tools"].([]any)
	if len(messages) != 2 || len(tools) != 1 || upstreamRequest["model"] != "code" {
		t.Fatalf("upstream request: %+v", upstreamRequest)
	}
	if messages[0].(map[string]any)["role"] != "system" || messages[0].(map[string]any)["content"] != "Be precise." {
		t.Errorf("system message: %+v", messages[0])
	}
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "Read" {
		t.Errorf("tool schema: %+v", function)
	}
}

func TestAnthropicMessagesStreamsTextAndToolCalls(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"id":"chatcmpl-2","choices":[{"delta":{"role":"assistant","content":"Checking "},"finish_reason":null}]}`,
			`{"id":"chatcmpl-2","choices":[{"delta":{"content":"now."},"finish_reason":null}]}`,
			`{"id":"chatcmpl-2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"Read","arguments":"{\"file_"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"path\":\"x.go\"}"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-2","choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":21,"completion_tokens":9}}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer up.Close()
	m := &registry.Model{ID: "small", Caps: []string{"code"}}
	s := &Server{
		Resolve: func(string) (*registry.Model, error) { return m, nil },
		Open: func(context.Context, *registry.Model) (runtime.Session, error) {
			return &upstream{Server: up}, nil
		},
		Names:     func() []string { return []string{"code"} },
		KeepAlive: time.Minute,
	}
	api := httptest.NewServer(s.Handler())
	defer api.Close()

	resp, err := http.Post(api.URL+"/v1/messages", "application/json", strings.NewReader(
		`{"model":"code","max_tokens":512,"stream":true,"messages":[{"role":"user","content":"Read x.go"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	stream, _ := io.ReadAll(resp.Body)
	got := string(stream)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream response: %d %q %s", resp.StatusCode, resp.Header.Get("Content-Type"), got)
	}
	wants := []string{
		`event: message_start`,
		`"type":"text_delta","text":"Checking "`,
		`"type":"text_delta","text":"now."`,
		`"type":"tool_use","id":"call_2","name":"Read"`,
		`"type":"input_json_delta","partial_json":"{\"file_"`,
		`"type":"input_json_delta","partial_json":"path\":\"x.go\"}"`,
		`"stop_reason":"tool_use"`,
		`"output_tokens":9`,
		`event: message_stop`,
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("stream lacks %q:\n%s", want, got)
		}
	}
}

func TestAnthropicMessagesPreservesToolResults(t *testing.T) {
	var req anthropicRequest
	raw := `{
		"model":"code","max_tokens":100,
		"messages":[
			{"role":"assistant","content":[
				{"type":"text","text":"I will inspect it."},
				{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"x.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"package main"}]},
				{"type":"text","text":"Now explain it."}
			]}
		],
		"tool_choice":{"type":"tool","name":"Read"}
	}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	converted, err := req.openAI()
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages: %+v", messages)
	}
	assistant := messages[0].(map[string]any)
	calls := assistant["tool_calls"].([]any)
	if assistant["content"] != "I will inspect it." || calls[0].(map[string]any)["id"] != "toolu_1" {
		t.Errorf("assistant turn: %+v", assistant)
	}
	result := messages[1].(map[string]any)
	if result["role"] != "tool" || result["tool_call_id"] != "toolu_1" || result["content"] != "package main" {
		t.Errorf("tool result: %+v", result)
	}
	if messages[2].(map[string]any)["content"] != "Now explain it." {
		t.Errorf("follow-up: %+v", messages[2])
	}
	choice := body["tool_choice"].(map[string]any)["function"].(map[string]any)
	if choice["name"] != "Read" {
		t.Errorf("tool choice: %+v", choice)
	}
}

// anthropicAPI serves the Anthropic endpoints over a model whose
// llama-server is the given handler.
func anthropicAPI(t *testing.T, llama http.HandlerFunc) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(llama)
	t.Cleanup(up.Close)
	m := &registry.Model{ID: "small", Caps: []string{"code"}}
	s := &Server{
		Resolve: func(string) (*registry.Model, error) { return m, nil },
		Open: func(context.Context, *registry.Model) (runtime.Session, error) {
			return &upstream{Server: up}, nil
		},
		Names:     func() []string { return []string{"code"} },
		KeepAlive: time.Minute,
	}
	api := httptest.NewServer(s.Handler())
	t.Cleanup(api.Close)
	return api
}

func TestAnthropicMessagesMovesALaterSystemMessageIntoTheUserTurn(t *testing.T) {
	// Claude Code sends its environment as a system message after the
	// user's; chat templates refuse a system message anywhere but first.
	var req anthropicRequest
	raw := `{
		"model":"code","max_tokens":100,"system":[{"type":"text","text":"Be precise."}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"A reminder."},{"type":"text","text":"Say pong"}]},
			{"role":"system","content":"# Environment"}
		]
	}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	converted, err := req.openAI()
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(converted, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
		t.Fatalf("messages: %s", converted)
	}
	for _, want := range []string{"A reminder.", "Say pong", "# Environment"} {
		if !strings.Contains(string(body.Messages[1].Content), want) {
			t.Errorf("user turn lacks %q: %s", want, body.Messages[1].Content)
		}
	}
}

func TestAnthropicMessagesSaysWhenThePromptIsTooLong(t *testing.T) {
	// Claude Code compacts the conversation when it reads this wording.
	api := anthropicAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":400,"message":"request (40000 tokens) exceeds the available context size (32768 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":40000,"n_ctx":32768}}`)
	})
	resp, err := http.Post(api.URL+"/v1/messages", "application/json", strings.NewReader(
		`{"model":"code","max_tokens":10,"messages":[{"role":"user","content":"long"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if resp.StatusCode != http.StatusBadRequest || got.Type != "error" || got.Error.Type != "invalid_request_error" ||
		got.Error.Message != "prompt is too long: 40000 tokens > 32768 maximum" {
		t.Errorf("%d %+v", resp.StatusCode, got)
	}
}

func TestAnthropicMessagesCountsTheCachedPrompt(t *testing.T) {
	// llama-server's timings count only the part of the prompt it had to
	// read; usage counts all of it, which is what the context holds.
	api := anthropicAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":419,\"completion_tokens\":5},\"timings\":{\"cache_n\":415,\"prompt_n\":4,\"predicted_n\":5}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	resp, err := http.Post(api.URL+"/v1/messages", "application/json", strings.NewReader(
		`{"model":"code","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	stream, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(stream), `"usage":{"input_tokens":419,"output_tokens":5}`) {
		t.Errorf("stream:\n%s", stream)
	}
}

func TestAnthropicMessagesAnswersAnEmptyReplyWithNoBlocks(t *testing.T) {
	api := anthropicAPI(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"x","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
	})
	resp, err := http.Post(api.URL+"/v1/messages", "application/json", strings.NewReader(
		`{"model":"code","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"content":[]`) {
		t.Errorf("response: %s", body)
	}
}

func TestAnthropicCountTokensDoesNotLoadAModel(t *testing.T) {
	h := newHarness(t, time.Minute, 0)
	code, body := h.post(t, "/v1/messages/count_tokens", `{
		"model":"text","system":"Be concise.",
		"messages":[{"role":"user","content":"Count this request"}]
	}`)
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK || got.InputTokens <= 0 {
		t.Errorf("count: %d %+v", code, got)
	}
	if opened := h.openedCount("small"); opened != 0 {
		t.Errorf("counting loaded the model %d times", opened)
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
