package platform

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeArch(t *testing.T) {
	cases := map[string]string{
		"x86_64": "amd64", "AMD64": "amd64", "x64": "amd64",
		"aarch64": "arm64", "arm64": "arm64", "ARM64": "arm64",
		"riscv64": "riscv64", " 386 ": "386",
	}
	for in, want := range cases {
		if got := NormalizeArch(in); got != want {
			t.Errorf("NormalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseOSRelease(t *testing.T) {
	ubuntu := "NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"24.04\"\n"
	if got := ParseOSRelease(ubuntu); got != "ubuntu 24.04" {
		t.Errorf("got %q", got)
	}
	if got := ParseOSRelease("ID=arch\n"); got != "arch" {
		t.Errorf("got %q", got)
	}
	if got := ParseOSRelease("garbage"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	s := "MemTotal:       16481632 kB\nMemFree:         1000 kB\n"
	if got := ParseMeminfo(s); got != 16095 {
		t.Errorf("got %d", got)
	}
	if got := ParseMeminfo("nothing"); got != 0 {
		t.Errorf("got %d", got)
	}
}

func TestParseCPUInfoX86(t *testing.T) {
	s := "processor\t: 0\nmodel name\t: AMD EPYC 7B13\nflags\t\t: fpu sse avx avx2 fma f16c avx512f avx512_vnni\nprocessor\t: 1\nmodel name\t: AMD EPYC 7B13\n"
	name, feats := ParseCPUInfo(s)
	if name != "AMD EPYC 7B13" {
		t.Errorf("name %q", name)
	}
	want := []string{"avx", "avx2", "avx512", "avx512vnni", "f16c", "fma"}
	if !reflect.DeepEqual(feats, want) {
		t.Errorf("features %v, want %v", feats, want)
	}
}

func TestParseCPUInfoARM(t *testing.T) {
	s := "processor\t: 0\nFeatures\t: fp asimd evtstrm aes asimddp sve i8mm\n"
	_, feats := ParseCPUInfo(s)
	want := []string{"dotprod", "i8mm", "neon", "sve"}
	if !reflect.DeepEqual(feats, want) {
		t.Errorf("features %v, want %v", feats, want)
	}
}

func TestParseLspciVGA(t *testing.T) {
	s := `00:02.0 "VGA compatible controller" "Intel Corporation" "TigerLake-LP GT2 [Iris Xe Graphics]" -ra01 "Dell" "Device 0000"
03:00.0 "Non-VGA unclassified device" "Advanced Micro Devices, Inc. [AMD/ATI]" "Renoir"
`
	if got, want := ParseLspciVGA(s), "Intel Corporation TigerLake-LP GT2 [Iris Xe Graphics]"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := ParseLspciVGA("no gpu here"); got != "" {
		t.Errorf("no controller: got %q", got)
	}
	d3d := `03:00.0 "3D controller" "NVIDIA Corporation" "GA104M [GeForce RTX 3070 Mobile]"
`
	if got, want := ParseLspciVGA(d3d), "NVIDIA Corporation GA104M [GeForce RTX 3070 Mobile]"; got != want {
		t.Errorf("3D controller: got %q, want %q", got, want)
	}
}

func TestLookTools(t *testing.T) {
	have := map[string]string{"nvim": "/usr/bin/nvim", "git": "/usr/bin/git", "ffmpeg": "/opt/bin/ffmpeg", "code": "/snap/bin/code"}
	look := func(n string) (string, error) {
		if p, ok := have[n]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	info := Info{Tools: LookTools(look)}
	if len(info.Tools) != 4 {
		t.Fatalf("tools %v", info.Tools)
	}
	if got := info.ToolsOfKind("editor"); !reflect.DeepEqual(got, []string{"code", "nvim"}) {
		t.Errorf("editors %v", got)
	}
	if info.Tools["ffmpeg"].Kind != "media" {
		t.Errorf("ffmpeg kind %q", info.Tools["ffmpeg"].Kind)
	}
}

func TestDetectHost(t *testing.T) {
	info := Detect()
	if info.OS == "" || info.Arch == "" || info.Cores < 1 {
		t.Fatalf("incomplete detection: %+v", info)
	}
	if info.OS == "linux" && info.RAMMB <= 0 {
		t.Errorf("expected RAM on linux, got %d", info.RAMMB)
	}
	if DiskFreeMB(t.TempDir()) < 0 {
		t.Error("negative disk space")
	}
}
