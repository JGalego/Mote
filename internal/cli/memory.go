package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jgalego/mote/internal/memory"
	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
)

// Memory is three local files' worth of recall, all opt-in: facts the user
// dictates with `mote remember`, a history of exchanges kept only when
// `memory` is on in the config, and search over that history with the embed
// model. Nothing is recorded or recalled unless asked for, and everything
// lives in files the user can read, edit and delete.

// defaultRecall is how many past exchanges --recall brings back.
const defaultRecall = 3

func (a *app) memoryStore() memory.Store { return memory.New(a.cfgDir, a.dataDir()) }

// memoryFor builds the text to put in front of model steps for one request.
// Facts always apply; --continue adds the last exchanges and --recall adds
// the ones closest in meaning, which needs the embed model.
func (a *app) memoryFor(ctx context.Context, request string, vals map[string]string, profile string, sessions map[string]mrt.Session) (string, error) {
	store := a.memoryStore()
	facts, err := store.Facts()
	if err != nil {
		return "", err
	}
	var recalled []memory.Entry
	if n := vals["--continue"]; n == "true" {
		entries, err := store.History(defaultRecall)
		if err != nil {
			return "", err
		}
		recalled = entries
	}
	if vals["--recall"] == "true" {
		hits, err := a.searchMemory(ctx, request, defaultRecall, profile, sessions)
		if err != nil {
			return "", err
		}
		for _, h := range hits {
			recalled = append(recalled, h.Entry)
		}
	}
	return memory.Prompt(facts, recalled), nil
}

// searchMemory finds past exchanges close in meaning to the request.
func (a *app) searchMemory(ctx context.Context, query string, k int, profile string, sessions map[string]mrt.Session) ([]memory.Hit, error) {
	em, prefix, err := a.embedder(ctx, profile, sessions)
	if err != nil {
		return nil, err
	}
	return a.memoryStore().Search(ctx, em, query, prefix, k, mrt.Cosine)
}

// embedder opens (or reuses) the embed model and reports its query prefix.
func (a *app) embedder(ctx context.Context, profile string, sessions map[string]mrt.Session) (mrt.Embedder, string, error) {
	env := a.env(profile, sessions)
	m, files, err := env.Resolve(ctx, "embed")
	if err != nil {
		return nil, "", err
	}
	sess, ok := sessions[m.ID]
	if !ok {
		b, err := env.Backend(m)
		if err != nil {
			return nil, "", err
		}
		done := a.status("loading " + m.ID + " for embed")
		sess, err = b.Open(ctx, m, files)
		done(err == nil)
		if err != nil {
			return nil, "", err
		}
		sessions[m.ID] = sess
	}
	em, ok := sess.(mrt.Embedder)
	if !ok {
		return nil, "", fmt.Errorf("%s cannot produce embeddings", m.ID)
	}
	return em, m.QueryPrefix, nil
}

// record stores one exchange, when the config asks mote to keep a history.
func (a *app) record(t task.Task, request string, res task.Result) {
	if !a.cfg.Memory || len(res.Files) > 0 {
		return
	}
	err := a.memoryStore().Record(memory.Entry{
		Time: time.Now(), Task: t.ID, Request: request, Response: res.Text,
	})
	if err != nil {
		fmt.Fprintf(a.err, "%s could not write history: %v\n", a.ue.Warn(), err)
	}
}

// remember implements `mote remember "..."`.
func (a *app) remember(args []string) error {
	fact := strings.TrimSpace(strings.Join(args, " "))
	if fact == "" {
		return usagef(`usage: mote remember "something worth keeping"`)
	}
	if err := a.memoryStore().Remember(fact); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s remembered\n", a.uo.OK())
	return nil
}

// forget implements `mote forget N|--all`.
func (a *app) forget(args []string) error {
	vals, pos, err := flags(args, nil, []string{"--all", "--history"})
	if err != nil {
		return err
	}
	store := a.memoryStore()
	switch {
	case vals["--history"] == "true":
		if err := store.Clear(); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s history cleared\n", a.uo.OK())
	case vals["--all"] == "true":
		if err := store.ForgetAll(); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s facts forgotten\n", a.uo.OK())
	case len(pos) == 1:
		n, err := strconv.Atoi(pos[0])
		if err != nil {
			return usagef("usage: mote forget N | --all | --history")
		}
		if err := store.Forget(n); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s forgotten\n", a.uo.OK())
	default:
		return usagef("usage: mote forget N | --all | --history")
	}
	return nil
}

// memoryCmd implements `mote memory [search QUERY]`.
func (a *app) memoryCmd(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"--model", "--profile"}, nil)
	if err != nil {
		return err
	}
	store := a.memoryStore()
	if len(pos) > 0 && pos[0] == "search" {
		query := strings.TrimSpace(strings.Join(pos[1:], " "))
		if query == "" {
			return usagef(`usage: mote memory search "what you are looking for"`)
		}
		profile, err := a.selectModels(vals)
		if err != nil {
			return err
		}
		sessions := map[string]mrt.Session{}
		defer task.CloseSessions(sessions)
		hits, err := a.searchMemory(ctx, query, defaultRecall*2, profile, sessions)
		if err != nil {
			return err
		}
		if len(hits) == 0 {
			fmt.Fprintln(a.out, a.uo.Dim("nothing remembered yet"))
			return nil
		}
		for _, h := range hits {
			fmt.Fprintf(a.out, "%s %s %s\n", a.uo.Bold(fmt.Sprintf("%.2f", h.Score)),
				a.uo.Accent(pad(h.Task, 11)), oneLine(h.Request))
			fmt.Fprintf(a.out, "%-16s %s\n", "", a.uo.Dim(oneLine(h.Response)))
		}
		return nil
	}
	if len(pos) > 0 {
		return usagef(`usage: mote memory [search "QUERY"]`)
	}

	facts, err := store.Facts()
	if err != nil {
		return err
	}
	if len(facts) == 0 {
		fmt.Fprintln(a.out, a.uo.Dim(`no facts; add one with mote remember "..."`))
	}
	for i, f := range facts {
		fmt.Fprintf(a.out, "%s %s\n", a.uo.Bold(fmt.Sprintf("%2d.", i+1)), f)
	}
	entries, err := store.History(defaultRecall)
	if err != nil {
		return err
	}
	if !a.cfg.Memory {
		fmt.Fprintln(a.out, a.uo.Dim("history off; turn it on with mote config set memory true"))
	}
	for _, e := range entries {
		fmt.Fprintf(a.out, "%s %s %s\n", a.uo.Dim(e.Time.Format("2006-01-02 15:04")),
			a.uo.Accent(pad(e.Task, 11)), oneLine(e.Request))
	}
	return nil
}

// oneLine keeps a remembered exchange to a single readable line.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return fitWidth(s, 72)
}

func fitWidth(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
