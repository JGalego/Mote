package cli

import (
	"fmt"
	"strings"

	"github.com/jgalego/mote/internal/task"
)

// topLevelCommands mirrors the cases in dispatch, so shell completion offers
// exactly the commands mote actually accepts.
var topLevelCommands = []string{
	"help", "version", "setup", "config", "doctor", "tasks", "mcp", "update",
	"run", "pipe", "listen", "do", "agent", "memory", "remember", "forget",
	"models", "bench", "tune", "serve", "chat", "index", "ask", "completion", "guide", "nb", "meta", "kernel",
}

// commandFlags lists the flags each command's flags() call accepts, kept
// next to those call sites so completion stays honest about them.
var commandFlags = map[string][]string{
	"run":     {"-o", "--output", "--model", "--profile", "--apply", "--continue", "--recall", "--yes", "-y", "--help", "-h"},
	"pipe":    {"-o", "--output", "--model", "--profile", "--apply", "--trace", "--yes", "-y", "--help", "-h"},
	"do":      {"-o", "--output", "--model", "--profile", "--router", "--apply", "--dry-run", "--continue", "--recall", "--plan", "--trace", "--yes", "-y", "--help", "-h"},
	"agent":   {"-o", "--output", "--model", "--profile", "--tools", "--steps", "--mcp", "--sandbox", "--allow-sh", "--yes", "-y", "--sandbox-net", "--help", "-h"},
	"listen":  {"--wake", "--device", "--chunk", "--model", "--profile", "--once", "--help", "-h"},
	"chat":    {"--model", "--profile", "--system", "--continue", "--recall", "--tools", "--steps", "--yes", "-y", "--help", "-h"},
	"nb":      {"-o", "--output", "--model", "--profile", "--force", "--dry-run", "--state", "--yes", "-y", "--help", "-h"},
	"meta":    {"-o", "--output", "--model", "--profile", "--run", "--force", "--yes", "-y", "--help", "-h"},
	"kernel":  {"--python", "--dir", "--help", "-h"},
	"guide":   {"--model", "--profile", "--help", "-h"},
	"ask":     {"--in", "--top", "--model", "--profile", "-o", "--output", "--sources", "--help", "-h"},
	"index":   {"--profile", "--help", "-h"},
	"memory":  {"--model", "--profile", "--help", "-h"},
	"bench":   {"--model", "--full", "--strict", "--help", "-h"},
	"serve":   {"--port", "--keep-alive", "--background", "--help", "-h"},
	"setup":   {"--config", "--profile", "--data-dir", "--yes", "-y", "--auto-download", "--no-download", "--gpu", "--no-gpu", "--help", "-h"},
	"forget":  {"--all", "--history", "--help", "-h"},
	"update":  {"--check", "--help", "-h"},
	"tune":    {"--apply", "--help", "-h"},
	"doctor":  {"--help", "-h"},
	"version": {"--help", "-h"},
}

var completionScripts = map[string]string{
	"bash": `_mote_complete() {
	local cur words
	words=("${COMP_WORDS[@]:1:COMP_CWORD}")
	cur="${COMP_WORDS[COMP_CWORD]}"
	COMPREPLY=($(compgen -W "$(mote __complete "${words[@]}")" -- "$cur"))
}
complete -F _mote_complete -o default -o bashdefault mote
`,
	"zsh": `autoload -Uz bashcompinit
bashcompinit
_mote_complete() {
	local cur words
	words=("${COMP_WORDS[@]:1:COMP_CWORD}")
	cur="${COMP_WORDS[COMP_CWORD]}"
	COMPREPLY=($(compgen -W "$(mote __complete "${words[@]}")" -- "$cur"))
}
complete -F _mote_complete -o default -o bashdefault mote
`,
	"fish": `function __mote_complete
	set -l tokens (commandline -opc)
	set -l cur (commandline -ct)
	mote __complete $tokens[2..-1] $cur
end
complete -c mote -a '(__mote_complete)'
`,
}

// completionCmd implements `mote completion bash|zsh|fish`: it prints a
// script that, once sourced, calls the hidden `mote __complete` below.
func (a *app) completionCmd(args []string) error {
	if len(args) == 1 {
		if script, ok := completionScripts[args[0]]; ok {
			fmt.Fprint(a.out, script)
			return nil
		}
	}
	return usagef("usage: mote completion bash|zsh|fish")
}

// completeCmd implements the hidden `mote __complete`, which shells call
// with the words typed so far (the last one possibly a partial word) and
// expect one candidate per line back.
func (a *app) completeCmd(args []string) error {
	for _, c := range a.complete(args) {
		fmt.Fprintln(a.out, c)
	}
	return nil
}

func (a *app) complete(args []string) []string {
	if len(args) == 0 {
		args = []string{""}
	}
	cur := args[len(args)-1]
	prior := args[:len(args)-1]

	var cands []string
	switch {
	case len(prior) == 0:
		cands = topLevelCommands
	case prior[0] == "run" && len(prior) == 1:
		cands = a.taskNames()
	case prior[0] == "completion" && len(prior) == 1:
		cands = []string{"bash", "zsh", "fish"}
	case prior[0] == "config" && len(prior) == 1:
		cands = []string{"show", "path", "set", "history", "rollback", "edit"}
	case prior[0] == "mcp" && len(prior) == 1:
		cands = []string{"tools"}
	case prior[0] == "memory" && len(prior) == 1:
		cands = []string{"search"}
	case prior[0] == "index" && len(prior) == 1:
		cands = []string{"status", "rm"}
	case prior[0] == "serve" && len(prior) == 1:
		cands = append([]string{"status", "stop"}, commandFlags["serve"]...)
	case prior[0] == "models":
		cands = a.completeModels(prior[1:])
	default:
		if strings.HasPrefix(cur, "-") {
			cands = commandFlags[prior[0]]
		}
	}
	return filterPrefix(cands, cur)
}

func (a *app) completeModels(prior []string) []string {
	if len(prior) == 0 {
		return []string{"list", "pull", "upgrade", "rm", "why", "verify"}
	}
	if len(prior) > 1 {
		return nil
	}
	switch prior[0] {
	case "pull":
		return append(append(a.modelIDs(), capNames()...), "--all", "--missing")
	case "rm", "remove":
		return a.modelIDs()
	case "why":
		return capNames()
	}
	return nil
}

func (a *app) modelIDs() []string {
	reg := a.registry()
	ids := make([]string, len(reg.Models))
	for i, m := range reg.Models {
		ids[i] = m.ID
	}
	return ids
}

// taskNames lists the tasks `mote run` accepts, falling back to the built-in
// set if the on-disk custom tasks can't be read.
func (a *app) taskNames() []string {
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		tasks, _ = task.Load()
	}
	names := make([]string, len(tasks))
	for i, t := range tasks {
		names[i] = t.ID
	}
	return names
}

func filterPrefix(cands []string, prefix string) []string {
	if prefix == "" {
		return cands
	}
	var out []string
	for _, c := range cands {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}
