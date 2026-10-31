package sed

import (
	"bytes"
	"fmt"
	"unicode"
	"unicode/utf8"
)

func translateBRE(pattern []byte, extended, posix bool) ([]byte, error) {
	var out bytes.Buffer
	inClass := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c == '\\' && i+1 < len(pattern) {
			next := pattern[i+1]
			if inClass {
				out.WriteByte(c)
				out.WriteByte(next)
				i++
				continue
			}
			if !extended && (next == '(' || next == ')' || next == '{' || next == '}') {
				out.WriteByte(next)
				i++
				continue
			}
			if !extended && (next == '+' || next == '?' || next == '|') {
				if posix {
					return nil, fmt.Errorf("GNU BRE operator \\%c is disabled by --posix", next)
				}
				out.WriteByte(next)
				i++
				continue
			}
			if !extended && next == '{' {
				if end := bytes.Index(pattern[i+2:], []byte(`\}`)); end >= 0 {
					out.WriteByte('{')
					out.Write(pattern[i+2 : i+2+end])
					out.WriteByte('}')
					i += 2 + end + 1
					continue
				}
			}
			if extended && (next == '(' || next == ')' || next == '{' || next == '}') {
				out.WriteByte('\\')
				out.WriteByte(next)
				i++
				continue
			}
			out.WriteByte('\\')
			out.WriteByte(next)
			i++
			continue
		}
		if c == '[' && !inClass {
			inClass = true
			out.WriteByte(c)
			continue
		}
		if c == ']' && inClass {
			inClass = false
			out.WriteByte(c)
			continue
		}
		if !inClass && !extended && bytes.ContainsRune([]byte("(){}+?|"), rune(c)) {
			out.WriteByte('\\')
		}
		out.WriteByte(c)
	}
	return out.Bytes(), nil
}

func expandSedReplacement(dst, replacement, src []byte, match []int) []byte {
	startLen := len(dst)
	for i := 0; i < len(replacement); i++ {
		switch replacement[i] {
		case '&':
			if match[0] >= 0 && match[1] >= match[0] {
				dst = append(dst, src[match[0]:match[1]]...)
			}
		case '\\':
			i++
			if i >= len(replacement) {
				dst = append(dst, '\\')
				break
			}
			switch c := replacement[i]; {
			case c >= '1' && c <= '9':
				group := int(c - '0')
				start, end := 2*group, 2*group+1
				if end < len(match) && match[start] >= 0 && match[end] >= match[start] {
					dst = append(dst, src[match[start]:match[end]]...)
				}
			case c == 'n':
				dst = append(dst, '\n')
			case c == 't':
				dst = append(dst, '\t')
			case c == 'r':
				dst = append(dst, '\r')
			case c == 'L' || c == 'U' || c == 'l' || c == 'u' || c == 'E':
				// GNU case-conversion escapes are applied in a second pass.
				dst = append(dst, '\\', c)
			default:
				dst = append(dst, c)
			}
		default:
			dst = append(dst, replacement[i])
		}
	}
	dst = append(dst[:startLen], applyCaseConversion(dst[startLen:])...)
	return dst
}

func applyCaseConversion(in []byte) []byte {
	var out []byte
	mode := rune(0)
	one := false
	for i := 0; i < len(in); {
		if in[i] == '\\' && i+1 < len(in) {
			switch in[i+1] {
			case 'L', 'U':
				mode = rune(in[i+1])
				one = false
				i += 2
				continue
			case 'l', 'u':
				mode = rune(in[i+1])
				one = true
				i += 2
				continue
			case 'E':
				mode = 0
				one = false
				i += 2
				continue
			}
		}
		r, n := decodeRunePreservingInvalid(in[i:])
		raw := in[i : i+n]
		if r > unicode.MaxRune {
			out = append(out, raw...)
			i += n
			continue
		}
		switch mode {
		case 'L', 'l':
			r = unicode.ToLower(r)
		case 'U', 'u':
			r = unicode.ToUpper(r)
		default:
			out = append(out, raw...)
			i += n
			continue
		}
		if r <= 0xff && len(raw) == 1 && raw[0] >= 0x80 {
			out = append(out, byte(r))
		} else {
			out = append(out, []byte(string(r))...)
		}
		i += n
		if one {
			mode = 0
			one = false
		}
	}
	return out
}

func decodeRunePreservingInvalid(data []byte) (rune, int) {
	r, n := utf8.DecodeRune(data)
	if r == utf8.RuneError && n == 1 {
		return unicode.MaxRune + 1 + rune(data[0]), 1
	}
	return r, n
}
