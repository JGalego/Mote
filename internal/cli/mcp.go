package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jgalego/mote/internal/mcp"
)

// MCP servers are one more source of tools for `mote agent`. They are
// listed in mcp.json in the config directory, started only when named with
// --mcp, and spoken to over stdin and stdout; mote does not reach servers
// over the network.

const (
	mcpStartTimeout = 30 * time.Second
	mcpCallTimeout  = 2 * time.Minute
)

func (a *app) mcpConfig() (mcp.Config, error) { return mcp.Load(mcp.ConfigPath(a.cfgDir)) }

// orderedKeys returns an object's keys in the order they were written,
// which encoding/json forgets. Arguments are generated in schema order, so
// a server's order is kept.
func orderedKeys(raw json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not an object")
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, t.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// scalarType reads a JSON schema "type", which may be a list such as
// ["string", "null"]; the first type that is not null is the one used.
func scalarType(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

// simpleValue reduces one argument's schema to what a grammar needs: a
// scalar type or an enum, or an array of those. Descriptions, formats and
// bounds are dropped. Anything else (nested objects, unions, references)
// is refused: llama.cpp converts some of it, but a small model filling it
// in is where calls go wrong, so such tools are skipped and said to be.
func simpleValue(name string, raw json.RawMessage) (map[string]any, string, error) {
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, "", fmt.Errorf("argument %s has no schema", name)
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf", "$ref", "not"} {
		if _, ok := s[k]; ok {
			return nil, "", fmt.Errorf("argument %s uses %s", name, k)
		}
	}
	if enum, ok := s["enum"].([]any); ok && len(enum) > 0 {
		return map[string]any{"enum": enum}, "one of " + enumList(enum), nil
	}
	switch t := scalarType(s["type"]); t {
	case "string", "integer", "number", "boolean":
		return map[string]any{"type": t}, t, nil
	case "array":
		items, _ := json.Marshal(s["items"])
		if s["items"] == nil {
			return nil, "", fmt.Errorf("argument %s is an array of unknown items", name)
		}
		inner, kind, err := simpleValue(name, items)
		if err != nil {
			return nil, "", err
		}
		if _, isArray := inner["items"]; isArray {
			return nil, "", fmt.Errorf("argument %s is an array of arrays", name)
		}
		return map[string]any{"type": "array", "items": inner}, "list of " + kind, nil
	case "object":
		return nil, "", fmt.Errorf("argument %s is an object", name)
	case "":
		return nil, "", fmt.Errorf("argument %s has no type", name)
	default:
		return nil, "", fmt.Errorf("argument %s has type %s", name, t)
	}
}

func enumList(enum []any) string {
	parts := make([]string, len(enum))
	for i, e := range enum {
		b, _ := json.Marshal(e)
		parts[i] = string(b)
	}
	return strings.Join(parts, "|")
}

// toolSchema turns an MCP input schema into a closed object schema that
// keeps the server's argument order and its required list, and the
// signature the prompt shows, e.g. search(query: string; limit?: integer).
func toolSchema(id string, raw json.RawMessage) (map[string]any, string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		raw = json.RawMessage(`{"type":"object"}`)
	}
	var top struct {
		Type       any             `json:"type"`
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, "", errors.New("its input schema is not an object")
	}
	if t := scalarType(top.Type); t != "" && t != "object" {
		return nil, "", fmt.Errorf("its input is a %s, not an object", t)
	}
	var names []string
	var byName map[string]json.RawMessage
	if len(top.Properties) > 0 {
		var err error
		if names, err = orderedKeys(top.Properties); err != nil {
			return nil, "", errors.New("its properties are not an object")
		}
		json.Unmarshal(top.Properties, &byName)
	}
	isRequired := map[string]bool{}
	for _, r := range top.Required {
		isRequired[r] = true
	}
	var ps []prop
	var required, sig []string
	for _, n := range names {
		v, kind, err := simpleValue(n, byName[n])
		if err != nil {
			return nil, "", err
		}
		ps = append(ps, prop{n, v})
		if isRequired[n] {
			required = append(required, n)
			sig = append(sig, n+": "+kind)
		} else {
			sig = append(sig, n+"?: "+kind)
		}
	}
	if required == nil {
		required = []string{}
	}
	schema := map[string]any{"type": "object", "properties": props(ps), "required": required, "additionalProperties": false}
	return schema, id + "(" + strings.Join(sig, "; ") + ")", nil
}

// maxSummary bounds a tool's description in the catalogue. Servers write
// paragraphs; a small model's prompt has room for a line each.
const maxSummary = 160

// summarize keeps a tool description to its first sentence, or failing
// that to the words that fit.
func summarize(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if i := strings.Index(s, ". "); i >= 0 && i < maxSummary {
		return s[:i+1]
	}
	if len(s) <= maxSummary {
		return s
	}
	cut := strings.LastIndex(s[:maxSummary], " ")
	if cut <= 0 {
		cut = maxSummary
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
	}
	return strings.TrimRight(s[:cut], " ,;:") + "…"
}

