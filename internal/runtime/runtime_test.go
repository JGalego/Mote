package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

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
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.link != "" {
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.link, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
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
	t.Helper()
	m := &registry.Model{ID: "fake", Backend: "llama.cpp", Context: 512, OutputAfter: "<asr_text>"}
	p := filepath.Join(dir, "fake.gguf")
	if err := os.WriteFile(p, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	var streamed []string
	res, err = s.Generate(context.Background(), Request{Prompt: "one two three", OnToken: func(t string) { streamed = append(streamed, t) }})
	if err != nil || res.Text != "echo: one two three" || len(streamed) != 4 || res.OutputTokens != 20 {
		t.Errorf("stream: %+v %q %v", res, streamed, err)
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
	if _, err := FindLlama("", s, rt, ""); !errors.Is(err, ErrNoRuntime) {
		t.Errorf("got %v", err)
	}
	if _, err := FindLlama(t.TempDir(), s, rt, ""); err == nil {
		t.Error("configured dir without binary accepted")
	}
	os.MkdirAll(s.RuntimeDir(rt, ""), 0o755)
	os.WriteFile(filepath.Join(s.RuntimeDir(rt, ""), exe("llama-server")), nil, 0o755)
	if d, err := FindLlama("", s, rt, ""); err != nil || d != s.RuntimeDir(rt, "") {
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

// TestConcurrentInstallsDoNotCorruptEachOther guards against two installs of
// the same build racing on one shared extraction directory: mote serve can
// pull a runtime lazily at the same moment a manual mote setup does. Both
// must end up with a complete, working binary, not a half-extracted or
// half-deleted directory.
func TestConcurrentInstallsDoNotCorruptEachOther(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "rt.tar.gz")
	writeTarGz(t, arc, []entry{{name: "llama-b1/" + exe("llama-server"), body: "bin", mode: 0o755}})
	body, err := os.ReadFile(arc)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	s := Store{Dir: filepath.Join(dir, "data")}
	rt := registry.Runtime{Version: "b1"}
	a := registry.Asset{URL: srv.URL + "/llama-b1-bin.tar.gz", SHA256: hash(body), Size: int64(len(body))}

	const n = 8
	results := make(chan error, n)
	for range n {
		go func() {
			_, err := s.InstallRuntime(context.Background(), Fetcher{AllowHTTP: true}, rt, a)
			results <- err
		}()
	}
	for range n {
		if err := <-results; err != nil {
			t.Errorf("concurrent install: %v", err)
		}
	}
	got := filepath.Join(s.RuntimeDir(rt, ""), exe("llama-server"))
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("binary missing after concurrent installs: %v", err)
	}
	if string(b) != "bin" {
		t.Errorf("binary corrupted by a concurrent install: %q", b)
	}
	entries, err := os.ReadDir(filepath.Join(s.Dir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover extraction directory: %s", e.Name())
		}
	}
}

func TestInstallAnotherBackendsRuntime(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "sd.tar.gz")
	writeTarGz(t, arc, []entry{{name: exe("sd-cli"), body: "bin", mode: 0o755}})
	body, _ := os.ReadFile(arc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	s := Store{Dir: filepath.Join(dir, "data")}
	sd := registry.Runtime{Name: "sd.cpp", Version: "master-1"}
	llama := registry.Runtime{Name: "llama.cpp", Version: "master-1"}
	// Each backend has its own directory, even at the same version.
	if s.RuntimeDir(sd, "") == s.RuntimeDir(llama, "") {
		t.Fatal("runtimes share a directory")
	}
	// A runtime with no name is llama.cpp's, as older registries wrote it.
	if s.RuntimeDir(registry.Runtime{Version: "b1"}, "") != filepath.Join(s.Dir, "runtime", "llama.cpp-b1") {
		t.Error("unnamed runtime moved")
	}
	if _, ok := s.RuntimeInstalled(sd, ""); ok {
		t.Error("installed before installing")
	}
	a := registry.Asset{URL: srv.URL + "/sd.tar.gz", SHA256: hash(body), Size: int64(len(body))}
	got, err := s.InstallRuntime(context.Background(), Fetcher{AllowHTTP: true}, sd, a)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := s.RuntimeInstalled(sd, ""); !ok || d != got {
		t.Errorf("installed at %s, found %s %v", got, d, ok)
	}
	// An archive without the backend's program is refused.
	s2 := Store{Dir: filepath.Join(dir, "data2")}
	if _, err := s2.InstallRuntime(context.Background(), Fetcher{AllowHTTP: true}, llama, a); err == nil || !strings.Contains(err.Error(), "llama-server") {
		t.Errorf("wrong archive: %v", err)
	}
}

func TestGPUBuildGetsItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	cpuArc := filepath.Join(dir, "cpu.tar.gz")
	writeTarGz(t, cpuArc, []entry{{name: exe("llama-server"), body: "cpu-bin", mode: 0o755}})
	cpuBody, _ := os.ReadFile(cpuArc)
	gpuArc := filepath.Join(dir, "gpu.tar.gz")
	writeTarGz(t, gpuArc, []entry{{name: exe("llama-server"), body: "gpu-bin", mode: 0o755}})
	gpuBody, _ := os.ReadFile(gpuArc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "gpu") {
			w.Write(gpuBody)
		} else {
			w.Write(cpuBody)
		}
	}))
	defer srv.Close()

	s := Store{Dir: filepath.Join(dir, "data")}
	rt := registry.Runtime{Name: "llama.cpp", Version: "b1"}
	cpuAsset := registry.Asset{URL: srv.URL + "/cpu.tar.gz", SHA256: hash(cpuBody), Size: int64(len(cpuBody))}
	gpuAsset := registry.Asset{Variant: "gpu", URL: srv.URL + "/gpu.tar.gz", SHA256: hash(gpuBody), Size: int64(len(gpuBody))}

	cpuDir, err := s.InstallRuntime(context.Background(), Fetcher{AllowHTTP: true}, rt, cpuAsset)
	if err != nil {
		t.Fatal(err)
	}
	gpuDir, err := s.InstallRuntime(context.Background(), Fetcher{AllowHTTP: true}, rt, gpuAsset)
	if err != nil {
		t.Fatal(err)
	}
	if cpuDir == gpuDir {
		t.Fatal("the CPU and GPU builds share a directory")
	}
	if gpuDir != s.RuntimeDir(rt, "gpu") {
		t.Errorf("gpu dir %s, want %s", gpuDir, s.RuntimeDir(rt, "gpu"))
	}
	// Both remain installed independently; neither overwrote the other.
	if _, ok := s.RuntimeInstalled(rt, ""); !ok {
		t.Error("cpu build missing after installing the gpu one")
	}
	if d, ok := s.RuntimeInstalled(rt, "gpu"); !ok || d != gpuDir {
		t.Errorf("gpu build not found: %s %v", d, ok)
	}
	cb, _ := os.ReadFile(filepath.Join(cpuDir, exe("llama-server")))
	gb, _ := os.ReadFile(filepath.Join(gpuDir, exe("llama-server")))
	if string(cb) != "cpu-bin" || string(gb) != "gpu-bin" {
		t.Errorf("wrong binaries: cpu=%q gpu=%q", cb, gb)
	}
	if d, err := FindLlama("", s, rt, "gpu"); err != nil || d != gpuDir {
		t.Errorf("FindLlama gpu: %s %v", d, err)
	}
}

func TestDownloadStallTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.Write([]byte("12345"))
		w.(http.Flusher).Flush()
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	dest := filepath.Join(t.TempDir(), "x")
	err := Fetcher{AllowHTTP: true, StallTimeout: 200 * time.Millisecond}.Download(context.Background(), srv.URL, dest, hash([]byte("1234567890")), 10, "x")
	if err == nil {
		t.Fatal("stalled download did not fail")
	}
	if st, _ := os.Stat(dest + ".part"); st == nil || st.Size() != 5 {
		t.Error("partial file not kept for resume")
	}
}

// TestAcquireLockReclaimsAStaleOne guards against a lock left behind by a
// crashed download blocking every future one: once it is older than
// staleLock, a new attempt must reclaim it rather than wait forever.
func TestAcquireLockReclaimsAStaleOne(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleLock - time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, release, err := acquireLock(ctx, lock)
	if err != nil {
		t.Fatalf("expected the stale lock to be reclaimed, got %v", err)
	}
	release()
}

// TestAcquireLockWaitsForAFreshOne is the other side of the same guard: a
// lock still being touched must not be reclaimed just because another
// caller is waiting for it.
func TestAcquireLockWaitsForAFreshOne(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, _, err := acquireLock(ctx, lock); err == nil {
		t.Error("expected acquireLock to give up on a fresh lock once ctx expired")
	}
}

