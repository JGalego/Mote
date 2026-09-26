// Package config manages the user's mote configuration. It lives outside the
// repository (in the OS config directory) and every saved version is kept in
// a history directory so changes can be inspected and rolled back.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Schema is the config format version.
const Schema = 1

// ErrNotConfigured means no config file exists yet.
var ErrNotConfigured = errors.New("mote is not set up yet; run `mote setup`")

type Config struct {
	Schema       int               `json:"schema"`
	Profile      string            `json:"profile"`
	Editor       string            `json:"editor,omitempty"`
	WakeWord     string            `json:"wake_word,omitempty"`
	Router       string            `json:"router,omitempty"`
	Workspace    string            `json:"workspace,omitempty"`
	DataDir      string            `json:"data_dir,omitempty"`
	AutoDownload bool              `json:"auto_download"`
	ReuseTools   bool              `json:"reuse_tools"`
	LlamaDir     string            `json:"llama_dir,omitempty"`
	Threads      int               `json:"threads,omitempty"`
	Models       map[string]string `json:"models,omitempty"`
	Tools        map[string]string `json:"tools,omitempty"`
}

// Default returns the configuration used when the user accepts all defaults.
func Default() Config {
	return Config{Schema: Schema, Profile: "small", ReuseTools: true}
}

// Dir returns the configuration directory.
func Dir() string {
	if d := os.Getenv("MOTE_CONFIG_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "mote")
}

// DefaultDataDir returns where models, the runtime and local benchmark
// results are stored unless the config says otherwise.
func DefaultDataDir() string {
	if d := os.Getenv("MOTE_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "mote")
		}
		return filepath.Join(home, "AppData", "Local", "mote")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "mote")
	default:
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, "mote")
		}
		return filepath.Join(home, ".local", "share", "mote")
	}
}

// Data returns the effective data directory for c.
func (c Config) Data() string {
	if os.Getenv("MOTE_HOME") == "" && c.DataDir != "" {
		return c.DataDir
	}
	return DefaultDataDir()
}

// Path returns the config file path inside dir.
func Path(dir string) string { return filepath.Join(dir, "config.json") }

// Parse decodes a config document strictly.
func Parse(b []byte) (Config, error) {
	c := Default()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return c, c.Validate()
}

// Load reads the config from dir.
func Load(dir string) (Config, error) {
	b, err := os.ReadFile(Path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotConfigured
	}
	if err != nil {
		return Config{}, err
	}
	c, err := Parse(b)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", Path(dir), err)
	}
	return c, nil
}

// Validate checks values that do not depend on the model registry.
func (c Config) Validate() error {
	var errs []string
	if c.Schema != Schema {
		errs = append(errs, fmt.Sprintf("schema %d not supported (want %d)", c.Schema, Schema))
	}
	if c.Profile == "" {
		errs = append(errs, "profile is empty")
	}
	if c.Threads < 0 || c.Threads > 1024 {
		errs = append(errs, "threads must be between 0 (auto) and 1024")
	}
	for k, p := range map[string]string{"workspace": c.Workspace, "data_dir": c.DataDir, "llama_dir": c.LlamaDir} {
		if p != "" && !filepath.IsAbs(p) {
			errs = append(errs, k+" must be an absolute path")
		}
	}
	for name, p := range c.Tools {
		if !filepath.IsAbs(p) {
			errs = append(errs, "tools."+name+" must be an absolute path")
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return errors.New("invalid config: " + strings.Join(errs, "; "))
	}
	return nil
}

// Entry is one saved version of the configuration.
type Entry struct {
	File    string    `json:"-"`
	SavedAt time.Time `json:"saved_at"`
	Reason  string    `json:"reason"`
	Config  Config    `json:"config"`
}

func encode(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return append(b, '\n')
}

// Save validates and writes c, recording it in the history with a reason.
// Saving an identical config is a no-op and returns false.
func Save(dir string, c Config, reason string) (bool, error) {
	if err := c.Validate(); err != nil {
		return false, err
	}
	b := encode(c)
	if cur, err := os.ReadFile(Path(dir)); err == nil && bytes.Equal(cur, b) {
		return false, nil
	}
	hist := filepath.Join(dir, "history")
	if err := os.MkdirAll(hist, 0o755); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	name := now.Format("20060102T150405.000000000Z") + ".json"
	if err := writeAtomic(filepath.Join(hist, name), encode(Entry{SavedAt: now, Reason: reason, Config: c})); err != nil {
		return false, err
	}
	return true, writeAtomic(Path(dir), b)
}

// History returns saved versions, newest first. Entry 0 is the current one.
func History(dir string) ([]Entry, error) {
	files, err := filepath.Glob(filepath.Join(dir, "history", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	var out []Entry
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		e.File = f
		out = append(out, e)
	}
	return out, nil
}

// Rollback restores history entry n (1 = the version before the current one)
// and records the restore as a new version.
func Rollback(dir string, n int) (Config, error) {
	h, err := History(dir)
	if err != nil {
		return Config{}, err
	}
	if n < 1 || n >= len(h) {
		return Config{}, fmt.Errorf("no configuration %d versions back (history has %d)", n, len(h))
	}
	e := h[n]
	_, err = Save(dir, e.Config, fmt.Sprintf("rollback to version from %s", e.SavedAt.Format(time.RFC3339)))
	return e.Config, err
}

// Keys lists settable keys for `mote config set`.
var Keys = []string{"profile", "editor", "workspace", "data_dir", "auto_download", "reuse_tools", "llama_dir", "threads", "wake_word", "router", "models.<capability>", "tools.<name>"}

// Set changes one key. An empty value clears optional keys.
func (c *Config) Set(key, value string) error {
	parseBool := func() (bool, error) {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf("%s expects true or false", key)
		}
		return b, nil
	}
	switch {
	case key == "profile":
		c.Profile = value
	case key == "editor":
		c.Editor = value
	case key == "wake_word":
		c.WakeWord = value
	case key == "router":
		if value != "" && value != "text" && value != "embed" {
			return fmt.Errorf("router expects text or embed")
		}
		c.Router = value
	case key == "workspace":
		c.Workspace = value
	case key == "data_dir":
		c.DataDir = value
	case key == "llama_dir":
		c.LlamaDir = value
	case key == "auto_download":
		b, err := parseBool()
		if err != nil {
			return err
		}
		c.AutoDownload = b
	case key == "reuse_tools":
		b, err := parseBool()
		if err != nil {
			return err
		}
		c.ReuseTools = b
	case key == "threads":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("threads expects a number")
		}
		c.Threads = n
	case strings.HasPrefix(key, "models."):
		c.Models = setMap(c.Models, strings.TrimPrefix(key, "models."), value)
	case strings.HasPrefix(key, "tools."):
		c.Tools = setMap(c.Tools, strings.TrimPrefix(key, "tools."), value)
	default:
		return fmt.Errorf("unknown key %q (known: %s)", key, strings.Join(Keys, ", "))
	}
	return c.Validate()
}

func setMap(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	if v == "" {
		delete(m, k)
	} else {
		m[k] = v
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
