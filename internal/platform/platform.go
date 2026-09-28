// Package platform detects the host OS, CPU, memory, disk and the local
// tools mote can reuse. Parsing is split from I/O so it can be tested with
// fixtures.
package platform

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Info describes the machine mote is running on.
type Info struct {
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Distro   string   `json:"distro,omitempty"`
	CPU      string   `json:"cpu,omitempty"`
	Cores    int      `json:"cores"`
	Features []string `json:"features,omitempty"`
	RAMMB    int      `json:"ram_mb"`
	// GPU is the name of a GPU mote could offload inference to, or empty
	// when none was detected. Detection is best-effort: presence here is
	// not a guarantee `mote config set gpu on` will find a matching
	// runtime build for it.
	GPU   string          `json:"gpu,omitempty"`
	Tools map[string]Tool `json:"tools"`
}

// Tool is an executable found on PATH.
type Tool struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// Known lists the tools mote looks for, grouped by kind. Editors are offered
// in the setup wizard; the rest are used by task steps.
var Known = []struct{ Name, Kind string }{
	{"code", "editor"}, {"nvim", "editor"}, {"vim", "editor"}, {"emacs", "editor"},
	{"hx", "editor"}, {"nano", "editor"}, {"notepad", "editor"},
	{"git", "vcs"},
	{"ffmpeg", "media"}, {"ffprobe", "media"},
	{"magick", "image"},
	{"llama-server", "runtime"}, {"llama-tts", "runtime"},
}

// Detect inspects the current machine. Fields that cannot be determined are
// left at their zero value rather than guessed.
func Detect() Info {
	info := Info{
		OS:    runtime.GOOS,
		Arch:  NormalizeArch(runtime.GOARCH),
		Cores: runtime.NumCPU(),
		Tools: LookTools(exec.LookPath),
	}
	info.RAMMB = totalRAMMB()
	info.CPU, info.Features = cpuInfo()
	info.GPU = gpuInfo()
	if info.OS == "linux" {
		if b, err := os.ReadFile("/etc/os-release"); err == nil {
			info.Distro = ParseOSRelease(string(b))
		}
	}
	return info
}

// LookTools resolves Known tools with the given lookup function.
func LookTools(look func(string) (string, error)) map[string]Tool {
	out := map[string]Tool{}
	for _, k := range Known {
		if p, err := look(k.Name); err == nil {
			out[k.Name] = Tool{Name: k.Name, Path: p, Kind: k.Kind}
		}
	}
	return out
}

// ToolsOfKind returns tool names of one kind, in Known order.
func (i Info) ToolsOfKind(kind string) []string {
	var names []string
	for _, k := range Known {
		if t, ok := i.Tools[k.Name]; ok && t.Kind == kind {
			names = append(names, t.Name)
		}
	}
	return names
}

// NormalizeArch maps the many spellings of CPU architectures onto the two
// names used by the registry (amd64, arm64). Unknown values pass through.
func NormalizeArch(a string) string {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "amd64", "x86_64", "x64", "x86-64":
		return "amd64"
	case "arm64", "aarch64", "armv8", "arm64e":
		return "arm64"
	}
	return strings.ToLower(strings.TrimSpace(a))
}

// ParseOSRelease returns a short distro label from /etc/os-release content.
func ParseOSRelease(s string) string {
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			kv[k] = strings.Trim(v, `"'`)
		}
	}
	if kv["ID"] == "" {
		return ""
	}
	if kv["VERSION_ID"] != "" {
		return kv["ID"] + " " + kv["VERSION_ID"]
	}
	return kv["ID"]
}

// ParseMeminfo returns MemTotal in MiB from /proc/meminfo content.
func ParseMeminfo(s string) int {
	for _, line := range strings.Split(s, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, err := strconv.Atoi(f[1])
			if err == nil {
				return kb / 1024
			}
		}
	}
	return 0
}

// ParseCPUInfo returns the model name and SIMD features relevant to CPU
// inference from /proc/cpuinfo content (x86 "flags" or ARM "Features").
func ParseCPUInfo(s string) (string, []string) {
	var name string
	var raw []string
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "model name", "Model", "cpu model":
			if name == "" {
				name = v
			}
		case "flags", "Features":
			if raw == nil {
				raw = strings.Fields(v)
			}
		}
	}
	return name, filterFeatures(raw)
}

var interesting = map[string]string{
	"avx": "avx", "avx2": "avx2", "fma": "fma", "f16c": "f16c",
	"avx512f": "avx512", "avx512_vnni": "avx512vnni", "avx_vnni": "avxvnni",
	"asimd": "neon", "asimddp": "dotprod", "sve": "sve", "i8mm": "i8mm",
}

func filterFeatures(raw []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range raw {
		if n, ok := interesting[strings.ToLower(f)]; ok && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

var quotedField = regexp.MustCompile(`"([^"]*)"`)

// ParseLspciVGA returns the vendor and model of the first display
// controller (VGA or 3D) in `lspci -mm` output, or "" if there is none.
func ParseLspciVGA(s string) string {
	for _, line := range strings.Split(s, "\n") {
		f := quotedField.FindAllStringSubmatch(line, -1)
		if len(f) < 3 {
			continue
		}
		class := f[0][1]
		if class != "VGA compatible controller" && class != "3D controller" {
			continue
		}
		return strings.TrimSpace(f[1][1] + " " + f[2][1])
	}
	return ""
}
