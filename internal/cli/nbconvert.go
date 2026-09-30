package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/jgalego/mote/internal/motebook"
)

// `mote nb export` and `mote nb import` move a notebook between the two
// forms it can take: a motebook, which is Markdown you can read and diff,
// and a Jupyter notebook for the mote kernel, which Jupyter and VS Code
// open. The conversion itself is internal/motebook's; this is the files.

// convertedName is the name a converted file gets when -o is not given: path
// with the extension of the form it is in replaced by that of the form it is
// becoming.
func convertedName(path string, from []string, to string) string {
	for _, ext := range from {
		if strings.HasSuffix(path, ext) && len(path) > len(ext) {
			return strings.TrimSuffix(path, ext) + to
		}
	}
	return path + to
}

// writeConverted writes what a conversion produced to where -o says, or to
// def. A file that is there is not replaced without --force, and - prints.
func (a *app) writeConverted(data []byte, vals map[string]string, def string, cells int) error {
	out := firstNonEmptyRaw(vals["-o"], vals["--output"], def)
	if out == "-" {
		_, err := a.out.Write(data)
		return err
	}
	if _, err := os.Stat(out); err == nil && vals["--force"] != "true" {
		return usagef("%s already exists; pass --force to replace it", out)
	}
	if err := writeFileAtomic(out, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.err, "%s wrote %s (%s)\n", a.ue.OK(), out, cellCount(cells))
	return nil
}

func (a *app) nbExport(path string, vals map[string]string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	book, err := motebook.Parse(string(src))
	if err != nil {
		return usagef("%s: %v", path, err)
	}
	data, err := book.IPYNB()
	if err != nil {
		return err
	}
	return a.writeConverted(data, vals, convertedName(path, []string{".mote.md", ".md"}, ".ipynb"), len(book.Cells))
}

func (a *app) nbImport(path string, vals map[string]string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	book, err := motebook.FromIPYNB(src, vals["--any-kernel"] == "true")
	if err != nil {
		return usagef("%s: %v", path, err)
	}
	return a.writeConverted([]byte(book.String()), vals, convertedName(path, []string{".ipynb"}, ".mote.md"), len(book.Cells))
}