// TestAcquireLockGivesUpOnRealPermissionErrors guards the other side of
// tolerating Windows' transient access-denied on a lock another holder just
// released (see acquireLock): a permission error that never clears, because
// the directory genuinely cannot be written to, must still fail rather than
// retry forever.
func TestAcquireLockGivesUpOnRealPermissionErrors(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows does not enforce POSIX directory write permissions the same way")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755) // let TempDir's own cleanup remove it
	lock := filepath.Join(dir, "x.lock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := acquireLock(ctx, lock)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected a permission error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > lockPermissionRetry+time.Second {
		t.Errorf("took %s to give up, want close to lockPermissionRetry (%s)", elapsed, lockPermissionRetry)
	}
}

func TestVerifyFileReportsWhatIsWrong(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	body := []byte("hello")
	os.WriteFile(p, body, 0o644)
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])

	if err := VerifyFile(p, good, int64(len(body))); err != nil {
		t.Errorf("good file: %v", err)
	}
	err := VerifyFile(p, good, 99)
	if err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Errorf("size: %v", err)
	}
	err = VerifyFile(p, strings.Repeat("0", 64), int64(len(body)))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("checksum: %v", err)
	}
	if err := VerifyFile(filepath.Join(dir, "missing"), good, 5); err == nil {
		t.Error("missing file accepted")
	}
}

func TestExtractRejectsUnknownArchives(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "thing.rar")
	os.WriteFile(p, []byte("x"), 0o644)
	err := Extract(p, filepath.Join(dir, "out"))
	if err == nil || !strings.Contains(err.Error(), "unsupported archive") {
		t.Errorf("unknown archive: %v", err)
	}
	// A corrupt archive of a supported type fails cleanly too.
	bad := filepath.Join(dir, "broken.tar.gz")
	os.WriteFile(bad, []byte("not gzip"), 0o644)
	if err := Extract(bad, filepath.Join(dir, "out2")); err == nil {
		t.Error("corrupt tar.gz accepted")
	}
	badZip := filepath.Join(dir, "broken.zip")
	os.WriteFile(badZip, []byte("not a zip"), 0o644)
	if err := Extract(badZip, filepath.Join(dir, "out3")); err == nil {
		t.Error("corrupt zip accepted")
	}
}

func TestStorePullDownloadsWhatIsMissing(t *testing.T) {
	body := []byte("model weights")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	m := &registry.Model{ID: "m", Files: []registry.File{{
		Role: "model", Name: "m.gguf", URL: srv.URL + "/m.gguf",
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)),
	}}}
	s := Store{Dir: t.TempDir()}
	if s.Installed(m) {
		t.Fatal("empty store reports the model installed")
	}
	f := Fetcher{AllowHTTP: true}
	if err := s.Pull(context.Background(), f, m); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !s.Installed(m) {
		t.Error("model not installed after pull")
	}
	if err := s.Verify(m); err != nil {
		t.Errorf("verify after pull: %v", err)
	}
	// Pulling again is a no-op rather than a re-download.
	if err := s.Pull(context.Background(), f, m); err != nil {
		t.Errorf("second pull: %v", err)
	}
	if err := s.Remove(m); err != nil {
		t.Fatal(err)
	}
	if s.Installed(m) {
		t.Error("model still installed after remove")
	}
	// A pull that cannot be verified is an error.
	m.Files[0].SHA256 = strings.Repeat("b", 64)
	if err := s.Pull(context.Background(), f, m); err == nil {
		t.Error("pull accepted a file with the wrong hash")
	}
}

