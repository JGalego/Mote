package index

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// words embeds texts as bags of words, so texts sharing words are close.
type words struct{ calls, texts int }

func (w *words) Embed(_ context.Context, texts []string) ([][]float32, error) {
	w.calls++
	w.texts += len(texts)
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 64)
		for _, word := range strings.Fields(strings.ToLower(t)) {
			h := 0
			for _, c := range strings.Trim(word, ".,?") {
				h = (h*31 + int(c)) % len(v)
			}
			v[h]++
		}
		out[i] = v
	}
	return out, nil
}

func cosine(a, b []float32) float64 {
	var d, na, nb float64
	for i := range a {
		d += float64(a[i] * b[i])
		na += float64(a[i] * a[i])
		nb += float64(b[i] * b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return d / math.Sqrt(na*nb)
}

func readPlain(p string) (string, error) {
	if strings.HasSuffix(p, ".bad") {
		return "", errors.New("cannot read this")
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateSearchAndRefresh(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "budget.md"), "# Budget\n\nThe committee approved a travel budget of 4000 euros.\n")
	write(t, filepath.Join(root, "notes", "retries.txt"), "line one\nline two\nRetries back off exponentially, up to five attempts.\n")
	write(t, filepath.Join(root, "photo.png"), "\x89PNG")
	write(t, filepath.Join(root, ".secret", "key.txt"), "hidden")
	write(t, filepath.Join(root, "node_modules", "x.js"), "dependency")
	write(t, filepath.Join(root, "scan.bad"), "x")
	em := &words{}
	opt := Options{Model: "m1", Embed: em, Read: readPlain}
	ix, _ := Load(filepath.Join(t.TempDir(), "files.json"))
	st, err := ix.Update(context.Background(), []string{root}, opt)
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 3 || st.Added != 2 || st.Skipped != 1 || st.Chunks != 2 {
		t.Errorf("stats %+v", st)
	}
	if err := ix.Save(); err != nil {
		t.Fatal(err)
	}
	ix, err = Load(ix.path)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := em.Embed(context.Background(), []string{"how many attempts do retries make"})
	hits := ix.Search(q[0], 1, "", cosine)
	if len(hits) != 1 || !strings.HasSuffix(hits[0].Path, "retries.txt") || hits[0].Line != 1 {
		t.Fatalf("hits %+v", hits)
	}
	if len(hits[0].Vec) != 64 {
		t.Errorf("vector did not survive saving: %d", len(hits[0].Vec))
	}
	if got := ix.Search(q[0], 5, filepath.Join(root, "notes"), cosine); len(got) != 1 {
		t.Errorf("within notes: %d hits", len(got))
	}

	// Nothing changed: nothing is embedded again.
	em.calls = 0
	if st, _ = ix.Update(context.Background(), []string{root}, opt); em.calls != 0 || st.Added+st.Changed+st.Removed != 0 {
		t.Errorf("unchanged files re-read: %+v, %d calls", st, em.calls)
	}
	// A changed file is re-read and a deleted one dropped.
	time.Sleep(10 * time.Millisecond)
	write(t, filepath.Join(root, "budget.md"), "# Budget\n\nThe travel budget is now 5000 euros.\n")
	os.Remove(filepath.Join(root, "notes", "retries.txt"))
	st, _ = ix.Update(context.Background(), []string{root}, opt)
	if st.Changed != 1 || st.Removed != 1 || st.Chunks != 1 || !strings.Contains(ix.Chunks[0].Text, "5000") {
		t.Errorf("after edits %+v %+v", st, ix.Chunks)
	}
	// Another embedding model means embedding everything again.
	em.calls = 0
	opt.Model = "m2"
	if st, _ = ix.Update(context.Background(), []string{root}, opt); st.Added != 1 || ix.Model != "m2" || em.calls == 0 {
		t.Errorf("model change %+v", st)
	}
}

func TestLongFilesSplitWithLineNumbers(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		b.WriteString("This is line number " + strings.Repeat("x", 20) + " of the long file.\n")
		if i%20 == 0 {
			b.WriteString("\n")
		}
	}
	write(t, filepath.Join(root, "long.txt"), b.String())
	em := &words{}
	ix, _ := Load(filepath.Join(t.TempDir(), "i.json"))
	if _, err := ix.Update(context.Background(), []string{root}, Options{Model: "m", Embed: em, Read: readPlain}); err != nil {
		t.Fatal(err)
	}
	if len(ix.Chunks) < 5 {
		t.Fatalf("%d chunks", len(ix.Chunks))
	}
	prev := 0
	for _, c := range ix.Chunks {
		if len(c.Text) > ChunkSize || c.Line <= prev {
			t.Errorf("chunk at line %d after %d, %d bytes", c.Line, prev, len(c.Text))
		}
		prev = c.Line
	}
	// Passages are embedded in batches, not one request each.
	if em.calls >= len(ix.Chunks) {
		t.Errorf("%d requests for %d passages", em.calls, len(ix.Chunks))
	}
}

func TestRemoveKeepsOtherRoots(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write(t, filepath.Join(a, "a.txt"), "alpha")
	write(t, filepath.Join(b, "b.txt"), "beta")
	ix, _ := Load(filepath.Join(t.TempDir(), "i.json"))
	opt := Options{Model: "m", Embed: &words{}, Read: readPlain}
	if _, err := ix.Update(context.Background(), []string{a, b}, opt); err != nil {
		t.Fatal(err)
	}
	if n, err := ix.Remove(a); err != nil || n != 1 {
		t.Fatalf("remove %d %v", n, err)
	}
	if len(ix.Roots) != 1 || len(ix.Chunks) != 1 || ix.Chunks[0].Text != "beta" {
		t.Errorf("after remove %+v", ix)
	}
	if _, err := ix.Remove(a); err == nil {
		t.Error("removing twice succeeded")
	}
	if _, err := ix.Update(context.Background(), []string{filepath.Join(b, "b.txt")}, opt); err == nil {
		t.Error("indexing a file, not a directory, succeeded")
	}
}

func TestBadIndexFileSaysHowToRecover(t *testing.T) {
	p := filepath.Join(t.TempDir(), "files.json")
	os.WriteFile(p, []byte("{"), 0o644)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "mote index rm --all") {
		t.Errorf("%v", err)
	}
}

// picky refuses long inputs the way llama-server does past its batch size.
type picky struct{ words }

func (p *picky) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	for _, t := range texts {
		if strings.Contains(t, "minified") {
			return nil, errors.New("llama-server: 500: input (577 tokens) is too large to process")
		}
	}
	return p.words.Embed(ctx, texts)
}

func TestAPassageTooLongSkipsOnlyItsFile(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.min.js"), "minified")
	write(t, filepath.Join(root, "b.txt"), "fine")
	ix, _ := Load(filepath.Join(t.TempDir(), "i.json"))
	st, err := ix.Update(context.Background(), []string{root}, Options{Model: "m", Embed: &picky{}, Read: readPlain})
	if err != nil {
		t.Fatal(err)
	}
	if st.Added != 1 || st.Skipped != 1 || !strings.Contains(ix.Files[filepath.Join(root, "a.min.js")].Skipped, "too long") {
		t.Errorf("%+v %+v", st, ix.Files)
	}
}
