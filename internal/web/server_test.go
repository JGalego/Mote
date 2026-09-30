package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/jgalego/mote/internal/motebook"
)

// fakeRunner stands in for the tasks: a cell "bad ..." does not exist, a cell
// in changes can change things, and any other cell prints "echo: " and itself
// with the values it reads filled in.
type fakeRunner struct {
	mu      sync.Mutex
	ran     []string
	changes map[string]bool
	fail    map[string]string
	files   map[string]bool
	hook    func(ctx context.Context, expr string) // runs inside Run, for a test to interfere
}

func (f *fakeRunner) Check(expr string) (bool, error) {
	if strings.HasPrefix(expr, "bad") {
		return false, &checkError{`unknown task "bad"`}
	}
	return f.changes[expr], nil
}

type checkError struct{ msg string }

func (e *checkError) Error() string { return e.msg }

func (f *fakeRunner) Run(ctx context.Context, expr string, values map[string]string, progress func(string)) (string, bool, error) {
	f.mu.Lock()
	f.ran = append(f.ran, expr)
	hook := f.hook
	f.mu.Unlock()
	progress("thinking")
	if hook != nil {
		hook(ctx, expr)
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if msg := f.fail[expr]; msg != "" {
		return "", false, &checkError{msg}
	}
	text := "echo: " + motebook.Substitute(expr, func(name string) (string, bool) { v, ok := values[name]; return v, ok })
	return text, f.files[expr], nil
}

func (f *fakeRunner) runs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ran...)
}

// site is a running server, and a client that has opened a session on it.
type site struct {
	t      *testing.T
	s      *Server
	ts     *httptest.Server
	dir    string
	path   string
	runner *fakeRunner
	client *http.Client
}