// TestSDCannotChatOrSpeak locks in that an image backend fails clearly for
// the request kinds it cannot serve, rather than being silently skipped.
func TestSDCannotChatOrSpeak(t *testing.T) {
	sd := &SD{}
	m := &registry.Model{ID: "flux"}
	if _, err := sd.Open(context.Background(), m, nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Open: got %v, want ErrUnsupported", err)
	}
	if _, err := sd.Speak(context.Background(), m, nil, "hi", "out.wav"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Speak: got %v, want ErrUnsupported", err)
	}
}

func TestImageMIME(t *testing.T) {
	cases := map[string]string{
		"a.png": "image/png", "a.PNG": "image/png",
		"a.jpg": "image/jpeg", "a.jpeg": "image/jpeg",
		"a.webp": "image/webp", "a.gif": "image/gif", "a.bmp": "image/bmp",
		"a.bin": "image/png", // unknown extensions are assumed to be png
	}
	for in, want := range cases {
		if got := imageMIME(in); got != want {
			t.Errorf("imageMIME(%q) = %q want %q", in, got, want)
		}
	}
}

func TestCosine(t *testing.T) {
	if got := Cosine([]float32{1, 0}, []float32{1, 0}); got < 0.999 {
		t.Errorf("identical vectors scored %v", got)
	}
	if got := Cosine([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Errorf("orthogonal vectors scored %v", got)
	}
	// Mismatched, empty or zero vectors are not comparable and score 0.
	for _, c := range [][2][]float32{
		{{1, 0}, {1, 0, 0}},
		{{}, {}},
		{{0, 0}, {1, 1}},
	} {
		if got := Cosine(c[0], c[1]); got != 0 {
			t.Errorf("Cosine(%v, %v) = %v", c[0], c[1], got)
		}
	}
}

func TestTailReadsTheEndOfALog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	os.WriteFile(p, []byte("one\ntwo\nthree\n"), 0o644)
	got := tail(p, 2)
	if !strings.Contains(got, "three") || strings.Contains(got, "one") {
		t.Errorf("tail: %q", got)
	}
	if tail(filepath.Join(t.TempDir(), "missing"), 2) != "" {
		t.Error("missing log should read as empty")
	}
}

// stubServer returns a *server talking to h instead of a real llama-server,
// so the HTTP handling can be tested without starting a process.
func stubServer(t *testing.T, h http.HandlerFunc) *server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &server{
		base: srv.URL, client: srv.Client(),
		model: &registry.Model{ID: "m"}, exited: make(chan struct{}),
	}
}

func TestRemoteOpen(t *testing.T) {
	ctx := context.Background()
	m := &registry.Model{ID: "m", Context: 512}

	// The server cannot be reached, and there is no fallback: ErrUnreachable.
	r := &Remote{URL: "http://127.0.0.1:1"}
	if _, err := r.Open(ctx, m, nil); !errors.Is(err, ErrUnreachable) {
		t.Errorf("unreachable, no fallback: got %v, want ErrUnreachable", err)
	}

	// A fallback is told why, and gets the chance to load the model locally.
	var fellBack error
	r = &Remote{URL: "http://127.0.0.1:1", Local: &Llama{Dir: t.TempDir()},
		Fallback: func(err error) { fellBack = err }}
	if _, err := r.Open(ctx, m, map[string]string{"model": "m.gguf"}); err == nil {
		t.Error("expected an error: there is no llama-server binary to fall back to")
	}
	if !errors.Is(fellBack, ErrUnreachable) {
		t.Errorf("fallback was not told why: %v", fellBack)
	}

	// A context already done is reported as such, not as merely unreachable.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	r = &Remote{URL: "http://127.0.0.1:1"}
	if _, err := r.Open(cctx, m, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context: got %v, want context.Canceled", err)
	}

	// The server answers but refuses the model: its own message wins.
	refuses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "no room for m"}})
	}))
	defer refuses.Close()
	r = &Remote{URL: refuses.URL}
	if _, err := r.Open(ctx, m, nil); err == nil || err.Error() != "no room for m" {
		t.Errorf("server error: got %v", err)
	}

	// A failure with a plain-text body is reported with the status and text.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer broken.Close()
	r = &Remote{URL: broken.URL}
	if _, err := r.Open(ctx, m, nil); err == nil || !strings.Contains(err.Error(), "internal error") {
		t.Errorf("plain text error: got %v", err)
	}

	// A successful load returns a session pointed at the resident server.
	loads := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"startup_ms": 42.0})
	}))
	defer loads.Close()
	r = &Remote{URL: loads.URL}
	sess, err := r.Open(ctx, m, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if u, ok := sess.(URLer); !ok || u.URL() != loads.URL {
		t.Errorf("session URL: got %+v", sess)
	}
}

