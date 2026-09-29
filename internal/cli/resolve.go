package cli

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jgalego/mote/internal/task"
)

// A file or dir argument mote cannot find where it is running is common
// enough — a request naming a file that lives in a subdirectory, a typed
// path with the wrong prefix, a plan that named something relative to a
// different folder — that every place mote turns words into a task's file
// arguments shares one fallback: look for something by that name elsewhere
// under the working directory, and ask before using it rather than guessing
// silently. `do`, `run`, `pipe` and `--plan` all go through the functions
// below when an argument they were given does not stat.

// skipSearchDirs are directories a filename search must not wander into:
// version control internals and dependency trees can hold hundreds of
// thousands of entries nothing is ever named for.
var skipSearchDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".venv": true,
	"venv": true, "__pycache__": true, "dist": true, "build": true,
	"target": true, ".cache": true, ".idea": true, ".vscode": true,
}

// filesByName indexes every entry under root by its base name, so a search
// for a word costs one walk of the tree rather than one per candidate. It
// skips directories nothing is ever named for, and gives up rather than
// walking forever in an enormous tree.
func filesByName(root string) map[string][]string {
	const limit = 200_000
	index := map[string][]string{}
	visited := 0
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return nil
		}
		visited++
		if visited > limit {
			return filepath.SkipAll
		}
		base := d.Name()
		if d.IsDir() && (skipSearchDirs[base] || strings.HasPrefix(base, ".")) {
			return filepath.SkipDir
		}
		index[base] = append(index[base], path)
		return nil
	})
	return index
}

// pickMatch asks before substituting a file that was not named directly:
// silently running against a different path than the one given could
// surprise whoever asked. --yes skips the ask the same way it skips
// confirmChosen's, and a run with no terminal to ask on fails with the
// candidates named, so a script can pass the exact path itself instead of
// getting an ambiguous answer.
func (a *app) pickMatch(word string, matches []string, vals map[string]string) (string, error) {
	sort.Strings(matches)
	if len(matches) > 1 {
		if !a.tty {
			return "", usagef("%q was not found here; it matches %s, so name the one you meant", word, strings.Join(matches, ", "))
		}
		fmt.Fprintf(a.err, "%s %s not found here; which did you mean?\n", a.ue.Warn(), word)
		for i, m := range matches {
			fmt.Fprintf(a.err, "  %d) %s\n", i+1, m)
		}
		fmt.Fprint(a.err, "> ")
		line, _ := bufio.NewReader(a.in).ReadString('\n')
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || n < 1 || n > len(matches) {
			return "", nil
		}
		return matches[n-1], nil
	}
	found := matches[0]
	if vals["--yes"] == "true" || vals["-y"] == "true" {
		return found, nil
	}
	if !a.tty {
		return "", usagef("%q was not found here, but %s looks like it; rerun with that path, or pass --yes to use it", word, found)
	}
	fmt.Fprintf(a.err, "%s %s not found here; use %s instead? %s [y/N] ",
		a.ue.Warn(), word, a.ue.Bold(found), a.ue.Dim("(found nearby)"))
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	if l := strings.ToLower(strings.TrimSpace(line)); l != "y" && l != "yes" {
		return "", nil
	}
	return found, nil
}

// resolveMissingFile is the fallback for a request that named a file or
// folder mote could not find where it is running: it looks for something by
// one of candidates' names elsewhere under the working directory and, once
// a match is confirmed, rewrites request to name it instead of guessing
// silently. An empty result with no error means give up and report whatever
// error the caller already had, not that anything went wrong here.
func (a *app) resolveMissingFile(request string, candidates []string, vals map[string]string) (string, error) {
	if len(candidates) == 0 {
		return "", nil
	}
	index := filesByName(".")
	for _, w := range candidates {
		matches := index[w]
		if len(matches) == 0 {
			continue
		}
		chosen, err := a.pickMatch(w, matches, vals)
		if err != nil || chosen == "" {
			return "", err
		}
		return strings.Replace(request, w, chosen, 1), nil
	}
	return "", nil
}

// looksLikeFilename reports whether a word has the shape of a file someone
// named, such as "invoice.txt". It bounds proactive searches — ones not
// already triggered by a failed bind — to words worth a filesystem walk over,
// so a plain-text request is never sent through one on the strength of a
// word like "in" or "value".
func looksLikeFilename(w string) bool {
	ext := filepath.Ext(w)
	return len(ext) >= 2 && len(ext) <= 9 && !strings.ContainsAny(ext, `/\`)
}

// candidateFilenames is missingWords narrowed to ones shaped like a file, for
// a caller that has not already failed to bind an argument and so must
// decide for itself whether a request is worth a filesystem walk over.
func candidateFilenames(request string) []string {
	var out []string
	for _, w := range missingWords(request) {
		if looksLikeFilename(w) {
			out = append(out, w)
		}
	}
	return out
}

// resolveTaskArgs checks a task's own file and dir arguments against disk,
// applying the same search-and-ask fallback to arguments given directly —
// typed to `mote run`, written into a `mote pipe` stage, or produced by
// `--plan` — as `do` applies to a spoken request. "-" and the pipe marker
// are left alone: they stand for a value yet to arrive, not a filename.
func (a *app) resolveTaskArgs(t task.Task, args []string, vals map[string]string) ([]string, error) {
	var index map[string][]string
	out := args
	copied := false
	for i, p := range t.Params {
		if i >= len(args) || (p.Kind != "file" && p.Kind != "dir") {
			continue
		}
		v := args[i]
		if v == "-" || strings.Contains(v, pipeMarker) {
			continue
		}
		if _, err := os.Stat(v); err == nil {
			continue
		}
		if index == nil {
			index = filesByName(".")
		}
		matches := index[filepath.Base(v)]
		if len(matches) == 0 {
			continue
		}
		chosen, err := a.pickMatch(v, matches, vals)
		if err != nil {
			return nil, err
		}
		if chosen == "" {
			continue
		}
		if !copied {
			out = append([]string(nil), args...)
			copied = true
		}
		out[i] = chosen
	}
	return out, nil
}
