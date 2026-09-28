// Package mcp is a small client for Model Context Protocol servers that run
// as local processes and speak JSON-RPC over their stdin and stdout. It does
// what mote's agent needs and no more: start a server, list its tools and
// call them. Servers reached over HTTP are not supported, since mote does
// not send work off the machine.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProtocolVersion is the revision mote asks for; a server answers with the
// one it will use.
const ProtocolVersion = "2025-06-18"

// Server is one entry of mcp.json, in the shape other MCP clients use, so
// an existing entry can be copied over.
type Server struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// URL and Type are read only to reject remote servers clearly.
	URL  string `json:"url,omitempty"`
	Type string `json:"type,omitempty"`
}

// Config is the contents of mcp.json.
type Config struct {
	Servers map[string]Server `json:"mcpServers"`
}

// Load reads mcp.json. A missing file means no servers.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	for name, s := range c.Servers {
		if err := s.check(); err != nil {
			return Config{}, fmt.Errorf("%s: server %q: %w", path, name, err)
		}
		if strings.ContainsAny(name, ". ") || name == "" {
			return Config{}, fmt.Errorf("%s: server name %q may not be empty or contain dots or spaces", path, name)
		}
	}
	return c, nil
}

// Names returns the configured servers in order.
func (c Config) Names() []string {
	var out []string
	for n := range c.Servers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s Server) check() error {
	if s.URL != "" || (s.Type != "" && s.Type != "stdio") {
		return errors.New("only local servers started with a command are supported, not remote ones")
	}
	if strings.TrimSpace(s.Command) == "" {
		return errors.New(`missing "command"`)
	}
	return nil
}

// Tool is a tool a server offers.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnly    *bool `json:"readOnlyHint"`
		Destructive *bool `json:"destructiveHint"`
	} `json:"annotations"`
}

// ReadOnly reports whether the server says the tool changes nothing.
func (t Tool) ReadOnly() bool { return t.Annotations.ReadOnly != nil && *t.Annotations.ReadOnly }

// Client talks to one running server.
type Client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailBuffer

	waitOnce sync.Once // Wait may be called once; failed and Close both need it

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response
	done    chan struct{} // closed when the server's output ends
	readErr error
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// message is anything the server sends: a response to mote, a request of
// its own, or a notification.
type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	response
}

