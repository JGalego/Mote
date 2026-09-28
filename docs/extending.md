# Extending mote

```
cmd/mote              CLI entry point
cmd/mote-refresh      regenerates registry/models.json (daily CI)
registry/             candidates.json, policy.json -> models.json; selection
internal/platform     OS/CPU/RAM/tool detection
internal/config       user config, history, rollback
internal/runtime      downloads, archive extraction, llama.cpp backend
internal/task         tasks.json pipelines and the ops they use
internal/bench        local benchmark cases, results, tuning proposals
internal/memory       facts, history and search over past exchanges
internal/mcp          a client for local MCP servers (stdio JSON-RPC)
internal/sandbox      runs the agent's shell commands under bwrap or sandbox-exec
internal/cli          commands and the setup wizard
install/              install.sh (Linux/macOS), install.ps1 (Windows)
```

**A task** is data. Add an entry to `internal/task/tasks.json`: positional
`params` (`text`, `file`, `dir`, or `input`, which takes a file's path or the
text itself, `-` for stdin), then `steps`, each naming an `op`, its
inputs and the value it produces (`as`). The last value must be `out`.
Optional `examples` are the phrasings someone would use; `mote do` matches a
request against each one separately, so a task is found by what it is for
rather than by its name.
Prompts use `{{name}}` for earlier values. `go test ./internal/task` validates
every task (unknown ops, undefined values, unknown capabilities).

**Documents** are read by the `read` op: PDFs with `pdftotext` (and, when a
PDF has no text layer, its first 20 pages rendered by `pdftoppm` and read by
the vision model), `.docx`, `.odt` and `.pptx` from their XML, anything else
as text. `"plain": true` also reduces HTML to the text a reader sees; without
it HTML is source, as `refactor` wants. A `generate` step with
`"split": "doc"` handles a `doc` too long for the model's context: it runs
once per chunk, then `reduce`, a prompt in which `{{doc}}` stands for the
partial answers, combines them (in rounds, if they are long too). Without
`reduce` the parts are joined in order, which is how `translate` works; each
part is then also kept short enough to come back in one reply.

**Your own task** needs no fork: put the same JSON in `*.json` files under
`tasks/` in the config directory (`$MOTE_TASKS_DIR` overrides it, `mote tasks`
prints the path). They are validated like the built-ins, listed as `custom`,
and an id that matches a built-in replaces it, so a prompt can be adjusted in
place. A file that does not parse is an error naming the file, not a task that
quietly disappears.

**A local program** needs no Go: an `exec` step runs it and makes its
standard output the value. `cmd` is the argument vector, the program first
and fixed, then arguments that may use `{{name}}`; `from` feeds a value on
stdin and `dir` sets the working directory. Nothing passes through a shell,
so a value is always exactly one argument. An argument that is only a
reference to an unset optional value is dropped. A value starting with `-`
is refused unless a literal `--` comes before it in `cmd`, since it would
otherwise reach the program as an option; put `--` before the values of any
task a model may call. Output is capped at 1 MiB, and `mote tasks` reports
whether the program is installed. See `examples/tools.json`. A task that can
change things (writes, deletes, sends) should set `"asks": true`: typed with
`mote run` it runs as usual, but chosen by a model in `mote agent` or
`mote do` it is confirmed first.

**The agent's shell** checks each command in `internal/cli/guard.go` before
it runs: a short list of catastrophic commands is refused outright, a list
of read-only programs (with the options that make them write, like
`find -delete`, excluded) runs under `--yes`, and the rest is asked about.
It reads command lines as text, so it fails safe but is not a security
boundary. That is `internal/sandbox`: when bwrap (Linux) or sandbox-exec
(macOS) works, commands run with only the working directory writable, home
hidden and no network, and `--yes` then approves writes as well. Its tests
run real commands in the sandbox and skip where none is available.

**A tool for the agent** is a task, which is how local programs get there
too, or an MCP server listed in `mcp.json`. `mote agent` offers each one
under a JSON schema built by `stepSchema` in `internal/cli/agent.go`, so a
small model can only name a real tool with arguments of the right shape.
`internal/fakemcp` is a fake server for tests, run by re-executing the test
binary.

**A local tool or modality** is an op: a Go function in
`internal/task/ops.go` registered in the `ops` map, with the executables it
needs listed in `tools` so `mote tasks` and `mote doctor` can report them.
Ops pass `Value`s (text and/or file paths), so a new modality is usually a new
op that converts files, like `audio` and `frames` do with ffmpeg.

**A model** is a candidate in `registry/candidates.json`: upstream Hugging
Face id (for license, parameter count and `evalResults`), the GGUF repo and
files, capabilities and context size. Run `go run ./cmd/mote-refresh` to pull
hashes and benchmarks into `models.json`. `registry/discovered.json` lists
small models with gate-benchmark results that are not candidates yet.

**A selection rule** is a gate in `registry/policy.json`: an upstream
benchmark (`dataset`, `task`), its direction and a threshold per profile.
Selection code is `registry/select.go`; ties go to fewer parameters, then
fewer bytes.

**A capability** needs an entry in `registry.Capabilities`, a gate (or an
explicit decision not to gate it), at least one candidate and a benchmark
case in `internal/bench/cases.json`. It also needs a user: a task, or a
command listed in the `byCommand` map in `internal/task/task_test.go`, which
is how `embed` is used (`mote do --router embed`, not a task).

A candidate's `files` map roles to names in its `repo`, or to
`{"repo": ..., "file": ...}` for a file kept elsewhere; each repo is pinned
to its own revision. A model on another backend names it in `backend`, and
that backend's runtime is pinned under `runtimes` in `candidates.json`, whose
asset names may use `*` (sd.cpp names its macOS build after the macOS
version). Such runtimes are installed the first time one of their models is
used, not at setup.

**A backend** implements `runtime.Backend` (`Open` a session that can
`Generate`, and `Speak`) and is chosen in `cli.app.backend` from the model's
`backend` field, which `registry.Validate` must also accept.
