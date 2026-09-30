package cli

import (
	"fmt"
	"strings"
)

// helpFlag pairs a flag's syntax, or a subcommand's form, with its
// description; both are printed as a two-column list.
type helpFlag struct {
	name string
	desc string
}

// helpTopic documents one top-level command in more depth than its one-line
// summary in usage, for `mote help CMD` and `mote CMD --help`.
type helpTopic struct {
	summary  string
	usage    []string   // full invocation lines, e.g. "mote chat [--system ...]"
	subs     []helpFlag // subcommand forms, when the command has any
	flags    []helpFlag // flags, most useful first
	notes    []string   // short paragraphs printed after subs/flags
	examples []string   // full command lines, e.g. `mote chat --system "..."`
}

var helpTopics = map[string]helpTopic{
	"help": {
		summary: "Show general usage, or detailed help for one command.",
		usage:   []string{"mote help [COMMAND]"},
		notes:   []string{"`mote COMMAND --help` (or -h) shows the same thing."},
		examples: []string{
			"mote help",
			"mote help chat",
			"mote models --help",
		},
	},
	"version": {
		summary:  "Print the version mote was built from.",
		usage:    []string{"mote version"},
		examples: []string{"mote version"},
	},
	"setup": {
		summary: "Install the runtime and the models for a profile.",
		usage:   []string{"mote setup [--yes] [--config FILE] [--profile P] [--data-dir DIR] [--auto-download|--no-download] [--gpu|--no-gpu]"},
		flags: []helpFlag{
			{"--yes, -y", "accept every prompt's default and run non-interactively"},
			{"--config FILE", "write the configuration to FILE instead of the default location"},
			{"--profile P", "preselect a profile (small, balanced, quality) instead of asking"},
			{"--data-dir DIR", "store models and other data under DIR"},
			{"--auto-download", "let mote download models it needs without asking each time"},
			{"--no-download", "configure mote without downloading anything yet"},
			{"--gpu", "offload inference to a detected GPU"},
			{"--no-gpu", "stay on the CPU even if a GPU is detected"},
		},
		examples: []string{
			"mote setup",
			"mote setup --profile small --yes",
			"mote setup --no-download --config ~/mote.json",
		},
	},
	"config": {
		summary: "Show or change mote's configuration; every version is kept.",
		usage: []string{
			"mote config [show]",
			"mote config path",
			"mote config set KEY VALUE",
			"mote config history",
			"mote config rollback [N]",
			"mote config edit",
		},
		subs: []helpFlag{
			{"show", "print the current configuration as JSON (default with no subcommand)"},
			{"path", "print the path to the configuration file"},
			{"set KEY VALUE", "change one key: profile, editor, workspace, data_dir, auto_download, reuse_tools, llama_dir, threads, repack, gpu, keep_alive, serve_port, wake_word, router, memory, models.<capability>, tools.<name>"},
			{"history", "list every saved version of the configuration"},
			{"rollback [N]", "restore the N-th most recent version (default 1)"},
			{"edit", "open the configuration file in $VISUAL or $EDITOR"},
		},
		examples: []string{
			"mote config show",
			"mote config set gpu on",
			"mote config set keep_alive 30m",
			"mote config rollback",
		},
	},
	"doctor": {
		summary:  "Check the installation: runtime, models, tools, data directory.",
		usage:    []string{"mote doctor"},
		examples: []string{"mote doctor"},
	},
	"tasks": {
		summary:  "List the tasks `mote run` can run, their arguments and what each one needs.",
		usage:    []string{"mote tasks"},
		examples: []string{"mote tasks"},
	},
	"mcp": {
		summary: "List the configured MCP servers, or start them and show what they offer.",
		usage:   []string{"mote mcp", "mote mcp tools [NAME...]"},
		subs: []helpFlag{
			{"(none)", "list the servers configured in mcp.json"},
			{"tools [NAME...]", "start the named servers (or all of them) and list their tools, and whether the agent can use each one"},
		},
		examples: []string{
			"mote mcp",
			"mote mcp tools",
			"mote mcp tools files",
		},
	},
	"update": {
		summary: "Update mote to the latest release, or check whether one exists.",
		usage:   []string{"mote update [--check]"},
		flags: []helpFlag{
			{"--check", "report whether an update is available without installing it"},
		},
		examples: []string{"mote update --check", "mote update"},
	},
	"run": {
		summary: "Run one of mote's built-in or custom tasks.",
		usage:   []string{"mote run TASK [ARGS...] [-o FILE] [--apply] [--model ID] [--profile P] [--continue] [--recall] [--yes|-y]"},
		flags: []helpFlag{
			{"-o, --output FILE", "write the result to FILE instead of stdout"},
			{"--model ID", "use this model for every capability it provides, instead of your profile's pick"},
			{"--profile P", "use this profile (small, balanced, quality) for this run"},
			{"--apply", "write a patch task's proposed changes to disk instead of only printing the diff"},
			{"--continue", "carry on from the task's last exchange, e.g. chat"},
			{"--recall", "bring back the closest past exchanges as context before answering"},
			{"--yes, -y", "approve confirmations mote would otherwise ask about"},
		},
		notes: []string{"Run `mote tasks` to see every task, its arguments and what it needs."},
		examples: []string{
			`mote run chat "Explain what a mutex is in two sentences"`,
			`mote run code "Python function that parses ISO 8601 dates" -o dates.py`,
			`mote run describe photo.jpg "What is written on the sign?"`,
			`mote run patch ./src "Rename the function load to read_config" --apply`,
		},
	},
	"pipe": {
		summary: "Chain tasks in one process, each stage receiving the last one's value.",
		usage:   []string{`mote pipe "A | B | sh: cmd" [-o FILE] [--model ID] [--profile P] [--apply] [--trace] [--yes|-y]`},
		flags: []helpFlag{
			{"-o, --output FILE", "write the last stage's result to FILE instead of stdout"},
			{"--model ID", "use this model for every capability it provides"},
			{"--profile P", "use this profile for this run"},
			{"--apply", "write a patch stage's changes to disk"},
			{"--trace", "print every stage's output, not only the last one"},
			{"--yes, -y", "approve confirmations mote would otherwise ask about"},
		},
		notes: []string{"Each stage gets the previous value as {} or - (or its first missing argument); `sh:` pipes it into a shell command."},
		examples: []string{
			`mote pipe "transcribe meeting.m4a | chat 'Summarise in 3 bullets: {}'"`,
			`mote pipe "frames clip.mp4 3 | describe | sh: tee notes.txt"`,
			`mote pipe --trace "code 'print the squares of 1 to 5 in Python' | sh: python3 -"`,
		},
	},
	"nb": {
		summary: "Run a motebook, or type into one: a Markdown file whose `mote` cells are tasks or pipelines, with each output kept under its cell.",
		usage: []string{
			"mote nb run FILE [-o FILE|-] [--force] [--dry-run] [--yes|-y] [--model ID] [--profile P]",
			"mote nb console [FILE | -o FILE] [--yes|-y] [--model ID] [--profile P]",
			"mote nb exec [--state FILE] [--yes|-y] [--model ID] [--profile P]",
			"mote nb export FILE [-o FILE|-] [--force]",
			"mote nb import FILE.ipynb [-o FILE|-] [--force] [--any-kernel]",
		},
		subs: []helpFlag{
			{"run FILE", "run the cells in order, saving FILE after each one"},
			{"console [FILE]", "type cells one at a time, like `jupyter console`: each runs as you enter it, its output printed below; kept in FILE, or asked about when you leave if there is none"},
			{"exec", "run the one cell on standard input and print its output; what a Jupyter kernel calls for each cell"},
			{"export FILE", "write a motebook as a Jupyter notebook for the mote kernel (FILE.ipynb)"},
			{"import FILE.ipynb", "read a Jupyter notebook as a motebook (FILE.mote.md)"},
		},
		flags: []helpFlag{
			{"-o, --output FILE", "with run, write the notebook to FILE instead of FILE itself; with export and import, name the result; - prints it. With console and no FILE, save the session to FILE when it ends"},
			{"--force", "with run, run every cell, even those whose text and inputs are unchanged; with export and import, replace the file if it exists"},
			{"--any-kernel", "with import, read a notebook written for a kernel other than mote"},
			{"--dry-run", "with run, show which cells would run, without running them"},
			{"--state FILE", "with exec, the JSON file holding the values earlier cells bound; read before the cell runs and written after"},
			{"--yes, -y", "run cells that can change things (shell stages, tasks that ask) without asking"},
			{"--model ID", "use this model for every capability it provides"},
			{"--profile P", "use this profile for this run"},
		},
		notes: []string{
			"A cell is a ```mote fence holding a task or a pipeline, as `mote pipe` takes it; ```mote as=NAME keeps its output for later cells as {{NAME}}. Values are filled into arguments, never into `sh:` stages.",
			"The output goes in an output fence right below the cell. Its key fingerprints the cell and what it read, so a rerun skips cells that have nothing new to compute.",
			"In `nb console`, a line is a cell, `NAME = pipeline` binds its output, and a line ending in \\ continues. A cell that fails is not kept.",
			"At a terminal the console edits the line: the arrow keys, Home, End and Delete move and change it, Up and Down recall earlier cells, and Tab completes /commands, tasks, {{names}} and file names. /tasks lists what a cell can run, /examples shows cells to try, /vars what you have bound, /cells what you have run, /note TEXT adds a paragraph of text, /embed FILE adds an image, audio or video, /undo drops the last thing added, /save FILE keeps them in a file, and /help and /exit do what they say.",
			"export and import move a notebook between a motebook and a Jupyter notebook: prose is kept as markdown cells, `NAME = pipeline` is how a Jupyter cell writes as=NAME, and what a cell printed goes with it. An imported output has no fingerprint, so `nb run` runs that cell again.",
			"With no FILE a console session lives in memory: on exit at a terminal mote asks whether to save the cells you ran, and where; without a terminal it says they were dropped unless you gave -o FILE.",
		},
		examples: []string{
			"mote nb run notes.mote.md",
			"mote nb run notes.mote.md --dry-run",
			"mote nb run notes.mote.md -o - --force",
			"mote nb console",
			"mote nb console scratch.mote.md",
			"mote nb console -o today.mote.md",
			"mote nb export notes.mote.md",
			"mote nb import analysis.ipynb -o analysis.mote.md",
			`echo 'city = chat "capital of France"' | mote nb exec --state session.json`,
		},
	},
	"kernel": {
		summary: "Register mote as a Jupyter kernel, so a notebook cell can be a mote task and its output shows below it.",
		usage: []string{
			"mote kernel install [--python PATH] [--dir DIR]",
			"mote kernel uninstall [--dir DIR]",
			"mote kernel path [--dir DIR]",
		},
		subs: []helpFlag{
			{"install", "write the kernel into Jupyter's data directory"},
			{"uninstall", "remove it"},
			{"path", "print where it is (or would be) installed"},
		},
		flags: []helpFlag{
			{"--python PATH", "the Python the kernel runs under, which needs ipykernel; the first python3 or python on your PATH by default"},
			{"--dir DIR", "Jupyter's data directory, instead of $JUPYTER_DATA_DIR or your platform's usual one"},
		},
		notes: []string{
			"Each cell is one task or pipeline written as `mote pipe` takes it, or `name = pipeline` to keep its output as {{name}} for later cells; images a task writes are shown as images. Cells run without asking first, being the ones you type. Restarting the kernel starts a session over.",
		},
		examples: []string{
			"mote kernel install",
			"mote kernel install --python ~/venvs/notebooks/bin/python",
			"mote kernel uninstall",
		},
	},
	"meta": {
		summary: "Write a motebook for a complex goal in one shot: the cells that, run in order, get it done.",
		usage:   []string{`mote meta "GOAL" [-o FILE] [--chat] [--run] [--force] [--yes|-y] [--model ID] [--profile P]`},
		flags: []helpFlag{
			{"-o, --output FILE", "write the notebook to FILE instead of printing it; an existing FILE is not replaced without --force"},
			{"--chat", "show the notebook and ask what to change before writing it; Enter keeps it, q gives it up (needs a terminal)"},
			{"--run", "run the notebook once it is written (needs -o)"},
			{"--force", "replace FILE if it exists"},
			{"--yes, -y", "with --run, run cells that can change things without asking"},
			{"--model ID", "use this model for every capability it provides"},
			{"--profile P", "use this profile for this run"},
		},
		notes: []string{
			"The text model writes a plan of tasks, at most six cells, and mote checks it (real tasks, arguments that fit, files that exist, cells that read only earlier cells) before anything is written. It writes tasks, never shell commands. Run the result with `mote nb run`.",
			"With --chat each change you ask for goes to the model with the current plan and comes back checked the same way; one that cannot run is reported and the last good plan stays.",
		},
		examples: []string{
			`mote meta "summarise talk.mp3, then translate the summary to French" -o talk.mote.md`,
			`mote meta "explain what a mutex is, then write a Go example" -o mutex.mote.md --run`,
			`mote meta "summarise talk.mp3, then translate it" --chat -o talk.mote.md`,
			`mote meta "review my last commit and draft a changelog entry"`,
		},
	},
	"do": {
		summary: "Pick the task that fits a request written in plain words, and run it.",
		usage:   []string{`mote do "REQUEST" [-o FILE] [--model ID] [--profile P] [--router text|embed] [--plan [--trace]] [--dry-run] [--apply] [--continue] [--recall] [--yes|-y]`},
		flags: []helpFlag{
			{"-o, --output FILE", "write the result to FILE instead of stdout"},
			{"--model ID", "use this model for every capability it provides"},
			{"--profile P", "use this profile for this run"},
			{"--router text|embed", "choose the task with the text model (default) or a 36 MB encoder with no generation"},
			{"--plan", "write and run a `mote pipe` pipeline for requests that take several steps"},
			{"--trace", "with --plan, print every stage's output"},
			{"--dry-run", "print the chosen command without running it"},
			{"--apply", "write a patch task's changes to disk"},
			{"--continue", "carry on from the task's last exchange"},
			{"--recall", "bring back the closest past exchanges as context"},
			{"--yes, -y", "approve confirmations mote would otherwise ask about"},
		},
		examples: []string{
			`mote do "summarise meeting.m4a in three bullets"`,
			`mote do "what is on the sign in photo.jpg" --dry-run`,
			`mote do --plan "document calc.py then translate it to French"`,
		},
	},
	"agent": {
		summary: "Work toward a goal in steps, calling tasks and local tools and reading what they return.",
		usage:   []string{`mote agent "GOAL" [-o FILE] [--model ID] [--profile P] [--tools a,b] [--mcp SERVER,...] [--steps N] [--allow-sh [--sandbox auto|on|off] [--sandbox-net]] [--yes|-y]`},
		flags: []helpFlag{
			{"-o, --output FILE", "write the answer to FILE instead of stdout"},
			{"--model ID", "use this model for every capability it provides"},
			{"--profile P", "use this profile for this run"},
			{"--tools a,b", "limit which tasks and tools the agent may call"},
			{"--mcp SERVER,...", "add the tools of local MCP servers listed in mcp.json"},
			{"--steps N", "cap the number of steps (default 8, max 30)"},
			{"--allow-sh", "let the agent run shell commands, checked and confirmed"},
			{"--sandbox auto|on|off", "require, skip, or auto-detect a sandbox for shell commands"},
			{"--sandbox-net", "restore network access inside the sandbox"},
			{"--yes, -y", "approve read-only shell commands automatically"},
		},
		notes: []string{"Use the balanced profile; the small profile's text model is too small to act reliably."},
		examples: []string{
			`mote agent "what is the total due in invoice.txt?"`,
			`mote agent "how many Go files are in this repository?" --allow-sh`,
			`mote agent "find where retries are configured" --tools search,chat`,
		},
	},
	"memory": {
		summary: "Show what mote remembers, or find a past exchange by meaning.",
		usage:   []string{"mote memory [--model ID] [--profile P]", `mote memory search "QUERY" [--model ID] [--profile P]`},
		subs: []helpFlag{
			{"(none)", "list remembered facts and, if kept, recent exchange history"},
			{`search "QUERY"`, "find the past exchange closest in meaning to QUERY"},
		},
		examples: []string{"mote memory", `mote memory search "mutex"`},
	},
	"remember": {
		summary:  "Keep a fact in front of every model step.",
		usage:    []string{`mote remember "FACT"`},
		examples: []string{`mote remember "I write Go, and prefer short answers"`},
	},
	"forget": {
		summary: "Remove a remembered fact, or clear facts or exchange history.",
		usage:   []string{"mote forget N", "mote forget --all", "mote forget --history"},
		flags: []helpFlag{
			{"--all", "forget every remembered fact"},
			{"--history", "clear the exchange history"},
		},
		examples: []string{"mote forget 2", "mote forget --all", "mote forget --history"},
	},
	"models": {
		summary: "Show models, sizes and which are in use; fetch, remove, explain or re-verify them.",
		usage: []string{
			"mote models [list]",
			"mote models pull ID|CAPABILITY...|--all|--missing [--yes]",
			"mote models upgrade [--check] [--prune] [--yes]",
			"mote models rm ID",
			"mote models why CAPABILITY",
			"mote models verify",
		},
		subs: []helpFlag{
			{"list", "show every model, its size and which capability it's the default for (default with no subcommand)"},
			{"pull ID|CAP...|--all|--missing", "download models by id or capability; --missing fetches only the ones your profile uses, --all fetches every one"},
			{"upgrade [--check] [--prune]", "fetch the newest registry and the best models it picks for your profile and RAM"},
			{"rm ID", "remove a downloaded model"},
			{"why CAPABILITY", "explain which model was chosen for a capability, and why"},
			{"verify", "re-check the SHA-256 of every installed model"},
		},
		flags: []helpFlag{
			{"--all", "with pull, fetch every model in the registry"},
			{"--missing", "with pull, fetch only the models your profile currently uses"},
			{"--check", "with upgrade, report changes without downloading"},
			{"--prune", "with upgrade, remove models no longer used after upgrading"},
			{"--yes, -y", "skip confirmation prompts"},
		},
		examples: []string{
			"mote models",
			"mote models pull text",
			"mote models pull --missing",
			"mote models why text",
			"mote models upgrade --check",
		},
	},
	"bench": {
		summary: "Measure installed models locally: startup, tokens/s, peak RSS, small pass/fail checks.",
		usage:   []string{"mote bench [--full] [--model ID] [--strict]"},
		flags: []helpFlag{
			{"--full", "also time loading with and without weight repacking"},
			{"--model ID", "benchmark only this model"},
			{"--strict", "exit non-zero if any check failed"},
		},
		examples: []string{"mote bench", "mote bench --full --strict"},
	},
	"tune": {
		summary: "Propose configuration changes from `mote bench`'s measurements.",
		usage:   []string{"mote tune [--apply]"},
		flags: []helpFlag{
			{"--apply", "record the proposed changes instead of only printing them"},
		},
		examples: []string{"mote tune", "mote tune --apply"},
	},
	"serve": {
		summary: "Keep models loaded and serve them to editors through an OpenAI-compatible API.",
		usage: []string{
			"mote serve [--port N] [--keep-alive DURATION] [--background]",
			"mote serve status",
			"mote serve stop",
		},
		subs: []helpFlag{
			{"(none)", "run the server, in the foreground unless --background, on 127.0.0.1:11435"},
			{"status", "show what is loaded, and for how long"},
			{"stop", "unload everything now"},
		},
		flags: []helpFlag{
			{"--port N", "listen on this port instead of 11435"},
			{"--keep-alive DURATION", "keep models loaded this long after the last use"},
			{"--background", "start the server detached and return immediately"},
		},
		examples: []string{"mote serve", "mote serve status", "mote serve stop"},
	},
	"chat": {
		summary: "Talk with the text model; it sees the earlier turns.",
		usage:   []string{`mote chat [--system "INSTRUCTIONS"] [--model ID] [--profile P] [--continue] [--recall] [--tools a,b|all] [--steps N] [--yes|-y]`},
		flags: []helpFlag{
			{"--system TEXT", "set the system prompt for the conversation"},
			{"--model ID", "use this model instead of the one your profile picks"},
			{"--profile P", "use this profile for this run"},
			{"--continue", "resume the previous chat history instead of starting fresh"},
			{"--recall", "bring back the closest past exchanges as context before answering each turn"},
			{"--tools a,b|all", "let chat run mote tasks to answer a turn, on the same think/act/observe loop as `mote agent`; without it, chat only talks"},
			{"--steps N", "cap the number of tool calls per turn (default 8, max 30)"},
			{"--yes, -y", `answer yes to a tool call mote would otherwise confirm (only tasks marked "asks" are confirmed at all)`},
		},
		notes: []string{chatHelp},
		examples: []string{
			"mote chat",
			`mote chat --system "You are a terse code reviewer"`,
			"mote chat --tools all",
		},
	},
	"index": {
		summary: "Index files under a folder so `mote ask` can answer from them.",
		usage: []string{
			"mote index DIR... [--profile P]",
			"mote index status",
			"mote index rm DIR...|--all",
		},
		subs: []helpFlag{
			{"DIR...", "index (or re-index changed files under) one or more folders; no folders refreshes what's indexed"},
			{"status", "show what's indexed"},
			{"rm DIR...|--all", "forget one or more folders, or the whole index"},
		},
		flags: []helpFlag{
			{"--profile P", "use this profile's embedding model"},
		},
		examples: []string{
			"mote index ~/notes ~/projects/mote/docs",
			"mote index status",
			"mote index rm ~/notes",
		},
	},
	"ask": {
		summary: "Answer a question from the indexed files closest to it, citing sources.",
		usage:   []string{`mote ask "QUESTION" [--in DIR] [--top N] [-o FILE] [--model ID] [--profile P] [--sources]`},
		flags: []helpFlag{
			{"--in DIR", "search only files indexed under DIR"},
			{"--top N", "use this many passages (default 6)"},
			{"-o, --output FILE", "write the answer to FILE instead of stdout"},
			{"--model ID", "use this model instead of the one your profile picks"},
			{"--profile P", "use this profile for this run"},
			{"--sources", "print the passages instead of asking a model to answer"},
		},
		examples: []string{
			`mote ask "what did we decide about retries"`,
			`mote ask "how does the logo get generated" --in ~/projects/mote/docs --sources`,
		},
	},
	"listen": {
		summary: "Wait for a wake word on the microphone, then run what you say next.",
		usage:   []string{"mote listen [TASK] [--wake PHRASE] [--device D] [--chunk SECONDS] [--model ID] [--profile P] [--once]"},
		flags: []helpFlag{
			{"--wake PHRASE", `wake word to listen for (default "hey mote")`},
			{"--device D", "input device (required on Windows)"},
			{"--chunk SECONDS", "seconds per recording chunk"},
			{"--model ID", "use this model instead of the one your profile picks"},
			{"--profile P", "use this profile for this run"},
			{"--once", "stop after one request"},
		},
		notes:    []string{"mote never listens unless you start it."},
		examples: []string{"mote listen", `mote listen code --wake "ok mote"`},
	},
	"completion": {
		summary: "Print a shell completion script.",
		usage:   []string{"mote completion bash|zsh|fish"},
		examples: []string{
			"source <(mote completion bash)",
			"mote completion zsh >> ~/.zshrc",
		},
	},
	"guide": {
		summary: "Ask the text model about mote itself: which command or task fits a goal.",
		usage:   []string{"mote guide [--model ID] [--profile P]"},
		flags: []helpFlag{
			{"--model ID", "use this model instead of the one your profile picks"},
			{"--profile P", "use this profile for this run"},
		},
		notes:    []string{chatHelp},
		examples: []string{"mote guide"},
	},
}

