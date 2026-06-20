package regast-poc

import (
	"unicode"
	"unicode/utf8"

	"mohabbat/mohabbat/regast-poc/syntax"
)

// stepLimit bounds the total work of a single match attempt, guarding against
// pathological backtracking. Source files are small, so the limit is generous.
const stepLimit = 1 << 22

// frame describes the active node-group region during matching. A nil frame
// means top-level (outer) matching over raw bytes; a non-nil frame means we are
// inside a node-group bounded by end, where the whitespace rules apply.
type frame struct {
	end    int
	parent *frame
}

// nodeCtx carries the whitespace-junction state while matching inside a
// node-group:
//   - lastEnd is the byte position just after the last consumed significant
//     character, or -1 if none has been consumed yet in this region;
//   - lastWord records whether that character was an identifier character;
//   - pendGap records whether the pattern contained whitespace since then.
//
// It is passed by value so backtracking restores it automatically.
type nodeCtx struct {
	lastEnd  int
	lastWord bool
	pendGap  bool
}

var rootCtx = nodeCtx{lastEnd: -1}

type machine struct {
	prog    *syntax.Prog
	src     []byte
	source  *Source
	cap     []int
	steps   int
	aborted bool
}

func (m *machine) regionEnd(fr *frame) int {
	if fr == nil {
		return len(m.src)
	}
	return fr.end
}

// match runs the compiled program from instruction pc at byte position pos
// under frame fr and junction context ctx. It returns true on the first
// (highest-priority) success.
func (m *machine) match(pc uint32, pos int, fr *frame, ctx nodeCtx) bool {
	m.steps++
	if m.steps > stepLimit {
		m.aborted = true
		return false
	}
	inst := &m.prog.Inst[pc]
	switch inst.Op {
	case syntax.InstFail:
		return false

	case syntax.InstAlt, syntax.InstAltMatch:
		if m.match(inst.Out, pos, fr, ctx) {
			return true
		}
		return m.match(inst.Arg, pos, fr, ctx)

	case syntax.InstNop:
		return m.match(inst.Out, pos, fr, ctx)

	case syntax.InstCapture:
		if int(inst.Arg) < len(m.cap) {
			// Snap the recorded boundary to a significant byte: capture-open
			// (even arg) moves forward past leading insignificant bytes,
			// capture-close (odd arg) moves back past trailing ones, so a group
			// never includes surrounding whitespace or comments. Matching still
			// continues at the real position.
			val := pos
			if fr != nil {
				if inst.Arg&1 == 0 {
					val = m.snapFwd(pos, m.regionEnd(fr))
				} else {
					val = m.snapBack(pos)
				}
			}
			old := m.cap[inst.Arg]
			m.cap[inst.Arg] = val
			if m.match(inst.Out, pos, fr, ctx) {
				return true
			}
			m.cap[inst.Arg] = old
			return false
		}
		return m.match(inst.Out, pos, fr, ctx)

	case syntax.InstEmptyWidth:
		if !m.emptyOK(syntax.EmptyOp(inst.Arg), pos) {
			return false
		}
		return m.match(inst.Out, pos, fr, ctx)

	case syntax.InstMatch:
		if fr != nil {
			return false // a node region must end via its EXIT marker
		}
		m.cap[1] = pos
		return true

	case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
		if r, ok := singleRune(inst); ok {
			switch r {
			case markerEnter:
				return m.enterNode(inst, pos, fr, ctx)
			case markerExit:
				return m.exitNode(inst, pos, fr, ctx)
			case markerGap:
				ng := ctx
				ng.pendGap = true
				return m.match(inst.Out, pos, fr, ng)
			}
		}
		return m.consume(inst, pos, fr, ctx)

	default:
		return false
	}
}

// junctionOK applies the whitespace rule at the boundary between the previously
// consumed significant character and the next one. Whitespace only matters when
// both sides are word characters (where it would merge or split a token); there
// the pattern's whitespace must agree with the source's separation. Anywhere
// else (operators, punctuation) whitespace is freely ignorable.
func junctionOK(ctx nodeCtx, nextWord, sep bool) bool {
	if ctx.lastEnd < 0 {
		return true // first character in the region
	}
	if ctx.lastWord && nextWord {
		return ctx.pendGap == sep
	}
	return true
}

