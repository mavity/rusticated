// Package regast_poc implements node-aware regular expressions over Go source.
//
// A pattern is an ordinary regular expression that may additionally contain
// node-groups, written between the white curly brackets ⦃ and ⦄. A node-group
// matches a whole Go AST node, ignoring the exact form of whitespace and
// comments, while letting a space in the pattern demand a real gap in the
// source. See README.md for the full design.
package regast_poc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
)

// span is a half-open byte range [start, end) into the source.
type span struct {
	start int
	end   int
}

// Source is a preprocessed Go file: the raw bytes plus two derived tables —
// the byte ranges of comments (used to compute significance) and the byte
// ranges of every AST node (used to resolve node-groups).
type Source struct {
	Src []byte

	comments []span // sorted by start, non-overlapping

	nodeStart []int // sorted ascending; parallel to nodeEnd
	nodeEnd   []int
}

// Preprocess scans src once and builds the comment and node tables.
//
// Parsing uses go/parser's error-tolerant mode: even when src has errors it
// returns a best-effort partial AST, and node spans are taken from whatever it
// yields. Only when the result is too broken to produce any AST is an error
// returned.
func Preprocess(filename string, src []byte) (*Source, error) {
	s := &Source{Src: src}

	// Comments: any byte inside a comment is insignificant.
	sfset := token.NewFileSet()
	sfile := sfset.AddFile(filename, sfset.Base(), len(src))
	var sc scanner.Scanner
	sc.Init(sfile, src, nil, scanner.ScanComments)
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT {
			off := sfset.Position(pos).Offset
			s.comments = append(s.comments, span{off, off + len(lit)})
		}
	}
	sort.Slice(s.comments, func(i, j int) bool {
		return s.comments[i].start < s.comments[j].start
	})

	// Nodes: every AST node's byte range, sorted by start.
	pfset := token.NewFileSet()
	f, err := parser.ParseFile(pfset, filename, src,
		parser.ParseComments|parser.AllErrors|parser.SkipObjectResolution)
	if f == nil {
		return nil, fmt.Errorf("regast_poc: cannot parse %s: %w", filename, err)
	}

	type se struct{ s, e int }
	var nodes []se
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if !n.Pos().IsValid() || !n.End().IsValid() {
			return true
		}
		st := pfset.Position(n.Pos()).Offset
		en := pfset.Position(n.End()).Offset
		if en > st && st >= 0 && en <= len(src) {
			nodes = append(nodes, se{st, en})
		}
		return true
	})
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].s != nodes[j].s {
			return nodes[i].s < nodes[j].s
		}
		return nodes[i].e < nodes[j].e
	})
	for _, nd := range nodes {
		s.nodeStart = append(s.nodeStart, nd.s)
		s.nodeEnd = append(s.nodeEnd, nd.e)
	}
	return s, nil
}

// isSpace reports whether b is Go source whitespace.
func isSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}

// significant reports whether byte i carries program meaning, i.e. it is
// neither whitespace nor part of a comment.
func (s *Source) significant(i int) bool {
	if i < 0 || i >= len(s.Src) {
		return false
	}
	if isSpace(s.Src[i]) {
		return false
	}
	// Binary search for the last comment starting at or before i.
	lo, hi := 0, len(s.comments)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if s.comments[mid].start <= i {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo > 0 {
		c := s.comments[lo-1]
		if i >= c.start && i < c.end {
			return false
		}
	}
	return true
}

// endsForStart returns the end offsets of all AST nodes that begin exactly at
// pos, as a sorted ascending slice with duplicates removed.
func (s *Source) endsForStart(pos int) []int {
	lo, hi := 0, len(s.nodeStart)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if s.nodeStart[mid] < pos {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	var ends []int
	for lo < len(s.nodeStart) && s.nodeStart[lo] == pos {
		e := s.nodeEnd[lo]
		if len(ends) == 0 || ends[len(ends)-1] != e {
			ends = append(ends, e)
		}
		lo++
	}
	return ends
}
