package sed

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"mohabbat/mohabbat/regast"
	"mohabbat/mohabbat/regast/syntax"
)

type addressKind uint8

const (
	addressLine addressKind = iota
	addressLast
	addressRegex
	addressStep
	addressPlus
	addressTilde
)

type address struct {
	kind   addressKind
	line   int64
	step   int64
	regex  *regast.Regexp
	source []byte
}

type command struct {
	first, second *address
	rangeOn       bool
	negate        bool
	op            byte
	exitStatus    int
	text          []byte
	label         string
	target        int
	regex         *regast.Regexp
	replacement   []byte
	subFlags      substitutionFlags
	transFrom     []rune
	transTo       []rune
}

type substitutionFlags struct {
	global     bool
	print      bool
	printFirst bool
	execute    bool
	ignoreCase bool
	multiline  bool
	occurrence int
	writeFile  string
}

type program struct {
	commands []command
	blockEnd []int
}

type scriptParser struct {
	src       []byte
	pos       int
	opts      options
	lastRegex []byte
}

func compileScript(src string, opts options) (*program, error) {
	p := &scriptParser{src: []byte(src), opts: opts}
	var result program
	labels := make(map[string]int)
	var pending []struct {
		pc    int
		label string
	}
	for {
		p.skipSeparators()
		if p.pos >= len(p.src) {
			break
		}
		cmd, err := p.parseCommand()
		if err != nil {
			return nil, err
		}
		if cmd.op == ':' {
			if cmd.label == "" {
				return nil, fmt.Errorf("empty label")
			}
			if _, exists := labels[cmd.label]; exists {
				return nil, fmt.Errorf("duplicate label %q", cmd.label)
			}
			labels[cmd.label] = len(result.commands)
		}
		if cmd.op == 'b' || cmd.op == 't' || cmd.op == 'T' {
			pending = append(pending, struct {
				pc    int
				label string
			}{len(result.commands), cmd.label})
		}
		result.commands = append(result.commands, cmd)
	}
	for _, branch := range pending {
		if branch.label == "" {
			result.commands[branch.pc].target = len(result.commands)
			continue
		}
		target, ok := labels[branch.label]
		if !ok {
			return nil, fmt.Errorf("undefined label %q", branch.label)
		}
		result.commands[branch.pc].target = target
	}
	ends, err := validateBlocks(result.commands)
	if err != nil {
		return nil, err
	}
	result.blockEnd = ends
	return &result, nil
}

