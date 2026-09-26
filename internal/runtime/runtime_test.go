package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/fakellama"
	"github.com/jgalego/mote/registry"
)

func TestMain(m *testing.M) {
	fakellama.MaybeRun()
	os.Exit(m.Run())
}

func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestDownloadVerifiesAndResumes(t *testing.T) {
	payload := bytes.Repeat([]byte("mote"), 4096)
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		http.ServeContent(w, r, "f", fixedTime, bytes.NewReader(payload))
	}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "f.gguf")
	os.WriteFile(dest+".part", payload[:1000], 0o644)

	f := Fetcher{AllowHTTP: true}
	if err := f.Download(context.Background(), srv.URL, dest, hash(payload), int64(len(payload)), "f"); err != nil {
		t.Fatal(err)
	}
	if ranges[0] != "bytes=1000-" {
		t.Errorf("did not resume: %q", ranges[0])
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, payload) {
		t.Error("content differs")
	}
	// Present with the right size: no request.
	if err := f.Download(context.Background(), srv.URL, dest, hash(payload), int64(len(payload)), "f"); err != nil || len(ranges) != 1 {
		t.Errorf("re-download happened: %v %d", err, len(ranges))
	}
}

func TestDownloadRejectsBadChecksumAndHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("evil")) }))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "x")
	err := Fetcher{AllowHTTP: true}.Download(context.Background(), srv.URL, dest, hash([]byte("good")), 4, "x")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Error("bad file kept")
	}
	if _, err := os.Stat(dest + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Error("bad partial kept")
	}
	err = Fetcher{}.Download(context.Background(), srv.URL, dest, hash([]byte("evil")), 4, "x")
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Errorf("plain http accepted: %v", err)
	}
}

type entry struct {
	name, body, link string
	mode             int64
}

func writeTarGz(t *testing.T, p string, entries []entry) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.link != "" {
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.link, 0
		}
		tw.WriteHeader(h)
		tw.Write([]byte(e.body))
	}
	tw.Close()
	gz.Close()
	os.WriteFile(p, buf.Bytes(), 0o644)
}

func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "a.tar.gz")
	writeTarGz(t, arc, []entry{
		{name: "llama-b1/llama-server", body: "bin", mode: 0o755},
		{name: "llama-b1/libllama.so.0", body: "lib", mode: 0o644},
		{name: "llama-b1/libllama.so", link: "libllama.so.0"},
	})
	out := filepath.Join(dir, "out")
	if err := Extract(arc, out); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(out, "llama-server"))
	if err != nil {
		t.Fatal("top-level directory not stripped")
	}
	if goruntime.GOOS != "windows" && st.Mode()&0o100 == 0 {
		t.Error("exec bit lost")
	}
	if b, _ := os.ReadFile(filepath.Join(out, "libllama.so")); goruntime.GOOS != "windows" && string(b) != "lib" {
		t.Error("symlink not recreated")
	}
}

func TestExtractRejectsEscapes(t *testing.T) {
	cases := map[string][]entry{
		"dotdot":  {{name: "../evil", body: "x"}},
		"abs":     {{name: "/etc/evil", body: "x"}},
		"symlink": {{name: "d/ok", body: "x"}, {name: "d/link", link: "../../etc/passwd"}},
	}
	for name, entries := range cases {
		dir := t.TempDir()
		arc := filepath.Join(dir, "a.tar.gz")
		writeTarGz(t, arc, entries)
		if err := Extract(arc, filepath.Join(dir, "out")); err == nil {
			t.Errorf("%s: escape accepted", name)
		}
	}
}

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "a.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{"llama-server.exe", "ggml.dll"} {
		w, _ := zw.Create(n)
		w.Write([]byte(n))
	}
	zw.Close()
	os.WriteFile(arc, buf.Bytes(), 0o644)
	out := filepath.Join(dir, "out")
	if err := Extract(arc, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "ggml.dll")); err != nil {
		t.Error("flat zip not extracted in place")
	}

	buf.Reset()
	zw = zip.NewWriter(&buf)
	w, _ := zw.Create("../../evil.dll")
	w.Write([]byte("x"))
	zw.Close()
	os.WriteFile(arc, buf.Bytes(), 0o644)
	if err := Extract(arc, filepath.Join(dir, "out2")); err == nil {
		t.Error("zip slip accepted")
	}
	if err := Extract(filepath.Join(dir, "a.rar"), out); err == nil {
		t.Error("unknown archive accepted")
	}
}

func TestClean(t *testing.T) {
	cases := [][3]string{
		{"<think>hmm</think>\n Answer ", "", "Answer"},
		{"language English<asr_text>Hello there.", "<asr_text>", "Hello there."},
		{"plain", "<asr_text>", "plain"},
	}
	for _, c := range cases {
		if got := Clean(c[0], c[1]); got != c[2] {
			t.Errorf("Clean(%q) = %q, want %q", c[0], got, c[2])
		}
	}
}

func testModel(t *testing.T, dir string) (*registry.Model, map[string]string) {
	m := &registry.Model{ID: "fake", Backend: "llama.cpp", Context: 512, OutputAfter: "<asr_text>"}
	p := filepath.Join(dir, "fake.gguf")
	os.WriteFile(p, []byte("gguf"), 0o644)
	return m, map[string]string{"model": p}
}

