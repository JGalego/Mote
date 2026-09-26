package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jgalego/mote/internal/bench"
	"github.com/jgalego/mote/internal/config"
	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
	"github.com/jgalego/mote/registry"
)

func (a *app) config(args []string) error {
	sub := "show"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "path":
		fmt.Fprintln(a.out, config.Path(a.cfgDir))
		return nil
	case "show":
		if a.cfgErr != nil {
			return a.cfgErr
		}
		b, _ := json.MarshalIndent(a.cfg, "", "  ")
		fmt.Fprintf(a.out, "# %s\n%s\n# data: %s\n", config.Path(a.cfgDir), b, a.cfg.Data())
		return nil
	case "set":
		if a.cfgErr != nil {
			return a.cfgErr
		}
		if len(args) != 2 {
			return usagef("usage: mote config set KEY VALUE (keys: %s)", strings.Join(config.Keys, ", "))
		}
		cfg := a.cfg
		if cfg.Models != nil {
			cfg.Models = copyMap(cfg.Models)
		}
		if cfg.Tools != nil {
			cfg.Tools = copyMap(cfg.Tools)
		}
		if err := cfg.Set(args[0], args[1]); err != nil {
			return usagef("%v", err)
		}
		if err := a.checkAgainstRegistry(cfg); err != nil {
			return usagef("%v", err)
		}
		_, err := config.Save(a.cfgDir, cfg, fmt.Sprintf("set %s=%s", args[0], args[1]))
		return err
	case "history":
		h, err := config.History(a.cfgDir)
		if err != nil {
			return err
		}
		for i, e := range h {
			fmt.Fprintf(a.out, "%3d  %s  %s\n", i, e.SavedAt.Local().Format("2006-01-02 15:04:05"), e.Reason)
		}
		return nil
	case "rollback":
		n := 1
		if len(args) == 1 {
			var err error
			if n, err = strconv.Atoi(args[0]); err != nil {
				return usagef("usage: mote config rollback [N]")
			}
		}
		c, err := config.Rollback(a.cfgDir, n)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "restored configuration (profile %s); `mote config history` shows all versions\n", c.Profile)
		return nil
	case "edit":
		ed := a.cfg.Editor
		if ed == "" {
			ed = firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
		}
		if ed == "" {
			return missingf("no editor configured; run `mote config set editor nvim` (or code, vim, emacs)")
		}
		path := config.Path(a.cfgDir)
		cmd := exec.Command(ed, path)
		if ed == "code" {
			cmd.Args = []string{ed, "--wait", path}
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		c, err := config.Load(a.cfgDir)
		if err != nil {
			return fmt.Errorf("edited config is invalid, fix it or run `mote config rollback 0`: %w", err)
		}
		_, err = config.Save(a.cfgDir, c, "edited by hand")
		return err
	}
	return usagef("unknown config subcommand %q", sub)
}

