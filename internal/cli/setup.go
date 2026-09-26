package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jgalego/mote/internal/config"
	"github.com/jgalego/mote/internal/platform"
	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

type prompter struct {
	r   *bufio.Reader
	a   *app
	yes bool
}

func (p *prompter) ask(q, def string) string {
	if p.yes {
		return def
	}
	o := p.a.uo
	fmt.Fprintf(p.a.out, "%s %s %s: ", o.Ask(), o.Bold(q), o.Dim("["+def+"]"))
	line, err := p.r.ReadString('\n')
	line = strings.TrimSpace(line)
	if err != nil && line == "" {
		fmt.Fprintln(p.a.out)
		return def
	}
	if line == "" {
		return def
	}
	return line
}

func (p *prompter) yesNo(q string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	for {
		switch strings.ToLower(p.ask(q, d)) {
		case strings.ToLower(d):
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		fmt.Fprintln(p.a.out, "  please answer y or n")
	}
}

func (p *prompter) choose(q string, opts []string, def int) int {
	if p.yes || len(opts) < 2 {
		return def
	}
	u := p.a.uo
	fmt.Fprintf(p.a.out, "%s %s\n", u.Ask(), u.Bold(q))
	for i, o := range opts {
		name, rest, _ := strings.Cut(o, " - ")
		line := u.Cyan(name)
		if rest != "" {
			line += " " + u.Dim(rest)
		}
		fmt.Fprintf(p.a.out, "  %s %s\n", u.Accent(fmt.Sprintf("%d)", i+1)), line)
	}
	for {
		n, err := strconv.Atoi(p.ask("choice", strconv.Itoa(def+1)))
		if err == nil && n >= 1 && n <= len(opts) {
			return n - 1
		}
		fmt.Fprintf(p.a.out, "  enter a number from 1 to %d\n", len(opts))
	}
}

func (a *app) setup(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"--config", "--profile", "--data-dir"},
		[]string{"--yes", "-y", "--auto-download", "--no-download"})
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("setup takes no arguments")
	}
	reg := a.registry()
	info := a.platform()
	p := &prompter{r: bufio.NewReader(a.in), a: a, yes: vals["--yes"] == "true" || vals["-y"] == "true"}
	if !p.yes && !a.tty && vals["--config"] == "" {
		fmt.Fprintln(a.out, "stdin is not a terminal; using defaults (pass --yes to silence this)")
		p.yes = true
	}

	cfg := config.Default()
	if a.cfgErr == nil {
		cfg = a.cfg // re-running setup starts from the current answers
	}
	if f := vals["--config"]; f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if cfg, err = config.Parse(b); err != nil {
			return usagef("%s: %v", f, err)
		}
		p.yes = true
	}
	if v := vals["--profile"]; v != "" {
		cfg.Profile = v
	}
	if v := vals["--data-dir"]; v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return err
		}
		cfg.DataDir = abs
	}
	if vals["--auto-download"] == "true" {
		cfg.AutoDownload = true
	}
	if _, ok := reg.Policy.Profiles[cfg.Profile]; !ok {
		return usagef("unknown profile %q (have %s)", cfg.Profile, strings.Join(reg.ProfileNames(), ", "))
	}

	if !p.yes || a.uo.Color() {
		fmt.Fprint(a.out, a.uo.Banner("small models · local machines · useful work"))
	}
	fmt.Fprintf(a.out, "%s Detected: %s\n", a.uo.OK(), describe(info, cfg.Data()))
	if info.RAMMB == 0 {
		gb, _ := strconv.Atoi(p.ask("Could not detect RAM. How many GB does this machine have?", "8"))
		if gb > 0 {
			a.info.RAMMB = gb * 1024
			info = a.platform()
		}
	}

	// Profile: show what each would use for text so the choice is concrete.
	if vals["--profile"] == "" && vals["--config"] == "" {
		names := reg.ProfileNames()
		var opts []string
		def := 0
		for i, n := range names {
			line := n + " - " + reg.Policy.Profiles[n].Description
			if ch, err := reg.Select("text", n, registry.Env{RAMMB: info.RAMMB}); err == nil {
				m, _ := reg.Model(ch.Model)
				line += fmt.Sprintf(" (text: %s, %s)", m.ID, mb(m.Bytes()))
			}
			opts = append(opts, line)
			if n == cfg.Profile {
				def = i
			}
		}
		cfg.Profile = names[p.choose("Speed/quality/disk trade-off:", opts, def)]
	}

	if eds := info.ToolsOfKind("editor"); len(eds) > 0 && vals["--config"] == "" {
		def := 0
		for i, e := range eds {
			if e == cfg.Editor || (cfg.Editor == "" && e == filepath.Base(firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR")))) {
				def = i
			}
		}
		cfg.Editor = eds[p.choose("Editor for opening results (`mote config edit`):", eds, def)]
	}

	if sys := mrt.SystemLlama(); sys != "" && vals["--config"] == "" {
		reuse := p.yesNo(fmt.Sprintf("Found llama.cpp at %s. Use it instead of downloading the pinned %s build?", sys, reg.Runtime.Version), cfg.LlamaDir == sys)
		cfg.LlamaDir = ""
		if reuse {
			cfg.LlamaDir = sys
		}
	}

	if vals["--data-dir"] == "" && vals["--config"] == "" {
		d := p.ask("Data directory for models and runtime", cfg.Data())
		if abs, err := filepath.Abs(d); err == nil && abs != config.DefaultDataDir() {
			cfg.DataDir = abs
		} else if err == nil {
			cfg.DataDir = ""
		}
	}
	if vals["--auto-download"] == "" && vals["--config"] == "" {
		cfg.AutoDownload = p.yesNo("Download models automatically the first time a task needs them?", cfg.AutoDownload)
	}

	if changed, err := config.Save(a.cfgDir, cfg, "setup"); err != nil {
		return err
	} else if changed {
		fmt.Fprintf(a.out, "%s Saved %s\n", a.uo.OK(), a.uo.Dim(config.Path(a.cfgDir)))
	}
	a.cfg, a.cfgErr = cfg, nil

	if cfg.LlamaDir == "" {
		asset, ok := reg.Asset(info.OS, info.Arch)
		if !ok {
			return missingf("no prebuilt llama.cpp for %s/%s; build llama.cpp yourself and run `mote config set llama_dir /path/to/bin`", info.OS, info.Arch)
		}
		st := a.store()
		if _, err := mrt.FindLlama("", st, reg.Runtime); err != nil {
			fmt.Fprintf(a.out, "%s Installing llama.cpp %s %s\n", a.uo.Arrow(), reg.Runtime.Version, a.uo.Dim(fmt.Sprintf("(%s, MIT, from %s)", mb(asset.Size), hostOf(asset.URL))))
			if _, err := st.InstallRuntime(ctx, a.fetcher, reg.Runtime, asset); err != nil {
				return err
			}
		}
	}
	if b, err := a.backend(&registry.Model{Backend: "llama.cpp"}); err == nil {
		if missing, err := b.(*mrt.Llama).CheckFlags(); err != nil || len(missing) > 0 {
			fmt.Fprintf(a.err, "warning: llama.cpp at %s lacks flags %v (%v); `mote doctor` has details\n", b.(*mrt.Llama).Dir, missing, err)
		}
	}

	m, _, err := a.choose("text", cfg.Profile)
	if err == nil && !a.store().Installed(m) && vals["--no-download"] == "" {
		if p.yesNo(fmt.Sprintf("Download the text model now (%s, %s, license %s)?", m.ID, mb(m.Bytes()), m.License), !p.yes) {
			if err := a.ensure(ctx, m, true); err != nil {
				return err
			}
		}
	}
	for _, t := range []string{"ffmpeg", "git"} {
		if _, err := a.tool(t); err != nil {
			fmt.Fprintf(a.out, "%s Optional: %s not found; audio/video tasks need ffmpeg; git lets `patch` skip ignored files (%s).\n", a.uo.Warn(), t, installHint(t))
		}
	}
	fmt.Fprintf(a.out, "%s %s Try %s %s\n", a.uo.OK(), a.uo.Bold("Ready."), a.uo.Cyan(`mote run chat "hello"`), a.uo.Dim("(mote tasks lists everything)"))
	return nil
}

func describe(i platform.Info, dataDir string) string {
	s := i.OS + "/" + i.Arch
	if i.Distro != "" {
		s += " (" + i.Distro + ")"
	}
	s += fmt.Sprintf(", %d cores", i.Cores)
	if len(i.Features) > 0 {
		s += " [" + strings.Join(i.Features, " ") + "]"
	}
	if i.RAMMB > 0 {
		s += fmt.Sprintf(", %.1f GB RAM", float64(i.RAMMB)/1024)
	}
	for dataDir != filepath.Dir(dataDir) {
		if _, err := os.Stat(dataDir); err == nil {
			break
		}
		dataDir = filepath.Dir(dataDir)
	}
	if free := platform.DiskFreeMB(dataDir); free > 0 {
		s += fmt.Sprintf(", %.0f GB free", float64(free)/1024)
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return strings.Fields(s)[0]
		}
	}
	return ""
}