func TestGenerateReportsServerProblems(t *testing.T) {
	ctx := context.Background()
	// A non-200 carries the body, which is where llama.cpp explains itself.
	s := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "context too long", http.StatusBadRequest)
	})
	_, err := s.Generate(ctx, Request{Prompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "context too long") {
		t.Errorf("status error: %v", err)
	}
	// A body that is not the expected shape is reported, not ignored.
	s = stubServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"choices":[]}`)) })
	if _, err := s.Generate(ctx, Request{Prompt: "hi"}); err == nil {
		t.Error("a reply with no choices was accepted")
	}
	s = stubServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not json")) })
	if _, err := s.Generate(ctx, Request{Prompt: "hi"}); err == nil {
		t.Error("a non-JSON reply was accepted")
	}
	// A dead server surfaces the transport error.
	dead := &server{base: "http://127.0.0.1:1", client: &http.Client{}, model: &registry.Model{ID: "m"}}
	if _, err := dead.Generate(ctx, Request{Prompt: "hi"}); err == nil {
		t.Error("an unreachable server was accepted")
	}
	// A missing image is caught before the request is sent.
	if _, err := s.Generate(ctx, Request{Images: []string{"/nope/missing.png"}}); err == nil {
		t.Error("a missing image was accepted")
	}
	if _, err := s.Generate(ctx, Request{Audio: []string{"/nope/missing.wav"}}); err == nil {
		t.Error("missing audio was accepted")
	}
}

func TestGenerateRejectsBadStreamChunks(t *testing.T) {
	s := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {not json}\n\n"))
	})
	_, err := s.Generate(context.Background(), Request{Prompt: "hi", OnToken: func(string) {}})
	if err == nil || !strings.Contains(err.Error(), "bad stream chunk") {
		t.Errorf("bad chunk: %v", err)
	}
}

func TestEmbedErrors(t *testing.T) {
	ctx := context.Background()
	// Nothing to embed is not a request at all.
	s := stubServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("server called for an empty batch") })
	if v, err := s.Embed(ctx, nil); err != nil || v != nil {
		t.Errorf("empty batch: %v %v", v, err)
	}
	s = stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no embedding support", http.StatusBadRequest)
	})
	if _, err := s.Embed(ctx, []string{"a"}); err == nil || !strings.Contains(err.Error(), "no embedding support") {
		t.Errorf("status: %v", err)
	}
	s = stubServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not json")) })
	if _, err := s.Embed(ctx, []string{"a"}); err == nil {
		t.Error("non-JSON accepted")
	}
	// One vector per text, or the caller cannot line them up.
	s = stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2]}]}`))
	})
	if _, err := s.Embed(ctx, []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "2 texts") {
		t.Errorf("count mismatch: %v", err)
	}
	s = stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"index":7,"embedding":[1,2]}]}`))
	})
	if _, err := s.Embed(ctx, []string{"a"}); err == nil || !strings.Contains(err.Error(), "index 7") {
		t.Errorf("bad index: %v", err)
	}
	dead := &server{base: "http://127.0.0.1:1", client: &http.Client{}, model: &registry.Model{ID: "m"}}
	if _, err := dead.Embed(ctx, []string{"a"}); err == nil {
		t.Error("unreachable server accepted")
	}
}

func TestEmbedReordersByIndex(t *testing.T) {
	s := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Out of order on purpose: the index decides, not the position.
		w.Write([]byte(`{"data":[{"index":1,"embedding":[9]},{"index":0,"embedding":[1]}]}`))
	})
	v, err := s.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if v[0][0] != 1 || v[1][0] != 9 {
		t.Errorf("vectors not placed by index: %v", v)
	}
}

func TestSpeakWithoutTheBinary(t *testing.T) {
	l := &Llama{Dir: t.TempDir()}
	_, err := l.Speak(context.Background(), &registry.Model{ID: "m"}, nil, "hello", filepath.Join(t.TempDir(), "o.wav"))
	if err == nil || !strings.Contains(err.Error(), "llama-tts not found") {
		t.Errorf("missing llama-tts: %v", err)
	}
}

func TestCheckFlagsWithoutTheBinary(t *testing.T) {
	l := &Llama{Dir: t.TempDir()}
	if _, err := l.CheckFlags(); err == nil {
		t.Error("missing llama-server accepted")
	}
}

func TestDownloadRejectsAndRecovers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f := Fetcher{AllowHTTP: true}

	// Only HTTPS, or HTTP to the loopback when explicitly allowed.
	err := Fetcher{}.Download(ctx, "http://example.org/x", filepath.Join(dir, "x"), "", 1, "x")
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Errorf("plain HTTP: %v", err)
	}
	if err := f.Download(ctx, "://bad url", filepath.Join(dir, "x"), "", 1, "x"); err == nil {
		t.Error("unparseable URL accepted")
	}
	// A 404 names the label so the user knows which file failed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	err = f.Download(ctx, srv.URL+"/gone", filepath.Join(dir, "gone"), "", 1, "the weights")
	if err == nil || !strings.Contains(err.Error(), "the weights") {
		t.Errorf("404: %v", err)
	}
	// A connection that dies mid-body is an error, not a truncated file.
	body := strings.Repeat("x", 1024)
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2048")
		w.Write([]byte(body))
	}))
	defer cut.Close()
	dest := filepath.Join(dir, "cut")
	if err := f.Download(ctx, cut.URL+"/f", dest, strings.Repeat("0", 64), 2048, "cut"); err == nil {
		t.Error("a truncated download was accepted")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a failed download left the file in place")
	}
}

func TestExtractZipRejectsSymlinksAndMakesDirs(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.zip")

	// A zip holding a directory entry and a file extracts both.
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	if _, err := zw.Create("top/sub/"); err != nil {
		t.Fatal(err)
	}
	w, _ := zw.Create("top/sub/file.txt")
	w.Write([]byte("hello"))
	zw.Close()
	os.WriteFile(archive, buf.Bytes(), 0o644)
	out := filepath.Join(dir, "out")
	if err := Extract(archive, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(out, "sub", "file.txt")); err != nil || string(b) != "hello" {
		t.Errorf("extracted file: %q %v", b, err)
	}

	// A symlink in a zip is refused rather than followed.
	buf2 := &bytes.Buffer{}
	zw2 := zip.NewWriter(buf2)
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(os.ModeSymlink | 0o777)
	lw, err := zw2.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	lw.Write([]byte("/etc/passwd"))
	zw2.Close()
	link := filepath.Join(dir, "link.zip")
	os.WriteFile(link, buf2.Bytes(), 0o644)
	err = Extract(link, filepath.Join(dir, "out2"))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("zip symlink: %v", err)
	}
}

func TestDownloadResumesFromAPartialFile(t *testing.T) {
	body := []byte(strings.Repeat("abcdefgh", 256)) // 2048 bytes
	sum := sha256.Sum256(body)
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		ranges = append(ranges, rng)
		if rng == "" {
			w.Write(body)
			return
		}
		var from int
		fmt.Sscanf(rng, "bytes=%d-", &from)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[from:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "f.bin")
	// Leave a partial file behind, as an interrupted download would.
	os.WriteFile(dest+".part", body[:512], 0o644)

	f := Fetcher{AllowHTTP: true}
	if err := f.Download(context.Background(), srv.URL+"/f.bin", dest, hex.EncodeToString(sum[:]), int64(len(body)), "f"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(ranges) != 1 || ranges[0] != "bytes=512-" {
		t.Errorf("did not resume: %q", ranges)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(body) {
		t.Errorf("resumed file is %d bytes", len(got))
	}
	// A second call sees the finished file and does nothing.
	before := len(ranges)
	if err := f.Download(context.Background(), srv.URL+"/f.bin", dest, hex.EncodeToString(sum[:]), int64(len(body)), "f"); err != nil {
		t.Fatal(err)
	}
	if len(ranges) != before {
		t.Error("an already-downloaded file was fetched again")
	}
}

func TestDownloadDiscardsAnOversizedPartial(t *testing.T) {
	body := []byte("small body")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			t.Errorf("resumed from a partial larger than the file: %q", r.Header.Get("Range"))
		}
		w.Write(body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "f.bin")
	os.WriteFile(dest+".part", []byte("this partial is far too long to be real"), 0o644)
	err := Fetcher{AllowHTTP: true}.Download(context.Background(), srv.URL+"/f", dest,
		hex.EncodeToString(sum[:]), int64(len(body)), "f")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(body) {
		t.Errorf("file: %q", got)
	}
}

func TestExtractTarGzDirectoriesAndModes(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	buf := &bytes.Buffer{}
	gz := gzip.NewWriter(buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "top/bin/", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "top/bin/tool", Typeflag: tar.TypeReg, Mode: 0o755, Size: 4})
	tw.Write([]byte("tool"))
	// An unusual entry type is skipped rather than failing the archive.
	tw.WriteHeader(&tar.Header{Name: "top/fifo", Typeflag: tar.TypeFifo, Mode: 0o644})
	tw.Close()
	gz.Close()
	os.WriteFile(archive, buf.Bytes(), 0o644)

	out := filepath.Join(dir, "out")
	if err := Extract(archive, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	st, err := os.Stat(filepath.Join(out, "bin", "tool"))
	if err != nil {
		t.Fatalf("extracted tool: %v", err)
	}
	if goruntime.GOOS != "windows" && st.Mode()&0o100 == 0 {
		t.Errorf("executable bit lost: %v", st.Mode())
	}
	if _, err := os.Stat(filepath.Join(out, "fifo")); err == nil {
		t.Error("an unsupported entry type was created")
	}
}

func TestOpenFailsWhenTheServerExits(t *testing.T) {
	dir := t.TempDir()
	// A "llama-server" that exits immediately must be reported, not waited on.
	script := "#!/bin/sh\necho boom >&2\nexit 1\n"
	if goruntime.GOOS == "windows" {
		t.Skip("the stand-in server is a shell script")
	}
	os.WriteFile(filepath.Join(dir, "llama-server"), []byte(script), 0o755)
	l := &Llama{Dir: dir, LogDir: t.TempDir(), Threads: 2, StartTimeout: 5 * time.Second}
	_, err := l.Open(context.Background(), &registry.Model{ID: "m", Context: 512}, map[string]string{"model": "m.gguf"})
	if err == nil || !strings.Contains(err.Error(), "exited while loading") {
		t.Errorf("server that exits: %v", err)
	}
}

// TestOpenDoesNotHangOnAStalledHealthCheck guards against a llama-server
// that accepts the health connection but never answers: Open must still
// respect StartTimeout instead of blocking inside healthy() forever.
func TestOpenDoesNotHangOnAStalledHealthCheck(t *testing.T) {
	dir := t.TempDir()
	fakellama.Install(t, dir)
	t.Setenv("MOTE_FAKE_LLAMA_HANG_HEALTH", "1")
	l := &Llama{Dir: dir, LogDir: t.TempDir(), StartTimeout: 300 * time.Millisecond}
	m, files := testModel(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := l.Open(ctx, m, files)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Open took %s to fail; a stalled health check is not bounded", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("expected a start-timeout error, got %v", err)
	}
}

func TestOpenFailsWhenThereIsNoBinary(t *testing.T) {
	l := &Llama{Dir: t.TempDir()}
	_, err := l.Open(context.Background(), &registry.Model{ID: "m", Context: 512}, map[string]string{"model": "m.gguf"})
	if err == nil || !strings.Contains(err.Error(), "start llama-server") {
		t.Errorf("missing binary: %v", err)
	}
}

func TestLoadArgsSkipWorkMoteDoesNotNeed(t *testing.T) {
	has := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	one := (&Llama{}).loadArgs()
	if !has(one, "--fit") || one[1] != "off" || !has(one, "--no-repack") {
		t.Errorf("one-shot: %q", one)
	}
	if kept := (&Llama{Repack: true}).loadArgs(); has(kept, "--no-repack") || !has(kept, "--fit") {
		t.Errorf("repacking: %q", kept)
	}
	for _, f := range []string{"--fit", "--no-repack"} {
		if !has(RequiredFlags, f) {
			t.Errorf("doctor does not check %s", f)
		}
	}
}

func TestCommonArgsChooseCPUOrGPU(t *testing.T) {
	has := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	_, files := testModel(t, t.TempDir())
	cpu := (&Llama{}).commonArgs(files)
	if !has(cpu, "--device") || has(cpu, "--gpu-layers") {
		t.Errorf("cpu: %q", cpu)
	}
	gpu := (&Llama{GPU: true}).commonArgs(files)
	if has(gpu, "--device") {
		t.Errorf("gpu: --device forced the runtime's own choice off: %q", gpu)
	}
	if !has(gpu, "--gpu-layers") {
		t.Errorf("gpu: no --gpu-layers: %q", gpu)
	}
	if !has(gpu, "--verbosity") || has(cpu, "--verbosity") {
		t.Errorf("only a GPU load logs where the weights went: cpu %q gpu %q", cpu, gpu)
	}
	for _, f := range []string{"--device", "--gpu-layers", "--verbosity"} {
		if !has(RequiredFlags, f) {
			t.Errorf("doctor does not check %s", f)
		}
	}
}

func TestOffloads(t *testing.T) {
	dir := t.TempDir()
	gpu := "load_tensors: offloaded 29/29 layers to GPU\nload_tensors:      Vulkan0 model buffer size =  1800.00 MiB\n"
	cpu := "load_tensors: offloaded 0/29 layers to GPU\nload_tensors:   CPU_Mapped model buffer size =  1800.00 MiB\n"
	os.WriteFile(filepath.Join(dir, "llama-server-a.log"), []byte(gpu), 0o644)
	os.WriteFile(filepath.Join(dir, "llama-server-b.log"), []byte(cpu), 0o644)
	got := map[string]Offload{}
	for _, o := range Offloads(dir) {
		got[o.Model] = o
	}
	if a := got["a"]; !a.OnGPU() || a.Layers != 29 || a.Device != "Vulkan0" {
		t.Errorf("a: %+v", a)
	}
	if b := got["b"]; b.OnGPU() || !b.Known {
		t.Errorf("b: %+v", b)
	}
	// Reworded prose must not matter: the buffer lines decide.
	os.WriteFile(filepath.Join(dir, "llama-server-c.log"), []byte("moved things to the GPU\n  MTL0 model buffer size = 1 MiB\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "llama-server-d.log"), []byte("nothing recognisable\n"), 0o644)
	got = map[string]Offload{}
	for _, o := range Offloads(dir) {
		got[o.Model] = o
	}
	if c := got["c"]; !c.OnGPU() || c.Device != "MTL0" {
		t.Errorf("c: %+v", c)
	}
	if d := got["d"]; d.Known || d.OnGPU() {
		t.Errorf("d: %+v", d)
	}
}