// helpReferenceText renders every command's exact usage and subcommands as
// plain text (no colour codes), for grounding `mote guide`'s system prompt.
// It exists because the compact `usage` banner leaves out several real
// flags (e.g. `do`'s --router, --continue, --recall) to stay readable in a
// terminal, and a small model asked about them from that alone tends to
// invent plausible-looking ones instead. helpTopics is built from the same
// flags() calls dispatch uses and checked against it in tests, so this is
// the accurate, if more verbose, version.
func helpReferenceText() string {
	var b strings.Builder
	for _, name := range topLevelCommands {
		h, ok := helpTopics[name]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "mote %s - %s\n", name, h.summary)
		for _, u := range h.usage {
			fmt.Fprintf(&b, "  %s\n", u)
		}
		for _, s := range h.subs {
			fmt.Fprintf(&b, "    %s: %s\n", s.name, s.desc)
		}
	}
	return b.String()
}

// hasHelpFlag reports whether args ask for help: -h or --help before a "--"
// that would otherwise end flag parsing.
func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// commandHelp implements `mote help CMD` and `mote CMD --help`.
func (a *app) commandHelp(name string) error {
	h, ok := helpTopics[name]
	if !ok {
		return usagef("unknown command %q; see `mote help`", name)
	}
	a.printCommandHelp(name, h)
	return nil
}

