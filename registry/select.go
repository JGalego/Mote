package registry

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Env is what selection knows about the local machine. Zero values mean
// "unknown" and do not constrain the choice.
type Env struct {
	RAMMB    int
	Measured map[string]Measured
}

// Measured holds locally measured figures for a model on this machine.
type Measured struct {
	PeakRSSMB    int     `json:"peak_rss_mb"`
	TokensPerSec float64 `json:"tokens_per_sec"`
}

// ErrNoModel is returned when no registry model can serve a capability.
var ErrNoModel = errors.New("no model in the registry fits")

// RAMNeed returns the RAM a model needs in MiB: the local measurement when
// available, otherwise the registry estimate. The second value is true when
// the figure was measured.
func (m *Model) RAMNeed(env Env) (int, bool) {
	if ms, ok := env.Measured[m.ID]; ok && ms.PeakRSSMB > 0 {
		return ms.PeakRSSMB, true
	}
	return m.RAMEstimate, false
}

// Select picks the model for a capability under a profile.
//
// Order of preference, per policy: the smallest model (parameters, then bytes)
// that fits the RAM budget, meets the capability's upstream benchmark gate and,
// if measured locally, meets the profile's minimum throughput. When nothing
// meets the gate, the best-scoring model that fits is returned with
// Meets=false so callers can say so.
func (r *Registry) Select(capability, profile string, env Env) (Choice, error) {
	prof, ok := r.Policy.Profiles[profile]
	if !ok {
		return Choice{}, fmt.Errorf("unknown profile %q (have %s)", profile, strings.Join(r.ProfileNames(), ", "))
	}
	if _, ok := Capabilities[capability]; !ok {
		return Choice{}, fmt.Errorf("unknown capability %q", capability)
	}
	budget := prof.MaxRAMMB
	if env.RAMMB > 0 {
		usable := int(float64(env.RAMMB) * r.Policy.UsableRAMFraction)
		if budget == 0 || usable < budget {
			budget = usable
		}
	}
	gate, gated := r.Policy.Gates[capability]
	threshold := gate.Min[profile]

	type cand struct {
		m     *Model
		ram   int
		meas  bool
		score float64
		has   bool
		pass  bool
	}
	var fits []cand
	var rejected []string
	for i := range r.Models {
		m := &r.Models[i]
		if !m.Has(capability) {
			continue
		}
		c := cand{m: m}
		c.ram, c.meas = m.RAMNeed(env)
		if budget > 0 && c.ram > budget {
			rejected = append(rejected, fmt.Sprintf("%s needs %d MiB > budget %d MiB", m.ID, c.ram, budget))
			continue
		}
		if ms, ok := env.Measured[m.ID]; ok && ms.TokensPerSec > 0 && prof.MinTokensPerSec > 0 && ms.TokensPerSec < prof.MinTokensPerSec {
			rejected = append(rejected, fmt.Sprintf("%s measured %.1f tok/s < %.1f", m.ID, ms.TokensPerSec, prof.MinTokensPerSec))
			continue
		}
		if gated {
			if b, ok := m.Measurement(gate.Dataset, gate.Task); ok {
				c.score, c.has = b.Value, true
				c.pass = gate.passes(b.Value, threshold)
			}
		} else {
			c.pass = true
		}
		fits = append(fits, c)
	}
	if len(fits) == 0 {
		msg := fmt.Sprintf("%v for %q under profile %q", ErrNoModel, capability, profile)
		if len(rejected) > 0 {
			msg += " (" + strings.Join(rejected, "; ") + ")"
		}
		return Choice{}, errors.New(msg)
	}

	sort.SliceStable(fits, func(i, j int) bool {
		a, b := fits[i], fits[j]
		if a.pass != b.pass {
			return a.pass
		}
		if !a.pass { // nothing passes: prefer measured, then better score
			if a.has != b.has {
				return a.has
			}
			if a.score != b.score {
				return gate.better(a.score, b.score)
			}
		}
		if a.m.ParamsB != b.m.ParamsB {
			return a.m.ParamsB < b.m.ParamsB
		}
		if a.m.Bytes() != b.m.Bytes() {
			return a.m.Bytes() < b.m.Bytes()
		}
		return a.m.ID < b.m.ID
	})
	best := fits[0]

	ramSrc := "estimated"
	if best.meas {
		ramSrc = "measured"
	}
	fit := fmt.Sprintf("%s RAM %d MiB within budget %d MiB", ramSrc, best.ram, budget)
	if budget == 0 {
		fit = fmt.Sprintf("%s RAM %d MiB", ramSrc, best.ram)
	}
	var reason string
	switch {
	case !gated:
		reason = fmt.Sprintf("smallest model with %s; no upstream benchmark gate is defined for this capability; %s", capability, fit)
	case best.pass:
		reason = fmt.Sprintf("smallest model meeting %s %s %s %s (upstream %s); %s",
			gate.Dataset, gate.Task, gate.op(), trim(threshold), trim(best.score), fit)
		if gate.Proxy {
			reason += "; gate is a proxy metric"
		}
	case best.has:
		reason = fmt.Sprintf("no fitting model meets %s %s %s %s; best available scores %s; %s",
			gate.Dataset, gate.Task, gate.op(), trim(threshold), trim(best.score), fit)
	default:
		reason = fmt.Sprintf("no fitting model has an upstream %s %s measurement; picked the smallest; %s", gate.Dataset, gate.Task, fit)
	}
	return Choice{Model: best.m.ID, Reason: reason, Meets: best.pass}, nil
}

// ComputeDefaults selects a model for every capability and profile without
// hardware limits. This is the reference table stored in the registry.
func (r *Registry) ComputeDefaults() map[string]map[string]Choice {
	out := map[string]map[string]Choice{}
	for _, p := range r.ProfileNames() {
		out[p] = map[string]Choice{}
		for c := range Capabilities {
			if ch, err := r.Select(c, p, Env{}); err == nil {
				out[p][c] = ch
			}
		}
	}
	return out
}

func (g Gate) passes(v, threshold float64) bool {
	if g.Higher {
		return v >= threshold
	}
	return v <= threshold
}

func (g Gate) better(a, b float64) bool {
	if g.Higher {
		return a > b
	}
	return a < b
}

func (g Gate) op() string {
	if g.Higher {
		return ">="
	}
	return "<="
}

func trim(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}