func TestLlamaSession(t *testing.T) {
	dir := t.TempDir()
	fakellama.Install(t, dir)
	l := &Llama{Dir: dir, LogDir: t.TempDir()}
	missing, err := l.CheckFlags()
	if err != nil || len(missing) != 0 {
		t.Fatalf("flags: %v %v", missing, err)
	}
	m, files := testModel(t, dir)
	img := filepath.Join(dir, "x.png")
	os.WriteFile(img, []byte("png"), 0o644)

	s, err := l.Open(context.Background(), m, files)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Generate(context.Background(), Request{Prompt: "hi", Images: []string{img}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "echo: hi [image_url]" || res.OutputTokens != 20 {
		t.Errorf("got %+v", res)
	}
	res, _ = s.Generate(context.Background(), Request{Prompt: "x", JSONSchema: []byte(`{"type":"object"}`)})
	if res.Text != `{"ok": true}` {
		t.Errorf("schema request: %q", res.Text)
	}
	st := s.Close()
	if st.StartupMS <= 0 {
		t.Errorf("stats %+v", st)
	}
	s.Close() // idempotent

	t.Setenv("MOTE_FAKE_ASR", "1")
	s, _ = l.Open(context.Background(), m, files)
	res, _ = s.Generate(context.Background(), Request{Audio: []string{img}})
	s.Close()
	if res.Text != "echo:  [input_audio]" && res.Text != "echo: [input_audio]" {
		t.Errorf("asr prefix not stripped: %q", res.Text)
	}
}

func TestLlamaOpenFailure(t *testing.T) {
	dir := t.TempDir()
	fakellama.Install(t, dir)
	l := &Llama{Dir: dir, LogDir: t.TempDir()}
	m := &registry.Model{ID: "fake", Context: 512}
	_, err := l.Open(context.Background(), m, map[string]string{"model": filepath.Join(dir, "missing.gguf")})
	if err == nil || !strings.Contains(err.Error(), "failed to load model") {
		t.Fatalf("expected load failure with log tail, got %v", err)
	}
}

func TestSpeak(t *testing.T) {
	dir := t.TempDir()
	fakellama.Install(t, dir)
	l := &Llama{Dir: dir, LogDir: t.TempDir()}
	m, files := testModel(t, dir)
	out := filepath.Join(dir, "o.wav")
	if _, err := l.Speak(context.Background(), m, files, "hello", out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); !bytes.HasPrefix(b, []byte("RIFF")) {
		t.Error("no wav written")
	}
}

func TestStoreAndFindLlama(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	rt := registry.Runtime{Version: "b1"}
	if _, err := FindLlama("", s, rt); !errors.Is(err, ErrNoRuntime) {
		t.Errorf("got %v", err)
	}
	if _, err := FindLlama(t.TempDir(), s, rt); err == nil {
		t.Error("configured dir without binary accepted")
	}
	os.MkdirAll(s.RuntimeDir(rt), 0o755)
	os.WriteFile(filepath.Join(s.RuntimeDir(rt), exe("llama-server")), nil, 0o755)
	if d, err := FindLlama("", s, rt); err != nil || d != s.RuntimeDir(rt) {
		t.Errorf("managed runtime not found: %s %v", d, err)
	}

	m := &registry.Model{ID: "m", Files: []registry.File{{Role: "model", Name: "a.gguf", Size: 3, SHA256: hash([]byte("abc"))}}}
	if s.Installed(m) || len(s.Missing(m)) != 1 {
		t.Error("missing file not reported")
	}
	os.MkdirAll(filepath.Dir(s.FilePath(m, m.Files[0])), 0o755)
	os.WriteFile(s.FilePath(m, m.Files[0]), []byte("abc"), 0o644)
	if !s.Installed(m) || s.Verify(m) != nil {
		t.Error("installed file not recognised")
	}
	os.WriteFile(s.FilePath(m, m.Files[0]), []byte("abd"), 0o644)
	if s.Verify(m) == nil {
		t.Error("corruption not detected")
	}
	s.Remove(m)
	if s.Installed(m) {
		t.Error("remove failed")
	}
}

func TestInstallRuntime(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "rt.tar.gz")
	writeTarGz(t, arc, []entry{{name: "llama-b1/" + exe("llama-server"), body: "bin", mode: 0o755}})
	body, _ := os.ReadFile(arc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	s := Store{Dir: filepath.Join(dir, "data")}
	rt := registry.Runtime{Version: "b1"}
	a := registry.Asset{URL: srv.URL + "/llama-b1-bin.tar.gz", SHA256: hash(body), Size: int64(len(body))}
	got, err := s.InstallRuntime(context.Background(), Fetcher{AllowHTTP: true}, rt, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(got, exe("llama-server"))); err != nil {
		t.Fatal(err)
	}
	// Idempotent: second call does not need the server.
	srv.Close()
	if _, err := s.InstallRuntime(context.Background(), Fetcher{AllowHTTP: true}, rt, a); err != nil {
		t.Errorf("reinstall: %v", err)
	}
}
