package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jgalego/mote/registry"
)

// wanted returns the models the configuration uses, one per capability,
// with the capabilities each serves. Optional capabilities count only when
// an installed model already provides one, so an upgrade keeps them current
// without pulling them in for someone who never used them.
func (a *app) wanted() ([]*registry.Model, map[string][]string) {
	reg := a.registry()
	used := map[string]bool{}
	for i := range reg.Models {
		if m := &reg.Models[i]; a.store().Installed(m) {
			for _, c := range m.Caps {
				used[c] = true
			}
		}
	}
	var ms []*registry.Model
	caps := map[string][]string{}
	for _, c := range capNames() {
		if registry.Optional[c] && !used[c] {
			continue
		}
		m, _, err := a.choose(c, a.cfg.Profile)
		if err != nil {
			fmt.Fprintf(a.err, "%s %s: %v\n", a.ue.Warn(), c, err)
			continue
		}
		if caps[m.ID] == nil {
			ms = append(ms, m)
		}
		caps[m.ID] = append(caps[m.ID], c)
	}
	return ms, caps
}

// pullable returns every registry model this machine can run, warning about
// those whose runtime has no build for it.
func (a *app) pullable() []*registry.Model {
	reg := a.registry()
	var ms []*registry.Model
	for i := range reg.Models {
		m := &reg.Models[i]
		if m.Backend != "llama.cpp" {
			if _, _, err := a.runtimeFor(m); err != nil {
				fmt.Fprintf(a.err, "%s skipping %s: %v\n", a.ue.Warn(), m.ID, err)
				continue
			}
		}
		ms = append(ms, m)
	}
	return ms
}

// unused returns the model directories in the data directory that hold none
// of keep, including models the registry no longer lists.
func (a *app) unused(keep []*registry.Model) []string {
	ids := map[string]bool{}
	for _, m := range keep {
		ids[m.ID] = true
	}
	entries, _ := os.ReadDir(filepath.Join(a.dataDir(), "models"))
	var out []string
	for _, e := range entries {
		if e.IsDir() && !ids[e.Name()] {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// downloadSize is what ensure would fetch for m: its missing files and, the
// first time a runtime is seen in counted, the runtime it needs.
func (a *app) downloadSize(m *registry.Model, counted map[string]bool) int64 {
	var n int64
	for _, f := range a.store().Missing(m) {
		n += f.Size
	}
	if m.Backend != "llama.cpp" && !counted[m.Backend] {
		if rt, asset, err := a.runtimeFor(m); err == nil {
			if _, ok := a.store().RuntimeInstalled(rt, ""); !ok {
				n += asset.Size
			}
		}
		counted[m.Backend] = true
	}
	return n
}

// pullMany downloads the models in ms that are not ready and removes the
// model directories in drop, after showing both and asking once.
func (a *app) pullMany(ctx context.Context, ms []*registry.Model, drop []string, yes bool) error {
	var todo []*registry.Model
	for _, m := range ms {
		if !a.ready(m) {
			todo = append(todo, m)
		}
	}
	if len(todo) == 0 && len(drop) == 0 {
		fmt.Fprintf(a.out, "%s nothing to download\n", a.uo.OK())
		return nil
	}
	o := a.uo
	var total int64
	counted := map[string]bool{}
	for _, m := range todo {
		n := a.downloadSize(m, counted)
		total += n
		fmt.Fprintf(a.out, "  %s %s %s\n", o.Green("+"), o.Bold(pad(m.ID, 18)), o.Dim(fmt.Sprintf("%s, %s", mb(n), m.License)))
	}
	for _, id := range drop {
		fmt.Fprintf(a.out, "  %s %s %s\n", o.Warn(), o.Bold(pad(id, 18)), o.Dim("remove"))
	}
	var q string
	switch {
	case len(todo) > 0 && len(drop) > 0:
		q = fmt.Sprintf("Download %d models (%s) and remove %d?", len(todo), mb(total), len(drop))
	case len(todo) > 0:
		q = fmt.Sprintf("Download %d models (%s)?", len(todo), mb(total))
	default:
		q = fmt.Sprintf("Remove %d models?", len(drop))
	}
	if !yes {
		if !a.tty {
			return usagef("%s; pass --yes to go ahead", strings.ToLower(q[:1])+strings.TrimSuffix(q[1:], "?"))
		}
		p := &prompter{r: bufio.NewReader(a.in), a: a}
		if !p.yesNo(q, true) {
			return nil
		}
	}
	for _, m := range todo {
		if err := a.ensure(ctx, m, true); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s %s installed %s\n", o.OK(), o.Bold(m.ID), o.Dim("(sha256 verified)"))
	}
	for _, id := range drop {
		if err := a.store().Remove(&registry.Model{ID: id}); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s %s removed\n", o.OK(), o.Bold(id))
	}
	return nil
}

// upgrade moves to the newest registry and the models it picks for this
// profile and machine, removing the ones no longer used with --prune.
func (a *app) upgrade(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, nil, []string{"--check", "--prune", "--yes", "-y"})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: mote models upgrade [--check] [--prune] [--yes]")
	}
	check := vals["--check"] == "true"
	cur := a.registry()
	if next, body, err := a.fetchRegistry(ctx); err != nil {
		fmt.Fprintf(a.err, "%s %v; comparing with registry %s\n", a.ue.Warn(), err, cur.Version)
	} else if registry.Newer(next.Version, cur.Version) {
		fmt.Fprintf(a.out, "registry %s -> %s\n", cur.Version, next.Version)
		if check {
			a.reg = next // compare with it, but leave it uninstalled
		} else if err := a.adoptRegistry(ctx, cur, next, body); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(a.out, "registry %s is current\n", cur.Version)
	}

	ms, caps := a.wanted()
	o := a.uo
	stale := 0
	for _, m := range ms {
		status := o.Green("installed")
		if !a.ready(m) {
			status = o.Cyan("to download")
			stale++
		}
		fmt.Fprintf(a.out, "  %s %s %s\n", o.Bold(pad(m.ID, 18)), o.Accent(pad(strings.Join(caps[m.ID], ","), 26)), status)
	}
	unused := a.unused(ms)
	for _, id := range unused {
		fmt.Fprintf(a.out, "  %s %s %s\n", o.Bold(pad(id, 18)), pad("", 26), o.Dim("no longer used"))
	}
	if check {
		switch {
		case stale > 0:
			fmt.Fprintf(a.out, "%d models to download; `mote models upgrade` fetches them\n", stale)
		case len(unused) > 0:
			fmt.Fprintln(a.out, "up to date; `mote models upgrade --prune` removes the unused models")
		default:
			fmt.Fprintln(a.out, "up to date")
		}
		return nil
	}
	var drop []string
	if vals["--prune"] == "true" {
		drop = unused
	} else if len(unused) > 0 {
		defer fmt.Fprintf(a.out, "%d unused models kept; `mote models upgrade --prune` removes them\n", len(unused))
	}
	return a.pullMany(ctx, ms, drop, vals["--yes"] == "true" || vals["-y"] == "true")
}
