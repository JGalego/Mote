package cli

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
)

// guideCmd implements `mote guide`: a conversation with the text model whose
// system prompt is mote's own command reference and task registry, so it can
// answer questions about mote itself ("what's the best way to do X", "can I
// pipe the output of X into Y") without drifting from what mote actually
// does. It otherwise works exactly like `mote chat`.
func (a *app) guideCmd(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"--model", "--profile"}, nil)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef(`usage: mote guide [--model ID]`)
	}
	profile, err := a.selectModels(vals)
	if err != nil {
		return err
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	sessions := map[string]mrt.Session{}
	defer task.CloseSessions(sessions)
	env := a.env(profile, sessions)
	remembered, err := a.memoryFor(ctx, "", nil, profile, sessions)
	if err != nil {
		return err
	}
	system := strings.TrimSpace(guideSystem(tasks) + "\n\n" + remembered)
	m, files, err := env.Resolve(ctx, "text")
	if err != nil {
		return err
	}
	b, err := env.Backend(m)
	if err != nil {
		return err
	}
	done := a.status("loading " + m.ID)
	sess, err := b.Open(ctx, m, files)
	done(err == nil)
	if err != nil {
		return err
	}
	sessions[m.ID] = sess
	live := a.tty && a.uo.Live()
	if live {
		fmt.Fprintln(a.err, a.ue.Dim("ask mote about mote · "+m.ID+" · "+chatHelp))
	}

	var turns []mrt.Turn
	sc := bufio.NewScanner(a.in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for {
		line, ok := a.readTurn(sc, live)
		if !ok {
			return sc.Err()
		}
		switch line {
		case "":
			continue
		case "/exit", "/quit", "/bye":
			return nil
		case "/new", "/reset", "/clear":
			turns = nil
			if live {
				fmt.Fprintln(a.err, a.ue.Dim("started over"))
			}
			continue
		case "/help", "/?":
			fmt.Fprintln(a.err, chatHelp)
			continue
		}
		req := mrt.Request{System: system, Prompt: line, Temperature: 0.2, MaxTokens: 2048}
		req.History = fitTurns(turns, m.Context, req.MaxTokens, len(system)+len(line))
		streamed := false
		think := a.status("thinking with " + m.ID)
		if a.uo.Live() {
			req.OnToken = func(tok string) {
				if !streamed {
					if tok = strings.TrimLeft(tok, " \n"); tok == "" {
						return
					}
					think(true) // the reply itself shows it is working
					think = nil
					streamed = true
				}
				fmt.Fprint(a.out, tok)
			}
		}
		res, err := sess.Generate(ctx, req)
		if think != nil {
			think(err == nil)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(a.err, "%s %v\n", a.ue.Fail(), err)
			continue
		}
		if streamed {
			fmt.Fprintln(a.out)
		} else {
			fmt.Fprintln(a.out, res.Text)
		}
		turns = append(turns, mrt.Turn{Role: "user", Text: line}, mrt.Turn{Role: "assistant", Text: res.Text})
		a.record(task.Task{ID: "guide"}, line, task.Result{Value: task.Value{Text: res.Text}})
	}
}

// guideSystem grounds the guide in mote's actual command reference and task
// registry, so its answers name real commands, flags and tasks rather than
// invented ones, and stay in sync as tasks are added or changed.
func guideSystem(tasks []task.Task) string {
	var b strings.Builder
	b.WriteString("You are mote's own built-in guide. Answer questions about how to use the mote " +
		"CLI: which command or task fits a goal, how a task's output can feed another task's input " +
		"in `mote pipe` (matching one's out-kind to the next's in-kinds), and what mote can and cannot " +
		"do. Be concise, and give concrete command lines. Only use commands, flags and tasks listed " +
		"below; never invent one that isn't there.\n\n")
	b.WriteString(usage)
	b.WriteString("\nFull command reference (exact flags and subcommands; never invent one not listed here):\n")
	b.WriteString(helpReferenceText())
	b.WriteString("\nTasks (id: in-kind(s) -> out-kind — summary):\n")
	for _, t := range tasks {
		fmt.Fprintf(&b, "- %s: %s -> %s — %s\n", t.ID, strings.Join(t.In, "+"), t.Out, t.Summary)
		if u := t.Usage(); u != "" {
			fmt.Fprintf(&b, "  usage: mote run %s %s\n", t.ID, u)
		}
		for _, ex := range t.Examples {
			fmt.Fprintf(&b, "  e.g. %q\n", ex)
		}
	}
	return b.String()
}
