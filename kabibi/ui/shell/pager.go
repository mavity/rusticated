package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

const pagerPrompt = "--More-- (Space: page, Enter: line, b: back, q: quit)"
const pagerClearScreen = "\x1b[2J\x1b[H"

func runPager(ctx context.Context, term *CommandTerminal, input io.Reader, output io.Writer) error {
	if term == nil {
		return errors.New("pager requires an active command terminal")
	}
	cols, _ := term.Size()
	pageRows := func() int {
		_, rows := term.Size()
		rows--
		if rows < 1 {
			return 1
		}
		return rows
	}

	releaseInput := term.SubscribeControlInput()
	defer releaseInput()

	reader := bufio.NewReader(input)
	lines := make([]string, 0, 64)
	eof := false
	readPage := func(start, rowBudget int, singleLine bool) (pagerPage, error) {
		usedRows := 0
		index := start
		for usedRows < rowBudget || index == start {
			if singleLine && index > start {
				break
			}
			if index < len(lines) {
				usedRows += pagerLineRows(lines[index], cols)
				index++
				continue
			}
			if eof {
				break
			}
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				lines = append(lines, line)
			}
			if errors.Is(err, io.EOF) {
				eof = true
			} else if err != nil {
				return pagerPage{}, fmt.Errorf("read pager input: %w", err)
			}
		}
		return pagerPage{start: start, end: index}, nil
	}
	first, err := readPage(0, pageRows(), false)
	if err != nil {
		return err
	}
	if first.end == first.start {
		return nil
	}
	pages := []pagerPage{first}
	pageIndex := 0
	for {
		if _, err := io.WriteString(output, pagerClearScreen); err != nil {
			return fmt.Errorf("clear pager page: %w", err)
		}
		page := pages[pageIndex]
		for _, line := range lines[page.start:page.end] {
			if _, err := io.WriteString(output, line); err != nil {
				return fmt.Errorf("write paged output: %w", err)
			}
		}
		if eof && page.end == len(lines) {
			return nil
		}
		if _, err := fmt.Fprintf(output, "\r\x1b[2K%s", pagerPrompt); err != nil {
			return fmt.Errorf("write pager prompt: %w", err)
		}
		key, err := readPagerKey(ctx, term)
		if err != nil {
			_, _ = io.WriteString(output, "\r\x1b[2K")
			return err
		}
		if _, err := io.WriteString(output, "\r\x1b[2K"); err != nil {
			return fmt.Errorf("clear pager prompt: %w", err)
		}

		switch key {
		case 'q', 'Q', 3:
			return nil
		case 'b', 'B':
			if pageIndex > 0 {
				pageIndex--
			}
		case '\r', '\n':
			next, err := readPage(page.end, 1, true)
			if err != nil {
				return err
			}
			if next.end == next.start {
				return nil
			}
			pages = append(pages[:pageIndex+1], next)
			pageIndex++
		default:
			if key != ' ' && key != 'f' && key != 'F' {
				continue
			}
			cols, _ = term.Size()
			next, err := readPage(page.end, pageRows(), false)
			if err != nil {
				return err
			}
			if next.end == next.start {
				return nil
			}
			pages = append(pages[:pageIndex+1], next)
			pageIndex++
		}
	}
}

type pagerPage struct {
	start int
	end   int
}

func pagerLineRows(line string, cols int) int {
	if cols < 1 {
		cols = 1
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	width := runewidth.StringWidth(ansi.Strip(line))
	if width < 1 {
		return 1
	}
	return (width + cols - 1) / cols
}

func readPagerKey(ctx context.Context, term *CommandTerminal) (byte, error) {
	var key [1]byte
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		_, err := term.ReadControlInputContext(ctx, key[:])
		if err != nil {
			return 0, fmt.Errorf("read pager input: %w", err)
		}
		switch key[0] {
		case 0x1b, '[', ']':
			continue
		}
		return key[0], nil
	}
}