func (p *scriptParser) skipSeparators() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\r', ';':
			p.pos++
		case '\n':
			p.pos++
		case '#':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *scriptParser) parseCommand() (command, error) {
	var cmd command
	first, err := p.parseAddress(false)
	if err != nil {
		return cmd, err
	}
	cmd.first = first
	if p.take(',') {
		second, err := p.parseAddress(true)
		if err != nil {
			return cmd, err
		}
		if second == nil {
			return cmd, fmt.Errorf("missing second address")
		}
		cmd.second = second
		if p.opts.posix && (first.kind == addressLine && first.line == 0 || second.kind == addressPlus || second.kind == addressTilde) {
			return cmd, fmt.Errorf("GNU address extension is disabled by --posix")
		}
	} else if first != nil && first.kind == addressLine && first.line == 0 {
		return cmd, fmt.Errorf("address 0 must be used as the first address of a range")
	}
	p.skipHorizontal()
	if p.take('!') {
		cmd.negate = true
		p.skipHorizontal()
	}
	if p.pos >= len(p.src) {
		return cmd, fmt.Errorf("missing command")
	}
	cmd.op = p.src[p.pos]
	p.pos++
	switch cmd.op {
	case '{', '}':
		return cmd, nil
	case 'a', 'i', 'c':
		p.skipHorizontal()
		if p.take('\\') {
			if p.pos < len(p.src) && p.src[p.pos] == '\n' {
				p.pos++
			}
			cmd.text = p.readText()
		} else if cmd.op == 'a' || cmd.op == 'i' || cmd.op == 'c' {
			cmd.text = p.readText()
		}
		return cmd, nil
	case 's':
		if p.pos >= len(p.src) {
			return cmd, fmt.Errorf("substitution command missing delimiter")
		}
		delim := p.src[p.pos]
		p.pos++
		pattern, err := p.readDelimited(delim)
		if err != nil {
			return cmd, fmt.Errorf("invalid substitution regexp: %w", err)
		}
		replacement, err := p.readDelimited(delim)
		if err != nil {
			return cmd, fmt.Errorf("unterminated substitution")
		}
		cmd.regex, err = p.compileRegex(pattern, false, false)
		if err != nil {
			return cmd, err
		}
		cmd.replacement = replacement
		cmd.subFlags, err = p.readSubstitutionFlags()
		if err != nil {
			return cmd, err
		}
		if cmd.subFlags.ignoreCase || cmd.subFlags.multiline {
			cmd.regex, err = p.compileRegex(pattern, cmd.subFlags.ignoreCase, cmd.subFlags.multiline)
			if err != nil {
				return cmd, err
			}
		}
		if cmd.subFlags.writeFile != "" && p.opts.sandbox {
			return cmd, fmt.Errorf("w command is disabled by --sandbox")
		}
		return cmd, nil
	case 'y':
		if p.pos >= len(p.src) {
			return cmd, fmt.Errorf("transliteration command missing delimiter")
		}
		delim := p.src[p.pos]
		p.pos++
		from, err := p.readDelimited(delim)
		if err != nil {
			return cmd, fmt.Errorf("unterminated transliteration source")
		}
		to, err := p.readDelimited(delim)
		if err != nil {
			return cmd, fmt.Errorf("unterminated transliteration target")
		}
		cmd.transFrom, cmd.transTo, err = transliterationRunes(from), transliterationRunes(to), nil
		if len(cmd.transFrom) != len(cmd.transTo) {
			return cmd, fmt.Errorf("transliteration strings have different lengths")
		}
		return cmd, nil
	case 'b', 't', 'T', ':':
		if p.opts.posix && cmd.op == 'T' {
			return cmd, fmt.Errorf("GNU command T is disabled by --posix")
		}
		cmd.label = strings.TrimSpace(string(p.readLine()))
		return cmd, nil
	case 'q', 'Q':
		arg := strings.TrimSpace(string(p.readLine()))
		if arg != "" {
			if p.opts.posix {
				return cmd, fmt.Errorf("GNU exit status argument is disabled by --posix")
			}
			status, err := strconv.Atoi(arg)
			if err != nil || status < 0 || status > 255 {
				return cmd, fmt.Errorf("invalid exit status %q", arg)
			}
			cmd.exitStatus = status
		}
		if p.opts.posix && cmd.op == 'Q' {
			return cmd, fmt.Errorf("GNU command Q is disabled by --posix")
		}
		return cmd, nil
	case 'r', 'R', 'w', 'W', 'e':
		cmd.label = strings.TrimSpace(string(p.readLine()))
		if (cmd.op == 'r' || cmd.op == 'R' || cmd.op == 'w' || cmd.op == 'W') && cmd.label == "" {
			return cmd, fmt.Errorf("%c command requires a file name", cmd.op)
		}
		if p.opts.posix && (cmd.op == 'R' || cmd.op == 'W' || cmd.op == 'e') {
			return cmd, fmt.Errorf("GNU command %c is disabled by --posix", cmd.op)
		}
		if p.opts.sandbox && (cmd.op == 'r' || cmd.op == 'R' || cmd.op == 'w' || cmd.op == 'W' || cmd.op == 'e') {
			return cmd, fmt.Errorf("%c command is disabled by --sandbox", cmd.op)
		}
		return cmd, nil
	case 'd', 'D', 'g', 'G', 'h', 'H', 'x', 'p', 'P', 'n', 'N', '=', 'l', 'z', 'F', 'v':
		if cmd.op == 'z' || cmd.op == 'F' || cmd.op == 'v' {
			if p.opts.posix {
				return cmd, fmt.Errorf("GNU command %c is disabled by --posix", cmd.op)
			}
		}
		return cmd, nil
	default:
		return cmd, fmt.Errorf("unknown command %q", cmd.op)
	}
}

