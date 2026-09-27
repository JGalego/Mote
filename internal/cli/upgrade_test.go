package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgalego/mote/registry"
)

// pinnedEnv pins a model to every required capability, so what an upgrade
// wants does not depend on the RAM of the machine running the tests, and
// serves reg as the published registry.
func pinnedEnv(t *testing.T, reg *registry.Registry) *env {
	t.Helper()
	e := newEnv(t)
	e.setup()
	pins := map[string]string{
		"text": "qwen3.5-0.8b", "code": "qwen3.5-0.8b", "extract": "qwen3.5-0.8b", "vision": "qwen3.5-0.8b",
		"asr": "qwen3-asr-0.6b", "tts": "qwen3-tts-1.7b", "embed": "bge-small-en-1.5",
	}
	for c, id := range pins {
		if code, _, errs := e.mote("", "config", "set", "models."+c, id); code != 0 {
			t.Fatalf("pin %s: %s", c, errs)
		}
	}
	body := registry.Encode(reg)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	t.Cleanup(srv.Close)
	t.Setenv("MOTE_REGISTRY_URL", srv.URL)
	return e
}

func (e *env) has(id string) bool {
	_, err := os.Stat(filepath.Join(e.home, "models", id))
	return err == nil
}

func TestPullAllAsksFirst(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for _, args := range [][]string{{"--all", "--missing"}, {"--all", "qwen3.5-0.8b"}, {}} {
		if code, _, _ := e.mote("", append([]string{"models", "pull"}, args...)...); code != ExitUsage {
			t.Errorf("pull %v: exit %d", args, code)
		}
	}
	e.install("qwen3.5-0.8b")
	code, out, errs := e.mote("", "models", "pull", "--all")
	if code != ExitUsage || !strings.Contains(errs, "pass --yes") {
		t.Errorf("--all without --yes off a terminal: %d %s", code, errs)
	}
	if !strings.Contains(out, "qwen3-4b-2507") || strings.Contains(out, "qwen3.5-0.8b") {
		t.Errorf("--all should list only what is missing: %s", out)
	}
	if e.has("qwen3-4b-2507") {
		t.Error("--all downloaded without confirmation")
	}
}

func TestPullMissingAndUpgradePrune(t *testing.T) {
	e := pinnedEnv(t, registry.Default())
	for _, id := range []string{"qwen3.5-0.8b", "qwen3-asr-0.6b", "qwen3-tts-1.7b", "bge-small-en-1.5", "qwen3-4b-2507"} {
		e.install(id)
	}
	os.MkdirAll(filepath.Join(e.home, "models", "retired-model"), 0o755)

	if code, out, errs := e.mote("", "models", "pull", "--missing"); code != 0 || !strings.Contains(out, "nothing to download") {
		t.Errorf("--missing with everything installed: %d %s %s", code, out, errs)
	}
	code, out, errs := e.mote("", "models", "upgrade", "--check")
	if code != 0 || !strings.Contains(out, "is current") || !strings.Contains(out, "up to date") {
		t.Errorf("check: %d %s %s", code, out, errs)
	}
	for _, id := range []string{"qwen3-4b-2507", "retired-model"} {
		if !strings.Contains(out, id) {
			t.Errorf("check does not report unused %s: %s", id, out)
		}
	}
	if strings.Contains(out, "flux2-klein-4b") {
		t.Errorf("an unused optional capability was offered: %s", out)
	}
	if code, _, errs := e.mote("", "models", "upgrade", "--prune"); code != ExitUsage || !strings.Contains(errs, "pass --yes") {
		t.Errorf("prune without --yes off a terminal: %d %s", code, errs)
	}
	if !e.has("qwen3-4b-2507") {
		t.Fatal("removed without confirmation")
	}
	if code, out, errs := e.mote("", "models", "upgrade", "--prune", "--yes"); code != 0 {
		t.Fatalf("prune: %d %s %s", code, out, errs)
	}
	if e.has("qwen3-4b-2507") || e.has("retired-model") {
		t.Error("unused models survived --prune")
	}
	if !e.has("qwen3.5-0.8b") || !e.has("bge-small-en-1.5") {
		t.Error("--prune removed a model in use")
	}
}

func TestUpgradeCheckLeavesEverythingAlone(t *testing.T) {
	reg := registry.Default()
	next := *reg
	next.Version = reg.Version + ".9"
	e := pinnedEnv(t, &next)
	e.install("qwen3.5-0.8b")

	code, out, errs := e.mote("", "models", "upgrade", "--check")
	if code != 0 || !strings.Contains(out, "->") || !strings.Contains(out, "3 models to download") {
		t.Errorf("check: %d %s %s", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(e.home, "registry", "models.json")); err == nil {
		t.Error("--check installed the registry")
	}
	if e.has("bge-small-en-1.5") {
		t.Error("--check downloaded a model")
	}
}