// enterNode resolves the AST nodes starting at pos and tries each as the bounds
// for the inner pattern. Larger nodes are tried first (greedy); the exact
// landing requirement in exitNode decides which actually fits.
func (m *machine) enterNode(inst *syntax.Inst, pos int, fr *frame, ctx nodeCtx) bool {
	if m.source == nil {
		return false
	}
	start := pos
	if fr != nil {
		// The node-group sits inside another node region: apply the junction
		// rule between the surrounding context and the node's first character.
		q := pos
		for q < m.regionEnd(fr) && !m.source.significant(q) {
			q++
		}
		if !junctionOK(ctx, isWordAt(m.src, q, m.regionEnd(fr)), q > ctx.lastEnd) {
			return false
		}
		start = q
	}
	ends := m.source.endsForStart(start)
	for k := len(ends) - 1; k >= 0; k-- {
		child := &frame{end: ends[k], parent: fr}
		if m.match(inst.Out, start, child, rootCtx) {
			return true
		}
	}
	return false
}

// exitNode verifies that the inner pattern consumed exactly the significant
// content of the node — only insignificant bytes may remain up to the node end
// — then continues in the parent region at the node end, treating the node as a
// single consumed unit for junction purposes.
func (m *machine) exitNode(inst *syntax.Inst, pos int, fr *frame, ctx nodeCtx) bool {
	if fr == nil {
		return false
	}
	q := pos
	for q < fr.end && !m.source.significant(q) {
		q++
	}
	if q != fr.end {
		return false
	}
	pctx := nodeCtx{lastEnd: fr.end, lastWord: m.lastWordBefore(fr.end)}
	return m.match(inst.Out, fr.end, fr.parent, pctx)
}

// consume matches a single rune at pos. Inside a node-group it first skips any
// insignificant bytes (whitespace/comments), then enforces the junction rule.
func (m *machine) consume(inst *syntax.Inst, pos int, fr *frame, ctx nodeCtx) bool {
	end := m.regionEnd(fr)
	if fr == nil {
		// Outer region: ordinary regex over raw bytes.
		if pos >= end {
			return false
		}
		r, w := utf8.DecodeRune(m.src[pos:end])
		if w == 0 || !inst.MatchRune(r) {
			return false
		}
		return m.match(inst.Out, pos+w, fr, ctx)
	}

	q := pos
	for q < end && !m.source.significant(q) {
		q++
	}
	if q >= end {
		return false
	}
	nextWord := isWordAt(m.src, q, end)
	if !junctionOK(ctx, nextWord, q > ctx.lastEnd) {
		return false
	}
	r, w := utf8.DecodeRune(m.src[q:end])
	if w == 0 || !inst.MatchRune(r) {
		return false
	}
	return m.match(inst.Out, q+w, fr, nodeCtx{lastEnd: q + w, lastWord: nextWord})
}

func (m *machine) emptyOK(op syntax.EmptyOp, pos int) bool {
	var before, after rune = -1, -1
	if pos > 0 {
		before, _ = utf8.DecodeLastRune(m.src[:pos])
	}
	if pos < len(m.src) {
		after, _ = utf8.DecodeRune(m.src[pos:])
	}
	return syntax.EmptyOpContext(before, after)&op == op
}

// isWordRune reports whether r is an identifier character for the purpose of
// the whitespace-junction rule (Go identifiers allow Unicode letters/digits).
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isWordAt(src []byte, i, end int) bool {
	r, _ := utf8.DecodeRune(src[i:end])
	return isWordRune(r)
}

// snapFwd advances pos past insignificant bytes, bounded by end.
func (m *machine) snapFwd(pos, end int) int {
	for pos < end && !m.source.significant(pos) {
		pos++
	}
	return pos
}

// snapBack moves pos back past trailing insignificant bytes.
func (m *machine) snapBack(pos int) int {
	for pos > 0 && !m.source.significant(pos-1) {
		pos--
	}
	return pos
}

// lastWordBefore reports whether the last significant character before end is a
// word character.
func (m *machine) lastWordBefore(end int) bool {
	i := end - 1
	for i >= 0 && !m.source.significant(i) {
		i--
	}
	if i < 0 {
		return false
	}
	for i > 0 && !utf8.RuneStart(m.src[i]) {
		i--
	}
	r, _ := utf8.DecodeRune(m.src[i:])
	return isWordRune(r)
}

// singleRune reports the single rune an instruction matches, if it matches
// exactly one rune value (covering folded single-rune forms too).
func singleRune(inst *syntax.Inst) (rune, bool) {
	switch inst.Op {
	case syntax.InstRune1:
		return inst.Rune[0], true
	case syntax.InstRune:
		if len(inst.Rune) == 1 {
			return inst.Rune[0], true
		}
		if len(inst.Rune) == 2 && inst.Rune[0] == inst.Rune[1] {
			return inst.Rune[0], true
		}
	}
	return 0, false
}
