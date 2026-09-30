package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jgalego/mote/internal/memory"
	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
	"github.com/jgalego/mote/internal/ui"
)

const chatHelp = "/new starts over · /exit or Ctrl-D ends · end a line with \\ to continue it · Tab completes a /command"

// chatCmd implements `mote chat`: a conversation with the text model, which
// sees the earlier turns. Each line of input is a turn, so it works on a
// pipe as well as in a terminal.
//
// With --tools, a turn is answered by the same think/act/observe loop as
// `mote agent` instead of a plain reply, so the model may run mote commands
// on the way to an answer; without it, chat never runs anything. A task
// marked "asks" is confirmed before each call unless --yes was given, same
// as `mote agent`.
func (a *app) chatCmd(ctx context.Context, args []string) error {
	vals, pos, err := flags(args,
		[]string{"--model", "--profile", "--system", "--tools", "--steps"},
		[]string{"--continue", "--recall", "--yes", "-y"})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef(`usage: mote chat [--system "INSTRUCTIONS"] [--model ID] [--continue] [--recall] [--tools a,b|all] [--steps N] [--yes]`)
	}
	_, useTools := vals["--tools"]
	yes := vals["--yes"] == "true" || vals["-y"] == "true"
	if !useTools && (vals["--steps"] != "" || yes) {
		return usagef("--steps and --yes only apply with --tools")
	}
	steps := defaultAgentSteps
	if v := vals["--steps"]; v != "" {
		if steps, err = strconv.Atoi(v); err != nil || steps < 1 || steps > maxAgentSteps {
			return usagef("--steps must be a number from 1 to %d", maxAgentSteps)
		}
	}
	profile, err := a.selectModels(vals)
	if err != nil {
		return err
	}
	sessions := map[string]mrt.Session{}
	defer task.CloseSessions(sessions)
	env := a.env(profile, sessions)
	// --recall searches per turn, against what is actually being asked, so
	// only --continue (which needs no query) is resolved once up front.
	remembered, err := a.memoryFor(ctx, "", map[string]string{"--continue": vals["--continue"]}, profile, sessions)
	if err != nil {
		return err
	}
	system := strings.TrimSpace(remembered + "\n\n" + vals["--system"])
	toolSystemBase := strings.TrimSpace(system + "\n\n" + agentSystem)
	recall := vals["--recall"] == "true"
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
		fmt.Fprintln(a.err, a.ue.Dim("chatting with "+m.ID+" · "+chatHelp))
	}

	var turns []mrt.Turn
	sc := bufio.NewScanner(a.in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	var tools []agentTool
	var wd string
	if useTools {
		toolsArg := vals["--tools"]
		if strings.TrimSpace(toolsArg) == "" {
			return usagef(`--tools needs a comma list of task ids, or "all"`)
		}
		names := toolsArg
		if toolsArg == "all" {
			names = ""
		}
		tasks, err := task.LoadFrom(a.tasksDir())
		if err != nil {
			return err
		}
		env.Memory = remembered
		env.Stdin = nil // a tool's "-" is text, not a request to read the terminal
		ask := func(call string) bool {
			if yes {
				return true
			}
			fmt.Fprintf(a.err, "%s run %s? [y/N] ", a.ue.Warn(), a.ue.Bold(call))
			if !sc.Scan() {
				return false
			}
			line := strings.ToLower(strings.TrimSpace(sc.Text()))
			return line == "y" || line == "yes"
		}
		if tools, err = a.agentTools(tasks, names, false, env, ask, nil, shellBox{}, nil); err != nil {
			return err
		}
		if !yes && !a.tty {
			for _, t := range tools {
				if t.asks {
					return usagef("%s can change things, so mote asks before each call, and stdin is not a terminal; pass --yes to call it without asking, or restrict --tools", t.id)
				}
			}
		}
		if wd, err = os.Getwd(); err != nil {
			return err
		}
		var ids []string
		for _, t := range tools {
			ids = append(ids, t.id)
		}
		fmt.Fprintf(a.err, "%s\n", a.ue.Dim("tools: "+strings.Join(ids, ", ")))
	}

	next := func() (string, bool) { return a.readTurn(sc, live) }
	if live && !useTools {
		// Tab-completing a /command needs raw terminal input, which would
		// also swallow the y/n a tool call asks for; --tools keeps plain
		// line reading so that prompt works.
		if f, ok := a.in.(*os.File); ok {
			if restore, ok := ui.EnterCbreak(f); ok {
				defer restore()
				br := bufio.NewReader(f)
				var history []string
				next = func() (string, bool) {
					return a.editTurnWith(br, a.ue.Accent("› "), lineOpts{complete: slashCompleter(slashCommands), history: &history})
				}
			}
		}
	}
	for {
		line, ok := next()
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
		recalled := ""
		if recall {
			hits, err := a.searchMemory(ctx, line, defaultRecall, profile, sessions)
			if err != nil {
				fmt.Fprintf(a.err, "%s %v\n", a.ue.Fail(), err)
			} else if len(hits) > 0 {
				var entries []memory.Entry
				for _, h := range hits {
					entries = append(entries, h.Entry)
				}
				recalled = memory.Prompt(nil, entries)
			}
		}
		if useTools {
			turnSystem := toolSystemBase
			if recalled != "" {
				turnSystem = strings.TrimSpace(turnSystem + "\n\n" + recalled)
			}
			if recent := fitTurns(turns, m.Context, 2048, len(turnSystem)+len(line)); len(recent) > 0 {
				turnSystem = strings.TrimSpace(turnSystem + "\n\n" + renderTurns(recent))
			}
			answer, err := a.runAgentLoop(ctx, profile, sessions, turnSystem, line, wd, tools, steps)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				fmt.Fprintf(a.err, "%s %v\n", a.ue.Fail(), err)
				continue
			}
			answer = strings.TrimSpace(answer)
			fmt.Fprintln(a.out, answer)
			turns = append(turns, mrt.Turn{Role: "user", Text: line}, mrt.Turn{Role: "assistant", Text: answer})
			a.record(task.Task{ID: "chat"}, line, task.Result{Value: task.Value{Text: answer}})
			continue
		}
		turnSystem := system
		if recalled != "" {
			turnSystem = strings.TrimSpace(turnSystem + "\n\n" + recalled)
		}
		req := mrt.Request{System: turnSystem, Prompt: line, Temperature: 0.2, MaxTokens: 2048}
		req.History = fitTurns(turns, m.Context, req.MaxTokens, len(turnSystem)+len(line))
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
		a.record(task.Task{ID: "chat"}, line, task.Result{Value: task.Value{Text: res.Text}})
	}
}

