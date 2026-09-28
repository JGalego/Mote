package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissing(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("got %v", err)
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	bad := map[string]string{
		"unknown field": `{"schema":1,"profile":"small","colour":"blue"}`,
		"bad schema":    `{"schema":7,"profile":"small"}`,
		"empty profile": `{"schema":1,"profile":""}`,
		"threads":       `{"schema":1,"profile":"small","threads":-2}`,
		"relative dir":  `{"schema":1,"profile":"small","data_dir":"models"}`,
		"relative tool": `{"schema":1,"profile":"small","tools":{"ffmpeg":"ffmpeg"}}`,
		"not json":      `profile = small`,
		"wrong type":    `{"schema":1,"profile":"small","auto_download":"yes"}`,
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c, err := Parse([]byte(`{"schema":1,"profile":"balanced"}`))
	if err != nil || c.Profile != "balanced" || !c.ReuseTools {
		t.Errorf("got %+v %v", c, err)
	}
}

func TestSaveHistoryRollback(t *testing.T) {
	dir := t.TempDir()
	c := Default()
	if changed, err := Save(dir, c, "setup"); err != nil || !changed {
		t.Fatalf("first save: %v %v", changed, err)
	}
	if changed, _ := Save(dir, c, "again"); changed {
		t.Error("identical save should be a no-op")
	}
	c.Profile = "quality"
	if _, err := Save(dir, c, "user asked"); err != nil {
		t.Fatal(err)
	}
	h, err := History(dir)
	if err != nil || len(h) != 2 {
		t.Fatalf("history %d %v", len(h), err)
	}
	if h[0].Reason != "user asked" || h[0].Config.Profile != "quality" {
		t.Errorf("newest entry %+v", h[0])
	}
	restored, err := Rollback(dir, 1)
	if err != nil || restored.Profile != "small" {
		t.Fatalf("rollback %+v %v", restored, err)
	}
	cur, _ := Load(dir)
	if cur.Profile != "small" {
		t.Errorf("current after rollback %s", cur.Profile)
	}
	h, _ = History(dir)
	if len(h) != 3 || !strings.HasPrefix(h[0].Reason, "rollback") {
		t.Errorf("rollback not recorded: %+v", h[0])
	}
	if _, err := Rollback(dir, 9); err == nil {
		t.Error("rollback past history accepted")
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	c := Default()
	c.Threads = -1
	if _, err := Save(dir, c, "x"); err == nil {
		t.Fatal("invalid config saved")
	}
	if _, err := os.Stat(Path(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Error("file written despite error")
	}
}

func TestSet(t *testing.T) {
	c := Default()
	abs, _ := filepath.Abs("/usr/bin/ffmpeg")
	steps := [][2]string{{"profile", "balanced"}, {"auto_download", "true"}, {"threads", "4"},
		{"models.text", "qwen3.5-2b"}, {"tools.ffmpeg", abs}, {"editor", "nvim"}}
	for _, s := range steps {
		if err := c.Set(s[0], s[1]); err != nil {
			t.Fatalf("%s: %v", s[0], err)
		}
	}
	if c.Profile != "balanced" || !c.AutoDownload || c.Threads != 4 || c.Models["text"] != "qwen3.5-2b" || c.Editor != "nvim" {
		t.Errorf("got %+v", c)
	}
	if err := c.Set("models.text", ""); err != nil || c.Models != nil {
		t.Errorf("clear failed: %v %v", err, c.Models)
	}
	for _, s := range [][2]string{{"nope", "1"}, {"auto_download", "maybe"}, {"threads", "many"}} {
		if err := c.Set(s[0], s[1]); err == nil {
			t.Errorf("%s=%s accepted", s[0], s[1])
		}
	}
}

func TestGPUSetting(t *testing.T) {
	c := Default()
	if c.GPU != "" {
		t.Errorf("gpu is not off by default: %q", c.GPU)
	}
	if err := c.Set("gpu", "on"); err != nil || c.GPU != "on" {
		t.Errorf("gpu=on: %v %q", err, c.GPU)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("gpu=on should validate: %v", err)
	}
	if err := c.Set("gpu", "off"); err != nil || c.GPU != "" {
		t.Errorf("gpu=off: %v %q", err, c.GPU)
	}
	if err := c.Set("gpu", "maybe"); err == nil {
		t.Error("gpu=maybe accepted")
	}
	c.GPU = "maybe"
	if err := c.Validate(); err == nil {
		t.Error("Validate accepted an invalid gpu value")
	}
}

func TestDirs(t *testing.T) {
	t.Setenv("MOTE_CONFIG_DIR", "/tmp/x")
	if Dir() != "/tmp/x" {
		t.Error("MOTE_CONFIG_DIR ignored")
	}
	t.Setenv("MOTE_HOME", "/tmp/y")
	if DefaultDataDir() != "/tmp/y" || (Config{DataDir: "/z"}).Data() != "/tmp/y" {
		t.Error("MOTE_HOME must win")
	}
	t.Setenv("MOTE_HOME", "")
	if (Config{DataDir: "/z"}).Data() != "/z" {
		t.Error("data_dir ignored")
	}
}

func TestDirAndDataDirFollowTheEnvironment(t *testing.T) {
	t.Setenv("MOTE_CONFIG_DIR", "/tmp/cfg-override")
	if Dir() != "/tmp/cfg-override" {
		t.Errorf("Dir: %s", Dir())
	}
	t.Setenv("MOTE_CONFIG_DIR", "")
	if d := Dir(); !strings.HasSuffix(d, "mote") {
		t.Errorf("default Dir: %s", d)
	}
	t.Setenv("MOTE_HOME", "/tmp/data-override")
	if DefaultDataDir() != "/tmp/data-override" {
		t.Errorf("DefaultDataDir: %s", DefaultDataDir())
	}
	// MOTE_HOME wins over a configured data_dir, so tests stay isolated.
	c := Config{DataDir: "/tmp/from-config"}
	if c.Data() != "/tmp/data-override" {
		t.Errorf("Data with MOTE_HOME set: %s", c.Data())
	}
	t.Setenv("MOTE_HOME", "")
	if c.Data() != "/tmp/from-config" {
		t.Errorf("Data from config: %s", c.Data())
	}
	if (Config{}).Data() == "" {
		t.Error("default data dir is empty")
	}
}

func TestSetEveryKey(t *testing.T) {
	c := Default()
	// Absolute paths are spelled differently per platform, and the config
	// insists on absolute ones.
	base := t.TempDir()
	abs := func(name string) string { return filepath.Join(base, name) }
	cases := []struct{ key, value string }{
		{"profile", "quality"}, {"editor", "vi"}, {"workspace", abs("w")},
		{"data_dir", abs("d")}, {"llama_dir", abs("l")}, {"threads", "4"},
		{"auto_download", "true"}, {"reuse_tools", "false"},
		{"wake_word", "hey there"}, {"router", "embed"}, {"memory", "true"},
		{"models.text", "some-model"}, {"tools.ffmpeg", abs("ffmpeg")},
	}
	for _, c2 := range cases {
		if err := c.Set(c2.key, c2.value); err != nil {
			t.Errorf("set %s=%s: %v", c2.key, c2.value, err)
		}
	}
	if c.Profile != "quality" || c.Editor != "vi" || c.Threads != 4 || !c.AutoDownload ||
		c.ReuseTools || c.WakeWord != "hey there" || c.Router != "embed" || !c.Memory {
		t.Errorf("config after sets: %+v", c)
	}
	if c.Models["text"] != "some-model" || c.Tools["ffmpeg"] != abs("ffmpeg") {
		t.Errorf("maps: %+v %+v", c.Models, c.Tools)
	}
	// Clearing a map entry removes it rather than storing an empty string.
	if err := c.Set("models.text", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Models["text"]; ok {
		t.Errorf("cleared key remains: %+v", c.Models)
	}
	for _, bad := range []struct{ key, value string }{
		{"auto_download", "maybe"}, {"reuse_tools", "sometimes"},
		{"threads", "lots"}, {"router", "psychic"}, {"memory", "perhaps"},
		{"nosuchkey", "x"},
	} {
		if err := c.Set(bad.key, bad.value); err == nil {
			t.Errorf("%s=%s accepted", bad.key, bad.value)
		}
	}
}

func TestLoadRejectsBrokenConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("missing config: %v", err)
	}
	os.WriteFile(Path(dir), []byte("{not json"), 0o644)
	if _, err := Load(dir); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Errorf("broken config: %v", err)
	}
	os.WriteFile(Path(dir), []byte(`{"schema":99,"profile":"small"}`), 0o644)
	if _, err := Load(dir); err == nil {
		t.Error("future schema accepted")
	}
}

func TestDataDirPerPlatform(t *testing.T) {
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}
	none := env(nil)
	// filepath.Join uses this machine's separator, so build the expected
	// paths the same way rather than writing them out.
	cases := []struct{ goos, want string }{
		{"linux", filepath.Join("/home/u", ".local", "share", "mote")},
		{"darwin", filepath.Join("/home/u", "Library", "Application Support", "mote")},
		{"windows", filepath.Join("/home/u", "AppData", "Local", "mote")},
	}
	for _, c := range cases {
		if got := dataDirOn(c.goos, "/home/u", none); got != c.want {
			t.Errorf("%s: got %s want %s", c.goos, got, c.want)
		}
	}
	// The platform conventions win over the home directory when set.
	if got := dataDirOn("linux", "/home/u", env(map[string]string{"XDG_DATA_HOME": "/xdg"})); got != filepath.Join("/xdg", "mote") {
		t.Errorf("XDG_DATA_HOME ignored: %s", got)
	}
	if got := dataDirOn("windows", "/home/u", env(map[string]string{"LOCALAPPDATA": `C:\App`})); got != filepath.Join(`C:\App`, "mote") {
		t.Errorf("LOCALAPPDATA ignored: %s", got)
	}
	// MOTE_HOME overrides everything, which is what keeps tests isolated.
	for _, goos := range []string{"linux", "darwin", "windows"} {
		if got := dataDirOn(goos, "/home/u", env(map[string]string{"MOTE_HOME": "/override", "XDG_DATA_HOME": "/xdg"})); got != "/override" {
			t.Errorf("%s: MOTE_HOME ignored: %s", goos, got)
		}
	}
}
