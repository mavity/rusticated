package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type dialogKind int

const (
	dialogInput dialogKind = iota
	dialogConfirm
	dialogChoice
)

type dialogAction int

const (
	actionMkdir dialogAction = iota
	actionRename
	actionNewFile
	actionCopy
	actionMove
	actionDelete
	actionSelectMask
	actionUnselectMask
)

// dlgChoice is a single FAR-style button in a choice dialog.
type dlgChoice struct {
	label  string // shown text, e.g. "Overwrite"
	hotkey string // single lowercase letter that resolves immediately
}

type dialogState struct {
	kind    dialogKind
	action  dialogAction
	title   string
	prompt  string
	input   textinput.Model
	pending *fileOp // for confirm dialogs that launch an op

	choices   []dlgChoice
	choiceIdx int
	onPick    func(m *model, idx int) (tea.Model, tea.Cmd)
}

// openInputDialog builds a modal that collects a single line of text.
func (m *model) openInputDialog(action dialogAction, title, prompt, initial string) {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colorYellow).Background(colorBlack)
	ti.TextStyle = lipgloss.NewStyle().Foreground(colorWhite).Background(colorBlack)
	ti.Cursor.Style = lipgloss.NewStyle().Background(colorWhite).Foreground(colorBlack)
	ti.Cursor.TextStyle = lipgloss.NewStyle().Foreground(colorBlack).Background(colorWhite)
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

