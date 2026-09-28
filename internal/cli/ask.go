package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jgalego/mote/internal/index"
	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
)

const askSystem = "You answer questions using only the numbered excerpts from the user's own files. " +
	"Cite the excerpts you used like [2]. If they do not contain the answer, say that you could not find it in the files."

// defaultTop is how many passages a question is answered from.
const defaultTop = 6

func (a *app) indexPath() string { return index.Path(a.dataDir()) }

// indexCmd implements `mote index`.
func (a *app) indexCmd(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "status", "ls", "list":
			return a.indexStatus()
		case "rm", "remove":
			return a.indexRemove(args[1:])
		}
	}
	vals, pos, err := flags(args, []string{"--profile"}, nil)
	if err != nil {
		return err
	}
	ix, err := index.Load(a.indexPath())
	if err != nil {
		return err
	}
	roots := pos
	if len(roots) == 0 {
		if roots = ix.Roots; len(roots) == 0 {
			return usagef("usage: mote index DIR... (then `mote ask \"QUESTION\"`)")
		}
	}
	profile, err := a.selectModels(vals)
	if err != nil {
		return err
	}
	sessions := map[string]mrt.Session{}
	defer task.CloseSessions(sessions)
	em, m, err := a.embedModel(ctx, profile, sessions)
	if err != nil {
		return err
	}
	env := a.env(profile, sessions)
	git, _ := a.tool("git")
	sp := a.ue.Spin("indexing " + strings.Join(roots, ", "))
	st, err := ix.Update(ctx, roots, index.Options{
		Model: m, Embed: em, Git: git,
		Read: func(p string) (string, error) { return task.ExtractText(ctx, env, p, false) },
		Progress: func(n, total int, p string) {
			sp.Set(fmt.Sprintf("indexing %d/%d %s", n, total, filepath.Base(p)))
		},
	})
	sp.Stop("")
	if err != nil {
		// What was indexed before the failure is kept.
		ix.Save()
		return err
	}
	if err := ix.Save(); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s %d files: %d new, %d changed, %d removed, %d unreadable · %d passages in the index\n",
		a.uo.OK(), st.Files, st.Added, st.Changed, st.Removed, st.Skipped, st.Chunks)
	return nil
}

func (a *app) indexStatus() error {
	ix, err := index.Load(a.indexPath())
	if err != nil {
		return err
	}
	if len(ix.Roots) == 0 {
		fmt.Fprintln(a.out, "nothing indexed yet; `mote index DIR` adds a folder")
		return nil
	}
	count := map[string][2]int{}
	var unreadable []string
	for p, f := range ix.Files {
		for _, r := range ix.Roots {
			if rel, err := filepath.Rel(r, p); err == nil && !strings.HasPrefix(rel, "..") {
				c := count[r]
				c[0]++
				c[1] += f.Chunks
				count[r] = c
				break
			}
		}
		if f.Skipped != "" {
			unreadable = append(unreadable, p+": "+f.Skipped)
		}
	}
	for _, r := range ix.Roots {
		fmt.Fprintf(a.out, "%s %s\n", a.uo.Bold(r), a.uo.Dim(fmt.Sprintf("%d files, %d passages", count[r][0], count[r][1])))
	}
	fmt.Fprintln(a.out, a.uo.Dim(fmt.Sprintf("embedded with %s, updated %s; `mote index` refreshes",
		ix.Model, ix.Updated.Local().Format("2006-01-02 15:04"))))
	if len(unreadable) > 0 {
		fmt.Fprintf(a.out, "%s %d files could not be read, e.g. %s\n", a.uo.Warn(), len(unreadable), unreadable[0])
	}
	return nil
}

