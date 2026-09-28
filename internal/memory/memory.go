// Package memory gives mote three kinds of recall, all of them local files
// the user can read and delete:
//
//   - facts: short lines the user asked mote to remember, put in front of
//     every model step, like a project's notes;
//   - history: what was asked and answered, so a follow-up can continue;
//   - search: the same history retrieved by meaning with an embedding model,
//     for when the useful exchange was not the last one.
//
// Facts live in the config directory beside config.json because they are
// the user's own words. History lives in the data directory with the models
// and logs, because it is a record that can grow and be thrown away.
package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxLine bounds one stored exchange so a huge reply cannot make the
// history unreadable or slow to scan.
const maxLine = 8 << 10

// Entry is one remembered exchange.
type Entry struct {
	Time     time.Time `json:"time"`
	Task     string    `json:"task"`
	Request  string    `json:"request"`
	Response string    `json:"response"`
}

// Store reads and writes the memory files. The zero value is unusable; call
// New.
type Store struct {
	FactsPath   string // <config dir>/facts.txt
	HistoryPath string // <data dir>/history.jsonl
}

// New locates the memory files.
func New(configDir, dataDir string) Store {
	return Store{
		FactsPath:   filepath.Join(configDir, "facts.txt"),
		HistoryPath: filepath.Join(dataDir, "history.jsonl"),
	}
}

// Facts returns the remembered lines, oldest first.
func (s Store) Facts() ([]string, error) {
	b, err := os.ReadFile(s.FactsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// Remember adds one fact. Newlines are folded so a fact stays one line, and
// a repeat of something already remembered is ignored.
func (s Store) Remember(fact string) error {
	fact = strings.Join(strings.Fields(fact), " ")
	if fact == "" {
		return errors.New("nothing to remember")
	}
	facts, err := s.Facts()
	if err != nil {
		return err
	}
	for _, f := range facts {
		if strings.EqualFold(f, fact) {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.FactsPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.FactsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, fact)
	return err
}

// Forget removes the nth fact, counting from 1.
func (s Store) Forget(n int) error {
	facts, err := s.Facts()
	if err != nil {
		return err
	}
	if n < 1 || n > len(facts) {
		return fmt.Errorf("there is no fact %d (mote memory lists them)", n)
	}
	facts = append(facts[:n-1], facts[n:]...)
	if len(facts) == 0 {
		return s.ForgetAll()
	}
	return writeAtomic(s.FactsPath, []byte(strings.Join(facts, "\n")+"\n"))
}

// writeAtomic replaces path's content without ever leaving it truncated or
// half-written, unlike an open-truncate-write.
func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
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

// ForgetAll deletes every fact.
func (s Store) ForgetAll() error {
	err := os.Remove(s.FactsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Record appends one exchange to the history. Empty exchanges and file-only
// results are not worth remembering, so callers skip them.
func (s Store) Record(e Entry) error {
	if strings.TrimSpace(e.Request) == "" || strings.TrimSpace(e.Response) == "" {
		return nil
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Request, e.Response = clip(e.Request, maxLine/2), clip(e.Response, maxLine/2)
	if err := os.MkdirAll(filepath.Dir(s.HistoryPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.HistoryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// clip shortens text to n bytes on a rune boundary, marking the cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// History returns the most recent entries, oldest first. A zero or negative
// limit returns everything.
func (s Store) History(limit int) ([]Entry, error) {
	f, err := os.Open(s.HistoryPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine*2)
	for sc.Scan() {
		var e Entry
		// A line written by an older version, or half-written by a crash,
		// should not lose the rest of the history.
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// Clear deletes the history.
func (s Store) Clear() error {
	err := os.Remove(s.HistoryPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Embedder is the part of a model session that memory needs to search.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Hit is one search result.
type Hit struct {
	Entry
	Score float64
}

// Search returns the entries closest in meaning to the query. prefix is the
// embedding model's query instruction, if it has one, and similar is the
// comparison function (runtime.Cosine).
func (s Store) Search(ctx context.Context, em Embedder, query, prefix string, k int, similar func(a, b []float32) float64) ([]Hit, error) {
	entries, err := s.History(0)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	texts := make([]string, 0, len(entries)+1)
	texts = append(texts, prefix+query)
	for _, e := range entries {
		texts = append(texts, e.Request+"\n"+e.Response)
	}
	vecs, err := em.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(entries))
	for i, e := range entries {
		hits = append(hits, Hit{Entry: e, Score: similar(vecs[0], vecs[i+1])})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// Prompt builds the text put in front of a model step. Facts come first
// because they are always true; recalled exchanges follow as context, each
// labelled so the model does not read them as the current request.
func Prompt(facts []string, recalled []Entry) string {
	var b strings.Builder
	if len(facts) > 0 {
		b.WriteString("Remember these facts about this user and their work:\n")
		for _, f := range facts {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	for _, e := range recalled {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "Earlier, asked: %s\nYou answered: %s\n", e.Request, e.Response)
	}
	return strings.TrimSpace(b.String())
}
