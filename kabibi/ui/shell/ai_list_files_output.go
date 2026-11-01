package shell

import (
	"fmt"
	"strings"
	"sync"

	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/tools/filelist"
)

type listFilesCommandOutput struct {
	mu       sync.RWMutex
	progress *filelist.Progress
	maxRows  int
}

func newListFilesCommandOutput(maxRows int) *listFilesCommandOutput {
	return &listFilesCommandOutput{maxRows: maxRows}
}

func (o *listFilesCommandOutput) setProgress(progress *filelist.Progress) {
	o.mu.Lock()
	o.progress = cloneListFilesProgress(progress)
	o.mu.Unlock()
}

func (o *listFilesCommandOutput) Render(width int) string {
	o.mu.RLock()
	progress := cloneListFilesProgress(o.progress)
	maxRows := o.maxRows
	o.mu.RUnlock()
	return renderListFilesProgress(progress, width, maxRows)
}

func cloneListFilesProgress(progress *filelist.Progress) *filelist.Progress {
	if progress == nil {
		return nil
	}
	copy := *progress
	copy.Matches = append([]filelist.ProgressMatch(nil), progress.Matches...)
	return &copy
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
		row := filelist.FormatMatch(filelist.Match{
			Path:     path,
			Size:     match.Size,
			Score:    match.Score,
			HasScore: match.HasScore,
			Quote:    quote,
		}, width-2)
		lines = append(lines, truncateProgressLine("- "+row, width))
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
	return max(0, availableHeight-2)
}

func listFilesOutputMatchLimit(availableHeight int) int {
	const spareRows = 3
	const fixedOutputRows = 3
	return max(0, availableHeight-spareRows-fixedOutputRows)
}
