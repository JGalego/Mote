package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolSchemaKeepsWhatAGrammarNeeds(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{
		"text":{"type":"string","description":"what to say"},
		"times":{"type":"integer","minimum":1},
		"loud":{"type":["null","boolean"]},
		"mode":{"type":"string","enum":["create","append"]},
		"tags":{"type":"array","items":{"type":"string"}}},
		"required":["text","mode"]}`)
	schema, sig, err := toolSchema("s.t", raw)
	if err != nil {
		t.Fatal(err)
	}
	if sig != `s.t(text: string; times?: integer; loud?: boolean; mode: one of "create"|"append"; tags?: list of string)` {
		t.Errorf("signature %s", sig)
	}
	b, _ := json.Marshal(schema)
	got := string(b)
	// The server's order survives, and so does its required list.
	if !(strings.Index(got, `"text"`) < strings.Index(got, `"times"`) && strings.Index(got, `"times"`) < strings.Index(got, `"loud"`) &&
		strings.Index(got, `"loud"`) < strings.Index(got, `"mode"`)) {
		t.Errorf("argument order lost: %s", got)
	}
	for _, want := range []string{`"required":["text","mode"]`, `"additionalProperties":false`, `"enum":["create","append"]`, `"loud":{"type":"boolean"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("schema lacks %s: %s", want, got)
		}
	}
	for _, drop := range []string{"description", "minimum"} {
		if strings.Contains(got, drop) {
			t.Errorf("schema kept %s: %s", drop, got)
		}
	}

	// No schema at all is a tool without arguments.
	for _, empty := range []string{``, `null`, `{"type":"object"}`} {
		schema, sig, err := toolSchema("s.t", json.RawMessage(empty))
		if err != nil || sig != "s.t()" {
			t.Errorf("%q: %s %v", empty, sig, err)
		}
		if b, _ := json.Marshal(schema); !strings.Contains(string(b), `"required":[]`) {
			t.Errorf("%q: %s", empty, b)
		}
	}

	for body, want := range map[string]string{
		`{"type":"object","properties":{"o":{"type":"object"}}}`:                          "o is an object",
		`{"type":"object","properties":{"o":{"anyOf":[{"type":"string"}]}}}`:              "anyOf",
		`{"type":"object","properties":{"o":{"$ref":"#/defs/x"}}}`:                        "$ref",
		`{"type":"object","properties":{"o":{"type":"array","items":{"type":"object"}}}}`: "object",
		`{"type":"object","properties":{"o":{"type":"array"}}}`:                           "unknown items",
		`{"type":"object","properties":{"o":{}}}`:                                         "no type",
		`{"type":"string"}`: "not an object",
	} {
		if _, _, err := toolSchema("s.t", json.RawMessage(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
}

func TestSummarizeKeepsTheFirstSentence(t *testing.T) {
	long := strings.Repeat("word ", 60)
	for in, want := range map[string]string{
		"Read a file. Handles text and more.": "Read a file.",
		"Read a file\nwith detail below":      "Read a file",
		"v1.2 of the reader":                  "v1.2 of the reader",
		long:                                  strings.TrimSpace(strings.Repeat("word ", 32)) + "…",
		strings.Repeat("é", 200):              "",
	} {
		got := summarize(in)
		if want != "" && got != want {
			t.Errorf("%.30q: %q, want %q", in, got, want)
		}
		if len(got) > maxSummary+len("…") || !utf8.ValidString(got) {
			t.Errorf("%.30q: %q is too long or not UTF-8", in, got)
		}
	}
}

// mcpServers writes mcp.json with fake servers, each running the test
// binary in the given mode.
func (e *env) mcpServers(modes map[string]string) {
	e.t.Helper()
	self, err := os.Executable()
	if err != nil {
		e.t.Fatal(err)
	}
	servers := map[string]any{}
	for name, mode := range modes {
		servers[name] = map[string]any{"command": self, "env": map[string]string{"MOTE_FAKE_MCP": mode}}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": servers})
	if err := os.WriteFile(filepath.Join(e.cfgDir, "mcp.json"), b, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func TestMCPCommand(t *testing.T) {
	e := agentEnv(t)
	code, out, _ := e.mote("", "mcp")
	if code != 0 || !strings.Contains(out, "no MCP servers") || !strings.Contains(out, "mcp.json") {
		t.Errorf("no servers: %d %s", code, out)
	}
	e.mcpServers(map[string]string{"fake": "ok", "broken": "crash"})
	code, out, _ = e.mote("", "mcp")
	if code != 0 || !strings.Contains(out, "fake") || !strings.Contains(out, "broken") {
		t.Errorf("list: %d %s", code, out)
	}
	code, out, errs := e.mote("", "mcp", "tools", "fake")
	if code != 0 {
		t.Fatalf("tools: %d %s", code, errs)
	}
	// The fake builds its schemas from Go maps, so it sends properties in
	// alphabetical order; that mote keeps a server's order is checked above.
	for _, want := range []string{"fake (6 tools)", "fake.echo(loud?: boolean; text: string; times?: integer)", "Repeat some text.",
		"fake.nested: argument opts is an object", "asks first"} {
		if !strings.Contains(out, want) {
			t.Errorf("tools output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "More detail") {
		t.Error("the description was not cut to its first line")
	}
	if code, _, errs := e.mote("", "mcp", "tools", "broken"); code == 0 || !strings.Contains(errs, "missing API token") {
		t.Errorf("a crashing server: %d %s", code, errs)
	}
	if code, _, _ := e.mote("", "mcp", "tools", "ghost"); code != ExitUsage {
		t.Errorf("unknown server: %d", code)
	}
	if code, _, _ := e.mote("", "mcp", "nonsense"); code != ExitUsage {
		t.Errorf("unknown subcommand: %d", code)
	}
	os.WriteFile(filepath.Join(e.cfgDir, "mcp.json"), []byte(`{"mcpServers":{"web":{"url":"https://example.com/mcp"}}}`), 0o644)
	if code, _, errs := e.mote("", "mcp"); code == 0 || !strings.Contains(errs, "remote") {
		t.Errorf("remote server: %d %s", code, errs)
	}
}

func TestAgentCallsMCPTools(t *testing.T) {
	e := agentEnv(t)
	e.mcpServers(map[string]string{"fake": "ok"})
	log := script(t,
		`{"thought":"t","action":{"tool":"fake.echo","args":{"text":"hi","times":2,"loud":true}}}`,
		`{"thought":"t","action":{"tool":"fake.fail","args":{}}}`,
		`{"thought":"t","action":{"tool":"fake.picture","args":{}}}`,
		`{"thought":"t","action":{"tool":"finish","args":{"answer":"done"}}}`)
	// Only read-only tools are named, so nothing needs asking about.
	code, out, errs := e.mote("", "agent", "--mcp", "fake", "--tools", "fake.echo,fake.fail,fake.picture,chat", "say hi twice")
	if code != 0 || out != "done\n" {
		t.Fatalf("agent: %d %q %s", code, out, errs)
	}
	if !strings.Contains(errs, "skipped fake.nested") {
		t.Errorf("the unusable tool was not reported:\n%s", errs)
	}
	steps := constrained(t, log)
	first := steps[0]
	if p := first["prompt"].(string); !strings.Contains(p, "fake.echo(loud?: boolean; text: string") || strings.Contains(p, "fake.write") {
		t.Errorf("tool list:\n%s", p)
	}
	if f := string(mustMarshal(first["format"])); !strings.Contains(f, `"const":"fake.echo"`) || strings.Contains(f, `"const":"fake.nested"`) {
		t.Errorf("schema offers the wrong tools: %s", f)
	}
	last := steps[3]["prompt"].(string)
	for _, want := range []string{"Observation: HI HI", "Observation: error: it broke", "Observation: [image image/png]\na red square"} {
		if !strings.Contains(last, want) {
			t.Errorf("observations lack %q:\n%s", want, last)
		}
	}
}

func TestAgentAsksBeforeMCPToolsThatChangeThings(t *testing.T) {
	e := agentEnv(t)
	e.mcpServers(map[string]string{"fake": "ok"})
	target := filepath.Join(t.TempDir(), "out.txt")
	write := `{"thought":"t","action":{"tool":"fake.write","args":{"path":` + jsonStr(target) + `,"mode":"create"}}}`
	done := `{"thought":"t","action":{"tool":"finish","args":{"answer":"ok"}}}`

	// fake.write is not marked read-only; with no terminal to ask on,
	// offering it needs --yes.
	script(t, write, done)
	if code, _, errs := e.mote("", "agent", "--mcp", "fake", "x"); code != ExitUsage || !strings.Contains(errs, "fake.write") {
		t.Errorf("unasked write: %d %s", code, errs)
	}
	// A whole server can be named in --tools.
	if code, _, errs := e.mote("", "agent", "--mcp", "fake", "--tools", "fake", "x"); code != ExitUsage || !strings.Contains(errs, "fake.write") {
		t.Errorf("server in --tools: %d %s", code, errs)
	}

	t.Setenv("MOTE_FORCE_LIVE", "1")
	log := script(t, write, done)
	if code, _, errs := e.mote("n\n", "agent", "--mcp", "fake", "x"); code != 0 || !strings.Contains(errs, "fake.write") {
		t.Fatalf("declined: %d %s", code, errs)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a declined call ran")
	}
	if p := constrained(t, log)[1]["prompt"].(string); !strings.Contains(p, "declined") {
		t.Errorf("the model was not told:\n%s", p)
	}
	script(t, write, done)
	if code, _, errs := e.mote("y\n", "agent", "--mcp", "fake", "x"); code != 0 {
		t.Fatalf("accepted: %d %s", code, errs)
	}
	if b, _ := os.ReadFile(target); string(b) != "written" {
		t.Error("an accepted call did not run")
	}
	t.Setenv("MOTE_FORCE_LIVE", "")

	if code, _, errs := e.mote("", "agent", "--mcp", "ghost", "x"); code != ExitUsage || !strings.Contains(errs, "ghost") {
		t.Errorf("unknown server: %d %s", code, errs)
	}
	e.mcpServers(map[string]string{"broken": "crash"})
	if code, _, errs := e.mote("", "agent", "--mcp", "broken", "x"); code == 0 || !strings.Contains(errs, "missing API token") {
		t.Errorf("crashing server: %d %s", code, errs)
	}
}
