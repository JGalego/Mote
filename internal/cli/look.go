package cli

import (
	"fmt"
	"strings"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/ui"
)

// barProgress renders downloads as progress bars on stderr.
type barProgress struct{ u *ui.UI }

func (b barProgress) Start(label string, have, total int64) mrt.Tracker {
	return b.u.NewBar(label, have, total)
}

// status returns a task.Env.Status implementation drawing spinners on stderr.
func (a *app) status(msg string) func(bool) {
	if hook := a.statusHook.Load(); hook != nil {
		return (*hook)(msg)
	}
	sp := a.ue.Spin(msg)
	return func(ok bool) {
		if strings.HasPrefix(msg, "thinking") && ok {
			sp.Stop("")
			return
		}
		mark := a.ue.OK()
		if !ok {
			mark = a.ue.Fail()
		}
		sp.Stop(fmt.Sprintf("%s %s %s", mark, msg, a.ue.Dim(fmt.Sprintf("%.1fs", sp.Elapsed().Seconds()))))
	}
}

// pad left-aligns s to width n before colouring, so tables line up.
func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func (a *app) usage() {
	o := a.uo
	fmt.Fprint(a.out, o.Banner("small models · local machines · useful work"))
	for _, line := range strings.Split(strings.TrimRight(usage, "\n"), "\n")[1:] {
		switch {
		case line == "Usage:" || line == "Examples:":
			fmt.Fprintln(a.out, o.Bold(line))
		case strings.HasPrefix(line, "  mote "):
			cmd, rest, _ := strings.Cut(strings.TrimPrefix(line, "  mote "), " ")
			fmt.Fprintf(a.out, "  %s %s %s\n", o.Dim("mote"), o.Cyan(cmd), rest)
		default:
			fmt.Fprintln(a.out, line)
		}
	}
}