func copyMap(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (a *app) checkAgainstRegistry(c config.Config) error {
	reg := a.registry()
	if _, ok := reg.Policy.Profiles[c.Profile]; !ok {
		return fmt.Errorf("unknown profile %q (have %s)", c.Profile, strings.Join(reg.ProfileNames(), ", "))
	}
	for capability, id := range c.Models {
		if _, ok := registry.Capabilities[capability]; !ok {
			return fmt.Errorf("unknown capability %q", capability)
		}
		m, ok := reg.Model(id)
		if !ok {
			return fmt.Errorf("unknown model %q", id)
		}
		if !m.Has(capability) {
			return fmt.Errorf("model %s does not provide %s", id, capability)
		}
	}
	return nil
}

func (a *app) doctor() error {
	fails := 0
	line := func(status, what, detail string) {
		if status == "FAIL" {
			fails++
		}
		o := a.uo
		if !o.Color() {
			fmt.Fprintf(a.out, "%-4s  %-14s %s\n", status, what, detail)
			return
		}
		sym := map[string]string{"ok": o.OK(), "warn": o.Warn(), "FAIL": o.Fail()}[status]
		if status != "ok" {
			detail = map[string]func(string) string{"warn": o.Yellow, "FAIL": o.Red}[status](detail)
		}
		fmt.Fprintf(a.out, "%s %s %s\n", sym, o.Bold(pad(what, 14)), detail)
	}
	info := a.platform()
	line("ok", "platform", describe(info, a.dataDir()))
	if info.RAMMB > 0 && info.RAMMB < 3072 {
		line("warn", "memory", "under 3 GB; only the smallest models will fit")
	}
	reg := a.registry()
	line("ok", "registry", fmt.Sprintf("%s (%d models)", reg.Version, len(reg.Models)))

	if a.cfgErr != nil {
		line("FAIL", "config", a.cfgErr.Error())
	} else if err := a.checkAgainstRegistry(a.cfg); err != nil {
		line("FAIL", "config", err.Error())
	} else {
		line("ok", "config", fmt.Sprintf("%s (profile %s)", config.Path(a.cfgDir), a.cfg.Profile))
	}

	if b, err := a.backend(&registry.Model{Backend: "llama.cpp"}); err != nil {
		line("FAIL", "runtime", err.Error())
	} else {
		l := b.(*mrt.Llama)
		missing, err := l.CheckFlags()
		switch {
		case err != nil:
			line("FAIL", "runtime", fmt.Sprintf("%s: %v", l.Dir, err))
		case len(missing) > 0:
			line("FAIL", "runtime", fmt.Sprintf("%s lacks flags %s; use the pinned build (`mote config set llama_dir \"\"` and `mote setup`)", l.Dir, strings.Join(missing, " ")))
		default:
			line("ok", "runtime", "llama.cpp in "+l.Dir)
		}
	}

	if a.cfgErr == nil {
		for _, c := range capNames() {
			m, ch, err := a.choose(c, a.cfg.Profile)
			switch {
			case err != nil:
				line("warn", "model "+c, err.Error())
			case !a.store().Installed(m):
				line("warn", "model "+c, fmt.Sprintf("%s not downloaded (%s); `mote models pull %s`", m.ID, mb(m.Bytes()), m.ID))
			case !ch.Meets:
				line("warn", "model "+c, fmt.Sprintf("%s (outside the %s profile's limits; see `mote models why %s`)", m.ID, a.cfg.Profile, c))
			default:
				line("ok", "model "+c, m.ID)
			}
		}
	}
	if tasks, err := task.LoadFrom(a.tasksDir()); err != nil {
		line("FAIL", "tasks", err.Error())
	} else {
		custom := 0
		for _, t := range tasks {
			if t.Custom() {
				custom++
			}
		}
		detail := fmt.Sprintf("%d built-in", len(tasks)-custom)
		if custom > 0 {
			detail += fmt.Sprintf(" + %d custom from %s", custom, a.tasksDir())
		}
		line("ok", "tasks", detail)
	}
	for _, t := range []string{"ffmpeg", "ffprobe", "git"} {
		if p, err := a.tool(t); err == nil {
			line("ok", t, p)
		} else {
			line("warn", t, err.Error())
		}
	}
	if fails > 0 {
		return fmt.Errorf("%d problem(s) found", fails)
	}
	return nil
}

func (a *app) bench(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"--model"}, []string{"--full"})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: mote bench [--full] [--model ID]")
	}
	reg := a.registry()
	var models []*registry.Model
	if id := vals["--model"]; id != "" {
		m, ok := reg.Model(id)
		if !ok {
			return usagef("unknown model %q", id)
		}
		if !a.store().Installed(m) {
			return missingf("%s is not downloaded; `mote models pull %s`", id, id)
		}
		models = append(models, m)
	} else {
		for i := range reg.Models {
			if a.store().Installed(&reg.Models[i]) {
				models = append(models, &reg.Models[i])
			}
		}
	}
	if len(models) == 0 {
		return missingf("no models installed; `mote models pull text` first")
	}
	full := vals["--full"] == "true"
	if !full && vals["--model"] == "" {
		// Quick mode measures only the models the config currently uses.
		used := map[string]bool{}
		for _, c := range capNames() {
			if m, _, err := a.choose(c, a.cfg.Profile); err == nil {
				used[m.ID] = true
			}
		}
		var keep []*registry.Model
		for _, m := range models {
			if used[m.ID] || m.Has("tts") {
				keep = append(keep, m)
			}
		}
		if len(keep) > 0 {
			models = keep
		}
	}
	tmp := filepath.Join(a.dataDir(), "tmp")
	os.MkdirAll(tmp, 0o755)
	entries, err := bench.Run(ctx, bench.Options{
		Full: full, Models: models, Files: a.store().Files, Backend: a.backend,
		Runtime: reg.Runtime.Version, TempDir: tmp, Log: a.err,
	})
	if len(entries) > 0 {
		if serr := bench.Save(a.dataDir(), describe(a.platform(), a.dataDir()), entries); serr != nil {
			return serr
		}
		o := a.uo
		fmt.Fprintln(a.out, o.Bold(fmt.Sprintf("%-16s %9s %10s %9s %10s %7s", "MODEL", "STARTUP", "GEN TOK/S", "PEAK RSS", "LATENCY", "CHECKS")))
		for _, e := range entries {
			checks := fmt.Sprintf("%3d/%-3d", e.Passed, e.Cases)
			if e.Passed == e.Cases {
				checks = o.Green(checks)
			} else {
				checks = o.Yellow(checks)
			}
			fmt.Fprintf(a.out, "%s %7.0fms %10.1f %6d MB %8.0fms %s\n", o.Bold(pad(e.Model, 16)), e.StartupMS, e.TokensPerSec, e.PeakRSSMB, e.LatencyMS, checks)
			for _, f := range e.Failures {
				fmt.Fprintln(a.out, "   ", o.Red("fail:"), f)
			}
			for _, s := range e.Skipped {
				fmt.Fprintln(a.out, "   ", o.Dim("skip: "+s))
			}
		}
		fmt.Fprintln(a.out, o.Dim(fmt.Sprintf("saved to %s; `mote tune` proposes config changes from these results", filepath.Join(a.dataDir(), "bench"))))
	}
	return err
}

