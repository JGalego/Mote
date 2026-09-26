package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
)

// `mote do "..."` takes a request in plain words, picks the task that fits
// and runs it. Two routers can decide: "text" asks the text model for one
// task id, constrained by a JSON schema so a small model cannot invent one,
// and "embed" compares the request with each task's description using the
// embed model, which is one encoder pass instead of a generated reply.

// routeSystem keeps the model from explaining itself; the schema already
// forces the shape, and a small model that starts prosing wastes tokens.
const routeSystem = "You route requests to tools. Reply with the single best task id."

// routable returns the tasks a spoken or typed request can drive, which
// means their first parameter is text or a file the request can name.
func routable(tasks []task.Task) []task.Task {
	var out []task.Task
	for _, t := range tasks {
		if len(t.Params) == 0 {
			continue
		}
		switch t.Params[0].Kind {
		case "text", "file", "dir":
			out = append(out, t)
		}
	}
	return out
}

// catalogue describes the choices for the model, one task per line.
func catalogue(tasks []task.Task) string {
	var b strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&b, "%s: %s (takes %s, produces %s)\n",
			t.ID, t.Summary, strings.Join(t.In, "+"), t.Out)
	}
	return b.String()
}

// routeSchema constrains the reply to one of the ids.
func routeSchema(tasks []task.Task) string {
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"task": map[string]any{"type": "string", "enum": ids}},
		"required":             []string{"task"},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// routeTask is the pipeline the text router runs: one constrained
// generation. Building it here rather than in tasks.json keeps the list of
// ids, which changes with the user's own tasks, out of the data file.
func routeTask() task.Task {
	return task.Task{
		ID:      "route",
		Summary: "Choose the task that fits a request",
		In:      []string{"text"},
		Out:     "data",
		Params: []task.Param{
			{Name: "request", Kind: "text"},
			{Name: "schema", Kind: "text"},
		},
		Steps: []task.Step{{
			Op: "generate", Cap: "text", As: "out",
			JSONSchema: "schema",
			System:     routeSystem,
			Prompt:     "Tasks:\n{{catalogue}}\nRequest: {{request}}\n\nWhich task fits best?",
		}},
	}
}

// routeText asks the text model to choose, with the answer constrained to a
// real id so a small model cannot invent one.
func (a *app) routeText(ctx context.Context, request string, tasks []task.Task, profile string, sessions map[string]mrt.Session) (string, error) {
	t := routeTask()
	t.Steps[0].Prompt = strings.ReplaceAll(t.Steps[0].Prompt, "{{catalogue}}", catalogue(tasks))
	res, err := t.Run(ctx, a.env(profile, sessions), []string{request, routeSchema(tasks)}, task.Options{})
	if err != nil {
		return "", err
	}
	var picked struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal([]byte(res.Text), &picked); err != nil {
		return "", fmt.Errorf("router replied with %.120s", res.Text)
	}
	if picked.Task == "" {
		return "", fmt.Errorf("router chose nothing")
	}
	return picked.Task, nil
}

// bindRequest turns a request into arguments for the chosen task: words
// naming something on disk fill its file and dir parameters, in order, and
// what remains fills its first text parameter. A task with no file
// parameters gets the request as written, since nothing was taken out of it.
func bindRequest(t task.Task, request string) ([]string, error) {
	var paths, rest []string
	for _, w := range strings.Fields(request) {
		clean := strings.Trim(w, `"'.,;:!?`)
		if clean != "" {
			if _, err := os.Stat(clean); err == nil {
				paths = append(paths, clean)
				continue
			}
		}
		rest = append(rest, w)
	}
	text := request
	if len(paths) > 0 {
		text = strings.Join(rest, " ")
	}

	var args []string
	textUsed := false
	for _, p := range t.Params {
		optional := p.Optional || p.Default != nil
		switch p.Kind {
		case "file", "dir":
			if len(paths) == 0 {
				if optional {
					return args, nil // nothing left to give it
				}
				return nil, usagef("%s needs a %s; name one in the request", t.ID, p.Kind)
			}
			args = append(args, paths[0])
			paths = paths[1:]
		case "text":
			if textUsed || strings.TrimSpace(text) == "" {
				return args, nil
			}
			args = append(args, text)
			textUsed = true
		}
	}
	return args, nil
}

