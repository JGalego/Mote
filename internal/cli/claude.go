package cli

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
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jgalego/mote/internal/proc"
	"github.com/jgalego/mote/registry"
)

const (
	claudeDefaultModel  = "code"
	claudeContextTokens = 32768
	// claudeOutputTokens bounds a reply. Claude Code keeps room for one out
	// of the context window before it compacts, up to 20000 tokens unless
	// told otherwise, which in a 32K window leaves none for the conversation.
	claudeOutputTokens = 8192
	// claudeSettings turns off auto mode, whose safety classifier sends a
	// prompt of some 30K tokens before each tool call: about all of a small
	// model's context, and minutes of reading on a CPU.
	claudeSettings = `{"permissions":{"disableAutoMode":"disable"}}`
)

// claudeCmd runs Claude Code's agent and tools against a Mote model. The
// server belongs to this command, so it stays available while Claude is open
// even when keep_alive is off, then releases every model when Claude exits.
func (a *app) claudeCmd(ctx context.Context, args []string) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return missingf("Claude Code is not installed or is not on PATH")
	}
	model, err := a.claudeModel(claudeModelArg(args))
	if err != nil {
		return err
	}
	if err := a.ensure(ctx, model, a.cfg.AutoDownload); err != nil {
		return err
	}

	url, stop, err := a.startClaudeServer(ctx)
	if err != nil {
		return err
	}
	defer stop()
	window := max(model.Context, claudeContextTokens)
	done := a.status(fmt.Sprintf("loading %s with a %dK context", model.ID, window/1024))
	err = preloadClaudeModel(ctx, url, model.ID)
	done(err == nil)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, claude, claudeChildArgs(args)...)
	proc.Tree(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.in, a.out, a.err
	cmd.Env = claudeEnv(os.Environ(), url, model.ID, window)
	if err := cmd.Run(); err != nil {
		// Claude has already said why it stopped.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return childExit{exit.ExitCode()}
		}
		return fmt.Errorf("claude: %w", err)
	}
	return nil
}

// claudeModel resolves the model Claude Code is to use: a capability, which
// picks the model this configuration uses for it, or a model id.
func (a *app) claudeModel(name string) (*registry.Model, error) {
	if _, ok := registry.Capabilities[name]; ok {
		model, _, err := a.choose(name, a.cfg.Profile)
		if err != nil {
			return nil, err
		}
		if model.Backend != "llama.cpp" {
			return nil, usagef("%s cannot run Claude Code", model.ID)
		}
		return model, nil
	}
	model, ok := a.registry().Model(name)
	if !ok {
		return nil, usagef("unknown model %q; use a model id, code, or text", name)
	}
	if model.Backend != "llama.cpp" || (!model.Has("code") && !model.Has("text")) {
		return nil, usagef("model %s cannot run Claude Code", name)
	}
	return model, nil
}

// claudeModelArg is the model named by Claude Code's --model, or code.
func claudeModelArg(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if value, ok := strings.CutPrefix(args[i], "--model="); ok {
			return value
		}
		if args[i] == "--model" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return claudeDefaultModel
}

// claudeChildArgs passes the arguments on to Claude Code, adding --bare
// unless they ask for it or opt out of it with Mote's own --no-bare, and
// settings that turn off auto mode unless they set the permission mode or
// settings themselves.
func claudeChildArgs(args []string) []string {
	child := make([]string, 0, len(args)+3)
	bare, full, own := false, false, false
	for i, arg := range args {
		if arg == "--" {
			child = append(child, args[i:]...)
			break
		}
		if arg == "--no-bare" {
			full = true
			continue
		}
		if arg == "--bare" {
			bare = true
		}
		name, _, _ := strings.Cut(arg, "=")
		if name == "--settings" || name == "--permission-mode" {
			own = true
		}
		child = append(child, arg)
	}
	if !own {
		child = append([]string{"--settings", claudeSettings}, child...)
	}
	if !full && !bare {
		child = append([]string{"--bare"}, child...)
	}
	return child
}

// startClaudeServer starts a model server on a loopback port of its own,
// with the context Claude Code needs, and returns its URL and how to stop
// it. It writes no log: Claude Code owns the terminal while it runs.
func (a *app) startClaudeServer(ctx context.Context) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("start Claude model server: %w", err)
	}
	keep := a.keepAlive()
	if keep == 0 {
		keep = 5 * time.Minute
	}
	srv := a.newServerWithContext(keep, claudeContextTokens)
	serverCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(serverCtx, ln) }()
	url := "http://" + ln.Addr().String()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := health(url); ok {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			srv.Stop()
			<-done
			return "", nil, fmt.Errorf("Claude model server did not start")
		}
		select {
		case <-ctx.Done():
			cancel()
			srv.Stop()
			<-done
			return "", nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		srv.Stop()
		<-done
	}
	return url, stop, nil
}

// preloadClaudeModel loads the model before Claude Code starts, so a model
// that cannot load is reported here and not as Claude retrying its request.
func preloadClaudeModel(ctx context.Context, url, model string) error {
	payload, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/mote/load", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("load Claude model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error.Message != "" {
		return fmt.Errorf("load Claude model: %s", body.Error.Message)
	}
	return fmt.Errorf("load Claude model: %s", strings.TrimSpace(string(raw)))
}

// claudeEnv points Claude Code at the server and keeps it there: every model
// it may ask for, for the main loop, subagents or small background calls, is
// the one model loaded, and credentials that would send it elsewhere are
// dropped.
func claudeEnv(environ []string, url, model string, contextTokens int) []string {
	set := map[string]string{
		"ANTHROPIC_BASE_URL": url,
		// A token, unlike ANTHROPIC_API_KEY, does not make Claude Code ask
		// whether to use it, with No as the default answer.
		"ANTHROPIC_AUTH_TOKEN":                     "mote-local",
		"ANTHROPIC_MODEL":                          model,
		"ANTHROPIC_DEFAULT_FABLE_MODEL":            model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":             model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL":           model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":            model,
		"ANTHROPIC_SMALL_FAST_MODEL":               model,
		"CLAUDE_CODE_SUBAGENT_MODEL":               model,
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":           strconv.Itoa(contextTokens),
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	}
	clear := map[string]bool{
		"ANTHROPIC_API_KEY":        true,
		"ANTHROPIC_API_HOST":       true,
		"ANTHROPIC_CUSTOM_HEADERS": true,
		"CLAUDE_CODE_OAUTH_TOKEN":  true,
		"CLAUDE_CODE_USE_BEDROCK":  true,
		"CLAUDE_CODE_USE_VERTEX":   true,
		"CLAUDE_CODE_USE_FOUNDRY":  true,
	}
	out := make([]string, 0, len(environ)+len(set)+1)
	outputSet := false
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := set[key]; replaced || clear[key] {
			continue
		}
		outputSet = outputSet || key == "CLAUDE_CODE_MAX_OUTPUT_TOKENS"
		out = append(out, item)
	}
	for key, value := range set {
		out = append(out, key+"="+value)
	}
	if !outputSet {
		out = append(out, "CLAUDE_CODE_MAX_OUTPUT_TOKENS="+strconv.Itoa(claudeOutputTokens))
	}
	return out
}