func (p *scriptParser) parseAddress(relative bool) (*address, error) {
	p.skipHorizontal()
	if p.pos >= len(p.src) {
		return nil, nil
	}
	if relative && (p.src[p.pos] == '+' || p.src[p.pos] == '~') {
		kind := addressPlus
		if p.src[p.pos] == '~' {
			kind = addressTilde
		}
		p.pos++
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		if start == p.pos {
			return nil, fmt.Errorf("missing relative address count")
		}
		count, _ := strconv.ParseInt(string(p.src[start:p.pos]), 10, 64)
		if count <= 0 {
			return nil, fmt.Errorf("relative address count must be positive")
		}
		return &address{kind: kind, line: count}, nil
	}
	switch p.src[p.pos] {
	case '$':
		p.pos++
		return &address{kind: addressLast}, nil
	case '/':
		p.pos++
		pattern, err := p.readDelimited('/')
		if err != nil {
			return nil, fmt.Errorf("unterminated address regexp")
		}
		re, err := p.compileRegex(pattern, false, false)
		if err != nil {
			return nil, err
		}
		return &address{kind: addressRegex, regex: re, source: pattern}, nil
	case '\\':
		if p.pos+1 < len(p.src) && p.src[p.pos+1] == '%' {
			if p.opts.posix {
				return nil, fmt.Errorf("GNU address delimiter is disabled by --posix")
			}
			p.pos += 2
			if p.pos >= len(p.src) {
				return nil, fmt.Errorf("missing GNU address delimiter")
			}
			delim := p.src[p.pos]
			p.pos++
			pattern, err := p.readDelimited(delim)
			if err != nil {
				return nil, fmt.Errorf("unterminated address regexp")
			}
			re, err := compileRegex(pattern, p.opts, false, false)
			if err != nil {
				return nil, err
			}
			return &address{kind: addressRegex, regex: re, source: pattern}, nil
		}
		return nil, nil
	default:
		if p.src[p.pos] < '0' || p.src[p.pos] > '9' {
			return nil, nil
		}
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		number, _ := strconv.ParseInt(string(p.src[start:p.pos]), 10, 64)
		if p.take('~') {
			if p.opts.posix {
				return nil, fmt.Errorf("GNU step address is disabled by --posix")
			}
			stepStart := p.pos
			for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
				p.pos++
			}
			if stepStart == p.pos {
				return nil, fmt.Errorf("missing step in address")
			}
			step, _ := strconv.ParseInt(string(p.src[stepStart:p.pos]), 10, 64)
			if step <= 0 {
				return nil, fmt.Errorf("address step must be positive")
			}
			return &address{kind: addressStep, line: number, step: step}, nil
		}
		if number == 0 && !p.opts.posix {
			return &address{kind: addressLine, line: 0}, nil
		}
		return &address{kind: addressLine, line: number}, nil
	}
}

func (p *scriptParser) readDelimited(delim byte) ([]byte, error) {
	var out []byte
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		if c == delim {
			return out, nil
		}
		if c == '\\' && p.pos < len(p.src) {
			next := p.src[p.pos]
			p.pos++
			if next == delim {
				out = append(out, delim)
			} else {
				out = append(out, '\\', next)
			}
			continue
		}
		out = append(out, c)
	}
	return nil, fmt.Errorf("missing delimiter")
}

func (p *scriptParser) readSubstitutionFlags() (substitutionFlags, error) {
	var flags substitutionFlags
	for p.pos < len(p.src) && p.src[p.pos] != '\n' && p.src[p.pos] != ';' {
		if p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\r' {
			p.pos++
			continue
		}
		c := p.src[p.pos]
		p.pos++
		switch c {
		case 'g':
			flags.global = true
		case 'p':
			flags.print = true
		case 'P':
			if p.opts.posix {
				return flags, fmt.Errorf("GNU substitution flag P is disabled by --posix")
			}
			flags.printFirst = true
		case 'e':
			if p.opts.posix {
				return flags, fmt.Errorf("GNU substitution flag e is disabled by --posix")
			}
			flags.execute = true
		case 'i', 'I':
			if p.opts.posix {
				return flags, fmt.Errorf("GNU substitution flag %c is disabled by --posix", c)
			}
			flags.ignoreCase = true
		case 'm', 'M':
			if p.opts.posix {
				return flags, fmt.Errorf("GNU substitution flag %c is disabled by --posix", c)
			}
			flags.multiline = true
		case 'w':
			name := strings.TrimSpace(string(p.readLine()))
			if name == "" {
				return flags, fmt.Errorf("w flag requires a file")
			}
			flags.writeFile = name
			return flags, nil
		default:
			if c >= '1' && c <= '9' {
				start := p.pos - 1
				for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
					p.pos++
				}
				n, err := strconv.Atoi(string(p.src[start:p.pos]))
				if err != nil {
					return flags, fmt.Errorf("invalid substitution occurrence")
				}
				flags.occurrence = n
			} else {
				return flags, fmt.Errorf("unknown substitution flag %q", c)
			}
		}
	}
	return flags, nil
}

