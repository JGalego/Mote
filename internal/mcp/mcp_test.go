package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgalego/mote/internal/fakemcp"
)

func TestMain(m *testing.M) {
	fakemcp.MaybeRun()
	os.Exit(m.Run())
}

// fake returns a server entry that runs the test binary as the fake MCP
// server in the given mode, and the file it logs methods to.
func fake(t *testing.T, mode string) (Server, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "methods")
	return Server{Command: self, Env: map[string]string{"MOTE_FAKE_MCP": mode, "MOTE_FAKE_MCP_LOG": log}}, log
}

// deadline bounds a test's calls: a request the server never answers
// should fail the test, not hang it.
func deadline(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func start(t *testing.T, mode string) (*Client, string) {
	t.Helper()
	s, log := fake(t, mode)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := Start(ctx, "fake", s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, log
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if c, err := Load(filepath.Join(dir, "none.json")); err != nil || len(c.Servers) != 0 {
		t.Errorf("a missing file should mean no servers: %v %v", c, err)
	}
	write := func(body string) string {
		p := filepath.Join(dir, "mcp.json")
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	c, err := Load(write(`{"mcpServers":{"files":{"command":"npx","args":["-y","server-files","."],"env":{"A":"1"}},"git":{"command":"uvx","args":["mcp-server-git"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Names(), ",") != "files,git" || c.Servers["files"].Args[2] != "." || c.Servers["files"].Env["A"] != "1" {
		t.Errorf("config %+v", c)
	}
	for body, want := range map[string]string{
		`{"mcpServers":`: "mcp.json",
		`{"mcpServers":{"web":{"url":"https://example.com/mcp"}}}`:         "remote",
		`{"mcpServers":{"web":{"type":"http","command":"x"}}}`:             "remote",
		`{"mcpServers":{"empty":{"args":["x"]}}}`:                          `missing "command"`,
		`{"mcpServers":{"a.b":{"command":"x"}}}`:                           "dots",
		`{"mcpServers":{"ok":{"command":"x"},"bad name":{"command":"x"}}}`: "spaces",
	} {
		if _, err := Load(write(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
}

func TestToolsFollowsPagesAndAnswersPings(t *testing.T) {
	c, log := start(t, "ok")
	tools, err := c.Tools(deadline(t))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "echo,fail,nested,write,huge,picture" {
		t.Errorf("tools %v", names)
	}
	if !tools[0].ReadOnly() || tools[3].ReadOnly() {
		t.Error("read-only hints misread")
	}
	c.Close()
	b, _ := os.ReadFile(log)
	methods := string(b)
	if !strings.HasPrefix(methods, "initialize\nnotifications/initialized\n") {
		t.Errorf("handshake out of order:\n%s", methods)
	}
	if strings.Contains(methods, "dropped") {
		t.Errorf("a request was sent with null params:\n%s", methods)
	}
	if !strings.Contains(methods, `response "srv-1"`) {
		t.Errorf("the server's ping went unanswered:\n%s", methods)
	}
}

func TestCall(t *testing.T) {
	c, _ := start(t, "ok")
	ctx := deadline(t)
	res, err := c.Call(ctx, "echo", json.RawMessage(`{"text":"hi","times":2}`))
	if err != nil || res.Text != "hi hi" || res.IsError {
		t.Errorf("echo: %+v %v", res, err)
	}
	res, err = c.Call(ctx, "fail", nil)
	if err != nil || !res.IsError || res.Text != "it broke" {
		t.Errorf("fail: %+v %v", res, err)
	}
	res, err = c.Call(ctx, "picture", nil)
	if err != nil || res.Text != "[image image/png]\na red square" {
		t.Errorf("picture: %+v %v", res, err)
	}
	dir := t.TempDir()
	res, err = c.Call(ctx, "write", json.RawMessage(`{"path":`+strings.ReplaceAll(`"`+filepath.Join(dir, "f")+`"`, `\`, `\\`)+`,"mode":"create"}`))
	if err != nil || res.IsError {
		t.Errorf("write: %+v %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "f")); string(b) != "written" {
		t.Error("the tool did not run")
	}
}

func TestAReplyTooLargeFailsOnlyThatCall(t *testing.T) {
	old := maxMessage
	maxMessage = 64 << 10
	t.Cleanup(func() { maxMessage = old })
	c, _ := start(t, "ok")
	ctx := deadline(t)
	// Just under the limit is fine, including a line longer than the
	// reader's own buffer.
	if res, err := c.Call(ctx, "huge", json.RawMessage(`{"bytes":60000}`)); err != nil || len(res.Text) != 60000 {
		t.Fatalf("under the limit: %d bytes, %v", len(res.Text), err)
	}
	_, err := c.Call(ctx, "huge", json.RawMessage(`{"bytes":200000}`))
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("over the limit: %v", err)
	}
	// The session survives it.
	if res, err := c.Call(ctx, "echo", json.RawMessage(`{"text":"still here"}`)); err != nil || res.Text != "still here" {
		t.Errorf("after a huge reply: %+v %v", res, err)
	}
}

func TestReadLine(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader("short\n"+strings.Repeat("y", 100)+"\nnext\nlast"), 16)
	for _, want := range []string{"short\n", "", "next\n", "last"} {
		line, err := readLine(br, 50)
		if want == "" {
			if err != errTooLarge {
				t.Errorf("long line: %q %v", line, err)
			}
			continue
		}
		if err != nil || string(line) != want {
			t.Errorf("got %q %v, want %q", line, err, want)
		}
	}
	if _, err := readLine(br, 50); err != io.EOF {
		t.Errorf("end: %v", err)
	}
}

func TestStrayOutputIsIgnored(t *testing.T) {
	c, _ := start(t, "noisy")
	if tools, err := c.Tools(deadline(t)); err != nil || len(tools) != 6 {
		t.Errorf("noisy server: %d tools, %v", len(tools), err)
	}
}

func TestAServerThatDiesSaysWhy(t *testing.T) {
	s, _ := fake(t, "crash")
	_, err := Start(context.Background(), "fake", s)
	if err == nil || !strings.Contains(err.Error(), "exited") || !strings.Contains(err.Error(), "missing API token") {
		t.Errorf("crash: %v", err)
	}
}

func TestAServerThatHangsTimesOut(t *testing.T) {
	s, _ := fake(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Start(ctx, "fake", s)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("hang: %v", err)
	}
	// Close had to kill it, but within its grace period.
	if time.Since(start) > 10*time.Second {
		t.Errorf("gave up only after %s", time.Since(start))
	}
}

func TestStartReportsAMissingProgram(t *testing.T) {
	_, err := Start(context.Background(), "ghost", Server{Command: "mote-no-such-server"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing program: %v", err)
	}
	if _, err := Start(context.Background(), "web", Server{URL: "https://example.com"}); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Errorf("remote server started: %v", err)
	}
}
