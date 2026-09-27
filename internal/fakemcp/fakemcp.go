// Package fakemcp lets tests stand in for an MCP server by re-executing the
// test binary. Call MaybeRun at the top of TestMain; a server entry whose
// command is the test binary and whose env sets MOTE_FAKE_MCP then runs it.
//
// MOTE_FAKE_MCP picks the behaviour: "ok" serves the tools below, "crash"
// exits at once with a message on stderr, "hang" never answers, and "noisy"
// writes a non-JSON line to stdout first, as some servers' logging does.
// MOTE_FAKE_MCP_LOG, when set, gets every method the server receives.
package fakemcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Tools are what the fake serves, over two pages of tools/list.
var pages = [][]map[string]any{
	{
		{"name": "echo", "description": "Repeat some text.\nMore detail the model need not see.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"text":  map[string]any{"type": "string", "description": "what to say"},
				"times": map[string]any{"type": "integer", "minimum": 1},
				"loud":  map[string]any{"type": []any{"boolean", "null"}},
			}, "required": []any{"text"}},
			"annotations": map[string]any{"readOnlyHint": true}},
		{"name": "fail", "description": "Always fails",
			"inputSchema": map[string]any{"type": "object"},
			"annotations": map[string]any{"readOnlyHint": true}},
	},
	{
		{"name": "nested", "description": "Takes an object the grammar will not get",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"opts": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
			}}},
		{"name": "write", "description": "Write a file",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"},
				"mode": map[string]any{"type": "string", "enum": []any{"create", "append"}},
			}, "required": []any{"path", "mode"}}},
		{"name": "huge", "description": "Returns as many bytes as asked for",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"bytes": map[string]any{"type": "integer"}}, "required": []any{"bytes"}},
			"annotations": map[string]any{"readOnlyHint": true}},
		{"name": "picture", "description": "Returns an image and a caption",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			"annotations": map[string]any{"readOnlyHint": true}},
	},
}

// MaybeRun turns the process into the fake server when asked, and exits.
func MaybeRun() {
	mode := os.Getenv("MOTE_FAKE_MCP")
	if mode == "" {
		return
	}
	os.Exit(serve(mode))
}

func logMethod(m string) {
	if p := os.Getenv("MOTE_FAKE_MCP_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, m)
			f.Close()
		}
	}
}

func serve(mode string) int {
	if mode == "crash" {
		fmt.Fprintln(os.Stderr, "fake server: missing API token")
		return 1
	}
	out := json.NewEncoder(os.Stdout)
	if mode == "noisy" {
		fmt.Println("starting fake server v0 (not JSON)")
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	pinged := false
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			continue
		}
		if req.Method == "" {
			logMethod("response " + string(req.ID))
			continue
		}
		logMethod(req.Method)
		if mode == "hang" || len(req.ID) == 0 {
			continue
		}
		// Like the reference servers, a request whose params are null
		// fails validation and gets no answer at all.
		if string(req.Params) == "null" {
			logMethod("dropped " + req.Method + " with null params")
			continue
		}
		reply := func(result any) {
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "fake", "version": "0"}})
		case "tools/list":
			// Servers may ask things of the client, and say things it
			// need not answer, at any time.
			if !pinged {
				out.Encode(map[string]any{"jsonrpc": "2.0", "id": "srv-1", "method": "ping"})
				out.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"level": "info", "data": "hi"}})
				pinged = true
			}
			var p struct {
				Cursor string `json:"cursor"`
			}
			json.Unmarshal(req.Params, &p)
			if p.Cursor == "" {
				reply(map[string]any{"tools": pages[0], "nextCursor": "page-2"})
			} else {
				reply(map[string]any{"tools": pages[1]})
			}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			json.Unmarshal(req.Params, &p)
			reply(call(p.Name, p.Arguments))
		default:
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "no such method " + req.Method}})
		}
	}
	return 0
}

func text(s string, isErr bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}, "isError": isErr}
}

func call(name string, args map[string]any) map[string]any {
	switch name {
	case "echo":
		n := 1
		if t, ok := args["times"].(float64); ok {
			n = int(t)
		}
		s := strings.TrimSpace(strings.Repeat(fmt.Sprint(args["text"])+" ", n))
		if args["loud"] == true {
			s = strings.ToUpper(s)
		}
		return text(s, false)
	case "fail":
		return text("it broke", true)
	case "write":
		path := fmt.Sprint(args["path"])
		if err := os.WriteFile(path, []byte("written"), 0o644); err != nil {
			return text(err.Error(), true)
		}
		return text("wrote "+path, false)
	case "huge":
		n, _ := args["bytes"].(float64)
		return text(strings.Repeat("x", int(n)), false)
	case "picture":
		return map[string]any{"content": []any{
			map[string]any{"type": "image", "mimeType": "image/png", "data": "iVBORw0KGgo="},
			map[string]any{"type": "text", "text": "a red square"},
		}}
	}
	return text("unknown tool "+name, true)
}
