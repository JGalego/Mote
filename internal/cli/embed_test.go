package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/motebook"
)

func TestEmbedMarkup(t *testing.T) {
	for _, c := range []struct{ link, want string }{
		{"examples/photo.jpg", "![photo](examples/photo.jpg)"},
		{"a.PNG", "![a](a.PNG)"},
		{"my photo.gif", "![my photo](my%20photo.gif)"},
		{"a[1].png", "![a 1](a%5B1%5D.png)"},
		{"clips/meeting.m4a", `<audio controls src="clips/meeting.m4a"></audio>`},
		{"talk.mp3", `<audio controls src="talk.mp3"></audio>`},
		{"clip.mp4", `<video controls src="clip.mp4"></video>`},
		{"a&b c.webm", `<video controls src="a&amp;b%20c.webm"></video>`},
		{"../up/one.MOV", `<video controls src="../up/one.MOV"></video>`},
	} {
		got, err := embedMarkup(c.link, c.link)
		if err != nil || got != c.want {
			t.Errorf("embedMarkup(%q) = %q, %v; want %q", c.link, got, err, c.want)
		}
	}
	for _, name := range []string{"notes.txt", "noextension", "archive.zip"} {
		if _, err := embedMarkup(name, name); err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "/note") {
			t.Errorf("embedMarkup(%q): %v", name, err)
		}
	}
}

func TestEmbedPathIsRelativeToTheNotebook(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	os.MkdirAll(filepath.Join(dir, "nb"), 0o755)
	os.MkdirAll(filepath.Join(dir, "media"), 0o755)

	inMemory := &nbSession{}
	if got := inMemory.embedPath("media/../media/a.png"); got != "media/a.png" {
		t.Errorf("no file: %q", got)
	}
	onDisk := &nbSession{path: filepath.Join("nb", "book.mote.md")}
	if got := onDisk.embedPath(filepath.Join("media", "a.png")); got != "../media/a.png" {
		t.Errorf("a file in another directory: %q", got)
	}
	if got := onDisk.embedPath(filepath.Join("nb", "a.png")); got != "a.png" {
		t.Errorf("a file beside the notebook: %q", got)
	}
	// An absolute path is found too.
	if got := onDisk.embedPath(filepath.Join(dir, "media", "a.png")); got != "../media/a.png" {
		t.Errorf("an absolute path: %q", got)
	}
}

func TestCheckEmbeddable(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.png")
	os.WriteFile(f, nil, 0o644)
	if err := checkEmbeddable(f); err != nil {
		t.Error(err)
	}
	if err := checkEmbeddable(filepath.Join(dir, "gone.png")); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("missing: %v", err)
	}
	if err := checkEmbeddable(dir); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("directory: %v", err)
	}
}

