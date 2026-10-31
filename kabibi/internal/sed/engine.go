package sed

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"unicode"

	"github.com/u-root/u-root/pkg/core"
)

type lineRecord struct {
	data         []byte
	terminated   bool
	name         string
	fileID       int
	globalNumber int64
	fileNumber   int64
	lastAll      bool
	lastFile     bool
}

type lineStream struct {
	files       []string
	nextFile    int
	current     *os.File
	reader      *bufio.Reader
	name        string
	fileID      int
	globalLine  int64
	fileLine    int64
	delim       byte
	stdin       io.Reader
	stdinReader *bufio.Reader
	dir         string
	pending     *lineRecord
	done        bool
}

func newLineStream(files []string, delim byte, stdin io.Reader) *lineStream {
	return &lineStream{files: files, delim: delim, stdin: stdin}
}

func (s *lineStream) openNext() error {
	if s.current != nil {
		_ = s.current.Close()
		s.current = nil
	}
	for s.nextFile < len(s.files) {
		name := s.files[s.nextFile]
		s.nextFile++
		s.fileID++
		s.fileLine = 0
		s.name = name
		if name == "-" {
			if s.stdinReader == nil {
				r := s.stdin
				if r == nil {
					r = os.Stdin
				}
				s.stdinReader = bufio.NewReader(r)
			}
			s.reader = s.stdinReader
			return nil
		}
		openName := name
		if !filepath.IsAbs(openName) {
			openName = filepath.Join(s.dir, openName)
		}
		f, err := os.Open(openName)
		if err != nil {
			return fmt.Errorf("open %q: %w", name, err)
		}
		s.current = f
		s.reader = bufio.NewReader(f)
		return nil
	}
	s.reader = nil
	s.done = true
	return nil
}

