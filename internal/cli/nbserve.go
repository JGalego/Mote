package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/jgalego/mote/internal/motebook"
	"github.com/jgalego/mote/internal/task"
	"github.com/jgalego/mote/internal/web"
)

// `mote nb serve FILE` opens a notebook in the browser: prose to read, cells
// to edit and run, and the images, audio and video they make, shown where they
// are. It serves this machine only, to whoever has the address it prints,
// which carries a secret; the notebook file stays the source of truth and is
// saved after every change.

const nbServeUsage = "usage: mote nb serve FILE [--port N] [--root DIR] [--open] [--yes] [--model ID] [--profile P]"

// webRunner runs the cells of a notebook the page shows, with mote's tasks.
type webRunner struct {
	a      *app
	tasks  []task.Task
	models *nbModels
	vals   map[string]string
}

func (r *webRunner) prepare(expr string) (nbCell, error) {
	return prepareCell(motebook.Cell{Expr: expr, Refs: motebook.Refs(expr)}, r.tasks)
}

func (r *webRunner) Check(expr string) (bool, error) {
	c, err := r.prepare(expr)
	return c.changes, err
}

func (r *webRunner) Run(ctx context.Context, expr string, values map[string]string, progress func(string)) (string, bool, error) {
	c, err := r.prepare(expr)
	if err != nil {
		return "", false, err
	}
	// What the tasks say they are doing goes to the page, not to a terminal
	// that nobody is watching the cell on.
	hook := func(msg string) func(bool) {
		progress(msg)
		return func(bool) {}
	}
	r.a.statusHook.Store(&hook)
	defer r.a.statusHook.Store(nil)
	started := time.Now()
	text, files, err := r.run(ctx, c, values)
	r.a.cellDone(oneLine(expr), started, err == nil)
	return text, files, err
}

func (r *webRunner) run(ctx context.Context, c nbCell, values map[string]string) (string, bool, error) {
	if err := r.models.load(ctx); err != nil {
		return "", false, err
	}
	return r.a.runCell(ctx, c, values, r.vals, r.models)
}

func (a *app) nbServe(ctx context.Context, path string, vals map[string]string) error {
	port := 0
	if v := vals["--port"]; v != "" {
		var err error
		if port, err = strconv.Atoi(v); err != nil || port < 0 || port > 65535 {
			return usagef("--port is a number from 0 to 65535")
		}
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	models := a.newNbModels(vals)
	defer models.close()
	srv, err := web.New(web.Options{
		Path:    path,
		Root:    vals["--root"],
		Runner:  &webRunner{a: a, tasks: tasks, models: models, vals: vals},
		Approve: vals["--yes"] == "true" || vals["-y"] == "true",
	})
	if err != nil {
		return usagef("%v", err)
	}
	defer srv.Close()

	// This machine only: the address is not for anyone else to open.
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("http://%s/?token=%s", ln.Addr(), srv.Token())
	fmt.Fprintf(a.err, "%s serving %s\n  %s\n%s\n", a.ue.OK(), path, a.ue.Bold(addr), a.ue.Dim("this address opens a session and works only on this machine; Ctrl-C stops"))
	if vals["--open"] == "true" {
		if err := openBrowser(addr); err != nil {
			fmt.Fprintf(a.err, "%s could not open a browser (%v); open the address above\n", a.ue.Warn(), err)
		}
	}

	go srv.Watch(ctx, time.Second) // a change made in an editor shows in the page
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		// Pages listening for events never let go, so ask politely, then don't.
		grace, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if hs.Shutdown(grace) != nil {
			hs.Close()
		}
	}()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openBrowser asks the desktop to open a page.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