func TestConsoleAddsTextAndMedia(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	chdir(t, dir)
	os.MkdirAll(filepath.Join(dir, "nb"), 0o755)
	os.MkdirAll(filepath.Join(dir, "media"), 0o755)
	for _, f := range []string{"photo.png", "clip.mp4", "talk.m4a", "my sound.mp3"} {
		os.WriteFile(filepath.Join(dir, "media", f), nil, 0o644)
	}
	book := filepath.Join("nb", "book.mote.md")

	input := strings.Join([]string{
		"/note First thought.",
		"/embed media/photo.png media/clip.mp4 media/talk.m4a",
		"chat one",
		`/embed "media/my sound.mp3"`,
		"/undo",
		"/undo",
		"/undo",
		"/note Second thought,\\",
		"over two lines.",
	}, "\n") + "\n"
	code, _, errs := e.mote(input, "nb", "console", book)
	if code != 0 {
		t.Fatalf("console: %d %s", code, errs)
	}
	for _, want := range []string{
		"added a note",
		"added an embed of media/photo.png, media/clip.mp4, media/talk.m4a",
		"dropped an embed of media/my sound.mp3", // the last thing added goes first
		"dropped cell 1",
		"dropped an embed of media/photo.png, media/clip.mp4, media/talk.m4a",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errs)
		}
	}
	// What is left: the first note, and the second, both plain Markdown.
	got := readFile(t, book)
	want := "First thought.\n\nSecond thought,\nover two lines.\n"
	if got != want {
		t.Errorf("notebook:\n%s\nwant:\n%s", got, want)
	}

	// The embeds, when they stay, are relative to the notebook and read back.
	code, _, errs = e.mote("/embed media/photo.png media/clip.mp4 media/talk.m4a\n", "nb", "console", book)
	if code != 0 {
		t.Fatalf("console: %d %s", code, errs)
	}
	got = readFile(t, book)
	for _, want := range []string{"![photo](../media/photo.png)", `<video controls src="../media/clip.mp4"></video>`, `<audio controls src="../media/talk.m4a"></audio>`} {
		if !strings.Contains(got, want) {
			t.Errorf("notebook lacks %q:\n%s", want, got)
		}
	}
	if b, err := motebook.Parse(got); err != nil || len(b.Segments()) != 1 {
		t.Errorf("the notebook does not read back as prose: %v", err)
	}
	// It runs as a notebook that has nothing to run.
	if code, _, errs := e.mote("", "nb", "run", book); code != ExitUsage || !strings.Contains(errs, "no cells") {
		t.Errorf("nb run of prose: %d %s", code, errs)
	}
}

func TestConsoleRefusesWhatCannotBeAdded(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	chdir(t, dir)
	os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o644)
	os.Mkdir(filepath.Join(dir, "pics.png"), 0o755)
	input := strings.Join([]string{
		"/note",
		"/note ```mote",
		"/embed",
		"/embed gone.png",
		"/embed notes.txt",
		"/embed pics.png",
		`/embed "unclosed`,
	}, "\n") + "\n"
	path := filepath.Join(dir, "book.mote.md")
	code, _, errs := e.mote(input, "nb", "console", path)
	if code != 0 {
		t.Fatalf("console: %d %s", code, errs)
	}
	for _, want := range []string{"usage: /note TEXT", "cannot hold a fenced block", "usage: /embed FILE...", "gone.png: no such file",
		"notes.txt is not an image, audio or video file", "pics.png is a directory", "unclosed"} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errs)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("a refused addition created the notebook:\n%s", readFile(t, path))
	}
}

func TestConsoleKeepsASessionThatHoldsOnlyNotes(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	chdir(t, dir)

	// -o saves on exit, cells or no cells.
	out := filepath.Join(dir, "notes.mote.md")
	if code, _, errs := e.mote("/note a plan\n", "nb", "console", "-o", out); code != 0 || !strings.Contains(errs, "saved your notes in") || readFile(t, out) != "a plan\n" {
		t.Errorf("-o: %d %s\n%s", code, errs, readFile(t, out))
	}
	// Without a terminal it says what it dropped.
	if _, _, errs := e.mote("/note a plan\n", "nb", "console"); !strings.Contains(errs, "your notes not saved") {
		t.Errorf("piped: %s", errs)
	}
	// At a terminal it asks, and says notes, not zero cells.
	t.Setenv("MOTE_FORCE_LIVE", "1")
	named := filepath.Join(dir, "asked.mote.md")
	_, _, errs := e.mote("/note a plan\n/exit\ny\n"+named+"\n", "nb", "console")
	if !strings.Contains(errs, "save your notes to a file? [y/N]") || strings.Contains(errs, "0 cells") || readFile(t, named) != "a plan\n" {
		t.Errorf("asked: %s\n%s", errs, readFile(t, named))
	}
}

