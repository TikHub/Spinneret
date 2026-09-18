// Package textdiff produces line-based unified diffs for policy and
// configuration version comparisons shown in the console.
package textdiff

import (
	"fmt"
	"strings"
)

// maxCells bounds the LCS table size so that pathological inputs cannot
// exhaust memory; larger inputs fall back to a whole-file replacement diff.
const maxCells = 25_000_000

type opKind byte

const (
	opEqual opKind = iota
	opDelete
	opInsert
)

type op struct {
	kind opKind
	line string
}

// Unified returns a unified diff of from and to with the given number of
// context lines. It returns "" when the texts are identical.
func Unified(fromName, toName, from, to string, context int) string {
	if from == to {
		return ""
	}
	if context < 0 {
		context = 0
	}
	a := splitLines(from)
	b := splitLines(to)
	ops := diffLines(a, b)

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", fromName, toName)
	for _, h := range hunks(ops, context) {
		writeHunk(&sb, ops, h)
	}
	return sb.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// diffLines computes an edit script using a longest-common-subsequence table
// after trimming the common prefix and suffix.
func diffLines(a, b []string) []op {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	ops := make([]op, 0, len(a)+len(b))
	for i := 0; i < prefix; i++ {
		ops = append(ops, op{opEqual, a[i]})
	}
	ma := a[prefix : len(a)-suffix]
	mb := b[prefix : len(b)-suffix]
	ops = append(ops, lcsOps(ma, mb)...)
	for i := len(a) - suffix; i < len(a); i++ {
		ops = append(ops, op{opEqual, a[i]})
	}
	return ops
}

func lcsOps(a, b []string) []op {
	n, m := len(a), len(b)
	if n == 0 || m == 0 || (n+1)*(m+1) > maxCells {
		out := make([]op, 0, n+m)
		for _, l := range a {
			out = append(out, op{opDelete, l})
		}
		for _, l := range b {
			out = append(out, op{opInsert, l})
		}
		return out
	}
	// table[i][j] = LCS length of a[i:] and b[j:]
	table := make([][]int32, n+1)
	for i := range table {
		table[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	out := make([]op, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, op{opEqual, a[i]})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			out = append(out, op{opDelete, a[i]})
			i++
		default:
			out = append(out, op{opInsert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, op{opDelete, a[i]})
	}
	for ; j < m; j++ {
		out = append(out, op{opInsert, b[j]})
	}
	return out
}

type hunk struct{ start, end int } // op index range [start, end)

func hunks(ops []op, context int) []hunk {
	var out []hunk
	for i := 0; i < len(ops); i++ {
		if ops[i].kind == opEqual {
			continue
		}
		start := i - context
		if start < 0 {
			start = 0
		}
		end := i + 1
		// Extend while further changes are within 2*context equal lines.
		for end < len(ops) {
			if ops[end].kind != opEqual {
				end++
				continue
			}
			run := 0
			for end+run < len(ops) && ops[end+run].kind == opEqual {
				run++
			}
			if end+run < len(ops) && run <= 2*context {
				end += run
				continue
			}
			end += min(run, context)
			break
		}
		if len(out) > 0 && start <= out[len(out)-1].end {
			out[len(out)-1].end = end
		} else {
			out = append(out, hunk{start, end})
		}
		i = end - 1
	}
	return out
}

func writeHunk(sb *strings.Builder, ops []op, h hunk) {
	// Compute 1-based starting line numbers in both files.
	aLine, bLine := 1, 1
	for i := 0; i < h.start; i++ {
		switch ops[i].kind {
		case opEqual:
			aLine++
			bLine++
		case opDelete:
			aLine++
		case opInsert:
			bLine++
		}
	}
	aCount, bCount := 0, 0
	for i := h.start; i < h.end; i++ {
		switch ops[i].kind {
		case opEqual:
			aCount++
			bCount++
		case opDelete:
			aCount++
		case opInsert:
			bCount++
		}
	}
	aStart, bStart := aLine, bLine
	if aCount == 0 {
		aStart--
	}
	if bCount == 0 {
		bStart--
	}
	fmt.Fprintf(sb, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
	for i := h.start; i < h.end; i++ {
		switch ops[i].kind {
		case opEqual:
			sb.WriteString(" ")
		case opDelete:
			sb.WriteString("-")
		case opInsert:
			sb.WriteString("+")
		}
		sb.WriteString(ops[i].line)
		sb.WriteString("\n")
	}
}