// mcpTool offers a server's tool to the agent. Tools the server does not
// mark read-only may change things, so they go through ask like sh does.
func mcpTool(c *mcp.Client, t mcp.Tool, ask func(string) bool) (agentTool, error) {
	id := c.Name() + "." + t.Name
	schema, sig, err := toolSchema(id, t.InputSchema)
	if err != nil {
		return agentTool{}, err
	}
	summary := summarize(t.Description)
	if summary == "" {
		summary = "Tool " + t.Name + " from the " + c.Name() + " server"
	}
	readOnly := t.ReadOnly()
	return agentTool{
		id: id, signature: sig, summary: summary, schema: schema, asks: !readOnly,
		run: func(ctx context.Context, args json.RawMessage) (string, error) {
			if !readOnly && !ask(id+" "+canonical(args)) {
				return "", errors.New("the user declined this call")
			}
			ctx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
			defer cancel()
			res, err := c.Call(ctx, t.Name, args)
			if err != nil {
				return "", err
			}
			if res.IsError {
				return "", errors.New(strings.TrimSpace(res.Text))
			}
			return res.Text, nil
		},
	}, nil
}

// startMCP starts the named servers and returns the tools they offer, with
// a note on stderr for each tool that cannot be offered. The caller closes
// the clients.
func (a *app) startMCP(ctx context.Context, names string, ask func(string) bool) ([]agentTool, []*mcp.Client, error) {
	cfg, err := a.mcpConfig()
	if err != nil {
		return nil, nil, err
	}
	var tools []agentTool
	var clients []*mcp.Client
	fail := func(err error) ([]agentTool, []*mcp.Client, error) {
		for _, c := range clients {
			c.Close()
		}
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, name := range strings.Split(names, ",") {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		s, ok := cfg.Servers[name]
		if !ok {
			return fail(usagef("no MCP server %q in %s; `mote mcp` lists them", name, mcp.ConfigPath(a.cfgDir)))
		}
		done := a.status("starting MCP server " + name)
		sctx, cancel := context.WithTimeout(ctx, mcpStartTimeout)
		c, err := mcp.Start(sctx, name, s)
		var list []mcp.Tool
		if err == nil {
			clients = append(clients, c)
			list, err = c.Tools(sctx)
		}
		cancel()
		done(err == nil)
		if err != nil {
			return fail(err)
		}
		for _, t := range list {
			tool, err := mcpTool(c, t, ask)
			if err != nil {
				fmt.Fprintf(a.err, "%s %s\n", a.ue.Warn(), a.ue.Dim(fmt.Sprintf("skipped %s.%s: %v", name, t.Name, err)))
				continue
			}
			tools = append(tools, tool)
		}
	}
	return tools, clients, nil
}

// mcpCmd implements `mote mcp`: list the configured servers, or with
// `tools`, start them and show what each offers and whether the agent can
// use it.
func (a *app) mcpCmd(ctx context.Context, args []string) error {
	cfg, err := a.mcpConfig()
	if err != nil {
		return err
	}
	path := mcp.ConfigPath(a.cfgDir)
	if len(args) == 0 {
		if len(cfg.Servers) == 0 {
			fmt.Fprintf(a.out, "no MCP servers; add them to %s, e.g.\n%s\n", path,
				`{"mcpServers": {"files": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]}}}`)
			return nil
		}
		for _, n := range cfg.Names() {
			s := cfg.Servers[n]
			fmt.Fprintf(a.out, "%s %s\n", a.uo.Bold(pad(n, 12)), strings.Join(append([]string{s.Command}, s.Args...), " "))
		}
		fmt.Fprintf(a.out, "%s\n", a.uo.Dim("from "+path+"; `mote mcp tools [NAME]` starts them and lists their tools"))
		return nil
	}
	if args[0] != "tools" {
		return usagef("usage: mote mcp [tools [NAME...]]")
	}
	names := args[1:]
	if len(names) == 0 {
		names = cfg.Names()
	}
	if len(names) == 0 {
		return fmt.Errorf("no MCP servers in %s", path)
	}
	never := func(string) bool { return false }
	for _, n := range names {
		s, ok := cfg.Servers[n]
		if !ok {
			return usagef("no MCP server %q in %s", n, path)
		}
		sctx, cancel := context.WithTimeout(ctx, mcpStartTimeout)
		c, err := mcp.Start(sctx, n, s)
		var list []mcp.Tool
		if err == nil {
			list, err = c.Tools(sctx)
			c.Close()
		}
		cancel()
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s %s\n", a.uo.Bold(n), a.uo.Dim(fmt.Sprintf("(%d tools)", len(list))))
		for _, t := range list {
			tool, err := mcpTool(c, t, never)
			switch {
			case err != nil:
				fmt.Fprintf(a.out, "  %s %s\n", a.uo.Yellow("skip"), a.uo.Dim(n+"."+t.Name+": "+err.Error()))
			case tool.asks:
				fmt.Fprintf(a.out, "  %s %s  %s\n", a.uo.Green("ok  "), tool.signature, a.uo.Dim(tool.summary+" · asks first"))
			default:
				fmt.Fprintf(a.out, "  %s %s  %s\n", a.uo.Green("ok  "), tool.signature, a.uo.Dim(tool.summary))
			}
		}
	}
	return nil
}
