// Package index keeps an embedding index of the user's own files, so a
// question can be answered from the passages closest to it in meaning. The
// index is one JSON file in the data directory; refreshing it re-reads only
// the files whose size or modification time changed.
package index

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jgalego/mote/internal/task"
)

// ChunkSize is the largest passage embedded, in bytes: about 200 tokens of
// prose and under 400 of dense code, inside the 512-token batch of small
// embedding models.
const ChunkSize = 800

// maxFile skips files too large to be anyone's notes.
const maxFile = 32 << 20

// batch is how many passages go to the embedding model per request.
const batch = 32

// skipDirs are directories never indexed when walking without git.
var skipDirs = map[string]bool{"node_modules": true, "vendor": true, "target": true, "dist": true, "build": true, "__pycache__": true, "venv": true}

// skipExt are files that hold no text worth reading.
var skipExt = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`.png .jpg .jpeg .gif .webp .bmp .ico .tif .tiff .heic .svgz
		.mp3 .wav .m4a .flac .ogg .opus .aac .mp4 .mkv .mov .avi .webm
		.zip .gz .tgz .bz2 .xz .7z .rar .tar .zst .jar .whl
		.exe .dll .so .dylib .a .o .bin .class .pyc .wasm
		.gguf .safetensors .pt .onnx .npy .sqlite .db .ttf .otf .woff .woff2 .lock`) {
		skipExt[e] = true
	}
}

// Embedder turns texts into vectors.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Index is the stored index.
type Index struct {
	// Model is the embedding model the vectors came from; vectors from
	// another model cannot be compared, so a change re-embeds everything.
	Model   string          `json:"model"`
	Roots   []string        `json:"roots"`
	Files   map[string]File `json:"files"`
	Chunks  []Chunk         `json:"chunks"`
	Updated time.Time       `json:"updated"`
	path    string
}

// File is what the index knows about one file.
type File struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	Chunks  int       `json:"chunks"`
	// Skipped says why the file has no passages, if it could not be read.
	Skipped string `json:"skipped,omitempty"`
}

// Chunk is one passage of a file.
type Chunk struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
	Vec  vector `json:"vec"`
}

// vector is stored as base64 little-endian float32s, a third the size of
// the same numbers written out in JSON.
type vector []float32

func (v vector) MarshalJSON() ([]byte, error) {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return json.Marshal(base64.StdEncoding.EncodeToString(b))
}

func (v *vector) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%4 != 0 {
		return errors.New("bad vector")
	}
	out := make(vector, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	*v = out
	return nil
}

// Path is where the index of a data directory lives.
func Path(dataDir string) string { return filepath.Join(dataDir, "index", "files.json") }

// Load reads the index at p; a missing file is an empty index.
func Load(p string) (*Index, error) {
	ix := &Index{Files: map[string]File{}, path: p}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return ix, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, ix); err != nil {
		return nil, fmt.Errorf("%s: %w; `mote index rm --all` starts over", p, err)
	}
	if ix.Files == nil {
		ix.Files = map[string]File{}
	}
	ix.path = p
	return ix, nil
}

// Save writes the index atomically.
func (ix *Index) Save() error {
	if err := os.MkdirAll(filepath.Dir(ix.path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	tmp := ix.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ix.path)
}

// Options connect an update to the file reader and the embedding model.
type Options struct {
	Model string
	Embed Embedder
	// Read returns a file's text, or an error saying why it has none.
	Read func(path string) (string, error)
	// Git, when set, lists the files of a work tree without its ignored
	// ones.
	Git string
	// Progress, when set, is told about each file read.
	Progress func(done, total int, path string)
}

// Stats says what an update did.
type Stats struct {
	Files, Added, Changed, Removed, Skipped, Chunks int
}

// Update indexes roots, adding them to the index, re-reading only files
// whose size or modification time changed and dropping files that are gone.
func (ix *Index) Update(ctx context.Context, roots []string, opt Options) (Stats, error) {
	var st Stats
	if ix.Model != opt.Model {
		ix.Files, ix.Chunks, ix.Model = map[string]File{}, nil, opt.Model
	}
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return st, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return st, err
		}
		if !info.IsDir() {
			return st, fmt.Errorf("%s is not a directory", root)
		}
		ix.addRoot(abs)
		files := list(abs, opt.Git)
		present := map[string]bool{}
		for i, p := range files {
			if err := ctx.Err(); err != nil {
				return st, err
			}
			present[p] = true
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			st.Files++
			old, known := ix.Files[p]
			if known && old.Size == fi.Size() && old.ModTime.Equal(fi.ModTime()) {
				continue
			}
			if opt.Progress != nil {
				opt.Progress(i+1, len(files), p)
			}
			f := File{Size: fi.Size(), ModTime: fi.ModTime()}
			var chunks []Chunk
			if fi.Size() > maxFile {
				f.Skipped = "larger than 32 MiB"
			} else if text, err := opt.Read(p); err != nil {
				f.Skipped = err.Error()
			} else if chunks, err = embedFile(ctx, opt, abs, p, text); err != nil {
				// A passage the model will not take costs this file, not
				// the whole index; anything else, like a model that
				// stopped answering, stops the update.
				if !strings.Contains(err.Error(), "too large") {
					return st, fmt.Errorf("%s: %w", p, err)
				}
				chunks, f.Skipped = nil, "a passage is too long for the embedding model"
			}
			if f.Skipped != "" {
				st.Skipped++
			} else if known {
				st.Changed++
			} else {
				st.Added++
			}
			f.Chunks = len(chunks)
			ix.drop(p)
			ix.Files[p] = f
			ix.Chunks = append(ix.Chunks, chunks...)
		}
		for p := range ix.Files {
			if under(p, abs) && !present[p] {
				ix.drop(p)
				delete(ix.Files, p)
				st.Removed++
			}
		}
	}
	ix.Updated = time.Now().UTC()
	st.Chunks = len(ix.Chunks)
	return st, nil
}

