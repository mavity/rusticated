package shell

import (
	"fmt"
	"io"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/tools/filelist"
)

type listFilesProgressOutput struct {
	writer  io.Writer
	width   int
	maxRows int
	rows    int
}

func newListFilesProgressOutput(writer io.Writer, width, maxRows int) *listFilesProgressOutput {
	if width <= 0 {
		width = 80
	}
	return &listFilesProgressOutput{
		writer:  writer,
		width:   width,
		maxRows: maxRows,
	}
}

func (o *listFilesProgressOutput) publish(progress *filelist.Progress) error {
	rendered := renderListFilesProgress(progress, o.width, o.maxRows)
	if rendered == "" {
		return nil
	}
	lines := strings.Split(rendered, "\n")
	rows := max(o.rows, len(lines))
	var frame strings.Builder
	if o.rows > 0 {
		fmt.Fprintf(&frame, "\x1b[%dA", o.rows)
	}
	frame.WriteByte('\r')
	for row := 0; row < rows; row++ {
		frame.WriteString("\x1b[2K")
		if row < len(lines) {
			frame.WriteString(lines[row])
		}
		frame.WriteString("\r\n")
	}
	if err := writeListFilesOutput(o.writer, frame.String()); err != nil {
		return err
	}
	o.rows = rows
	return nil
}

func (o *listFilesProgressOutput) clear() error {
	if o.rows == 0 {
		return nil
	}
	var frame strings.Builder
	fmt.Fprintf(&frame, "\x1b[%dA\r", o.rows)
	for row := 0; row < o.rows; row++ {
		frame.WriteString("\x1b[2K\r\n")
	}
	fmt.Fprintf(&frame, "\x1b[%dA\r", o.rows)
	if err := writeListFilesOutput(o.writer, frame.String()); err != nil {
		return err
	}
	o.rows = 0
	return nil
}

func writeListFilesOutput(writer io.Writer, text string) error {
	written, err := io.WriteString(writer, text)
	if err != nil {
		return err
	}
	if written != len(text) {
		return io.ErrShortWrite
	}
	return nil
}

func renderListFilesProgress(progress *filelist.Progress, width, maxRows int) string {
	if progress == nil || width <= 0 || maxRows <= 0 {
		return ""
	}
	if maxRows == 1 {
		return truncateProgressLine(progressCurrentStatus(progress), width)
	}
	matchRows := min(max(0, maxRows-2), len(progress.Matches))
	header := fmt.Sprintf("Top %d matches | viewed %d/%d | about %s/file",
		matchRows, progress.Viewed, progress.Total, filelist.FormatAveragePerFile(progress.AvgPerFile))
	lines := []string{truncateProgressLine(header, width)}
	for _, match := range progress.Matches[:matchRows] {
		path := strings.Join(strings.Fields(sanitizeMarkdownControls(match.Path)), " ")
		quote := strings.Join(strings.Fields(sanitizeMarkdownControls(match.Quote)), " ")
		row := filelist.FormatMatchPreview(filelist.Match{
			Path:           path,
			Size:           match.Size,
			Score:          match.Score,
			HasScore:       match.HasScore,
			Quote:          quote,
			QuoteStartLine: match.QuoteStartLine,
			QuoteEndLine:   match.QuoteEndLine,
		}, width)
		lines = append(lines, truncateProgressLine(row, width))
	}
	lines = append(lines, truncateProgressLine(progressCurrentStatus(progress), width))
	if len(lines) > maxRows {
		lines = lines[:maxRows]
	}
	return strings.Join(lines, "\n")
}

func progressCurrentStatus(progress *filelist.Progress) string {
	switch {
	case progress.Phase == "query":
		return "Preparing query embedding"
	case progress.Phase == "quote" && progress.CurrentPath != "":
		return "Finding quote: " + strings.Join(strings.Fields(sanitizeMarkdownControls(progress.CurrentPath)), " ")
	case progress.CurrentPath != "":
		return "Matching: " + strings.Join(strings.Fields(sanitizeMarkdownControls(progress.CurrentPath)), " ")
	default:
		return "Preparing search"
	}
}

func truncateProgressLine(text string, width int) string {
	if width <= 0 || runewidth.StringWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range text {
		runeWidth := runewidth.RuneWidth(r)
		if runeWidth <= 0 {
			continue
		}
		if used+runeWidth > width-1 {
			break
		}
		b.WriteRune(r)
		used += runeWidth
	}
	return b.String() + "…"
}

func listFilesProgressMatchLimit(availableHeight int) int {
	return max(0, listFilesProgressOutputRows(availableHeight)-2)
}

func listFilesProgressOutputRows(availableHeight int) int {
	return max(0, availableHeight-2)
}

func listFilesOutputMatchLimit(availableHeight int) int {
	const spareRows = 3
	const fixedOutputRows = 3
	return max(0, availableHeight-spareRows-fixedOutputRows)
}
