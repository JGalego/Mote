package task

import (
	"fmt"
	"strings"
)

// maxDiffCells bounds the O(n·m) table lcsOps builds. Beyond it, the whole
// file is shown as replaced instead of risking gigabytes of memory on one
// large file with many short lines.
const maxDiffCells = 4 << 20 // about 16 MiB of int32, e.g. 2048x2048 lines

// Unified returns a unified diff (3 lines of context) between two texts, or
// "" when they are equal. Small models are unreliable at writing hunks, so
// mote asks them for whole files and computes the diff itself.
func Unified(path, before, after string) string {
	if before == after {
		return ""
	}
	a, b := splitLines(before), splitLines(after)
	var ops []diffOp
	if int64(len(a))*int64(len(b)) > maxDiffCells {
		ops = replaceOps(a, b)
	} else {
		ops = lcsOps(a, b)
	}

	const ctx = 3
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		// Grow a hunk around this change, merging changes closer than 2*ctx.
		start := i - ctx
		if start < 0 {
			start = 0
		}
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*ctx {
				end += min(ctx, run-end)
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := ops[start].ai, ops[start].bi, 0, 0
		var body strings.Builder
		for _, o := range ops[start:end] {
			switch o.kind {
			case ' ':
				aLen++
				bLen++
				body.WriteString(" " + o.text + "\n")
			case '-':
				aLen++
				body.WriteString("-" + o.text + "\n")
			case '+':
				bLen++
				body.WriteString("+" + o.text + "\n")
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n%s", hunkRange(aStart, aLen), hunkRange(bStart, bLen), body.String())
		i = end
	}
	return out.String()
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

type diffOp struct {
	kind   byte // ' ', '-', '+'
	text   string
	ai, bi int // positions in a and b before this op
}

// replaceOps removes every line of a then adds every line of b, with no
// attempt at a minimal diff. It is what lcsOps computes anyway when a and b
// share nothing, so it is a correct diff, just not always the shortest one.
func replaceOps(a, b []string) []diffOp {
	ops := make([]diffOp, 0, len(a)+len(b))
	for i, l := range a {
		ops = append(ops, diffOp{'-', l, i, 0})
	}
	for j, l := range b {
		ops = append(ops, diffOp{'+', l, len(a), j})
	}
	return ops
}

func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i], i, j})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, diffOp{'-', a[i], i, j})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j], i, j})
			j++
		}
	}
	return ops
}
