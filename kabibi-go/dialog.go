package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type dialogKind int

const (
	dialogInput dialogKind = iota
	dialogConfirm
)

type dialogAction int

const (
	actionMkdir dialogAction = iota
	actionRename
	actionNewFile
	actionCopy
	actionMove
	actionDelete
)

type dialogState struct {
	kind    dialogKind
	action  dialogAction
	title   string
	prompt  string
	input   textinput.Model
	pending *fileOp // for confirm dialogs that launch an op
}

// openInputDialog builds a modal that collects a single line of text.
func (m *model) openInputDialog(action dialogAction, title, prompt, initial string) {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colorYellow).Background(colorBlack)
	ti.TextStyle = lipgloss.NewStyle().Foreground(colorWhite).Background(colorBlack)
	ti.Cursor.Style = lipgloss.NewStyle().Background(colorWhite).Foreground(colorBlack)
	ti.Cursor.TextStyle = lipgloss.NewStyle().Foreground(colorWhite).Background(colorBlack)
	ti.Cursor.SetMode(cursor.CursorStatic)
	ti.SetValue(initial)
	ti.CursorEnd()
	ti.Focus()
	m.dialog = &dialogState{
		kind:   dialogInput,
		action: action,
		title:  title,
		prompt: prompt,
		input:  ti,
	}
	m.mode = modeDialog
}

// openConfirmDialog builds a yes/no modal that runs pending when accepted.
func (m *model) openConfirmDialog(action dialogAction, title, prompt string, pending *fileOp) {
	m.dialog = &dialogState{
		kind:    dialogConfirm,
		action:  action,
		title:   title,
		prompt:  prompt,
		pending: pending,
	}
	m.mode = modeDialog
}

func (m *model) closeDialog() {
	m.dialog = nil
	if m.opActive {
		return
	}
	m.mode = modeBrowser
}

// updateDialog handles keys while a modal is open.
func (m *model) updateDialog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := m.dialog
	if d == nil {
		m.mode = modeBrowser
		return m, nil
	}

	switch msg.String() {
	case "esc":
		m.closeDialog()
		return m, nil
	case "enter":
		if d.kind == dialogConfirm {
			return m.acceptConfirm()
		}
		return m.acceptInput()
	case "y", "Y":
		if d.kind == dialogConfirm {
			return m.acceptConfirm()
		}
	case "n", "N":
		if d.kind == dialogConfirm {
			m.closeDialog()
			return m, nil
		}
	}

	if d.kind == dialogInput {
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) acceptConfirm() (tea.Model, tea.Cmd) {
	d := m.dialog
	op := d.pending
	m.dialog = nil
	m.mode = modeBrowser
	if op == nil {
		return m, nil
	}
	return m, m.startFileOp(*op)
}

func (m *model) acceptInput() (tea.Model, tea.Cmd) {
	d := m.dialog
	name := strings.TrimSpace(d.input.Value())
	l, dir, p := m.activePaneState()
	if l == nil || name == "" {
		m.closeDialog()
		return m, nil
	}

	var status string
	switch d.action {
	case actionMkdir:
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			status = "mkdir failed: " + err.Error()
		} else {
			status = "Created " + name
		}
	case actionNewFile:
		target := filepath.Join(dir, name)
		if pathExists(target) {
			status = name + " already exists"
		} else if f, err := os.Create(target); err != nil {
			status = "create failed: " + err.Error()
		} else {
			f.Close()
			status = "Created " + name
		}
	case actionRename:
		if fi, ok := l.SelectedItem().(fileItem); ok && fi.name != ".." {
			oldPath := filepath.Join(dir, fi.name)
			newPath := filepath.Join(dir, name)
			if err := os.Rename(oldPath, newPath); err != nil {
				status = "rename failed: " + err.Error()
			} else {
				status = fi.name + " -> " + name
			}
		}
	}

	m.dialog = nil
	m.mode = modeBrowser
	m.loadDir(p, dir, name)
	m.refreshPrompt()
	return m, m.AddPlume(status)
}