func (s *lineStream) rawNext() (*lineRecord, error) {
	for !s.done {
		if s.reader == nil {
			if err := s.openNext(); err != nil {
				return nil, err
			}
			if s.done {
				break
			}
		}
		data, err := s.reader.ReadBytes(s.delim)
		if len(data) > 0 {
			terminated := data[len(data)-1] == s.delim
			if terminated {
				data = data[:len(data)-1]
			}
			s.globalLine++
			s.fileLine++
			return &lineRecord{
				data:         data,
				terminated:   terminated,
				name:         s.name,
				fileID:       s.fileID,
				globalNumber: s.globalLine,
				fileNumber:   s.fileLine,
			}, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read %q: %w", s.name, err)
		}
		if err := s.openNext(); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (s *lineStream) next() (*lineRecord, error) {
	if s.pending == nil {
		record, err := s.rawNext()
		if err != nil || record == nil {
			return nil, err
		}
		s.pending = record
	}
	current := s.pending
	next, err := s.rawNext()
	if err != nil {
		s.pending = nil
		return nil, err
	}
	s.pending = next
	current.lastAll = next == nil
	current.lastFile = next == nil || next.fileID != current.fileID
	return current, nil
}

func (s *lineStream) close() {
	if s.current != nil {
		_ = s.current.Close()
		s.current = nil
	}
}

func runFiles(ctx context.Context, p *program, files []string, dir string, opts options, stdin io.Reader, stdout io.Writer, ioEnv core.IO) (int, bool, error) {
	delim := byte('\n')
	if opts.nullData {
		delim = 0
	}
	stream := newLineStream(files, delim, stdin)
	stream.dir = dir
	defer stream.close()
	state := execState{
		ctx: ctx, program: p, opts: opts, delim: delim, dir: dir, ioEnv: ioEnv,
		out: stdout, rangeState: make([]bool, len(p.commands)), rangeStart: make([]int64, len(p.commands)),
		writeFiles: make(map[string]*os.File), readFiles: make(map[string]*os.File),
		readers: make(map[string]*bufio.Reader),
	}
	defer state.closeWriteFiles()
	defer state.closeReadFiles()
	if err := state.prepareWriteFiles(); err != nil {
		return 0, false, err
	}
	var previousFile int
	for {
		record, err := stream.next()
		if err != nil {
			return 0, false, err
		}
		if record == nil {
			return state.exitStatus, false, nil
		}
		if opts.separate && previousFile != 0 && record.fileID != previousFile {
			clear(state.rangeState)
			state.substituted = false
		}
		state.record = record
		state.pattern = append(state.pattern[:0], record.data...)
		state.terminated = record.terminated
		if err := state.runCycle(stream); err != nil {
			return 0, false, err
		}
		if state.quit {
			return state.exitStatus, true, nil
		}
		previousFile = state.record.fileID
	}
}

type execState struct {
	ctx         context.Context
	program     *program
	opts        options
	delim       byte
	dir         string
	ioEnv       core.IO
	out         io.Writer
	record      *lineRecord
	pattern     []byte
	hold        []byte
	terminated  bool
	substituted bool
	quit        bool
	exitStatus  int
	rangeState  []bool
	rangeStart  []int64
	writeFiles  map[string]*os.File
	readFiles   map[string]*os.File
	readers     map[string]*bufio.Reader
	appendQueue [][]byte
}

func (s *execState) runCycle(stream *lineStream) error {
	s.appendQueue = s.appendQueue[:0]
	s.substituted = false
	for {
		restartScript := false
		printPattern := true
		for pc := 0; pc < len(s.program.commands); {
			if err := s.ctx.Err(); err != nil {
				return err
			}
			cmd := s.program.commands[pc]
			selected, endOfRange := s.selected(pc, cmd)
			if !selected {
				if cmd.op == '{' {
					pc = s.program.blockEnd[pc] + 1
				} else {
					pc++
				}
				continue
			}
			switch cmd.op {
			case '{', '}', ':':
				pc++
			case 'd':
				printPattern = false
				pc = len(s.program.commands)
			case 'D':
				if i := bytes.IndexByte(s.pattern, s.delim); i >= 0 {
					s.pattern = append(s.pattern[:0], s.pattern[i+1:]...)
					restartScript = true
					pc = len(s.program.commands)
					break
				} else {
					printPattern = false
					pc = len(s.program.commands)
				}
			case 'p':
				if err := s.printSpace(s.pattern, true); err != nil {
					return err
				}
				pc++
			case 'P':
				space := s.pattern
				if i := bytes.IndexByte(space, s.delim); i >= 0 {
					space = space[:i]
				}
				if err := s.printSpace(space, true); err != nil {
					return err
				}
				pc++
			case 'h':
				s.hold = append(s.hold[:0], s.pattern...)
				pc++
			case 'H':
				s.hold = append(s.hold, s.delim)
				s.hold = append(s.hold, s.pattern...)
				pc++
			case 'g':
				s.pattern = append(s.pattern[:0], s.hold...)
				pc++
			case 'G':
				s.pattern = append(s.pattern, s.delim)
				s.pattern = append(s.pattern, s.hold...)
				pc++
			case 'x':
				s.pattern, s.hold = s.hold, s.pattern
				pc++
			case 's':
				did, err := s.substitute(cmd)
				if err != nil {
					return err
				}
				if did {
					s.substituted = true
					if cmd.subFlags.print {
						if err := s.printSpace(s.pattern, true); err != nil {
							return err
						}
					}
					if cmd.subFlags.printFirst {
						space := s.pattern
						if i := bytes.IndexByte(space, s.delim); i >= 0 {
							space = space[:i]
						}
						if err := s.printSpace(space, true); err != nil {
							return err
						}
					}
					if cmd.subFlags.writeFile != "" {
						if err := s.writeSpace(cmd.subFlags.writeFile, s.pattern, true, false); err != nil {
							return err
						}
					}
					if cmd.subFlags.execute {
						if err := s.executePattern(); err != nil {
							return err
						}
					}
				}
				pc++
			case 'a':
				s.appendQueue = append(s.appendQueue, append([]byte(nil), cmd.text...))
				pc++
			case 'i':
				if err := s.printSpace(cmd.text, true); err != nil {
					return err
				}
				pc++
			case 'c':
				printPattern = false
				if cmd.second == nil || endOfRange || cmd.negate {
					s.appendQueue = append(s.appendQueue, append([]byte(nil), cmd.text...))
				}
				pc = len(s.program.commands)
			case 'n':
				if !s.opts.quiet {
					if err := s.printSpace(s.pattern, s.terminated); err != nil {
						return err
					}
				}
				if err := s.flushAppendQueue(); err != nil {
					return err
				}
				oldFile := s.record.fileID
				next, err := stream.next()
				if err != nil {
					return err
				}
				if next == nil {
					s.quit = true
					return nil
				}
				s.record = next
				s.pattern = append(s.pattern[:0], next.data...)
				s.terminated = next.terminated
				if s.opts.separate && next.fileID != oldFile {
					s.resetFileState(next.fileID)
				}
				s.substituted = false
				pc++
			case 'N':
				oldFile := s.record.fileID
				next, err := stream.next()
				if err != nil {
					return err
				}
				if next == nil {
					s.quit = true
					return nil
				}
				s.pattern = append(s.pattern, s.delim)
				s.pattern = append(s.pattern, next.data...)
				s.record = next
				s.terminated = next.terminated
				if s.opts.separate && next.fileID != oldFile {
					s.resetFileState(next.fileID)
				}
				s.substituted = false
				pc++
			case 'q', 'Q':
				if cmd.op == 'q' && !s.opts.quiet {
					if err := s.printSpace(s.pattern, s.terminated); err != nil {
						return err
					}
				}
				if cmd.op == 'q' {
					if err := s.flushAppendQueue(); err != nil {
						return err
					}
				}
				s.exitStatus = cmd.exitStatus
				s.quit = true
				return nil
			case '=':
				number := s.record.globalNumber
				if s.opts.separate {
					number = s.record.fileNumber
				}
				if _, err := fmt.Fprintf(s.out, "%d%c", number, s.delim); err != nil {
					return err
				}
				pc++
			case 'l':
				if err := s.listSpace(); err != nil {
					return err
				}
				pc++
			case 'z':
				s.pattern = s.pattern[:0]
				pc++
			case 'F':
				if _, err := fmt.Fprintf(s.out, "%s%c", s.record.name, s.delim); err != nil {
					return err
				}
				pc++
			case 'y':
				s.transliterate(cmd)
				pc++
			case 'b':
				pc = cmd.target
			case 't':
				if s.substituted {
					s.substituted = false
					pc = cmd.target
				} else {
					pc++
				}
			case 'T':
				if !s.substituted {
					pc = cmd.target
				} else {
					s.substituted = false
					pc++
				}
			case 'r', 'R':
				if err := s.queueRead(cmd); err != nil {
					return err
				}
				pc++
			case 'w', 'W':
				if err := s.writeSpace(cmd.label, s.pattern, true, cmd.op == 'W'); err != nil {
					return err
				}
				pc++
			case 'e':
				if len(cmd.label) == 0 {
					if err := s.executePattern(); err != nil {
						return err
					}
				} else if _, err := s.runExternal(string(cmd.label), true); err != nil {
					return err
				}
				pc++
			case 'v':
				pc++
			default:
				return fmt.Errorf("unimplemented command %q", cmd.op)
			}
		}
		if restartScript {
			continue
		}
		if printPattern && !s.opts.quiet {
			if err := s.printSpace(s.pattern, s.terminated); err != nil {
				return err
			}
		}
		if err := s.flushAppendQueue(); err != nil {
			return err
		}
		if s.opts.unbuffered {
			if f, ok := s.out.(interface{ Flush() error }); ok {
				if err := f.Flush(); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

func (s *execState) flushAppendQueue() error {
	for _, data := range s.appendQueue {
		if err := s.printSpace(data, true); err != nil {
			return err
		}
	}
	s.appendQueue = s.appendQueue[:0]
	return nil
}

func (s *execState) resetFileState(fileID int) {
	_ = fileID
	clear(s.rangeState)
	clear(s.rangeStart)
	s.substituted = false
}

func (s *execState) selected(pc int, cmd command) (bool, bool) {
	matched := true
	endOfRange := false
	if cmd.first != nil {
		if cmd.second == nil {
			matched = s.addressMatches(cmd.first)
		} else if s.rangeState[pc] {
			matched = true
			endOfRange = s.addressMatchesRangeEnd(cmd.second, s.rangeStart[pc])
			if endOfRange {
				s.rangeState[pc] = false
			}
		} else if s.addressMatches(cmd.first) {
			matched = true
			s.rangeStart[pc] = s.lineNumber()
			if cmd.second.kind == addressLine && cmd.second.line <= s.lineNumber() {
				endOfRange = true
			} else if cmd.second.kind == addressRegex && !(cmd.first.kind == addressLine && cmd.first.line == 0) {
				s.rangeState[pc] = true
			} else if cmd.second.kind == addressRegex {
				endOfRange = s.addressMatchesRangeEnd(cmd.second, s.rangeStart[pc])
				if !endOfRange {
					s.rangeState[pc] = true
				}
			} else if cmd.second.kind == addressPlus || cmd.second.kind == addressTilde {
				if cmd.second.kind == addressPlus && cmd.second.line == 0 {
					endOfRange = true
				} else {
					s.rangeState[pc] = true
				}
			} else {
				s.rangeState[pc] = true
			}
		} else {
			matched = false
		}
	}
	if cmd.negate {
		matched = !matched
	}
	return matched, endOfRange
}

func (s *execState) addressMatchesRangeEnd(addr *address, start int64) bool {
	switch addr.kind {
	case addressPlus:
		return s.lineNumber() >= start+addr.line
	case addressTilde:
		return s.lineNumber() > start && s.lineNumber()%addr.line == 0
	default:
		return s.addressMatches(addr)
	}
}

func (s *execState) lineNumber() int64 {
	if s.opts.separate {
		return s.record.fileNumber
	}
	return s.record.globalNumber
}

func (s *execState) addressMatches(addr *address) bool {
	switch addr.kind {
	case addressLine:
		if addr.line == 0 {
			return s.lineNumber() == 1
		}
		return s.lineNumber() == addr.line
	case addressLast:
		return s.record.lastAll || s.opts.separate && s.record.lastFile
	case addressRegex:
		return addr.regex.FindIndex(s.pattern) != nil
	case addressStep:
		n := s.lineNumber()
		return n >= addr.line && (n-addr.line)%addr.step == 0
	case addressPlus:
		return s.lineNumber() == addr.line
	case addressTilde:
		return s.lineNumber() == addr.line
	default:
		return false
	}
}

func (s *execState) printSpace(data []byte, addDelim bool) error {
	if _, err := s.out.Write(data); err != nil {
		return err
	}
	if addDelim {
		_, err := s.out.Write([]byte{s.delim})
		return err
	}
	return nil
}

func (s *execState) substitute(cmd command) (bool, error) {
	original := s.pattern
	var out []byte
	cursor, search, count, replaced := 0, 0, 0, false
	for search <= len(original) {
		match, err := cmd.regex.TryFindSubmatchIndex(original[search:], 0)
		if err != nil {
			return false, err
		}
		if len(match) == 0 {
			break
		}
		for i := range match {
			if match[i] >= 0 {
				match[i] += search
			}
		}
		start, end := match[0], match[1]
		count++
		shouldReplace := cmd.subFlags.global || count == 1
		if cmd.subFlags.occurrence > 0 {
			shouldReplace = count >= cmd.subFlags.occurrence
			if !cmd.subFlags.global {
				shouldReplace = count == cmd.subFlags.occurrence
			}
		}
		if shouldReplace {
			out = append(out, original[cursor:start]...)
			out = expandSedReplacement(out, cmd.replacement, original, match)
			cursor = end
			replaced = true
			if !cmd.subFlags.global {
				break
			}
		}
		next := end
		if end == start {
			if end >= len(original) {
				break
			}
			_, width := decodeRunePreservingInvalid(original[end:])
			next = end + width
		}
		search = next
	}
	if !replaced {
		return false, nil
	}
	out = append(out, original[cursor:]...)
	s.pattern = out
	return true, nil
}

func (s *execState) transliterate(cmd command) {
	mapRune := make(map[rune]rune, len(cmd.transFrom))
	for i, r := range cmd.transFrom {
		mapRune[r] = cmd.transTo[i]
	}
	out := make([]byte, 0, len(s.pattern))
	for i := 0; i < len(s.pattern); {
		r, n := decodeRunePreservingInvalid(s.pattern[i:])
		if replacement, ok := mapRune[r]; ok {
			if r > unicode.MaxRune {
				out = append(out, byte(replacement-unicode.MaxRune-1))
			} else {
				out = append(out, []byte(string(replacement))...)
			}
		} else {
			out = append(out, s.pattern[i:i+n]...)
		}
		i += n
	}
	s.pattern = out
}

func (s *execState) listSpace() error {
	var out []byte
	width, limit := 0, s.opts.lineLength
	if limit < 0 {
		limit = 70
	}
	appendEncoded := func(encoded []byte) {
		if limit > 0 && width > 0 && width+len(encoded) > limit {
			out = append(out, '\\', '\n')
			width = 0
		}
		out = append(out, encoded...)
		width += len(encoded)
	}
	for _, b := range s.pattern {
		var encoded []byte
		switch b {
		case '\\':
			encoded = []byte(`\\`)
		case '\t':
			encoded = []byte(`\t`)
		case '\r':
			encoded = []byte(`\r`)
		case '\n':
			encoded = []byte(`\n`)
		default:
			if b < 0x20 || b >= 0x7f {
				encoded = []byte(fmt.Sprintf(`\x%02x`, b))
			} else {
				encoded = []byte{b}
			}
		}
		appendEncoded(encoded)
	}
	out = append(out, '$', '\n')
	_, err := s.out.Write(out)
	return err
}

func (s *execState) queueRead(cmd command) error {
	name := s.resolvePath(cmd.label)
	if cmd.op == 'r' {
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if len(data) > 0 && data[len(data)-1] == s.delim {
			data = data[:len(data)-1]
		}
		s.appendQueue = append(s.appendQueue, data)
		return nil
	}
	reader := s.readers[name]
	if reader == nil {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		reader = bufio.NewReader(f)
		s.readFiles[name] = f
		s.readers[name] = reader
	}
	line, err := reader.ReadBytes(s.delim)
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return nil
	}
	if len(line) > 0 && line[len(line)-1] == s.delim {
		line = line[:len(line)-1]
	}
	s.appendQueue = append(s.appendQueue, line)
	return nil
}

func (s *execState) writeSpace(file string, data []byte, newline, firstLine bool) error {
	name := s.resolvePath(file)
	f, err := s.openWriteFile(name)
	if err != nil {
		return err
	}
	if firstLine {
		if i := bytes.IndexByte(data, s.delim); i >= 0 {
			data = data[:i]
		}
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if newline {
		_, err := f.Write([]byte{s.delim})
		return err
	}
	return nil
}

func (s *execState) prepareWriteFiles() error {
	for _, cmd := range s.program.commands {
		switch cmd.op {
		case 'w', 'W':
			if _, err := s.openWriteFile(s.resolvePath(cmd.label)); err != nil {
				return err
			}
		case 's':
			if cmd.subFlags.writeFile != "" {
				if _, err := s.openWriteFile(s.resolvePath(cmd.subFlags.writeFile)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *execState) openWriteFile(name string) (*os.File, error) {
	f := s.writeFiles[name]
	if f == nil {
		var err error
		f, err = os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
		if err != nil {
			return nil, err
		}
		s.writeFiles[name] = f
	}
	return f, nil
}

func (s *execState) closeWriteFiles() {
	for _, f := range s.writeFiles {
		_ = f.Close()
	}
}

func (s *execState) closeReadFiles() {
	for _, file := range s.readFiles {
		_ = file.Close()
	}
}

func (s *execState) executePattern() error {
	command := string(s.pattern)
	output, err := s.runExternal(command, false)
	if err != nil {
		return err
	}
	s.pattern = bytes.TrimSuffix(output, []byte{s.delim})
	return nil
}

func (s *execState) runExternal(command string, print bool) ([]byte, error) {
	if command == "" {
		command = string(s.pattern)
	}
	shell, args := "sh", []string{"-c", command}
	if runtime.GOOS == "windows" {
		shell, args = "cmd.exe", []string{"/C", command}
	}
	cmd := exec.CommandContext(s.ctx, shell, args...)
	cmd.Dir = s.dir
	cmd.Stdin = bytes.NewReader(nil)
	if s.ioEnv.Environ != nil {
		cmd.Env = s.ioEnv.Environ()
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if print {
		_, err = s.out.Write(output)
	}
	return output, err
}

func (s *execState) resolvePath(name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(s.dir, name)
}

func runInPlace(ctx context.Context, p *program, name, dir string, opts options, ioEnv core.IO) (int, bool, error) {
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if opts.followLinks {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return 0, false, err
		}
		path = resolved
	}
	input, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	info, err := input.Stat()
	if err != nil {
		input.Close()
		return 0, false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".sed-*")
	if err != nil {
		input.Close()
		return 0, false, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	opts.inPlace = false
	status, stopped, err := runFiles(ctx, p, []string{name}, dir, opts, input, temp, ioEnv)
	if err != nil {
		input.Close()
		temp.Close()
		return 0, false, err
	}
	if err := input.Close(); err != nil {
		temp.Close()
		return 0, false, err
	}
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		temp.Close()
		return 0, false, err
	}
	if err := temp.Close(); err != nil {
		return 0, false, err
	}
	backup := ""
	if opts.backup != "" {
		backup = path + opts.backup
		if err := os.Rename(path, backup); err != nil {
			return 0, false, err
		}
	} else {
		backup = tempName + ".original"
		if err := os.Rename(path, backup); err != nil {
			return 0, false, err
		}
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Rename(backup, path)
		return 0, false, err
	}
	if opts.backup == "" {
		_ = os.Remove(backup)
	}
	return status, stopped, nil
}
