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
