package regast

import (
	"fmt"
	"strings"
)

// Marker runes. These are private-use code points injected into the lowered
// pattern to carry node-group semantics through the standard regexp compiler.
// The executor intercepts them; they never match real source. Patterns that
// contain these literal runes are unsupported.
const (
	markerEnter rune = 0xE010 // start of a node-group region
	markerExit  rune = 0xE011 // end of a node-group region
	markerGap   rune = 0xE012 // a required gap (whitespace/comment) assertion
)

// Node-group brackets.
const (
	openBracket  = '⦃' // U+2983 LEFT WHITE CURLY BRACKET
	closeBracket = '⦄' // U+2984 RIGHT WHITE CURLY BRACKET
)

var (
	enterLit = fmt.Sprintf(`\x{%X}`, markerEnter)
	exitLit  = fmt.Sprintf(`\x{%X}`, markerExit)
	gapLit   = fmt.Sprintf(`\x{%X}`, markerGap)
)

// lower rewrites a node-aware pattern into an ordinary regexp string that the
// standard syntax package can parse. Each node-group ⦃…⦄ becomes a capturing
// group wrapping an ENTER marker, the lowered inner pattern, and an EXIT
// marker. Inside a node-group, runs of whitespace become GAP markers (with
// leading and trailing whitespace trimmed); outside, the pattern is left as
// ordinary regex.
func lower(pattern string) (string, error) {
	runes := []rune(pattern)
	var b strings.Builder
	i := 0
	if err := lowerSeq(runes, &i, false, &b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// lowerSeq processes runes starting at *i. When inNode is true it returns once
// it reaches the ⦄ that closes the current node-group, leaving *i pointing at
// that bracket for the caller to consume.
func lowerSeq(runes []rune, i *int, inNode bool, b *strings.Builder) error {
	emittedAtom := false
	pendingGap := false
	flushGap := func() {
		if pendingGap {
			b.WriteString(gapLit)
			pendingGap = false
		}
	}

	for *i < len(runes) {
		r := runes[*i]
		switch {
		case r == closeBracket:
			if inNode {
				return nil // caller consumes the bracket
			}
			return fmt.Errorf("regast: unmatched %c at offset %d", closeBracket, *i)

		case r == openBracket:
			flushGap()
			b.WriteString("(")
			b.WriteString(enterLit)
			*i++ // consume ⦃
			if err := lowerSeq(runes, i, true, b); err != nil {
				return err
			}
			if *i >= len(runes) || runes[*i] != closeBracket {
				return fmt.Errorf("regast: missing %c for %c", closeBracket, openBracket)
			}
			*i++ // consume ⦄
			b.WriteString(exitLit)
			b.WriteString(")")
			emittedAtom = true

		case r == '\\':
			flushGap()
			b.WriteRune('\\')
			*i++
			if *i < len(runes) {
				b.WriteRune(runes[*i])
				*i++
			}
			emittedAtom = true

		case r == '[':
			flushGap()
			if err := copyClass(runes, i, b); err != nil {
				return err
			}
			emittedAtom = true

		case inNode && isPatternSpace(r):
			for *i < len(runes) && isPatternSpace(runes[*i]) {
				*i++
			}
			if emittedAtom {
				pendingGap = true // deferred; trailing whitespace is dropped
			}

		default:
			flushGap()
			b.WriteRune(r)
			*i++
			emittedAtom = true
		}
	}

	if inNode {
		return fmt.Errorf("regast: missing %c at end of pattern", closeBracket)
	}
	return nil
}

// copyClass copies a character class [...] verbatim, including any whitespace,
// so class contents keep their literal meaning even inside a node-group.
func copyClass(runes []rune, i *int, b *strings.Builder) error {
	b.WriteRune('[') // runes[*i] == '['
	*i++
	if *i < len(runes) && runes[*i] == '^' {
		b.WriteRune('^')
		*i++
	}
	if *i < len(runes) && runes[*i] == ']' {
		b.WriteRune(']') // leading ] is a literal class member
		*i++
	}
	for *i < len(runes) {
		r := runes[*i]
		if r == '\\' {
			b.WriteRune('\\')
			*i++
			if *i < len(runes) {
				b.WriteRune(runes[*i])
				*i++
			}
			continue
		}
		b.WriteRune(r)
		*i++
		if r == ']' {
			return nil
		}
	}
	return fmt.Errorf("regast: unterminated [ in pattern")
}

func isPatternSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}
