package bench

import (
	"fmt"
	"sort"

	"github.com/jgalego/mote/internal/config"
	"github.com/jgalego/mote/registry"
)

// Change is a proposed configuration change with its evidence: a model for
// a capability, or, when Key is set, a setting.
type Change struct {
	Key    string `json:"key,omitempty"`
	Cap    string `json:"capability,omitempty"`
	From   string `json:"from"`
	To     string `json:"to"`
	Pin    bool   `json:"pin"` // false: remove the pin and follow the policy default
	Reason string `json:"reason"`
}

// Propose compares the models the config currently uses with what the
// selection policy picks once local measurements are taken into account.
// It only proposes; Apply makes the change.
func Propose(reg *registry.Registry, cfg config.Config, res Results, ramMB int) []Change {
	plain := registry.Env{RAMMB: ramMB}
	measured := registry.Env{RAMMB: ramMB, Measured: res.Measured()}
	var caps []string
	for c := range registry.Capabilities {
		caps = append(caps, c)
	}
	sort.Strings(caps)
	var out []Change
	for _, c := range caps {
		def, err := reg.Select(c, cfg.Profile, plain)
		if err != nil {
			continue
		}
		cur := def.Model
		if pin, ok := cfg.Models[c]; ok {
			cur = pin
		}
		best, err := reg.Select(c, cfg.Profile, measured)
		if err != nil || best.Model == cur {
			continue
		}
		reason := best.Reason
		if e, ok := res.Entries[cur]; ok {
			reason = fmt.Sprintf("%s measured %.1f tok/s, %d MiB, %d/%d checks; %s", cur, e.TokensPerSec, e.PeakRSSMB, e.Passed, e.Cases, reason)
		}
		out = append(out, Change{Cap: c, From: cur, To: best.Model, Pin: best.Model != def.Model, Reason: reason})
	}
	if ch, ok := proposeRepack(reg, cfg, res, plain); ok {
		out = append(out, ch)
	}
	return out
}

// repackTokens is the prompt length that decides repacking: about a page
// of a document with its instructions, what most tasks send.
const repackTokens = 1000

// proposeRepack sets repack from the text model's measured load times:
// on when repacking pays for itself before a typical prompt is read.
func proposeRepack(reg *registry.Registry, cfg config.Config, res Results, env registry.Env) (Change, bool) {
	id := cfg.Models["text"]
	if id == "" {
		ch, err := reg.Select("text", cfg.Profile, env)
		if err != nil {
			return Change{}, false
		}
		id = ch.Model
	}
	e, ok := res.Entries[id]
	if !ok {
		return Change{}, false
	}
	be, ok := e.RepackBreakEven()
	if !ok {
		return Change{}, false
	}
	want := "off"
	if be >= 0 && be <= repackTokens {
		want = "on"
	}
	cur := cfg.Repack
	if cur == "" {
		cur = "auto"
	}
	// Auto already skips repacking for the one-shot servers commands use.
	if want == cfg.Repack || (want == "off" && cur == "auto") {
		return Change{}, false
	}
	var why string
	switch {
	case be < 0:
		why = "repacking never pays for itself"
	case be == 0:
		why = "repacking adds no load time"
	default:
		why = fmt.Sprintf("repacking pays for itself from about %d prompt tokens", be)
	}
	return Change{Key: "repack", From: cur, To: want, Reason: fmt.Sprintf(
		"%s loads in %.0f ms and reads %.0f prompt tok/s without repacking, %.0f ms and %.0f tok/s with; %s",
		id, e.NoRepack.StartupMS, e.NoRepack.PromptTPS, e.Repack.StartupMS, e.Repack.PromptTPS, why)}, true
}

// Apply returns cfg with the changes made.
func Apply(cfg config.Config, changes []Change) config.Config {
	models := map[string]string{}
	for k, v := range cfg.Models {
		models[k] = v
	}
	for _, ch := range changes {
		if ch.Key != "" {
			cfg.Set(ch.Key, ch.To)
			continue
		}
		if ch.Pin {
			models[ch.Cap] = ch.To
		} else {
			delete(models, ch.Cap)
		}
	}
	cfg.Models = nil
	if len(models) > 0 {
		cfg.Models = models
	}
	return cfg
}
