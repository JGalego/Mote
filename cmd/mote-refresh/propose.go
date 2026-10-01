package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jgalego/mote/registry"
)

// runPropose looks for discovered models that would change a default,
// appends them to candidates.json and regenerates models.json, and writes a
// description for the pull request to report. With nothing to propose it
// writes nothing and leaves the registry alone.
func runPropose(dir, report string) error {
	var prev registry.Registry
	if err := readJSON(filepath.Join(dir, "models.json"), &prev); err != nil {
		return err
	}
	var pol registry.Policy
	if err := readJSON(filepath.Join(dir, "policy.json"), &pol); err != nil {
		return err
	}
	var cands registry.Candidates
	if err := readJSON(filepath.Join(dir, "candidates.json"), &cands); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	src := &registry.HF{
		GitHubToken: os.Getenv("GITHUB_TOKEN"),
		HFBase:      os.Getenv("MOTE_HF_BASE"),
		GitHubBase:  os.Getenv("MOTE_GITHUB_BASE"),
	}
	found, err := registry.Discover(ctx, pol, cands, src)
	if err != nil {
		return err
	}
	props, warns := registry.Propose(ctx, &prev, pol, found, src)
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	printProposals(props)
	var kept []registry.Proposal
	for _, p := range props {
		if p.Kept() {
			kept = append(kept, p)
		}
	}
	props = kept
	if len(props) == 0 {
		fmt.Println("no discovered model would change a default")
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "candidates.json"))
	if err != nil {
		return err
	}
	var added []registry.Candidate
	for _, p := range props {
		added = append(added, p.Candidate)
	}
	out, err := appendCandidates(raw, added)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, "candidates.json"), out); err != nil {
		return err
	}
	// A normal refresh pins the new files and recomputes the defaults, so
	// what is committed is exactly what the daily job would produce.
	if err := run(dir, false, false, false); err != nil {
		return err
	}
	return os.WriteFile(report, []byte(describe(props)), 0o644)
}

// appendCandidates adds candidates to the end of the "candidates" array
// without reformatting the hand-written entries before them.
func appendCandidates(raw []byte, add []registry.Candidate) ([]byte, error) {
	s := strings.TrimRight(string(raw), " \n")
	const tail = "\n  ]\n}"
	if !strings.HasSuffix(s, tail) {
		return nil, fmt.Errorf("candidates.json: the candidates array is not the last key; add the entries by hand")
	}
	s = strings.TrimSuffix(s, tail)
	for _, c := range add {
		b, err := json.MarshalIndent(c, "    ", "  ")
		if err != nil {
			return nil, err
		}
		s += ",\n    " + string(b)
	}
	return []byte(s + tail + "\n"), nil
}

// printProposals shows every model that was tried, whether or not it
// changes a default, so a run that proposes nothing still shows its work.
func printProposals(props []registry.Proposal) {
	fmt.Printf("tried %d discovered models:\n", len(props))
	for _, p := range props {
		c := p.Candidate
		clears := strings.Join(p.Clears, ",")
		if clears == "" {
			clears = "no profile"
		}
		fmt.Printf("  %-32s %.2fB  %s %s  ~%d MiB RAM  clears the text gate for: %s\n",
			c.Upstream, p.ParamsB, p.Dataset, trimFloat(p.Score), p.RAMMB, clears)
		if p.Kept() {
			for _, ch := range p.Changes {
				fmt.Printf("    -> takes %s/%s from %s\n", ch.Profile, ch.Capability, orNone(ch.From))
			}
			continue
		}
		var picks []string
		for _, prof := range sortedKeys(p.Text) {
			picks = append(picks, prof+"="+p.Text[prof])
		}
		fmt.Printf("    no default changes; text stays %s\n", strings.Join(picks, " "))
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func describe(props []registry.Proposal) string {
	var b strings.Builder
	b.WriteString("These models are on a gate leaderboard, have a GGUF build, and would change a default under the policy in `registry/policy.json`. ")
	b.WriteString("Scores are upstream Hugging Face `evalResults`; nothing here was run on a CPU yet.\n\n")
	for _, p := range props {
		c := p.Candidate
		fmt.Fprintf(&b, "### %s\n\n[%s](https://huggingface.co/%s) (%s %s), GGUF from [%s](https://huggingface.co/%s), `%s`\n\n",
			c.Name, c.Upstream, c.Upstream, p.Dataset, trimFloat(p.Score), c.Repo, c.Repo, c.Files["model"].File)
		b.WriteString("| Profile | Capability | Replaces | Why |\n|---|---|---|---|\n")
		for _, ch := range p.Changes {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", ch.Profile, ch.Capability, orNone(ch.From), ch.Reason)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Review before merging\n\n")
	b.WriteString("- [ ] The candidate is an instruct or chat model, not a base model (MMLU-Pro is reported for both).\n")
	b.WriteString("- [ ] `context`, `args` (for example reasoning off) and `caps` suit the model; they are defaults, not tuned.\n")
	b.WriteString("- [ ] The `quality` workflow, dispatched on this branch, passes `mote bench --full --strict` on the default profile.\n")
	return b.String()
}

func trimFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}