func newSite(t *testing.T, notebook string, mutate ...func(*Options)) *site {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "book.mote.md")
	if notebook != "" {
		if err := os.WriteFile(path, []byte(notebook), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeRunner{changes: map[string]bool{}, fail: map[string]string{}, files: map[string]bool{}}
	o := Options{Path: path, Runner: runner}
	for _, m := range mutate {
		m(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	site := &site{t: t, s: s, ts: ts, dir: dir, path: path, runner: runner}
	site.client = site.open()
	return site
}

// open returns a client that has been given a session, the way a browser is
// by the address mote prints.
func (st *site) open() *http.Client {
	jar := &cookieJar{}
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(st.ts.URL + "/?token=" + st.s.Token())
	if err != nil {
		st.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		st.t.Fatalf("opening a session: %d", resp.StatusCode)
	}
	return c
}

// cookieJar keeps the cookies of the one host it talks to.
type cookieJar struct{ cookies []*http.Cookie }

func (j *cookieJar) SetCookies(_ *urlURL, cs []*http.Cookie) { j.cookies = append(j.cookies, cs...) }
func (j *cookieJar) Cookies(*urlURL) []*http.Cookie          { return j.cookies }

func (st *site) get(path string) (*http.Response, []byte) {
	st.t.Helper()
	resp, err := st.client.Get(st.ts.URL + path)
	if err != nil {
		st.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (st *site) post(path string, body any) (*http.Response, []byte) {
	st.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", st.ts.URL+path, bytes.NewReader(b))
	req.Header.Set("X-Mote", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := st.client.Do(req)
	if err != nil {
		st.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func TestSessionNeedsTheToken(t *testing.T) {
	st := newSite(t, "")
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/", "/api/notebook", "/app.js", "/highlight.js", "/app.css", "/api/events", "/file?p=a.png"} {
		resp, err := anon.Get(st.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s without a session: %d, want 403", path, resp.StatusCode)
		}
	}
	// The wrong token opens nothing.
	resp, _ := anon.Get(st.ts.URL + "/?token=nope")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 {
		t.Errorf("wrong token: %d, %v", resp.StatusCode, resp.Cookies())
	}
	// The right one sets a cookie script cannot read and other sites cannot
	// send, and takes itself off the address bar.
	resp, _ = anon.Get(st.ts.URL + "/?token=" + st.s.Token())
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("right token: %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	cs := resp.Cookies()
	if len(cs) != 1 || !cs[0].HttpOnly || cs[0].SameSite != http.SameSiteStrictMode || cs[0].Value != st.s.Token() {
		t.Errorf("session cookie: %+v", cs)
	}
	// With the session, the page, its scripts and the notebook open.
	for _, asset := range []string{"/app.js", "/highlight.js", "/app.css"} {
		if resp, body := st.get(asset); resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("%s: %d, %d bytes", asset, resp.StatusCode, len(body))
		}
	}
	if resp, body := st.get("/"); resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") || len(body) == 0 {
		t.Errorf("index: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := st.get("/api/notebook"); resp.StatusCode != http.StatusOK {
		t.Errorf("notebook: %d", resp.StatusCode)
	}
}

func TestTokensAreRandomAndCanBeChosen(t *testing.T) {
	a, b := newSite(t, ""), newSite(t, "")
	if a.s.Token() == b.s.Token() || len(a.s.Token()) != 32 {
		t.Errorf("tokens %q %q", a.s.Token(), b.s.Token())
	}
	c := newSite(t, "", func(o *Options) { o.Token = "chosen" })
	if c.s.Token() != "chosen" {
		t.Errorf("token %q", c.s.Token())
	}
}

func TestOnlyThisMachineIsServed(t *testing.T) {
	st := newSite(t, "")
	for host, want := range map[string]int{
		"127.0.0.1:8080": http.StatusOK, "localhost:8080": http.StatusOK, "[::1]:8080": http.StatusOK, "localhost": http.StatusOK,
		"evil.example": http.StatusForbidden, "evil.example:8080": http.StatusForbidden, "127.0.0.1.evil.example": http.StatusForbidden,
		"192.168.1.5:8080": http.StatusForbidden,
	} {
		req, _ := http.NewRequest("GET", st.ts.URL+"/api/notebook", nil)
		req.Host = host
		resp, err := st.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %q: %d, want %d", host, resp.StatusCode, want)
		}
	}
	// Nor is a session opened for a name that is not this machine's.
	req, _ := http.NewRequest("GET", st.ts.URL+"/?token="+st.s.Token(), nil)
	req.Host = "evil.example"
	resp, _ := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("token given to another host: %d", resp.StatusCode)
	}
}

func TestPostsMustComeFromThePage(t *testing.T) {
	st := newSite(t, "")
	body := []byte(`{"rev":1,"op":"insert","index":0,"type":"prose","markdown":"x"}`)
	do := func(mutate func(*http.Request)) int {
		req, _ := http.NewRequest("POST", st.ts.URL+"/api/edit", bytes.NewReader(body))
		mutate(req)
		resp, err := st.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := do(func(r *http.Request) {}); got != http.StatusForbidden {
		t.Errorf("no X-Mote header (a form on another site): %d", got)
	}
	if got := do(func(r *http.Request) { r.Header.Set("X-Mote", "1"); r.Header.Set("Origin", "http://evil.example") }); got != http.StatusForbidden {
		t.Errorf("a foreign Origin: %d", got)
	}
	if got := do(func(r *http.Request) { r.Header.Set("X-Mote", "1"); r.Header.Set("Origin", st.ts.URL) }); got != http.StatusOK {
		t.Errorf("the page itself: %d", got)
	}
}

func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	st := newSite(t, "")
	for _, path := range []string{"/", "/api/notebook", "/app.js"} {
		resp, _ := st.get(path)
		h := resp.Header
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP lacks %q: %s", path, want, csp)
			}
		}
		if strings.Contains(csp, "unsafe-inline") && strings.Contains(csp, "script-src 'self' 'unsafe") {
			t.Errorf("%s: script may run inline: %s", path, csp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" ||
			h.Get("Referrer-Policy") != "no-referrer" || h.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", path, h)
		}
	}
	// Unknown paths are unknown, not the page.
	if resp, _ := st.get("/nothing"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/nothing: %d", resp.StatusCode)
	}
}

// files makes a folder with media in it, and a folder beside it with more.
func files(t *testing.T, st *site) (outside string) {
	t.Helper()
	png := []byte("\x89PNG\r\n\x1a\nxxxxxxxx")
	write := func(rel string, data []byte) {
		p := filepath.Join(st.dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pic.png", png)
	write("sub/clip.mp4", bytes.Repeat([]byte("v"), 1000))
	write("notes.txt", []byte("private"))
	write("shape.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	outside = filepath.Join(filepath.Dir(st.dir), filepath.Base(st.dir)+"-outside")
	os.MkdirAll(outside, 0o755)
	t.Cleanup(func() { os.RemoveAll(outside) })
	os.WriteFile(filepath.Join(outside, "secret.png"), png, 0o644)
	return outside
}

func TestFilesAreServedFromTheNotebooksFolderOnly(t *testing.T) {
	st := newSite(t, "")
	outside := files(t, st)
	get := func(p string) *http.Response {
		resp, _ := st.get(filePath + "?p=" + urlEscape(p))
		return resp
	}

	if resp := get("pic.png"); resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("pic.png: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp := get("sub/clip.mp4"); resp.StatusCode != http.StatusOK {
		t.Errorf("sub/clip.mp4: %d", resp.StatusCode)
	}
	if resp := get("sub/../pic.png"); resp.StatusCode != http.StatusOK {
		t.Errorf("a path that goes down and up: %d", resp.StatusCode)
	}
	for _, p := range []string{
		"notes.txt", // not media
		"../" + filepath.Base(outside) + "/secret.png", // beside the folder, not in it
		"../../etc/passwd",
		"/etc/passwd",
		"\\windows\\win.ini",
		"gone.png",
		"sub", // a folder
		"",
		"pic.png\x00.txt",
	} {
		if resp := get(p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%q: %d, want 404", p, resp.StatusCode)
		}
	}
	// A file can be asked for a piece at a time, which a video needs to seek.
	req, _ := http.NewRequest("GET", st.ts.URL+filePath+"?p=sub%2Fclip.mp4", nil)
	req.Header.Set("Range", "bytes=10-19")
	resp, err := st.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if body, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusPartialContent || len(body) != 10 {
		t.Errorf("range: %d, %d bytes", resp.StatusCode, len(body))
	}
	// A picture that is a page must not run script as this one.
	resp = get("shape.svg")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("svg: %d, CSP %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
}

func TestFilesCannotEscapeThroughALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need privileges on Windows")
	}
	st := newSite(t, "")
	outside := files(t, st)
	if err := os.Symlink(outside, filepath.Join(st.dir, "link")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(st.dir, "direct.png")); err != nil {
		t.Skip(err)
	}
	for _, p := range []string{"link/secret.png", "direct.png"} {
		if resp, _ := st.get(filePath + "?p=" + urlEscape(p)); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d, want 404: a link led out of the folder", p, resp.StatusCode)
		}
	}
}

func TestTheServedFolderCanBeWidened(t *testing.T) {
	var outside string
	dir := t.TempDir()
	outside = filepath.Join(dir, "media")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "x.png"), []byte("\x89PNG"), 0o644)
	os.MkdirAll(filepath.Join(dir, "nb"), 0o755)
	path := filepath.Join(dir, "nb", "book.mote.md")
	s, err := New(Options{Path: path, Root: dir, Runner: &fakeRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	defer s.Close()
	c := &http.Client{Jar: &cookieJar{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := c.Get(ts.URL + "/?token=" + s.Token())
	r.Body.Close()
	// The notebook is in nb/ and refers to a sibling with ../, which the
	// wider folder allows; something above the folder it still does not.
	for p, want := range map[string]int{"../media/x.png": 200, "../../x.png": 404} {
		resp, err := c.Get(ts.URL + filePath + "?p=" + urlEscape(p))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", p, resp.StatusCode, want)
		}
	}
	if _, err := New(Options{Path: path, Root: filepath.Join(dir, "missing"), Runner: &fakeRunner{}}); err == nil {
		t.Error("a folder that is not there was accepted")
	}
}

func TestSessionsOnTwoPortsDoNotShareACookie(t *testing.T) {
	a, b := newSite(t, ""), newSite(t, "")
	// One browser, both notebooks open: one cookie jar.
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	for _, st := range []*site{a, b} {
		resp, err := c.Get(st.ts.URL + "/?token=" + st.s.Token())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	for name, st := range map[string]*site{"first": a, "second": b} {
		resp, err := c.Get(st.ts.URL + "/api/notebook")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("the %s notebook: %d; opening the other logged it out", name, resp.StatusCode)
		}
	}
	// And the cookie is named for the port it was given on.
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(a.ts.URL, "http://"))
	u, _ := url.Parse(a.ts.URL)
	found := false
	for _, ck := range jar.Cookies(u) {
		found = found || ck.Name == "mote_session_"+port
	}
	if !found {
		t.Errorf("no cookie named for port %s: %v", port, jar.Cookies(u))
	}
}

func TestThePageLoadsNoRemoteImages(t *testing.T) {
	st := newSite(t, "")
	resp, _ := st.get("/")
	if csp := resp.Header.Get("Content-Security-Policy"); strings.Contains(csp, "https:") {
		t.Errorf("a notebook could have the page fetch from anywhere, which tells a server it was opened: %s", csp)
	}
}
