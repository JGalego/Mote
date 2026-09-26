package runtime

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Fetcher downloads files over HTTPS and verifies their SHA-256. Partial
// downloads are resumed. Nothing downloaded is ever executed by the fetcher.
type Fetcher struct {
	Client   *http.Client
	Progress Progress // optional
	// AllowHTTP permits plain HTTP to loopback addresses (tests only).
	AllowHTTP bool
	// StallTimeout aborts a download that receives no data for this long
	// (default one minute).
	StallTimeout time.Duration
}

// Progress receives download progress; the CLI renders it as a bar.
type Progress interface {
	Start(label string, have, total int64) Tracker
}

// Tracker follows one download.
type Tracker interface {
	Set(done int64)
	Finish(err error)
}

func (f Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 0}
}

// Download fetches url into dest, verifying size and sha256. If dest already
// exists with the right size it is left alone (callers verify on demand).
func (f Fetcher) Download(ctx context.Context, rawURL, dest, sum string, size int64, label string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "https" && !(f.AllowHTTP && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return fmt.Errorf("refusing non-HTTPS download: %s", rawURL)
	}
	if st, err := os.Stat(dest); err == nil && st.Size() == size {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"
	var have int64
	if st, err := os.Stat(part); err == nil && st.Size() < size {
		have = st.Size()
	} else {
		os.Remove(part)
	}

	// Abort if no bytes arrive for StallTimeout; the partial file is kept so
	// the next attempt resumes.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := f.StallTimeout
	if stall == 0 {
		stall = 60 * time.Second
	}
	watchdog := time.AfterFunc(stall, cancel)
	defer watchdog.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "mote")
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", label, err)
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		have = 0
		flags |= os.O_TRUNC
	default:
		return fmt.Errorf("download %s: %s", label, resp.Status)
	}
	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	pw := &progress{done: have, alive: func() { watchdog.Reset(stall) }}
	if f.Progress != nil {
		pw.t = f.Progress.Start(label, have, size)
	}
	_, err = io.Copy(out, io.TeeReader(io.LimitReader(resp.Body, size-have+1), pw))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = VerifyFile(part, sum, size)
		if err != nil {
			os.Remove(part)
		}
	}
	if pw.t != nil {
		pw.t.Finish(err)
	}
	if err != nil {
		if _, serr := os.Stat(part); serr == nil {
			return fmt.Errorf("download %s: %w (partial file kept for resume)", label, err)
		}
		return fmt.Errorf("download %s: %w", label, err)
	}
	return os.Rename(part, dest)
}

// VerifyFile checks a file's size and SHA-256.
func VerifyFile(p, sum string, size int64) error {
	fh, err := os.Open(p)
	if err != nil {
		return err
	}
	defer fh.Close()
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("size mismatch for %s: got %d bytes, want %d", filepath.Base(p), n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", filepath.Base(p), got, sum)
	}
	return nil
}

type progress struct {
	t     Tracker
	done  int64
	alive func()
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	p.alive()
	if p.t != nil {
		p.t.Set(p.done)
	}
	return len(b), nil
}

// Extract unpacks a .tar.gz or .zip archive into dest. A single top-level
// directory shared by all entries is stripped. Entries that would escape dest
// (absolute paths, "..", symlinks pointing outside) are rejected.
func Extract(archive, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(archive, ".tar.gz"), strings.HasSuffix(archive, ".tgz"):
		return extractTarGz(archive, dest)
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, dest)
	}
	return fmt.Errorf("unsupported archive %s", filepath.Base(archive))
}

// safeJoin maps an archive entry name to a path under dest.
func safeJoin(dest, name, strip string) (string, bool, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if strip != "" {
		if strings.TrimSuffix(name, "/") == strip {
			return "", true, nil
		}
		name = strings.TrimPrefix(name, strip+"/")
	}
	unsafe := strings.HasPrefix(name, "/") || len(name) > 1 && name[1] == ':'
	for _, part := range strings.Split(name, "/") {
		unsafe = unsafe || part == ".."
	}
	if unsafe {
		return "", false, fmt.Errorf("unsafe path in archive: %q", name)
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", true, nil
	}
	return filepath.Join(dest, filepath.FromSlash(clean)), false, nil
}

func commonTop(names []string) string {
	top := ""
	for _, n := range names {
		n = strings.ReplaceAll(n, "\\", "/")
		first, rest, _ := strings.Cut(n, "/")
		if rest == "" && !strings.HasSuffix(n, "/") {
			return "" // a file at the root
		}
		if first == ".." || first == "." || first == "" {
			return ""
		}
		if top == "" {
			top = first
		} else if first != top {
			return ""
		}
	}
	return top
}

func extractTarGz(archive, dest string) error {
	names, err := tarNames(archive)
	if err != nil {
		return err
	}
	strip := commonTop(names)
	fh, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, skip, err := safeJoin(dest, h.Name, strip)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(target, tr, os.FileMode(h.Mode)&0o755|0o600); err != nil {
				return err
			}
		case tar.TypeSymlink:
			link := h.Linkname
			resolved := filepath.Join(filepath.Dir(target), filepath.FromSlash(link))
			if filepath.IsAbs(link) || !strings.HasPrefix(resolved, filepath.Clean(dest)+string(os.PathSeparator)) {
				return fmt.Errorf("unsafe symlink in archive: %s -> %s", h.Name, link)
			}
			os.Remove(target)
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		}
	}
}

func tarNames(archive string) ([]string, error) {
	fh, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		names = append(names, h.Name)
	}
}

func extractZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	strip := commonTop(names)
	for _, f := range zr.File {
		target, skip, err := safeJoin(dest, f.Name, strip)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not supported in zip archives: %s", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(target, rc, f.Mode().Perm()|0o600)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(target string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