// dialogBox renders the modal box (centering is done by the caller overlay).
func (m *model) dialogBox() string {
	d := m.dialog
	if d == nil {
		return ""
	}

	boxWidth := 52
	if boxWidth > m.width-4 {
		boxWidth = m.width - 4
	}
	if boxWidth < 20 {
		boxWidth = 20
	}
	innerW := boxWidth - 2

	var lines []string
	lines = append(lines, dlgTitle(d.title, innerW))
	lines = append(lines, dlgLine("", innerW, colorWhite))
	lines = append(lines, dlgLine(d.prompt, innerW, colorWhite))
	lines = append(lines, dlgLine("", innerW, colorWhite))
	if d.kind == dialogInput {
		d.input.Width = innerW - 3
		raw := d.input.View()
		if w := lipgloss.Width(raw); w < innerW {
			raw += lipgloss.NewStyle().Background(colorBlack).Render(strings.Repeat(" ", innerW-w))
		}
		lines = append(lines, raw)
		lines = append(lines, dlgLine("", innerW, colorWhite))
		lines = append(lines, dlgLine("Enter: confirm   Esc: cancel", innerW, colorGray))
	} else {
		lines = append(lines, dlgLine("Y / Enter: yes    N / Esc: no", innerW, colorGray))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorWhite).
		BorderBackground(colorDarkGray).
		Background(colorDarkGray).
		Foreground(colorWhite).
		Padding(0, 1).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))

	return box
}

// progressBox renders the running file-operation modal with a progress bar.
func (m *model) progressBox() string {
	boxWidth := 56
	if boxWidth > m.width-4 {
		boxWidth = m.width - 4
	}
	if boxWidth < 24 {
		boxWidth = 24
	}
	innerW := boxWidth - 2

	pct := 0
	if m.opTotal > 0 {
		pct = int((m.opDone * 100) / m.opTotal)
	}
	if pct > 100 {
		pct = 100
	}

	barWidth := innerW - 6
	if barWidth < 8 {
		barWidth = 8
	}

	current := m.opCurrent
	if current != "" {
		current = filepath.Base(current)
	}

	bar := renderProgressBar(pct, barWidth, colorDarkGray, false)
	barLine := bar + lipgloss.NewStyle().Background(colorDarkGray).Foreground(colorWhite).Render(fmt.Sprintf(" %3d%%", pct))
	if w := lipgloss.Width(barLine); w < innerW {
		barLine += lipgloss.NewStyle().Background(colorDarkGray).Render(strings.Repeat(" ", innerW-w))
	}

	lines := []string{
		dlgTitle(m.opKind.verb(), innerW),
		dlgLine("", innerW, colorWhite),
		dlgLine(current, innerW, colorWhite),
		dlgLine("", innerW, colorWhite),
		barLine,
		dlgLine("", innerW, colorWhite),
		dlgLine("Esc: cancel", innerW, colorGray),
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorWhite).
		BorderBackground(colorDarkGray).
		Background(colorDarkGray).
		Foreground(colorWhite).
		Padding(0, 1).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))

	return box
}

// dlgTitle renders a full-width highlighted title bar for a modal.
func dlgTitle(title string, width int) string {
	return lipgloss.NewStyle().Background(colorYellow).Foreground(colorNavy).Bold(true).
		Width(width).Render(" " + title)
}

// dlgLine renders a full-width modal body line with a uniform background.
func dlgLine(text string, width int, fg lipgloss.Color) string {
	return lipgloss.NewStyle().Background(colorDarkGray).Foreground(fg).
		Width(width).Render(truncateStringToWidth(text, width))
}

// summarizeSources produces a short human description for confirm prompts.
func summarizeSources(sources []string) string {
	if len(sources) == 1 {
		return filepath.Base(sources[0])
	}
	return fmt.Sprintf("%d items", len(sources))
}
