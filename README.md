<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo/mote-dark.svg">
    <img alt="mote" src="docs/logo/mote-light.svg" width="360">
  </picture>
</p>

<p align="center">Small models, local machines, useful work.</p>

<p align="center">
  <a href="https://github.com/JGalego/Mote/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/JGalego/Mote/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/JGalego/Mote/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/JGalego/Mote?display_name=tag&sort=semver"></a>
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
  <img alt="Platforms" src="https://img.shields.io/badge/platform-linux%20%7C%20macOS%20%7C%20windows-lightgrey.svg">
</p>

mote runs X-to-Y AI tasks (text, code, images, audio, video, files) on your CPU with small quantized models. It is a single static binary that drives [llama.cpp](https://github.com/ggml-org/llama.cpp) and the tools you already have (`ffmpeg`, `git`, your editor). No accounts, no cloud inference, no telemetry.

## Contents

- [Getting started](#getting-started)
- [Commands](#commands)
- [Tasks](#tasks)
  - [chat](#chat---)
  - [code](#code---)
  - [refactor](#refactor---)
  - [doc](#doc---)
  - [extract](#extract---)
  - [describe](#describe-----)
  - [transcribe](#transcribe---)
  - [speak](#speak---)
  - [frames](#frames---)
  - [video](#video---)
  - [convert](#convert---)
  - [patch](#patch---)
  - [chained](#chained-)
  - [chosen](#chosen-)
  - [spoken](#spoken-)
  - [your own](#your-own-)
- [Memory](#memory)
- [How it works](#how-it-works)
- [Models](#models)
- [License](#license)

## Getting started

### Linux 🐧 / macOS 🍎

```sh
curl -fsSL https://raw.githubusercontent.com/JGalego/Mote/main/install/install.sh | sh
```

### Windows 🪟

```powershell
irm https://raw.githubusercontent.com/JGalego/Mote/main/install/install.ps1 | iex
```

### Development 🧪

Install the current source instead of a release. Both commands replace whatever `mote` is installed, so run them as often as you like to move to the tip of `main`; set `MOTE_VERSION` to build a branch, tag or commit instead.

Building needs [Go 🦫](https://go.dev/dl/).

```sh
curl -fsSL https://raw.githubusercontent.com/JGalego/Mote/main/install/install.sh | MOTE_SOURCE=1 sh
```

```powershell
$env:MOTE_SOURCE=1; irm https://raw.githubusercontent.com/JGalego/Mote/main/install/install.ps1 | iex
```

In a clone, `go install ./cmd/mote` does the same into `$(go env GOPATH)/bin`. `mote version` reports the commit it was built from.

<!--img src="docs/demo/install.gif" width=70%/-->

## Commands

| Command | What it does |
| --- | --- |
| `mote setup [--profile P] [--yes]` | Install the runtime and the models for a profile (`small`, `balanced`, `quality`) |
| `mote run TASK [ARGS...]` | Run a task, e.g. `mote run chat "Explain what a mutex is"`; `-o FILE` writes the output, `--model ID` overrides the model, `--apply` writes `patch` changes |
| `mote pipe "A \| B \| sh: cmd"` | Chain tasks in one process, each stage receiving the last one's value: `{}` or `-` places it, `sh:` runs a shell command, `--trace` shows each step |
| `mote do "REQUEST"` | Pick the task that fits a request written in plain words and run it; `--router embed` chooses with the encoder, `--dry-run` shows the choice |
| `mote remember "FACT"` | Keep a fact in front of every model step; `mote memory` lists them, `mote forget N\|--all` removes them |
| `mote memory [search "Q"]` | Show what mote remembers, or find a past exchange by meaning |
| `mote listen [TASK]` | Wait for a wake word on the microphone, then run what you say next (never listens unless you start it) |
| `mote tasks` | List the tasks, their arguments and what each one needs |
| `mote models [pull\|rm\|why\|verify]` | Show models, sizes and which are in use; fetch, remove, explain or re-verify them |
| `mote bench [--full]` | Measure installed models locally: startup, tokens/s, peak RSS, small pass/fail checks |
| `mote tune [--apply]` | Propose config changes from those measurements, and record them with `--apply` |
| `mote doctor` | Check the installation: runtime, models, tools, data directory |
| `mote config [show\|set\|history\|rollback\|edit]` | Show or change the configuration; every version is kept |
| `mote update [--check]` | Update mote, or only report whether an update exists |
| `mote version` | Print the version |

## Tasks

### chat 📝 → 📝

`mote run chat PROMPT` — Answer a prompt.

![mote run chat](docs/demo/chat.gif)

### code 📝 → 💻

`mote run code PROMPT` — Write code from a description.

![mote run code](docs/demo/code.gif)

### refactor 💻 → 💻

`mote run refactor FILE INSTRUCTION` — Rewrite a source file following an instruction.

![mote run refactor](docs/demo/refactor.gif)

### doc 💻 → 📖

`mote run doc FILE` — Write Markdown documentation for a source file.

![mote run doc](docs/demo/doc.gif)

### extract 📄 → 📊

`mote run extract FILE [SCHEMA]` — Extract structured JSON from a document.

![mote run extract](docs/demo/extract.gif)

### describe 📷 + 📝 → 📝

`mote run describe IMAGE [QUESTION]` — Describe an image or answer a question about it.

![mote run describe](docs/demo/describe.gif)

### transcribe 🔊 → 📝

`mote run transcribe AUDIO` — Transcribe speech from an audio or video file (ffmpeg converts non-WAV input).

![mote run transcribe](docs/demo/transcribe.gif)

### speak 📝 → 🔊

`mote run speak TEXT` — Synthesize speech to a WAV file.

![mote run speak](docs/demo/speak.gif)

### frames 🎬 → 📷

`mote run frames VIDEO [COUNT]` — Extract evenly spaced frames from a video.

![mote run frames](docs/demo/frames.gif)

### video 🎬 → 📝

`mote run video VIDEO` — Summarize a video from sampled frames and its speech.

![mote run video](docs/demo/video.gif)

### convert 📷 → 📷

`mote run convert FILE [WIDTH]` — Convert or resize an image, audio or video file with ffmpeg.

![mote run convert](docs/demo/convert.gif)

### patch 📁 → 🩹

`mote run patch DIR INSTRUCTION` — Propose changes to a directory as a diff; `--apply` writes them to disk.

![mote run patch](docs/demo/patch.gif)

### chained 🔗

`mote pipe` runs several tasks in one process, so models stay loaded between stages instead of being reloaded per command:

```sh
mote pipe "transcribe meeting.m4a | chat 'Summarise in 3 bullets: {}'"
mote pipe "code 'print the first 10 Fibonacci numbers' | sh: python3 -"
mote pipe --trace "frames clip.mp4 3 | describe | sh: tee notes.txt"
```

Each stage receives the previous stage's value: `{}` inside an argument or a bare `-` says where it goes, and with neither it fills the first argument you left out. Several files fan out into one run per file. A stage starting with `sh:` (or `!`) is a shell command reading that value on stdin, so ordinary tools mix in.

Prefer `sh:` when typing interactively: bash and zsh expand `!` inside double quotes as history before mote sees it, unless you wrap the pipeline in `'single quotes'`.

Only the last stage prints. `--trace` shows what each earlier stage handed on, and a stage that produces nothing stops the pipeline instead of letting the next model answer an empty question.

Plain shell pipes still work too (`mote run code "..." | python3`), at the cost of reloading a model per command.

### chosen 🎯

`mote do` reads a request in plain words, picks the task that fits and runs
it. The text model makes the choice, constrained by a JSON schema to a real
task id, so it cannot invent one:

```sh
mote do "explain what a mutex is"
mote do "summarise meeting.m4a in three bullets"
mote do "what is on the sign in photo.jpg" --dry-run
```

Words naming a file or directory that exists fill the task's file arguments;
what is left becomes its text argument. `--dry-run` prints the `mote run`
command it chose without running it.

Two routers are available. The default asks the text model, which reasons
about the request but costs a generation. `--router embed` (or
`mote config set router embed`) instead compares the request with each task's
description and examples using a 36 MB encoder: one forward pass per text,
no tokens generated, milliseconds on a CPU.

Both read the `examples` in [`tasks.json`](internal/task/tasks.json), so a
task of your own is routable as soon as you give it a few phrasings.

### spoken 🎤

Nothing records until you ask it to. `mote listen` captures short chunks from
the microphone with ffmpeg, transcribes each with the `asr` model you already
have, and when it hears the wake word runs the rest of the sentence:

```sh
mote listen                       # "hey mote, explain what a mutex is"
mote listen code --wake "ok mote" # send what you say to another task
```

The phrase comes from `--wake`, else `mote config set wake_word "..."`, else
`hey mote`. `--chunk` sets how many seconds each recording lasts, `--device`
picks the input (required on Windows, where dshow has no default), and
`--once` stops after the first request. The asr model stays loaded between
chunks, so only the first one waits for it.

### your own 🧩

Tasks are data, not code: put a file shaped like [`tasks.json`](internal/task/tasks.json) in `tasks/*.json` under the config directory (`mote tasks` prints the path) and it is validated, listed as `custom` and run like the rest. An id that matches a built-in replaces it.

See [extending mote](docs/extending.md).

## Memory

Mote forgets everything between runs unless you ask it not to, and what it
keeps are plain files you can read, edit and delete.

```sh
mote remember "I write Go, and prefer short answers"   # a fact, kept in front of every model step
mote config set memory true                            # start keeping a history of exchanges
mote run chat "and in Python?" --continue              # carry on from the last exchanges
mote run chat "remind me about locks" --recall         # bring back the closest past exchanges
mote memory search "mutex"                             # find one yourself
mote forget 2 | mote forget --all | mote forget --history
```

Facts live in `facts.txt` in the config directory; the history is
`history.jsonl` in the data directory, written only while `memory` is true,
and never sent anywhere. `--recall` and `mote memory search` compare meaning
with the same encoder the router uses, so they find an exchange that used
different words.

## How it works

```mermaid
%%{init: {"flowchart": {"wrappingWidth": 320}}}%%
flowchart TD
  IN["<code>mote run TASK ARGS</code>"] --> PIPE["🧩 task pipeline<br/><code>tasks.json</code>"]

  PIPE -- "model step<br/><code>text · code · extract</code><br/><code>vision · asr · tts</code>" --> LLAMA["🦙 <code>llama-server</code> · <code>llama-tts</code><br/>127.0.0.1 · CPU only"]
  PIPE -- "tool step" --> TOOLS["🔧 <code>ffmpeg</code> · <code>ffprobe</code> · <code>git</code>"]

  DATA[("💾 data dir<br/>GGUF models · runtime · logs")] --> LLAMA
  HF(["🤗 Hugging Face"]) -. "<code>mote setup</code><br/><code>mote models pull</code><br/>pinned to a commit · SHA-256" .-> DATA

  LLAMA --> OUT["📤 stdout · <code>-o FILE</code> · <code>--apply</code>"]
  TOOLS --> OUT
```

Inference runs in a llama.cpp server bound to `127.0.0.1` with GPU offload disabled (`--device none`). GGUF weights come from Hugging Face only when you allow it, pinned to a commit and verified by SHA-256.

Models, logs and benchmark results live in the data directory (`~/.local/share/mote`, `~/Library/Application Support/mote`, `%LOCALAPPDATA%\mote`), configuration in the OS config directory. After setup, only `mote update` and explicit pulls touch the network.

Tasks are short pipelines in [`internal/task/tasks.json`](internal/task/tasks.json): each step calls a model capability (`text`, `code`, `extract`, `vision`, `asr`, `tts`) or a local tool (`ffmpeg`, `git`), and every capability has its own specialized model.

## Models

```mermaid
%%{init: {"flowchart": {"wrappingWidth": 320}}}%%
flowchart LR
  subgraph ci["🤖 daily, in CI"]
    CAND["📋 <code>candidates.json</code><br/>models · quantizations"] --> REG["📒 <code>models.json</code>"]
    POL["📏 <code>policy.json</code><br/>gate per capability"] --> REG
    HF(["🤗 Hugging Face<br/><code>evalResults</code> · hashes"]) --> REG
  end

  subgraph you["💻 on your machine"]
    PROF["⚙️ profile<br/><code>small</code> · <code>balanced</code> · <code>quality</code>"] --> PICK
    PICK{"smallest model that<br/>fits your RAM and<br/>passes the gate"} --> WHY["🎯 one model per capability<br/><code>mote models why text</code>"]
    WHY --> TUNE["📊 <code>mote bench</code> → <code>mote tune --apply</code>"]
  end

  REG --> PICK
```

[`registry/models.json`](registry/models.json) is generated from [`candidates.json`](registry/candidates.json) (the models and quantizations we consider) and [`policy.json`](registry/policy.json) (per-capability thresholds for the `small`, `balanced` and `quality` profiles): mote picks the smallest model that fits your RAM and meets the threshold, and `mote models why text` prints the reasoning and sources.

Scores come from Hugging Face `evalResults`, stored with source, date and verification flag; RAM figures are labelled estimates. A [daily workflow](.github/workflows/refresh.yml) re-reads them and commits only what changed. Locally, `mote bench` measures installed models, `mote tune --apply` turns the results into config, and `mote config rollback` undoes it.

## License

[MIT 🏛️](LICENSE.md)

Models are downloaded under their own licenses, which are shown before download.
