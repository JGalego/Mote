package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConvertedName(t *testing.T) {
	md, nb := []string{".mote.md", ".md"}, []string{".ipynb"}
	for _, c := range []struct {
		path string
		from []string
		to   string
		want string
	}{
		{"notes.mote.md", md, ".ipynb", "notes.ipynb"},
		{"notes.md", md, ".ipynb", "notes.ipynb"},
		{"dir/notes", md, ".ipynb", "dir/notes.ipynb"},
		{".md", md, ".ipynb", ".md.ipynb"},
		{"a.ipynb", nb, ".mote.md", "a.mote.md"},
		{"a.json", nb, ".mote.md", "a.json.mote.md"},
	} {
		if got := convertedName(c.path, c.from, c.to); got != c.want {
			t.Errorf("convertedName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

const convertibleBook = "# Notes\n\n```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n\nThen:\n\n```mote\nchat \"{{a}}\"\n```\n"

func TestNbExportAndImportRoundTrip(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	book := filepath.Join(dir, "notes.mote.md")
	os.WriteFile(book, []byte(convertibleBook), 0o644)

	code, _, errs := e.mote("", "nb", "export", book)
	if code != 0 || !strings.Contains(errs, "notes.ipynb (2 cells)") {
		t.Fatalf("export: %d %s", code, errs)
	}
	var nb struct {
		NBFormat int
		Cells    []struct{ Cell_type string }
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "notes.ipynb"))), &nb); err != nil || nb.NBFormat != 4 || len(nb.Cells) != 4 {
		t.Fatalf("notebook: %v %+v", err, nb)
	}

	// Importing it gives the motebook back, minus the fingerprint.
	back := filepath.Join(dir, "back.mote.md")
	code, _, errs = e.mote("", "nb", "import", filepath.Join(dir, "notes.ipynb"), "-o", back)
	if code != 0 || !strings.Contains(errs, "(2 cells)") {
		t.Fatalf("import: %d %s", code, errs)
	}
	if got, want := readFile(t, back), strings.Replace(convertibleBook, "output key=k", "output", 1); got != want {
		t.Errorf("round trip:\n%s\nwant:\n%s", got, want)
	}
	// An imported output is not trusted, so a run computes the cell again.
	if code, _, errs := e.mote("", "nb", "run", back, "-o", "-"); code != 0 || !strings.Contains(errs, "2 run, 0 unchanged") {
		t.Errorf("nb run of an import: %d %s", code, errs)
	}
}

func TestNbExportPrintsAndRefusesToOverwrite(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	book := filepath.Join(dir, "notes.mote.md")
	os.WriteFile(book, []byte(convertibleBook), 0o644)

	code, out, errs := e.mote("", "nb", "export", book, "-o", "-")
	if code != 0 || !json.Valid([]byte(out)) {
		t.Fatalf("-o -: %d %q %s", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.ipynb")); err == nil {
		t.Error("-o - wrote a file")
	}
	target := filepath.Join(dir, "x.ipynb")
	os.WriteFile(target, []byte("mine"), 0o644)
	if code, _, errs := e.mote("", "nb", "export", book, "-o", target); code != ExitUsage || !strings.Contains(errs, "--force") || readFile(t, target) != "mine" {
		t.Errorf("existing file: %d %s", code, errs)
	}
	if code, _, errs := e.mote("", "nb", "export", book, "-o", target, "--force"); code != 0 || readFile(t, target) == "mine" {
		t.Errorf("--force: %d %s", code, errs)
	}
}

func TestNbImportRefusesWhatItCannotRead(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(src), 0o644)
		return p
	}
	python := write("py.ipynb", `{"nbformat": 4, "metadata": {"kernelspec": {"name": "python3"}}, "cells": [
	  {"cell_type": "code", "metadata": {}, "source": "chat hi", "outputs": [], "execution_count": null}]}`)

	code, _, errs := e.mote("", "nb", "import", python)
	if code != ExitUsage || !strings.Contains(errs, "kernel is python3") {
		t.Errorf("another kernel: %d %s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "py.mote.md")); err == nil {
		t.Error("a refused import wrote a file")
	}
	if code, _, errs := e.mote("", "nb", "import", python, "--any-kernel"); code != 0 || !strings.Contains(readFile(t, filepath.Join(dir, "py.mote.md")), "chat hi") {
		t.Errorf("--any-kernel: %d %s", code, errs)
	}
	if code, _, _ := e.mote("", "nb", "import", write("bad.ipynb", "{nope")); code != ExitUsage {
		t.Errorf("not JSON: exit %d", code)
	}
	if code, _, _ := e.mote("", "nb", "import", filepath.Join(dir, "missing.ipynb")); code == 0 {
		t.Error("a missing file was accepted")
	}
	if code, _, _ := e.mote("", "nb", "export", write("bad.md", "```mote\nchat {{x}}\n```\n")); code != ExitUsage {
		t.Errorf("export of a bad notebook: exit %d", code)
	}
}

func TestNbConvertFlagsBelongToTheirSubcommands(t *testing.T) {
	e, path := nbSessionEnv(t)
	for _, args := range [][]string{
		{"nb", "export", path, "--any-kernel"},
		{"nb", "export", path, "--dry-run"},
		{"nb", "import", path, "--state", "s"},
		{"nb", "run", path, "--any-kernel"},
		{"nb", "export"},
		{"nb", "import"},
	} {
		if code, _, _ := e.mote("", args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
}