func (a *app) printCommandHelp(name string, h helpTopic) {
	o := a.uo
	fmt.Fprintf(a.out, "%s %s\n\n", o.Bold("mote "+name), o.Dim("— "+h.summary))
	fmt.Fprintln(a.out, o.Bold("Usage:"))
	for _, u := range h.usage {
		a.printInvocation(u)
	}
	if len(h.subs) > 0 {
		fmt.Fprintf(a.out, "\n%s\n", o.Bold("Subcommands:"))
		for _, s := range h.subs {
			fmt.Fprintf(a.out, "  %s %s\n", o.Cyan(pad(s.name, 30)), s.desc)
		}
	}
	if len(h.flags) > 0 {
		fmt.Fprintf(a.out, "\n%s\n", o.Bold("Flags:"))
		for _, f := range h.flags {
			fmt.Fprintf(a.out, "  %s %s\n", o.Accent(pad(f.name, 22)), f.desc)
		}
	}
	for _, n := range h.notes {
		fmt.Fprintf(a.out, "\n%s\n", o.Dim(n))
	}
	if len(h.examples) > 0 {
		fmt.Fprintf(a.out, "\n%s\n", o.Bold("Examples:"))
		for _, e := range h.examples {
			a.printInvocation(e)
		}
	}
}

// printInvocation prints one "mote CMD ARGS" line the way usage() does:
// "mote" dim, the command cyan, everything else plain.
func (a *app) printInvocation(line string) {
	o := a.uo
	line = strings.TrimPrefix(line, "mote ")
	cmd, rest, ok := strings.Cut(line, " ")
	if !ok {
		fmt.Fprintf(a.out, "  %s %s\n", o.Dim("mote"), o.Cyan(cmd))
		return
	}
	fmt.Fprintf(a.out, "  %s %s %s\n", o.Dim("mote"), o.Cyan(cmd), rest)
}
