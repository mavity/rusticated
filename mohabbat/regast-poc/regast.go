package regast_poc

import (
	"fmt"

	"mohabbat/mohabbat/regast_poc/syntax"
)

// Pattern is a compiled node-aware regular expression.
type Pattern struct {
	prog *syntax.Prog
	ncap int
}

// Compile parses and compiles a node-aware pattern.
func Compile(pattern string) (*Pattern, error) {
	lowered, err := lower(pattern)
	if err != nil {
		return nil, err
	}
	re, err := syntax.Parse(lowered, syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("regast_poc: %w", err)
	}
	re = re.Simplify()
	prog, err := syntax.Compile(re)
	if err != nil {
		return nil, fmt.Errorf("regast_poc: %w", err)
	}
	ncap := prog.NumCap
	if ncap < 2 {
		ncap = 2
	}
	return &Pattern{prog: prog, ncap: ncap}, nil
}

// find returns the leftmost match whose start is at or after `from`, as a slice
// of submatch byte offsets (group 0 is the whole match; group k is at indices
// 2k, 2k+1; an absent group is -1).
func (p *Pattern) find(s *Source, from int) ([]int, bool) {
	for start := from; start <= len(s.Src); start++ {
		m := &machine{prog: p.prog, src: s.Src, source: s}
		m.cap = make([]int, p.ncap)
		for i := range m.cap {
			m.cap[i] = -1
		}
		m.cap[0] = start
		if m.match(uint32(p.prog.Start), start, nil, rootCtx) {
			return m.cap, true
		}
	}
	return nil, false
}

// Find returns the submatch offsets of the leftmost match, or ok=false.
func (p *Pattern) Find(s *Source) (loc []int, ok bool) {
	return p.find(s, 0)
}

// FindAll returns the submatch offsets of all successive non-overlapping
// matches, left to right.
func (p *Pattern) FindAll(s *Source) [][]int {
	var res [][]int
	pos := 0
	for pos <= len(s.Src) {
		cap, ok := p.find(s, pos)
		if !ok {
			break
		}
		res = append(res, cap)
		if cap[1] > pos {
			pos = cap[1]
		} else {
			pos++ // empty match: advance to make progress
		}
	}
	return res
}

// Replace replaces every non-overlapping match with the expansion of repl.
// In repl, $0 is the whole match and $1, $2, … (or ${1}, ${12}) are capture
// groups; a node-group counts as a capture group, so its reference expands to
// the node's raw source text. $$ is a literal $.
func (p *Pattern) Replace(s *Source, repl string) ([]byte, error) {
	matches := p.FindAll(s)
	var out []byte
	last := 0
	for _, cap := range matches {
		start, end := cap[0], cap[1]
		if start < last {
			continue
		}
		out = append(out, s.Src[last:start]...)
		out = append(out, expand(repl, cap, s.Src)...)
		last = end
	}
	out = append(out, s.Src[last:]...)
	return out, nil
}

// expand renders a replacement template against the capture offsets.
func expand(repl string, cap []int, src []byte) []byte {
	var out []byte
	i := 0
	for i < len(repl) {
		c := repl[i]
		if c != '$' {
			out = append(out, c)
			i++
			continue
		}
		i++
		if i < len(repl) && repl[i] == '$' {
			out = append(out, '$')
			i++
			continue
		}
		braces := false
		if i < len(repl) && repl[i] == '{' {
			braces = true
			i++
		}
		start := i
		for i < len(repl) && repl[i] >= '0' && repl[i] <= '9' {
			i++
		}
		if i == start {
			out = append(out, '$')
			if braces {
				out = append(out, '{')
			}
			continue
		}
		num := 0
		for _, d := range repl[start:i] {
			num = num*10 + int(d-'0')
		}
		if braces && i < len(repl) && repl[i] == '}' {
			i++
		}
		gi := 2 * num
		if gi+1 < len(cap) && cap[gi] >= 0 && cap[gi+1] >= 0 {
			out = append(out, src[cap[gi]:cap[gi+1]]...)
		}
	}
	return out
}
