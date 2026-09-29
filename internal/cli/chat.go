package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
	"github.com/jgalego/mote/internal/ui"
)

const chatHelp = "/new starts over · /exit or Ctrl-D ends · end a line with \\ to continue it · Tab completes a /command"

// chatCmd implements `mote chat`: a conversation with the text model, which
// sees the earlier turns. Each line of input is a turn, so it works on a
// pipe as well as in a terminal.
func (a *app) chatCmd(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"--model", "--profile", "--system"}, nil)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef(`usage: mote chat [--system "INSTRUCTIONS"] [--model ID]`)
	}
	profile, err := a.selectModels(vals)
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
	system := strings.TrimSpace(remembered + "\n\n" + vals["--system"])
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
	next := func() (string, bool) { return a.readTurn(sc, live) }
	if live {
		if f, ok := a.in.(*os.File); ok {
			if restore, ok := ui.EnterCbreak(f); ok {
				defer restore()
				br := bufio.NewReader(f)
				next = func() (string, bool) { return a.editTurn(br) }
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
		a.record(task.Task{ID: "chat"}, line, task.Result{Value: task.Value{Text: res.Text}})
	}
}

// readTurn reads one turn, joining lines that end in a backslash.
func (a *app) readTurn(sc *bufio.Scanner, live bool) (string, bool) {
	var parts []string
	for {
		if live {
			prompt := a.ue.Accent("› ")
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
