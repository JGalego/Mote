package bench

import (
	"fmt"
	"sort"

	"github.com/jgalego/mote/internal/config"
	"github.com/jgalego/mote/registry"
)

// Change is a proposed configuration change with its evidence.
type Change struct {
	Cap    string `json:"capability"`
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
	return out
}

// Apply returns cfg with the changes made.
func Apply(cfg config.Config, changes []Change) config.Config {
	models := map[string]string{}
	for k, v := range cfg.Models {
		models[k] = v
	}
	for _, ch := range changes {
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