// Start launches a server and completes the MCP handshake. The process is
// tied to ctx only for the handshake; Close stops it.
func Start(ctx context.Context, name string, s Server) (*Client, error) {
	if err := s.check(); err != nil {
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}
	prog := s.Command
	if !strings.ContainsAny(prog, `/\`) {
		p, err := exec.LookPath(prog)
		if err != nil {
			return nil, fmt.Errorf("mcp %s: %s was not found on PATH", name, prog)
		}
		prog = p
	}
	cmd := exec.Command(prog, s.Args...)
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &Client{name: name, cmd: cmd, stdin: stdin, stderr: &tailBuffer{max: 8 << 10},
		pending: map[int64]chan response{}, done: make(chan struct{})}
	cmd.Stderr = c.stderr
	// Wait also waits for stderr to be copied; a child of the server that
	// keeps it open must not hang mote.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}
	go c.read(stdout)

	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	err = c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mote", "version": "1"},
	}, &init)
	if err == nil {
		err = c.notify("notifications/initialized")
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Name is the server's name in mcp.json.
func (c *Client) Name() string { return c.name }

// maxMessage bounds one message from a server. A tool that returns a whole
// binary file can exceed it; that call fails and the session goes on. A
// variable so tests can lower it.
var maxMessage = 64 << 20

// errTooLarge marks a message that exceeded maxMessage.
var errTooLarge = errors.New("message too large")

// readLine returns the next line, or errTooLarge after skipping to the end
// of one longer than max, so a single huge reply cannot end the session.
func readLine(br *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	tooLarge := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLarge {
			if len(line)+len(chunk) > max {
				tooLarge, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		switch {
		case err == bufio.ErrBufferFull:
			continue
		case err != nil && (len(line) == 0 || err != io.EOF):
			return nil, err
		case tooLarge:
			return nil, errTooLarge
		default:
			return line, nil
		}
	}
}

// read routes everything the server writes, one JSON message per line.
func (c *Client) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	var err error
	for {
		var line []byte
		line, err = readLine(br, maxMessage)
		if err == errTooLarge {
			// Requests go one at a time, so the reply that was too large
			// belongs to whichever call is waiting.
			c.failPending(fmt.Sprintf("the server's reply was larger than %d MiB", maxMessage>>20))
			continue
		}
		if err != nil {
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue // a stray log line on stdout; not ours to parse
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			c.answer(m)
		case m.Method != "":
			// A notification (progress, logging): nothing to do.
		default:
			var id int64
			if json.Unmarshal(m.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ok {
				ch <- m.response
			}
		}
	}
	c.mu.Lock()
	if err != io.EOF {
		c.readErr = err
	}
	c.mu.Unlock()
	close(c.done)
}

// failPending ends every waiting call with an error.
func (c *Client) failPending(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		r := response{}
		r.Error = &struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{Code: -32000, Message: msg}
		ch <- r
		delete(c.pending, id)
	}
}

// answer replies to a request from the server. mote offers no client
// capabilities, so only ping is understood.
func (c *Client) answer(m message) {
	reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	if m.Method == "ping" {
		reply["result"] = map[string]any{}
	} else {
		reply["error"] = map[string]any{"code": -32601, "message": "method not supported by mote: " + m.Method}
	}
	c.send(reply)
}

func (c *Client) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

func (c *Client) notify(method string) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method})
}

// call sends a request and waits for its response, the server's exit, or
// the context, whichever comes first.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.send(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return c.failed(method, err)
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return fmt.Errorf("mcp %s: %s: %s", c.name, method, r.Error.Message)
		}
		if out != nil {
			if err := json.Unmarshal(r.Result, out); err != nil {
				return fmt.Errorf("mcp %s: %s: unexpected reply: %.200s", c.name, method, r.Result)
			}
		}
		return nil
	case <-c.done:
		c.mu.Lock()
		readErr := c.readErr
		c.mu.Unlock()
		if readErr != nil {
			return fmt.Errorf("mcp %s: %s: reading from the server: %w", c.name, method, readErr)
		}
		return c.failed(method, errors.New("the server exited"))
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("mcp %s: %s: %w", c.name, method, ctx.Err())
	}
}

// wait reaps the server process once it has exited, which also finishes
// copying its stderr; WaitDelay bounds how long that can take.
func (c *Client) wait() { c.waitOnce.Do(func() { c.cmd.Wait() }) }

// failed describes a server that stopped answering, with what it last
// wrote to stderr, which is usually why. Its output ending can be noticed
// before the last of its stderr has been copied, so the process is waited
// for first.
func (c *Client) failed(method string, err error) error {
	select {
	case <-c.done:
		c.wait()
	default:
	}
	msg := fmt.Sprintf("mcp %s: %s: %v", c.name, method, err)
	if tail := strings.TrimSpace(c.stderr.String()); tail != "" {
		msg += ": " + tail
	}
	return errors.New(msg)
}

// Tools lists every tool, following pagination.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for page := 0; page < 100; page++ {
		// No cursor means no params at all: a nil map would be sent as
		// "params": null, which real servers drop without answering.
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		var res struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.call(ctx, "tools/list", params, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			return all, nil
		}
		cursor = res.NextCursor
	}
	return nil, fmt.Errorf("mcp %s: tools/list did not end after 100 pages", c.name)
}

// Result is what a tool call returned, as text.
type Result struct {
	Text    string
	IsError bool
}

// Call runs a tool. Text content is returned as it is; other kinds of
// content are named so the model knows they were there.
func (c *Client) Call(ctx context.Context, tool string, args json.RawMessage) (Result, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Resource struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, &res); err != nil {
		return Result{}, err
	}
	var parts []string
	for _, p := range res.Content {
		switch {
		case p.Type == "text":
			parts = append(parts, p.Text)
		case p.Type == "resource" && p.Resource.Text != "":
			parts = append(parts, p.Resource.Text)
		case p.Type == "resource":
			parts = append(parts, "[resource "+p.Resource.URI+"]")
		default:
			parts = append(parts, "["+p.Type+" "+p.MimeType+"]")
		}
	}
	if len(parts) == 0 && len(res.Structured) > 0 {
		parts = append(parts, string(res.Structured))
	}
	return Result{Text: strings.Join(parts, "\n"), IsError: res.IsError}, nil
}

// Close ends the session: closing stdin asks the server to exit, and one
// that has not closed its output shortly after is killed. The reader must
// finish before Wait, which closes the pipe it reads.
func (c *Client) Close() {
	c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		c.cmd.Process.Kill()
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			// A child of the server still holds its stdout; stop waiting.
		}
	}
	c.wait()
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return strings.Join(lines, "\n")
}

// ConfigPath is where mcp.json lives in a config directory.
func ConfigPath(cfgDir string) string { return filepath.Join(cfgDir, "mcp.json") }
