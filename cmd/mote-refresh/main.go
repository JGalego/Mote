// Command mote-refresh regenerates registry/models.json from the curated
// candidates and policy using public upstream data (Hugging Face evalResults
// and file metadata, GitHub release digests). It is run by the daily CI job
// and can be run locally. It never invents numbers: a candidate whose data
// cannot be fetched keeps its previous entry, and an invalid result is not
// written.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jgalego/mote/registry"
)

func main() {
	dir := flag.String("dir", "registry", "registry directory")
	runtime := flag.Bool("runtime", false, "allow bumping the llama.cpp pin")
	discover := flag.Bool("discover", false, "write discovered.json from gate leaderboards")
	check := flag.Bool("check", false, "exit 3 if the registry would change, without writing")
	propose := flag.String("propose", "", "add discovered models that would change a default to candidates.json, and write a PR description to this file")
	flag.Parse()

	if *propose != "" {
		if err := runPropose(*dir, *propose); err != nil {
			fmt.Fprintln(os.Stderr, "mote-refresh:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*dir, *runtime, *discover, *check); err != nil {
		fmt.Fprintln(os.Stderr, "mote-refresh:", err)
		if err == errChanged {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

var errChanged = fmt.Errorf("registry is out of date")

func run(dir string, withRuntime, discover, check bool) error {
	prevRaw, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		return err
	}
	var prev registry.Registry
	if err := json.Unmarshal(prevRaw, &prev); err != nil {
		return fmt.Errorf("models.json: %w", err)
	}
	var pol registry.Policy
	if err := readJSON(filepath.Join(dir, "policy.json"), &pol); err != nil {
		return err
	}
	var cands registry.Candidates
	if err := readJSON(filepath.Join(dir, "candidates.json"), &cands); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// MOTE_HF_BASE and MOTE_GITHUB_BASE point the refresh at a stub in
	// tests; empty means the real APIs.
	src := &registry.HF{
		GitHubToken: os.Getenv("GITHUB_TOKEN"),
		HFBase:      os.Getenv("MOTE_HF_BASE"),
		GitHubBase:  os.Getenv("MOTE_GITHUB_BASE"),
	}
	next, warns, err := registry.Refresh(ctx, &prev, pol, cands, src, registry.RefreshOptions{Now: time.Now(), Runtime: withRuntime})
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if err != nil {
		return err
	}
	out := registry.Encode(next)
	changed := string(out) != string(prevRaw)
	if check {
		if changed {
			return errChanged
		}
		return nil
	}
	if changed {
		if err := writeAtomic(filepath.Join(dir, "models.json"), out); err != nil {
			return err
		}
		fmt.Printf("models.json: %s -> %s\n", prev.Version, next.Version)
	} else {
		fmt.Println("models.json: unchanged")
	}

	if discover {
		found, err := registry.Discover(ctx, pol, cands, src)
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning: discovery skipped:", err)
			return nil
		}
		b, _ := json.MarshalIndent(map[string]any{
			"note":   "Small models with upstream results on gate benchmarks that are not candidates yet. For maintainer review; never used for selection.",
			"models": found,
		}, "", "  ")
		return writeAtomic(filepath.Join(dir, "discovered.json"), append(b, '\n'))
	}
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