// embedFile splits a file's text into passages and embeds them, each with
// the file's name in front, which is often what a question mentions.
func embedFile(ctx context.Context, opt Options, root, p, text string) ([]Chunk, error) {
	pieces := task.Chunk(text, ChunkSize)
	rel, _ := filepath.Rel(root, p)
	var chunks []Chunk
	// Pieces are consecutive parts of the text, so each is found after the
	// end of the one before, and its line counted from there.
	cursor, line := 0, 1
	for _, piece := range pieces {
		if strings.TrimSpace(piece) == "" {
			continue
		}
		start := line
		if i := strings.Index(text[cursor:], piece); i >= 0 {
			start = line + strings.Count(text[cursor:cursor+i], "\n")
			line = start + strings.Count(piece, "\n")
			cursor += i + len(piece)
		}
		chunks = append(chunks, Chunk{Path: p, Line: start, Text: piece})
	}
	for start := 0; start < len(chunks); start += batch {
		end := min(start+batch, len(chunks))
		texts := make([]string, 0, end-start)
		for _, c := range chunks[start:end] {
			texts = append(texts, filepath.ToSlash(rel)+"\n"+c.Text)
		}
		vecs, err := opt.Embed.Embed(ctx, texts)
		if err != nil {
			return nil, err
		}
		for i, v := range vecs {
			chunks[start+i].Vec = v
		}
	}
	return chunks, nil
}

func (ix *Index) addRoot(abs string) {
	for _, r := range ix.Roots {
		if r == abs {
			return
		}
	}
	ix.Roots = append(ix.Roots, abs)
	sort.Strings(ix.Roots)
}

// drop removes a file's passages.
func (ix *Index) drop(p string) {
	kept := ix.Chunks[:0]
	for _, c := range ix.Chunks {
		if c.Path != p {
			kept = append(kept, c)
		}
	}
	ix.Chunks = kept
}

// Remove forgets a root and everything under it, returning how many files
// that was.
func (ix *Index) Remove(root string) (int, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	found := false
	var roots []string
	for _, r := range ix.Roots {
		if r == abs {
			found = true
			continue
		}
		roots = append(roots, r)
	}
	if !found {
		return 0, fmt.Errorf("%s is not indexed; `mote index status` lists what is", root)
	}
	ix.Roots = roots
	n := 0
	for p := range ix.Files {
		// Files under another indexed root stay.
		if under(p, abs) && !ix.covered(p) {
			ix.drop(p)
			delete(ix.Files, p)
			n++
		}
	}
	return n, nil
}

func (ix *Index) covered(p string) bool {
	for _, r := range ix.Roots {
		if under(p, r) {
			return true
		}
	}
	return false
}

func under(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Hit is a passage and how close it is to the query.
type Hit struct {
	Chunk
	Score float64
}

// Search returns the k passages closest to the query vector, only from
// files under within when it is set.
func (ix *Index) Search(q []float32, k int, within string, similar func(a, b []float32) float64) []Hit {
	var hits []Hit
	for _, c := range ix.Chunks {
		if within != "" && !under(c.Path, within) {
			continue
		}
		hits = append(hits, Hit{Chunk: c, Score: similar(q, c.Vec)})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// list returns the files under root worth reading: in a git work tree the
// tracked and untracked ones git does not ignore, else every file outside
// hidden and dependency directories.
func list(root, git string) []string {
	var files []string
	if git != "" {
		if out, err := exec.Command(git, "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output(); err == nil {
			for _, f := range strings.Split(string(out), "\x00") {
				if f != "" && !skipExt[strings.ToLower(filepath.Ext(f))] {
					files = append(files, filepath.Join(root, filepath.FromSlash(f)))
				}
			}
			sort.Strings(files)
			return files
		}
	}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(name, ".") && !skipExt[strings.ToLower(filepath.Ext(name))] {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files
}