// openChoiceDialog builds a modal with FAR-style buttons resolved by hotkey,
// arrows, or Enter. onPick is called with the chosen index (-1 on Esc).
func (m *model) openChoiceDialog(title, prompt string, choices []dlgChoice, onPick func(m *model, idx int) (tea.Model, tea.Cmd)) {
	m.dialog = &dialogState{
		kind:    dialogChoice,
		title:   title,
		prompt:  prompt,
		choices: choices,
		onPick:  onPick,
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

	if d.kind == dialogChoice {
		return m.updateChoice(msg)
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

// updateChoice handles keys for FAR-style button dialogs: hotkeys resolve
// immediately, arrows/Tab move the highlight, Enter picks it, Esc cancels.
func (m *model) updateChoice(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := m.dialog
	key := msg.String()
	switch key {
	case "esc":
		return m.pickChoice(-1)
	case "enter", " ":
		return m.pickChoice(d.choiceIdx)
	case "left", "up":
		if d.choiceIdx > 0 {
			d.choiceIdx--
		}
		return m, nil
	case "right", "down", "tab":
		if d.choiceIdx < len(d.choices)-1 {
			d.choiceIdx++
		}
		return m, nil
	}
	lower := strings.ToLower(key)
	for i, c := range d.choices {
		if c.hotkey != "" && lower == c.hotkey {
			return m.pickChoice(i)
		}
	}
	return m, nil
}

// pickChoice resolves a choice dialog and dispatches to its handler.
func (m *model) pickChoice(idx int) (tea.Model, tea.Cmd) {
	d := m.dialog
	if d == nil {
		return m, nil
	}
	onPick := d.onPick
	m.dialog = nil
	if !m.opActive {
		m.mode = modeBrowser
	}
	if onPick != nil {
		return onPick(m, idx)
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

	// Copy/move with an editable destination path.
	if d.action == actionCopy || d.action == actionMove {
		op := d.pending
		m.dialog = nil
		m.mode = modeBrowser
		if op == nil {
			return m, nil
		}
		op.dest = name
		return m, m.startFileOp(*op)
	}

	// Select / unselect files by wildcard mask.
	if d.action == actionSelectMask || d.action == actionUnselectMask {
		want := d.action == actionSelectMask
		n := m.applyMask(p, name, want)
		m.dialog = nil
		m.mode = modeBrowser
		m.updateDelegates()
		verb := "Selected"
		if !want {
			verb = "Unselected"
		}
		return m, m.AddPlume(fmt.Sprintf("%s %d item(s) matching %s", verb, n, name))
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

// applyMask sets the selected flag on items in pane p whose name matches the
// glob mask, returning the number of items changed.
func (m *model) applyMask(p pane, mask string, want bool) int {
	var l *list.Model
	if p == leftPane {
		l = &m.leftList
	} else if p == rightPane {
		l = &m.rightList
	} else {
		return 0
	}
	count := 0
	items := l.Items()
	for i, it := range items {
		fi, ok := it.(fileItem)
		if !ok || fi.name == ".." {
			continue
		}
		if ok, _ := filepath.Match(mask, fi.name); ok {
			if fi.selected != want {
				fi.selected = want
				items[i] = fi
				count++
			}
		}
	}
	l.SetItems(items)
	return count
}

// dlgBtn is one rendered button in a FAR-style centered button row.
type dlgBtn struct {
	label    string
	hotkey   string
	selected bool
}

// renderButtonRow renders buttons centered on the dialog background, the
// highlighted button inverted to the yellow action colour.
func renderButtonRow(width int, btns []dlgBtn) string {
	gap := lipgloss.NewStyle().Background(colorDlgBg).Render("  ")
	var rendered []string
	for _, b := range btns {
		st := lipgloss.NewStyle().Padding(0, 1)
		if b.selected {
			st = st.Background(colorBtnBg).Foreground(colorBtnFg).Bold(true)
		} else {
			st = st.Background(colorBtnAltBg).Foreground(colorBtnAltFg)
		}
		rendered = append(rendered, st.Render(b.label))
	}
	row := strings.Join(rendered, gap)
	w := lipgloss.Width(row)
	if w >= width {
		return row
	}
	pad := (width - w) / 2
	fill := lipgloss.NewStyle().Background(colorDlgBg)
	return fill.Render(strings.Repeat(" ", pad)) + row + fill.Render(strings.Repeat(" ", width-w-pad))
}

// dialogBox renders the modal box (centering is done by the caller overlay).
func (m *model) dialogBox() string {
	d := m.dialog
	if d == nil {
		return ""
	}

	boxWidth := 54
	if boxWidth > m.width-4 {
		boxWidth = m.width - 4
	}
	if boxWidth < 24 {
		boxWidth = 24
	}
	innerW := boxWidth - 2

	var lines []string
	lines = append(lines, dlgTitle(d.title, innerW))
	lines = append(lines, dlgLine("", innerW, colorDlgText))
	lines = append(lines, dlgLine("  "+d.prompt, innerW, colorDlgText))
	lines = append(lines, dlgLine("", innerW, colorDlgText))

	switch d.kind {
	case dialogInput:
		d.input.Width = innerW - 5
		raw := d.input.View()
		if w := lipgloss.Width(raw); w < innerW-2 {
			raw += lipgloss.NewStyle().Background(colorBlack).Render(strings.Repeat(" ", innerW-2-w))
		}
		field := lipgloss.NewStyle().Background(colorBlack).Render(" ") + raw + lipgloss.NewStyle().Background(colorBlack).Render(" ")
		lines = append(lines, dlgCenter(field, innerW))
		lines = append(lines, dlgLine("", innerW, colorDlgText))
		lines = append(lines, renderButtonRow(innerW, []dlgBtn{{label: "[ OK ]", selected: true}, {label: "[ Cancel ]"}}))
		lines = append(lines, dlgLine("", innerW, colorDlgText))
		lines = append(lines, dlgLine("  Enter: confirm    Esc: cancel", innerW, colorDlgMuted))
	case dialogChoice:
		var btns []dlgBtn
		for i, c := range d.choices {
			btns = append(btns, dlgBtn{label: c.label, hotkey: c.hotkey, selected: i == d.choiceIdx})
		}
		lines = append(lines, renderButtonRow(innerW, btns))
		lines = append(lines, dlgLine("", innerW, colorDlgText))
		lines = append(lines, dlgLine("  ←/→ select · Enter confirm · Esc cancel", innerW, colorDlgMuted))
	default: // dialogConfirm
		lines = append(lines, renderButtonRow(innerW, []dlgBtn{{label: "[Y]es", hotkey: "y", selected: true}, {label: "[N]o", hotkey: "n"}}))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDlgBorder).
		BorderBackground(colorDlgBg).
		Background(colorDlgBg).
		Foreground(colorDlgText).
		Padding(0, 1).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))

	return box
}

// progressBox renders the running file-operation modal with per-file and total
// progress bars plus the transfer rate.
func (m *model) progressBox() string {
	boxWidth := 58
	if boxWidth > m.width-4 {
		boxWidth = m.width - 4
	}
	if boxWidth < 28 {
		boxWidth = 28
	}
	innerW := boxWidth - 2

	barWidth := innerW - 6
	if barWidth < 8 {
		barWidth = 8
	}

	current := m.opCurrent
	if current != "" {
		current = filepath.Base(current)
	}

	filePct := pctOf(m.opFileDone, m.opFileTotal)
	batchPct := pctOf(m.opDone, m.opTotal)

	fileBar := progressBarLine(filePct, barWidth, innerW)
	batchBar := progressBarLine(batchPct, barWidth, innerW)

	totals := fmt.Sprintf("  %s / %s   %s/s",
		formatBytes(m.opDone), formatBytes(m.opTotal), formatBytes(m.opRate))

	lines := []string{
		dlgTitle(m.opKind.verb(), innerW),
		dlgLine("", innerW, colorDlgText),
		dlgLine("  "+current, innerW, colorDlgText),
		fileBar,
		dlgLine("", innerW, colorDlgText),
		dlgLine(totals, innerW, colorDlgMuted),
		batchBar,
		dlgLine("", innerW, colorDlgText),
		dlgLine("  Esc: cancel", innerW, colorDlgMuted),
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDlgBorder).
		BorderBackground(colorDlgBg).
		Background(colorDlgBg).
		Foreground(colorDlgText).
		Padding(0, 1).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))

	return box
}

// progressBarLine renders a bar plus percentage, padded to the modal width.
func progressBarLine(pct, barWidth, innerW int) string {
	bar := renderProgressBar(pct, barWidth, colorDlgBg, false)
	line := bar + lipgloss.NewStyle().Background(colorDlgBg).Foreground(colorDlgText).Render(fmt.Sprintf(" %3d%%", pct))
	if w := lipgloss.Width(line); w < innerW {
		line += lipgloss.NewStyle().Background(colorDlgBg).Render(strings.Repeat(" ", innerW-w))
	}
	return line
}

func pctOf(done, total int64) int {
	if total <= 0 {
		return 0
	}
	p := int((done * 100) / total)
	if p > 100 {
		p = 100
	}
	if p < 0 {
		p = 0
	}
	return p
}

// formatBytes renders a byte count in human-friendly units.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// dlgCenter centers a pre-rendered fragment on the dialog background.
func dlgCenter(frag string, width int) string {
	w := lipgloss.Width(frag)
	if w >= width {
		return frag
	}
	pad := (width - w) / 2
	fill := lipgloss.NewStyle().Background(colorDlgBg)
	return fill.Render(strings.Repeat(" ", pad)) + frag + fill.Render(strings.Repeat(" ", width-w-pad))
}

// dlgTitle renders a full-width highlighted title bar for a modal.
func dlgTitle(title string, width int) string {
	st := lipgloss.NewStyle().Background(colorDlgBorder).Foreground(colorBtnFg).Bold(true).Width(width).Align(lipgloss.Center)
	return st.Render(title)
}

// dlgLine renders a full-width modal body line with a uniform background.
func dlgLine(text string, width int, fg lipgloss.Color) string {
	return lipgloss.NewStyle().Background(colorDlgBg).Foreground(fg).
		Width(width).Render(truncateStringToWidth(text, width))
}

// summarizeSources produces a short human description for confirm prompts.
func summarizeSources(sources []string) string {
	if len(sources) == 1 {
		return filepath.Base(sources[0])
	}
	return fmt.Sprintf("%d items", len(sources))
}