func (a *app) tune(args []string) error {
	vals, pos, err := flags(args, nil, []string{"--apply"})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: mote tune [--apply]")
	}
	res, err := bench.Load(a.dataDir())
	if err != nil {
		return err
	}
	if len(res.Entries) == 0 {
		return missingf("no local benchmark results; run `mote bench` first")
	}
	changes := bench.Propose(a.registry(), a.cfg, res, a.ramMB())
	if len(changes) == 0 {
		fmt.Fprintln(a.out, "no changes: the current configuration matches the policy given local measurements")
		return nil
	}
	for _, ch := range changes {
		action := "pin"
		if !ch.Pin {
			action = "unpin (back to policy default)"
		}
		fmt.Fprintf(a.out, "%s: %s %s %s %s\n  %s\n", a.uo.Accent(ch.Cap), ch.From, a.uo.Arrow(), a.uo.Bold(ch.To), a.uo.Dim("["+action+"]"), ch.Reason)
	}
	dir := filepath.Join(a.dataDir(), "proposals")
	os.MkdirAll(dir, 0o755)
	b, _ := json.MarshalIndent(map[string]any{"created": time.Now().UTC(), "changes": changes}, "", "  ")
	p := filepath.Join(dir, time.Now().UTC().Format("20060102T150405Z")+".json")
	if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if vals["--apply"] != "true" {
		fmt.Fprintf(a.out, "proposal saved to %s; apply with `mote tune --apply`\n", p)
		return nil
	}
	var why []string
	for _, ch := range changes {
		why = append(why, ch.Cap+"->"+ch.To)
	}
	if _, err := config.Save(a.cfgDir, bench.Apply(a.cfg, changes), "tune: "+strings.Join(why, ", ")+" (proposal "+filepath.Base(p)+")"); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "applied; undo with `mote config rollback`")
	return nil
}

func (a *app) update(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, nil, []string{"--check"})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("usage: mote update [--check]")
	}
	u, err := url.Parse(a.regURL)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return fmt.Errorf("registry URL must be https: %s", a.regURL)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.regURL, nil)
	req.Header.Set("User-Agent", "mote/"+Version)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("fetch registry: %w (nothing changed)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch registry: %s (nothing changed)", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	next, err := registry.Parse(body)
	if err != nil {
		return fmt.Errorf("downloaded registry rejected, keeping %s: %w", a.registry().Version, err)
	}
	cur := a.registry()
	if !registry.Newer(next.Version, cur.Version) {
		fmt.Fprintf(a.out, "registry %s is current\n", cur.Version)
		return nil
	}
	profile := config.Default().Profile
	if a.cfgErr == nil {
		profile = a.cfg.Profile
	}
	fmt.Fprintf(a.out, "registry %s -> %s\n", cur.Version, next.Version)
	for _, c := range capNames() {
		o, _ := cur.Select(c, profile, registry.Env{RAMMB: a.ramMB()})
		n, _ := next.Select(c, profile, registry.Env{RAMMB: a.ramMB()})
		if o.Model != n.Model {
			fmt.Fprintf(a.out, "  %s: %s -> %s (%s)\n", c, o.Model, n.Model, n.Reason)
		}
	}
	if next.Runtime.Version != cur.Runtime.Version {
		fmt.Fprintf(a.out, "  runtime: llama.cpp %s -> %s\n", cur.Runtime.Version, next.Runtime.Version)
	}
	if vals["--check"] == "true" {
		return nil
	}
	path := a.userRegistryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		os.WriteFile(filepath.Join(filepath.Dir(path), "models.prev.json"), old, 0o644)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return err
	}
	a.reg = next
	if a.cfgErr == nil && a.cfg.LlamaDir == "" && next.Runtime.Version != cur.Runtime.Version {
		info := a.platform()
		if asset, ok := next.Asset(info.OS, info.Arch); ok {
			fmt.Fprintf(a.out, "Installing llama.cpp %s (%s)\n", next.Runtime.Version, mb(asset.Size))
			if _, err := a.store().InstallRuntime(ctx, a.fetcher, next.Runtime, asset); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(a.out, "updated; the previous registry is kept as models.prev.json. New mote releases: re-run the installer.")
	return nil
}
