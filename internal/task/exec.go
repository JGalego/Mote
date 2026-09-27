package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// An exec step runs a local program with an argument vector, so a tool
// already on the machine becomes a task without any Go:
//
//	{"op": "exec", "cmd": ["rg", "-n", "--", "{{pattern}}", "{{dir}}"], "as": "out"}
//
// Arguments are substituted one by one and never pass through a shell, so a
// value containing spaces, quotes or ; stays a single argument. Its standard
// output becomes the value; "from" feeds a value on stdin and "dir" sets the
// working directory.

// wholeRef matches an argument that is nothing but one {{name}}.
var wholeRef = regexp.MustCompile(`^\{\{([a-z_]+)\}\}$`)

func (s Step) validateCmd() error {
	if s.Op != "exec" {
		if len(s.Cmd) > 0 {
			return fmt.Errorf("cmd is only for exec steps, not %s", s.Op)
		}
		return nil
	}
	if len(s.Cmd) == 0 || strings.TrimSpace(s.Cmd[0]) == "" {
		return errors.New(`exec needs "cmd", starting with the program to run`)
	}
	if strings.Contains(s.Cmd[0], "{{") {
		return errors.New("the program in cmd must be fixed, not a {{value}}")
	}
	return nil
}

// execArgs substitutes values into cmd's arguments. An argument that is
// only a reference to an unset or empty value is dropped, which is how an
// optional parameter leaves no trace. A value that begins with - is refused
// unless a literal -- came before it: otherwise a value such as
// "--pre=sh" would be read by the program as an option, which matters once
// the values come from a model rather than from the person typing.
func (r *run) execArgs(cmd []string) ([]string, error) {
	var out []string
	endOfOptions := false
	for _, a := range cmd {
		if a == "--" {
			endOfOptions = true
		}
		m := wholeRef.FindStringSubmatch(a)
		if m == nil {
			out = append(out, r.expand(a))
			continue
		}
		v := r.vars[m[1]].Text
		if v == "" {
			continue
		}
		if strings.HasPrefix(v, "-") && !endOfOptions {
			return nil, fmt.Errorf("%w: %s is %q, which %s would read as an option; put \"--\" before {{%s}} in cmd if it may start with -",
				ErrUsage, m[1], v, filepath.Base(cmd[0]), m[1])
		}
		out = append(out, v)
	}
	return out, nil
}

// capped keeps the first max bytes written to it and notes whether more
// arrived, so a chatty program cannot fill memory or a prompt.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func opExec(r *run, s Step) (Value, error) {
	prog, err := r.tool(s.Cmd[0])
	if err != nil {
		return Value{}, err
	}
	args, err := r.execArgs(s.Cmd[1:])
	if err != nil {
		return Value{}, err
	}
	cmd := exec.CommandContext(r.ctx, prog, args...)
	if s.Dir != "" {
		cmd.Dir = r.vars[s.Dir].Text
	}
	if s.From != "" {
		in := r.vars[s.From]
		stdin := in.Text
		if len(in.Files) > 0 && in.Text == in.Files[0] {
			stdin = strings.Join(in.Files, "\n") + "\n"
		}
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout := &capped{max: maxRead}
	stderr := &capped{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	done := r.status("running " + s.Cmd[0])
	err = cmd.Run()
	done(err == nil)
	if err != nil {
		if ctxErr := r.ctx.Err(); errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded) {
			return Value{}, ctxErr
		}
		msg := strings.TrimSpace(lastLines(stderr.buf.String(), 5))
		if msg == "" {
			msg = strings.TrimSpace(lastLines(stdout.buf.String(), 5))
		}
		return Value{}, fmt.Errorf("%s: %v: %s", filepath.Base(prog), err, msg)
	}
	if stdout.over {
		r.logf("exec: %s wrote more than %d KiB; the rest was dropped", s.Cmd[0], maxRead>>10)
	}
	return Value{Text: stdout.buf.String()}, nil
}