// do implements `mote do`.
func (a *app) do(ctx context.Context, args []string) error {
	vals, pos, err := flags(args,
		[]string{"-o", "--output", "--model", "--profile", "--router"},
		[]string{"--apply", "--dry-run"})
	if err != nil {
		return err
	}
	request := strings.TrimSpace(strings.Join(pos, " "))
	if request == "" {
		return usagef(`usage: mote do "REQUEST" [--router text|embed] [--dry-run]`)
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	choices := routable(tasks)
	if len(choices) == 0 {
		return fmt.Errorf("no task can take a request")
	}
	router := firstNonEmpty(vals["--router"], a.cfg.Router, "text")
	profile, err := a.selectModels(vals)
	if err != nil {
		return err
	}
	sessions := map[string]mrt.Session{}
	defer task.CloseSessions(sessions)

	var id string
	switch router {
	case "text":
		id, err = a.routeText(ctx, request, choices, profile, sessions)
	case "embed":
		id, err = a.routeEmbed(ctx, request, choices, profile, sessions)
	default:
		return usagef("unknown router %q", router)
	}
	if err != nil {
		return err
	}
	t, ok := task.Find(choices, id)
	if !ok {
		return fmt.Errorf("router chose %q, which is not a task", id)
	}
	taskArgs, err := bindRequest(t, request)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.err, "%s %s\n", a.ue.Arrow(),
		a.ue.Dim("mote run "+t.ID+" "+strings.Join(quoteArgs(taskArgs), " ")))
	if vals["--dry-run"] == "true" {
		return nil
	}
	out := firstNonEmpty(vals["-o"], vals["--output"])
	res, err := t.Run(ctx, a.env(profile, sessions), taskArgs, task.Options{Output: out, Apply: vals["--apply"] == "true"})
	if err != nil {
		return err
	}
	return a.emit(t, res, out, nil)
}

// quoteArgs shows the arguments the way you would have to type them.
func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		if strings.ContainsAny(a, " \t\"'") {
			out[i] = strconv.Quote(a)
		}
	}
	return out
}

// describeTask is what the embed router compares a request against. The
// summary carries the meaning; the id and the words around it help when a
// request names the task outright.
func describeTask(t task.Task) string {
	return fmt.Sprintf("%s: %s. Takes %s and produces %s.",
		t.ID, t.Summary, strings.Join(t.In, " and "), t.Out)
}

// nearest returns the task whose description sits closest to the request.
func nearest(request []float32, tasks [][]float32, ids []string) (string, float64) {
	best, score := "", -2.0
	for i, v := range tasks {
		if s := mrt.Cosine(request, v); s > score {
			best, score = ids[i], s
		}
	}
	return best, score
}

// routeEmbed picks a task with the embed model: one encoder pass over the
// request and one per task description, then the closest match. No tokens
// are generated, so it costs milliseconds rather than a reply.
func (a *app) routeEmbed(ctx context.Context, request string, tasks []task.Task, profile string, sessions map[string]mrt.Session) (string, error) {
	env := a.env(profile, sessions)
	m, files, err := env.Resolve(ctx, "embed")
	if err != nil {
		return "", err
	}
	b, err := env.Backend(m)
	if err != nil {
		return "", err
	}
	sess, ok := sessions[m.ID]
	if !ok {
		done := a.status("loading " + m.ID + " for embed")
		sess, err = b.Open(ctx, m, files)
		done(err == nil)
		if err != nil {
			return "", err
		}
		sessions[m.ID] = sess
	}
	embedder, ok := sess.(mrt.Embedder)
	if !ok {
		return "", fmt.Errorf("%s cannot produce embeddings", m.ID)
	}
	texts := make([]string, 0, len(tasks)+1)
	ids := make([]string, 0, len(tasks))
	texts = append(texts, request)
	for _, t := range tasks {
		texts = append(texts, describeTask(t))
		ids = append(ids, t.ID)
	}
	vecs, err := embedder.Embed(ctx, texts)
	if err != nil {
		return "", err
	}
	id, score := nearest(vecs[0], vecs[1:], ids)
	if id == "" {
		return "", fmt.Errorf("no task was close to the request")
	}
	fmt.Fprintf(a.err, "%s\n", a.ue.Dim(fmt.Sprintf("embed router chose %s (cosine %.3f)", id, score)))
	return id, nil
}
