package registry

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Proposal is a discovered model drafted as a candidate and tried against
// the registry. It is worth proposing only when Changes is not empty.
type Proposal struct {
	Candidate Candidate
	Score     float64
	Dataset   string
	ParamsB   float64
	RAMMB     int
	// Clears lists the profiles whose text gate its score meets.
	Clears []string
	// Changes lists each default it takes over, with the model it replaces.
	Changes []Change
	// Text is what each profile picks for text with it added, when it
	// changes nothing: the models it lost to.
	Text map[string]string
}

// Kept reports whether the proposal changes a default.
func (p Proposal) Kept() bool { return len(p.Changes) > 0 }

// Change is one (profile, capability) default a proposal would take over.
type Change struct {
	Profile, Capability string
	From, To            string
	Reason              string
}

var (
	quantRe = regexp.MustCompile(`(?i)-Q4_K_M\.gguf$`)
	splitRe = regexp.MustCompile(`-\d{5}-of-\d{5}\.gguf$`)
	slugRe  = regexp.MustCompile(`[^a-z0-9.]+`)
)

// textCaps are the capabilities a model found on a text gate can be
// proposed for. Vision, speech and the rest need files or arguments that
// only a maintainer can choose, so they are never drafted.
var textCaps = map[string]bool{"text": true, "code": true}

// Propose drafts a candidate for each discovered model that has a GGUF repo
// and a score on a text gate and tries it against prev, returning every one
// it could draft, smallest first. Those that change a default (see Kept) are
// added to the registry as it goes, so a later one is judged against the
// earlier ones. Nothing it returns is trusted
// beyond the upstream score: the candidate's context size and arguments are
// defaults for a maintainer to review.
func Propose(ctx context.Context, prev *Registry, pol Policy, found []Discovered, src Source) ([]Proposal, []string) {
	var warn []string
	textData := map[string]bool{}
	for name, g := range pol.Gates {
		if textCaps[name] {
			textData[g.Dataset] = true
		}
	}
	pool := append([]Discovered(nil), found...)
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].ParamsB < pool[j].ParamsB })

	cur := *prev
	cur.Models = append([]Model(nil), prev.Models...)
	cur.Defaults = prev.ComputeDefaults()
	var out []Proposal
	for _, d := range pool {
		if d.GGUF == "" || !textData[d.Dataset] {
			continue
		}
		c, err := draftCandidate(ctx, d, src)
		if err != nil {
			warn = append(warn, fmt.Sprintf("%s: %v; not proposed", d.Upstream, err))
			continue
		}
		if _, dup := cur.Model(c.ID); dup {
			continue
		}
		m, err := buildModel(ctx, c, pol, src)
		if err != nil {
			warn = append(warn, fmt.Sprintf("%s: %v; not proposed", d.Upstream, err))
			continue
		}
		trial := cur
		trial.Models = append(append([]Model(nil), cur.Models...), m)
		trial.Defaults = trial.ComputeDefaults()
		if err := trial.Validate(); err != nil {
			warn = append(warn, fmt.Sprintf("%s: %v; not proposed", d.Upstream, err))
			continue
		}
		p := Proposal{Candidate: c, Score: d.Value, Dataset: d.Dataset, ParamsB: m.ParamsB, RAMMB: m.RAMEstimate,
			Changes: takeovers(cur.Defaults, trial.Defaults, c.ID)}
		if g, ok := pol.Gates["text"]; ok {
			if b, ok := m.Measurement(g.Dataset, g.Task); ok {
				for _, prof := range trial.ProfileNames() {
					if g.passes(b.Value, g.Min[prof]) {
						p.Clears = append(p.Clears, prof)
					}
				}
			}
		}
		if p.Kept() {
			cur = trial
		} else {
			p.Text = map[string]string{}
			for prof, caps := range trial.Defaults {
				p.Text[prof] = caps["text"].Model
			}
		}
		out = append(out, p)
	}
	return out, warn
}

// takeovers lists the defaults that now point at id and did not before.
func takeovers(before, after map[string]map[string]Choice, id string) []Change {
	var out []Change
	for prof, caps := range after {
		for cap, ch := range caps {
			if ch.Model == id && before[prof][cap].Model != id {
				out = append(out, Change{prof, cap, before[prof][cap].Model, id, ch.Reason})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Profile != out[j].Profile {
			return out[i].Profile < out[j].Profile
		}
		return out[i].Capability < out[j].Capability
	})
	return out
}

// draftCandidate fills in a candidate for a discovered model: its Q4_K_M
// file (the quant the other candidates use), text and code, and a context
// of 8192. It fails when the repo has no single-file Q4_K_M.
func draftCandidate(ctx context.Context, d Discovered, src Source) (Candidate, error) {
	_, files, err := src.RepoFiles(ctx, d.GGUF)
	if err != nil {
		return Candidate{}, err
	}
	var names []string
	for name := range files {
		if quantRe.MatchString(name) && !splitRe.MatchString(name) && !strings.Contains(name, "mmproj") && !strings.Contains(name, "/") {
			names = append(names, name)
		}
	}
	if len(names) != 1 {
		return Candidate{}, fmt.Errorf("%s has %d Q4_K_M files, want exactly 1", d.GGUF, len(names))
	}
	base := d.Upstream[strings.LastIndex(d.Upstream, "/")+1:]
	return Candidate{
		ID:       strings.Trim(slugRe.ReplaceAllString(strings.ToLower(base), "-"), "-"),
		Name:     base,
		Upstream: d.Upstream,
		Repo:     d.GGUF,
		Quant:    "Q4_K_M",
		Files:    map[string]FileSpec{"model": {File: names[0]}},
		Caps:     []string{"text", "code"},
		Context:  8192,
		Notes:    "Drafted by mote-refresh -propose; review the chat template, context size and arguments before merging.",
	}, nil
}