func (p *scriptParser) readText() []byte {
	var out []byte
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		if c == '\\' && p.pos < len(p.src) && p.src[p.pos] == '\n' {
			p.pos++
			out = append(out, '\n')
			continue
		}
		if c == '\n' {
			break
		}
		out = append(out, c)
	}
	return out
}

func (p *scriptParser) readLine() []byte {
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] != '\n' && p.src[p.pos] != ';' {
		p.pos++
	}
	line := bytes.TrimSpace(p.src[start:p.pos])
	if p.pos < len(p.src) && p.src[p.pos] == '\n' {
		p.pos++
	}
	return append([]byte(nil), line...)
}

func (p *scriptParser) skipHorizontal() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\r') {
		p.pos++
	}
}

func (p *scriptParser) take(c byte) bool {
	if p.pos < len(p.src) && p.src[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func validateBlocks(commands []command) ([]int, error) {
	ends := make([]int, len(commands))
	stack := make([]int, 0)
	for i, cmd := range commands {
		switch cmd.op {
		case '{':
			stack = append(stack, i)
		case '}':
			if len(stack) == 0 {
				return nil, fmt.Errorf("unexpected }")
			}
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			ends[start] = i
			ends[i] = i
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("missing }")
	}
	return ends, nil
}

func dumpProgram(w io.Writer, p *program) error {
	for i, cmd := range p.commands {
		if _, err := fmt.Fprintf(w, "%d: %c %q\n", i, cmd.op, cmd.text); err != nil {
			return err
		}
	}
	return nil
}

func compileRegex(pattern []byte, opts options, fold, multiline bool) (*regast.Regexp, error) {
	translated, err := translateBRE(pattern, opts.extended, opts.posix)
	if err != nil {
		return nil, err
	}
	flags := syntax.Perl&^syntax.OneLine | syntax.Backref | syntax.DotNL
	if !multiline {
		flags |= syntax.OneLine
	}
	if fold {
		flags |= syntax.FoldCase
	}
	re, err := regast.CompileBytesWithFlags(translated, flags)
	if err != nil {
		return nil, fmt.Errorf("regexp %q: %w", pattern, err)
	}
	return re, nil
}

func (p *scriptParser) compileRegex(pattern []byte, fold, multiline bool) (*regast.Regexp, error) {
	if len(pattern) == 0 {
		if len(p.lastRegex) == 0 {
			return nil, fmt.Errorf("empty regular expression with no previous expression")
		}
		pattern = p.lastRegex
	} else {
		p.lastRegex = append(p.lastRegex[:0], pattern...)
	}
	return compileRegex(pattern, p.opts, fold, multiline)
}

func transliterationRunes(raw []byte) []rune {
	runes := make([]rune, 0, len(raw))
	for len(raw) > 0 {
		if raw[0] == '\\' && len(raw) > 1 {
			raw = raw[1:]
			switch raw[0] {
			case 'n':
				runes = append(runes, '\n')
				raw = raw[1:]
				continue
			case 't':
				runes = append(runes, '\t')
				raw = raw[1:]
				continue
			case 'r':
				runes = append(runes, '\r')
				raw = raw[1:]
				continue
			case 'f':
				runes = append(runes, '\f')
				raw = raw[1:]
				continue
			case 'v':
				runes = append(runes, '\v')
				raw = raw[1:]
				continue
			case 'x':
				count := 0
				for count < 2 && count+1 < len(raw) && isHexDigit(raw[count+1]) {
					count++
				}
				if count > 0 {
					value, _ := strconv.ParseUint(string(raw[1:count+1]), 16, 8)
					runes = append(runes, rune(value))
					raw = raw[count+1:]
					continue
				}
			default:
				if raw[0] >= '0' && raw[0] <= '7' {
					count := 0
					for count < 3 && count < len(raw) && raw[count] >= '0' && raw[count] <= '7' {
						count++
					}
					value, _ := strconv.ParseUint(string(raw[:count]), 8, 8)
					runes = append(runes, rune(value))
					raw = raw[count:]
					continue
				}
			}
		}
		r, n := utf8.DecodeRune(raw)
		if n == 0 {
			break
		}
		if r == utf8.RuneError && n == 1 {
			r = unicode.MaxRune + 1 + rune(raw[0])
		}
		runes = append(runes, r)
		raw = raw[n:]
	}
	return runes
}

func isHexDigit(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}