func (a *app) indexRemove(args []string) error {
	vals, pos, err := flags(args, nil, []string{"--all"})
	if err != nil {
		return err
	}
	if vals["--all"] == "true" {
		if err := os.Remove(a.indexPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintf(a.out, "%s index removed\n", a.uo.OK())
		return nil
	}
	if len(pos) == 0 {
		return usagef("usage: mote index rm DIR... | --all")
	}
	ix, err := index.Load(a.indexPath())
	if err != nil {
		return err
	}
	for _, p := range pos {
		n, err := ix.Remove(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s forgot %s (%d files)\n", a.uo.OK(), p, n)
	}
	return ix.Save()
}

// embedModel opens the embedding model and returns it with its id.
func (a *app) embedModel(ctx context.Context, profile string, sessions map[string]mrt.Session) (mrt.Embedder, string, error) {
	em, _, err := a.embedder(ctx, profile, sessions)
	if err != nil {
		return nil, "", err
	}
	m, _, err := a.choose("embed", profile)
	if err != nil {
		return nil, "", err
	}
	return em, m.ID, nil
}

// passages finds the indexed passages closest to a question.
func (a *app) passages(ctx context.Context, question, within string, top int, profile string, sessions map[string]mrt.Session) ([]index.Hit, error) {
	ix, err := index.Load(a.indexPath())
	if err != nil {
		return nil, err
	}
	if len(ix.Chunks) == 0 {
		return nil, missingf("nothing indexed yet; run `mote index DIR` on the folders to ask about")
	}
	em, id, err := a.embedModel(ctx, profile, sessions)
	if err != nil {
		return nil, err
	}
	if id != ix.Model {
		return nil, missingf("the index was built with %s, and the embedding model is now %s; run `mote index` to rebuild it", ix.Model, id)
	}
	m, _, _ := a.choose("embed", profile)
	vecs, err := em.Embed(ctx, []string{m.QueryPrefix + question})
	if err != nil {
		return nil, err
	}
	if within != "" {
		if within, err = filepath.Abs(within); err != nil {
			return nil, err
		}
	}
	return ix.Search(vecs[0], top, within, mrt.Cosine), nil
}

// excerpts numbers passages for a prompt.
func excerpts(hits []index.Hit) string {
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] %s, line %d\n%s\n\n", i+1, h.Path, h.Line, h.Text)
	}
	return b.String()
}

// askCmd implements `mote ask`: an answer from the passages of the indexed
// files closest to the question, with the sources it came from.
func (a *app) askCmd(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"--in", "--top", "--model", "--profile", "-o", "--output"}, []string{"--sources"})
	if err != nil {
		return err
	}
	question := strings.TrimSpace(strings.Join(pos, " "))
	if question == "" {
		return usagef(`usage: mote ask "QUESTION" [--in DIR] [--top N] [--sources]`)
	}
	top := defaultTop
	if v := vals["--top"]; v != "" {
		if top, err = strconv.Atoi(v); err != nil || top < 1 || top > 50 {
			return usagef("--top expects a number from 1 to 50")
		}
	}
	profile, err := a.selectModels(vals)
	if err != nil {
		return err
	}
	sessions := map[string]mrt.Session{}
	defer task.CloseSessions(sessions)
	hits, err := a.passages(ctx, question, vals["--in"], top, profile, sessions)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		return fmt.Errorf("no indexed files under %s", vals["--in"])
	}
	if vals["--sources"] == "true" {
		for _, h := range hits {
			fmt.Fprintf(a.out, "%s %s\n%s\n\n", a.uo.Bold(fmt.Sprintf("%s:%d", h.Path, h.Line)), a.uo.Dim(fmt.Sprintf("%.3f", h.Score)), h.Text)
		}
		return nil
	}
	env := a.env(profile, sessions)
	if env.Memory, err = a.memoryFor(ctx, question, nil, profile, sessions); err != nil {
		return err
	}
	t := task.Task{ID: "ask", Out: "text",
		Params: []task.Param{{Name: "excerpts", Kind: "text"}, {Name: "question", Kind: "text"}},
		Steps: []task.Step{{Op: "generate", Cap: "text", System: askSystem,
			Prompt: "Excerpts from my files:\n\n{{excerpts}}Question: {{question}}", As: "out"}}}
	out := firstNonEmpty(vals["-o"], vals["--output"])
	if out == "" && a.uo.Live() {
		env.Stream = func(tok string) { fmt.Fprint(a.out, tok) }
	}
	defer os.RemoveAll(env.TempDir)
	res, err := t.Run(ctx, env, []string{excerpts(hits), question}, task.Options{Output: out})
	if err != nil {
		return err
	}
	a.record(t, question, res)
	if err := a.emit(t, res, out, nil); err != nil {
		return err
	}
	// The sources, so the answer can be checked.
	for i, h := range hits {
		fmt.Fprintln(a.err, a.ue.Dim(fmt.Sprintf("[%d] %s:%d", i+1, h.Path, h.Line)))
	}
	return nil
}

// indexTool offers the index to the agent, when there is one.
func (a *app) indexTool(profile string, sessions map[string]mrt.Session) (agentTool, bool) {
	ix, err := index.Load(a.indexPath())
	if err != nil || len(ix.Chunks) == 0 {
		return agentTool{}, false
	}
	return agentTool{
		id:        "files",
		signature: "files(query)",
		summary:   "Find the passages of the user's indexed files (" + strings.Join(ix.Roots, ", ") + ") closest in meaning to a query",
		examples:  []string{"what did we decide about the budget", "where are my notes on retries"},
		schema:    object(prop{"query", map[string]any{"type": "string"}}),
		run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.Query) == "" {
				return "", fmt.Errorf("files needs a query")
			}
			hits, err := a.passages(ctx, args.Query, "", 4, profile, sessions)
			if err != nil {
				return "", err
			}
			return excerpts(hits), nil
		},
	}, true
}