// readTurn reads one turn, joining lines that end in a backslash.
func (a *app) readTurn(sc *bufio.Scanner, live bool) (string, bool) {
	return a.readTurnPrompt(sc, live, a.ue.Accent("› "))
}

// readTurnPrompt is readTurn with its own prompt for the first line.
func (a *app) readTurnPrompt(sc *bufio.Scanner, live bool, first string) (string, bool) {
	var parts []string
	for {
		if live {
			prompt := first
			if len(parts) > 0 {
				prompt = a.ue.Dim("… ")
			}
			fmt.Fprint(a.err, prompt)
		}
		if !sc.Scan() {
			if live {
				fmt.Fprintln(a.err)
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n"), true
			}
			return "", false
		}
		line := sc.Text()
		if strings.HasSuffix(line, "\\") {
			parts = append(parts, strings.TrimSuffix(line, "\\"))
			continue
		}
		parts = append(parts, line)
		return strings.TrimSpace(strings.Join(parts, "\n")), true
	}
}

// renderTurns shows earlier chat turns as plain text, so a tool-calling
// turn's system prompt can carry the conversation the think/act/observe
// loop otherwise never sees (its own goal and history are this turn's).
func renderTurns(turns []mrt.Turn) string {
	var b strings.Builder
	b.WriteString("Recent conversation:\n")
	for _, t := range turns {
		fmt.Fprintf(&b, "%s: %s\n", t.Role, t.Text)
	}
	return strings.TrimSpace(b.String())
}

// fitTurns keeps the most recent turns that fit in a context of ctxTokens
// beside a reply and used characters of system prompt and question, at
// about three characters a token. Turns go in pairs, so the model never
// sees an answer without its question.
func fitTurns(turns []mrt.Turn, ctxTokens, reply, used int) []mrt.Turn {
	if ctxTokens <= 0 {
		return turns
	}
	budget := (ctxTokens-reply-256)*3 - used
	start := len(turns)
	for start >= 2 {
		n := len(turns[start-2].Text) + len(turns[start-1].Text)
		if n > budget {
			break
		}
		budget -= n
		start -= 2
	}
	return turns[start:]
}