func TestConsoleCompletesTheFileAfterEmbed(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	os.MkdirAll(filepath.Join(dir, "media"), 0o755)
	os.WriteFile(filepath.Join(dir, "media", "clip.mp4"), nil, 0o644)
	s := completionSession(t)
	for _, c := range []struct {
		line  string
		start int
		want  string
	}{
		{"/embed med", 7, "media/"},
		{"/embed media/c", 7, "media/clip.mp4"},
		{"/embed media/clip.mp4 med", 22, "media/"}, // a second file
		{"/save med", 6, "media/"},
		{"/embed ", 0, ""},
		{"/note med", 0, ""}, // text, not a file
		{"/tasks med", 0, ""},
	} {
		start, got := completion(t, s, c.line)
		if got != c.want || (got != "" && start != c.start) {
			t.Errorf("%q: %d %q, want %d %q", c.line, start, got, c.start, c.want)
		}
	}
	if _, got := completion(t, s, "/no"); got != "/note " {
		t.Errorf("/no: %q", got)
	}
	if _, got := completion(t, s, "/em"); got != "/embed " {
		t.Errorf("/em: %q", got)
	}
}

func TestConsoleColoursNoteAndEmbedAsCommandsWithArguments(t *testing.T) {
	a, _ := colourApp(t)
	s := &nbSession{a: a}
	for _, line := range []string{"/note some text", "/embed a.png", "/note", "/embed"} {
		if got := s.highlight([]rune(line)); !strings.HasPrefix(got, cyan) {
			t.Errorf("%q: %q", line, got)
		}
	}
}

func TestEmbedsInASessionFollowItToTheFolderItIsSavedIn(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	chdir(t, dir)
	os.WriteFile(filepath.Join(dir, "pic.png"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "my clip.mp4"), nil, 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)

	// Saved on exit with -o.
	code, _, errs := e.mote("/embed pic.png\n", "nb", "console", "-o", filepath.Join("sub", "a.mote.md"))
	if code != 0 {
		t.Fatalf("console: %d %s", code, errs)
	}
	if got := readFile(t, filepath.Join("sub", "a.mote.md")); got != "![pic](../pic.png)\n" {
		t.Errorf("-o: %q", got)
	}
	// Saved with /save, then added to: both refer to their files from sub/,
	// and so does what /undo goes back to.
	input := "/embed pic.png\n/save sub/b.mote.md\n/embed \"my clip.mp4\"\n/undo\n"
	if code, _, errs := e.mote(input, "nb", "console"); code != 0 {
		t.Fatalf("console: %d %s", code, errs)
	}
	if got := readFile(t, filepath.Join("sub", "b.mote.md")); got != "![pic](../pic.png)\n" {
		t.Errorf("/save then /undo: %q", got)
	}
	if code, _, errs := e.mote("/embed \"my clip.mp4\"\n", "nb", "console", filepath.Join("sub", "b.mote.md")); code != 0 {
		t.Fatalf("console: %d %s", code, errs)
	}
	if got := readFile(t, filepath.Join("sub", "b.mote.md")); !strings.Contains(got, `<video controls src="../my%20clip.mp4"></video>`) {
		t.Errorf("a file session: %q", got)
	}
}

func TestEmbedPathSeesThroughALinkedFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need privileges on Windows")
	}
	real := t.TempDir()
	os.MkdirAll(filepath.Join(real, "media"), 0o755)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	// The working folder is reached through the link, the notebook by its
	// real path, as /var and /private/var are on macOS.
	chdir(t, link)
	s := &nbSession{path: filepath.Join(real, "book.mote.md")}
	if got := s.embedPath(filepath.Join("media", "a.png")); got != "media/a.png" {
		t.Errorf("got %q", got)
	}
}

func TestConsoleSaysWhenTheFileNamedWithOIsTaken(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "later.mote.md")
	// The file appears while the session runs: the cell makes it.
	if runtime.GOOS == "windows" {
		t.Skip("the sh syntax here does not apply to cmd")
	}
	input := "chat hi | sh: echo taken > " + out + "\n/note made\n"
	code, _, errs := e.mote(input, "nb", "console", "-o", out, "--yes")
	if code == 0 || !strings.Contains(errs, "already exists") || !strings.Contains(errs, "not saved") {
		t.Errorf("exit %d: %s", code, errs)
	}
	if got := readFile(t, out); got != "taken\n" {
		t.Errorf("the file that appeared was replaced: %q", got)
	}
}
