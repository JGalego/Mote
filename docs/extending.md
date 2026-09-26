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
internal/cli          commands and the setup wizard
install/              install.sh (Linux/macOS), install.ps1 (Windows)
```

**A task** is data. Add an entry to `internal/task/tasks.json`: positional
`params` (`text`, `file`, `dir`), then `steps`, each naming an `op`, its
inputs and the value it produces (`as`). The last value must be `out`.
Prompts use `{{name}}` for earlier values. `go test ./internal/task` validates
every task (unknown ops, undefined values, unknown capabilities).

**Your own task** needs no fork: put the same JSON in `*.json` files under
`tasks/` in the config directory (`$MOTE_TASKS_DIR` overrides it, `mote tasks`
prints the path). They are validated like the built-ins, listed as `custom`,
and an id that matches a built-in replaces it, so a prompt can be adjusted in
place. A file that does not parse is an error naming the file, not a task that
quietly disappears.

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
case in `internal/bench/cases.json`.

**A backend** implements `runtime.Backend` (`Open` a session that can
`Generate`, and `Speak`) and is chosen in `cli.app.backend` from the model's
`backend` field, which `registry.Validate` must also accept.
