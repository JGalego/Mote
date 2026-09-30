package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jgalego/mote/internal/atomicfile"
	"github.com/jgalego/mote/internal/motebook"
)

//go:embed static
var static embed.FS

// Runner runs the cells of a notebook. The server holds the notebook and the
// runner knows what a cell means, so this package does not depend on what
// tasks there are.
type Runner interface {
	// Check says whether a cell can run, and if it can, whether running it
	// can change things, which the page asks about first.
	Check(expr string) (changes bool, err error)
	// Run runs one cell. values are what the cells before it bound, and
	// progress is told what is going on ("loading a model", "thinking").
	// files is set when what came back is the paths of files, which may not be
	// there the next time.
	Run(ctx context.Context, expr string, values map[string]string, progress func(string)) (text string, files bool, err error)
}

// Options is what a server needs.
type Options struct {
	Path   string // the notebook file; it need not exist yet
	Root   string // the folder whose images, audio and video may be served; the notebook's by default
	Runner Runner
	// Approve runs cells that can change things without the page asking.
	Approve bool
	// Token is the secret in the URL that opens a session; one is made if empty.
	Token string
}

// Server serves one notebook.
type Server struct {
	opts   Options
	dir    string // the notebook's folder
	root   *os.Root
	rootAt string
	md     *Renderer
	token  string
	events *broker

	mu    sync.Mutex // guards what follows
	book  *motebook.Book
	rev   int
	known bool // the file has been seen, and mod and size are its
	mod   time.Time
	size  int64

	running sync.Mutex // one cell runs at a time
}

// New opens the notebook, or starts an empty one if the file is not there.
func New(o Options) (*Server, error) {
	abs, err := filepath.Abs(o.Path)
	if err != nil {
		return nil, err
	}
	o.Path = abs
	dir := filepath.Dir(abs)
	rootAt := o.Root
	if rootAt == "" {
		rootAt = dir
	}
	if rootAt, err = filepath.Abs(rootAt); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootAt)
	if err != nil {
		return nil, fmt.Errorf("the folder to serve files from: %w", err)
	}
	token := o.Token
	if token == "" {
		if token, err = randomToken(); err != nil {
			return nil, err
		}
	}
	s := &Server{opts: o, dir: dir, root: root, rootAt: rootAt, md: NewRenderer(), token: token, events: newBroker()}
	if err := s.syncLocked(); err != nil {
		root.Close()
		return nil, err
	}
	return s, nil
}

// Close releases what the server holds.
func (s *Server) Close() error { return s.root.Close() }

// Token is the secret that opens a session.
func (s *Server) Token() string { return s.token }

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// syncLocked reads the notebook from disk if it is not the one the server
// last saw, so an edit made in an editor is not overwritten by the page. It
// needs s.mu, or to be called before the server is shared.
func (s *Server) syncLocked() error {
	fi, err := os.Stat(s.opts.Path)
	if errors.Is(err, fs.ErrNotExist) {
		if s.book == nil || s.known {
			s.book, _ = motebook.Parse("")
			s.known = false
			s.rev++
		}
		return nil
	}
	if err != nil {
		return err
	}
	if s.known && fi.ModTime().Equal(s.mod) && fi.Size() == s.size {
		return nil
	}
	data, err := os.ReadFile(s.opts.Path)
	if err != nil {
		return err
	}
	book, err := motebook.Parse(string(data))
	if err != nil {
		if s.book == nil {
			return fmt.Errorf("%s: %w", s.opts.Path, err)
		}
		return fmt.Errorf("the notebook on disk cannot be read: %w", err)
	}
	s.book, s.known, s.mod, s.size = book, true, fi.ModTime(), fi.Size()
	s.rev++
	return nil
}

// saveLocked writes the notebook and makes it the current one.
func (s *Server) saveLocked(book *motebook.Book) error {
	if err := atomicfile.Write(s.opts.Path, []byte(book.String()), 0o644); err != nil {
		return err
	}
	fi, err := os.Stat(s.opts.Path)
	if err != nil {
		return err
	}
	s.book, s.known, s.mod, s.size = book, true, fi.ModTime(), fi.Size()
	s.rev++
	return nil
}

// Handler serves the page, its API and the files it shows.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /app.js", s.guard(s.asset("static/app.js", "text/javascript; charset=utf-8")))
	mux.Handle("GET /app.css", s.guard(s.asset("static/app.css", "text/css; charset=utf-8")))
	mux.Handle("GET /api/notebook", s.guard(http.HandlerFunc(s.getNotebook)))
	mux.Handle("POST /api/edit", s.guard(http.HandlerFunc(s.edit)))
	mux.Handle("POST /api/run", s.guard(http.HandlerFunc(s.run)))
	mux.Handle("GET /api/events", s.guard(http.HandlerFunc(s.serveEvents)))
	mux.Handle("GET "+filePath, s.guard(http.HandlerFunc(s.file)))
	return s.headers(mux)
}

const cookieName = "mote_session"

// headers is on every response: nothing here needs to be framed, sniffed or
// to run script that did not come from this server.
func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' https: data:; media-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// allowedHost keeps a page on another site from reaching a server on this
// machine by pointing its own name at 127.0.0.1.
func allowedHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	switch strings.Trim(host, "[]") {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// authorised reports whether a request carries the session cookie.
func (s *Server) authorised(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1
}

// guard lets through only requests for this machine, from this page, in a
// session that was opened with the token.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if !s.authorised(r) {
			http.Error(w, "open the address mote printed, which carries the token", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			// A form on another site can post here; a script that adds a
			// header of its own cannot without asking first.
			if r.Header.Get("X-Mote") != "1" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// index opens a session when it is given the token, and serves the page to one
// that is open.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if !allowedHost(r.Host) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	if t := r.URL.Query().Get("token"); t != "" {
		if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) != 1 {
			http.Error(w, "wrong token", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		// Off the address bar, so the token is not left in history or shared
		// by copying the URL.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.authorised(r) {
		http.Error(w, "open the address mote printed, which carries the token", http.StatusForbidden)
		return
	}
	s.asset("static/index.html", "text/html; charset=utf-8").ServeHTTP(w, r)
}

func (s *Server) asset(name, contentType string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := static.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Write(b)
	})
}

// file serves an image, audio or video that a notebook shows. The path is
// the notebook's own way of writing it, relative to the notebook, and it must
// be inside the folder the server was given; only these kinds of file are
// handed out, since the folder may be one with more in it.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.underRoot(r.URL.Query().Get("p"))
	if !ok || motebook.Kind(rel) == "" {
		http.NotFound(w, r)
		return
	}
	f, err := s.root.Open(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(rel))); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// An SVG opened on its own is a page: it must not run script as this one.
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	http.ServeContent(w, r, filepath.Base(rel), fi.ModTime(), f)
}

// underRoot turns a path written relative to the notebook into one relative
// to the served folder, and says whether it is inside it.
func (s *Server) underRoot(p string) (string, bool) {
	if p == "" || strings.ContainsRune(p, 0) || filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return "", false
	}
	rel, err := filepath.Rel(s.rootAt, filepath.Join(s.dir, filepath.FromSlash(p)))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// relToNotebook writes a path the way a notebook would refer to it, if the
// server can serve it, else "". path is as a cell printed it, so relative to
// where mote was run, or absolute.
func (s *Server) relToNotebook(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(s.dir, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if _, ok := s.underRoot(rel); !ok {
		return "", false
	}
	return rel, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, format string, a ...any) {
	writeJSON(w, status, map[string]any{"error": fmt.Sprintf(format, a...)})
}
