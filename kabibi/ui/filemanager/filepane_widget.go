package filemanager

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// FilePaneWidget is the complete model for a single file-browser pane.
// It owns navigation state, handles keyboard input, and renders cells.
// Title is structural state passed by the parent; operational state (items,
// selection, scroll) is managed internally.
type FilePaneWidget struct {
	Title      string            // display name shown in the title bar
	OnOpenFile func(path string) // called when the user selects a regular file

	active      bool
	dir         string
	items       []fileItem
	selectedIdx int
	service     FSService

	Width  int
	Height int
	X      int
	Y      int
}

func NewFilePaneWidget(title string) *FilePaneWidget {
	return &FilePaneWidget{Title: title}
}

func (w *FilePaneWidget) SetActive(v bool) {
	if w == nil {
		return
	}
	w.active = v
}

func (w *FilePaneWidget) IsActive() bool {
	if w == nil {
		return false
	}
	return w.active
}

func (w *FilePaneWidget) SetService(s FSService) {
	if w == nil {
		return
	}
	w.service = s
}

func (w *FilePaneWidget) Dir() string {
	if w == nil {
		return ""
	}
	return w.dir
}

func (w *FilePaneWidget) SetDir(dir string) {
	if w == nil {
		return
	}
	w.dir = dir
}

// LoadDir loads path from the service into internal state.
func (w *FilePaneWidget) LoadDir(path string) error {
	if w == nil || w.service == nil {
		return nil
	}
	entries, err := w.service.ReadDir(path)
	if err != nil {
		return err
	}
	w.dir = path
	w.items = make([]fileItem, 0, len(entries)+1)
	if filepath.Dir(path) != path {
		w.items = append(w.items, fileItem{name: "..", isDir: true})
	}
	for _, e := range entries {
		w.items = append(w.items, fileItem{name: e.Name, isDir: e.IsDir})
	}
	// Sort: ".." first, then dirs before files, then alphabetically.
	sort.Slice(w.items, func(i, j int) bool {
		ii, jj := w.items[i], w.items[j]
		if ii.name == ".." {
			return true
		}
		if jj.name == ".." {
			return false
		}
		if ii.isDir != jj.isDir {
			return ii.isDir
		}
		return strings.ToLower(ii.name) < strings.ToLower(jj.name)
	})
	// Always reset selection to the top on directory change.
	w.selectedIdx = 0
	return nil
}

// SelectedItem returns the currently highlighted item.
func (w *FilePaneWidget) SelectedItem() (fileItem, bool) {
	if w == nil || w.selectedIdx < 0 || w.selectedIdx >= len(w.items) {
		return fileItem{}, false
	}
	return w.items[w.selectedIdx], true
}

// SetItemsBridge populates items directly; used by syncDualPaneWidgets during
// the transition period only — remove when Phase F completes the wire-in.
func (w *FilePaneWidget) Items() []fileItem {
	if w == nil {
		return nil
	}
	return w.items
}

func (w *FilePaneWidget) SelectedIndex() int {
	if w == nil {
		return 0
	}
	return w.selectedIdx
}

// Select sets the highlighted index; no-op if out of range.
func (w *FilePaneWidget) Select(idx int) {
	if w == nil || idx < 0 || idx >= len(w.items) {
		return
	}
	w.selectedIdx = idx
}

// SelectByName scrolls to the first item whose name matches; no-op if not found.
func (w *FilePaneWidget) SelectByName(name string) {
	if w == nil {
		return
	}
	for i, fi := range w.items {
		if fi.name == name {
			w.selectedIdx = i
			return
		}
	}
}

// SetItems replaces the item list and clamps the selection.
func (w *FilePaneWidget) SetItems(items []fileItem) {
	if w == nil {
		return
	}
	w.items = items
	if w.selectedIdx >= len(items) {
		w.selectedIdx = 0
	}
}

// MarkToggle flips the selected flag on item idx. Returns false for ".." or out of range.
func (w *FilePaneWidget) MarkToggle(idx int) bool {
	if w == nil || idx < 0 || idx >= len(w.items) || w.items[idx].name == ".." {
		return false
	}
	w.items[idx].selected = !w.items[idx].selected
	return true
}

// MarkedNames returns the names of all marked items, excluding "..".
func (w *FilePaneWidget) MarkedNames() []string {
	if w == nil {
		return nil
	}
	var names []string
	for _, fi := range w.items {
		if fi.selected && fi.name != ".." {
			names = append(names, fi.name)
		}
	}
	return names
}

// ApplyMask marks or unmarks all items whose name matches the glob pattern.
func (w *FilePaneWidget) ApplyMask(mask string, want bool) int {
	if w == nil {
		return 0
	}
	count := 0
	for i, fi := range w.items {
		if fi.name == ".." {
			continue
		}
		if ok, _ := filepath.Match(mask, fi.name); ok {
			if fi.selected != want {
				w.items[i].selected = want
				count++
			}
		}
	}
	return count
}

func (w *FilePaneWidget) SetItemsBridge(items []fileItem, selectedIdx int) {
	if w == nil {
		return
	}
	w.items = items
	if selectedIdx >= 0 && selectedIdx < len(items) {
		w.selectedIdx = selectedIdx
	} else {
		w.selectedIdx = 0
	}
}

func (w *FilePaneWidget) Measure(c Constraints) Size {
	if c.MaxW <= 0 {
		c.MaxW = 20
	}
	if c.MaxH <= 0 {
		c.MaxH = 10
	}
	return Size{W: c.MaxW, H: c.MaxH}
}

func (w *FilePaneWidget) Layout(r Rect) {
	w.X = r.X
	w.Y = r.Y
	w.Width = r.W
	w.Height = r.H
}

func (w *FilePaneWidget) Render() CellBuf {
	buf := NewCellBuf(w.Width, w.Height)
	if w.Width <= 0 || w.Height <= 0 {
		return buf
	}

	bg := colorBlue
	buf.Fill(Rect{X: 0, Y: 0, W: w.Width, H: w.Height}, Cell{FG: colorWhite, BG: bg})

	if w.Width >= 2 && w.Height >= 2 {
		for x := 1; x < w.Width-1; x++ {
			buf.Set(x, 0, Cell{R: '─', FG: colorGray, BG: bg})
			buf.Set(x, w.Height-1, Cell{R: '─', FG: colorGray, BG: bg})
		}
		for y := 1; y < w.Height-1; y++ {
			buf.Set(0, y, Cell{R: '│', FG: colorGray, BG: bg})
			buf.Set(w.Width-1, y, Cell{R: '│', FG: colorGray, BG: bg})
		}
		buf.Set(0, 0, Cell{R: '┌', FG: colorGray, BG: bg})
		buf.Set(w.Width-1, 0, Cell{R: '┐', FG: colorGray, BG: bg})
		buf.Set(0, w.Height-1, Cell{R: '└', FG: colorGray, BG: bg})
		buf.Set(w.Width-1, w.Height-1, Cell{R: '┘', FG: colorGray, BG: bg})
	}

	title := w.Title
	if title == "" {
		title = " . "
	}
	if runewidth.StringWidth(title) > w.Width-2 {
		title = truncateStringToWidth(title, w.Width-2)
	}
	if w.Width > 2 {
		label := " " + title + " "
		if runewidth.StringWidth(label) > w.Width-2 {
			label = " " + truncateStringToWidth(title, w.Width-4) + " "
		}
		start := 1 + (w.Width-2-runewidth.StringWidth(label))/2
		titleBG := colorBlue
		titleFG := colorGray
		if w.active {
			titleBG = colorYellow
			titleFG = colorDarkGray
		}
		for i, r := range label {
			if start+i >= w.Width-1 {
				break
			}
			buf.Set(start+i, 0, Cell{R: r, FG: titleFG, BG: titleBG, Bold: true})
		}
	}

	innerW := w.Width - 2
	innerH := w.Height - 2
	if innerW <= 0 || innerH <= 0 {
		return buf
	}

	numCols := innerW / 18
	if numCols < 1 {
		numCols = 1
	}
	itemsPerCol := innerH
	if itemsPerCol < 1 {
		itemsPerCol = 1
	}
	itemsPerPage := numCols * itemsPerCol
	selectedIdx := w.selectedIdx
	if selectedIdx < 0 {
		selectedIdx = 0
	}
	page := selectedIdx / itemsPerPage
	startIdx := page * itemsPerPage
	colWidth := innerW / numCols
	if colWidth < 1 {
		colWidth = 1
	}

	for c := 0; c < numCols; c++ {
		for r := 0; r < itemsPerCol; r++ {
			idx := startIdx + c*itemsPerCol + r
			if idx >= len(w.items) {
				continue
			}
			item := w.items[idx]
			name := item.name
			if item.selected && name != ".." {
				name = "•" + name
			}
			if runewidth.StringWidth(name) > colWidth-2 {
				if colWidth >= 4 {
					name = truncateStringToWidth(name, colWidth-3) + "…"
				} else if colWidth >= 1 {
					name = truncateStringToWidth(name, colWidth-1)
				}
			}
			if runewidth.StringWidth(name) < colWidth {
				name += strings.Repeat(" ", colWidth-runewidth.StringWidth(name))
			}
			cellY := 1 + r
			cellX := 1 + c*colWidth
			for i, rn := range name {
				if cellX+i >= w.Width-1 {
					break
				}
				buf.Set(cellX+i, cellY, paneCell(rn, item, idx == selectedIdx && w.active, item.selected))
			}
		}
	}
	return buf
}

func paneCell(rn rune, item fileItem, isSelected, marked bool) Cell {
	bg := colorBlue
	if marked {
		return Cell{R: rn, FG: colorYellow, BG: bg, Bold: true}
	}
	if isSelected {
		if item.isDir {
			return Cell{R: rn, FG: colorDarkGray, BG: colorYellow}
		}
		return Cell{R: rn, FG: colorNavy, BG: colorYellow}
	}
	if item.isDir {
		return Cell{R: rn, FG: colorWhite, BG: bg}
	}
	return Cell{R: rn, FG: colorCyan, BG: bg}
}

// HandleKey handles Up/Down navigation and Enter to open files or enter directories.
func (w *FilePaneWidget) HandleKey(msg tea.KeyMsg) tea.Cmd {
	if w == nil {
		return nil
	}
	switch msg.Type {
	case tea.KeyUp:
		if w.selectedIdx > 0 {
			w.selectedIdx--
		}
	case tea.KeyDown:
		if w.selectedIdx < len(w.items)-1 {
			w.selectedIdx++
		}
	case tea.KeyEnter, tea.KeyRight:
		if item, ok := w.SelectedItem(); ok {
			if item.isDir {
				var target string
				if item.name == ".." {
					target = filepath.Dir(w.dir)
				} else {
					target = filepath.Join(w.dir, item.name)
				}
				_ = w.LoadDir(target)
			} else if w.OnOpenFile != nil {
				w.OnOpenFile(filepath.Join(w.dir, item.name))
			}
		}
	case tea.KeyLeft, tea.KeyBackspace:
		if w.dir != "" {
			_ = w.LoadDir(filepath.Dir(w.dir))
		}
	}
	return nil
}

func (w *FilePaneWidget) HandleMouse(msg tea.MouseMsg) tea.Cmd { return nil }

var _ Widget = (*FilePaneWidget)(nil)
var _ InputWidget = (*FilePaneWidget)(nil)

// DualPaneWidget owns two FilePaneWidgets and routes keyboard input to the active one.
type DualPaneWidget struct {
	Left   *FilePaneWidget
	Right  *FilePaneWidget
	Width  int
	Height int
	X      int
	Y      int

	activePane int // 0=left, 1=right
}

func NewDualPaneWidget() *DualPaneWidget {
	dp := &DualPaneWidget{
		Left:  NewFilePaneWidget(""),
		Right: NewFilePaneWidget(""),
	}
	dp.SetActivePane(0)
	return dp
}

func (w *DualPaneWidget) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	if c.MaxW <= 0 {
		c.MaxW = 80
	}
	if c.MaxH <= 0 {
		c.MaxH = 20
	}
	return Size{W: c.MaxW, H: c.MaxH}
}

func (w *DualPaneWidget) Layout(r Rect) {
	if w == nil {
		return
	}
	w.X = r.X
	w.Y = r.Y
	w.Width = r.W
	w.Height = r.H
	if w.Left == nil {
		w.Left = NewFilePaneWidget("")
	}
	if w.Right == nil {
		w.Right = NewFilePaneWidget("")
	}
	leftW := w.Width / 2
	if leftW < 0 {
		leftW = 0
	}
	if w.Width > 0 {
		leftW = (w.Width - 1) / 2
	}
	if leftW < 0 {
		leftW = 0
	}
	rightX := leftW + 1
	if rightX >= w.Width {
		rightX = w.Width - 1
	}
	rightW := w.Width - rightX
	if rightW < 0 {
		rightW = 0
	}
	if w.Width <= 1 {
		leftW = w.Width
		rightX = 0
		rightW = 0
	}
	w.Left.Layout(Rect{X: r.X, Y: r.Y, W: leftW, H: r.H})
	if rightW > 0 {
		w.Right.Layout(Rect{X: r.X + rightX, Y: r.Y, W: rightW, H: r.H})
	} else {
		w.Right.Layout(Rect{X: r.X + leftW, Y: r.Y, W: 0, H: r.H})
	}
}

func (w *DualPaneWidget) Render() CellBuf {
	buf := NewCellBuf(w.Width, w.Height)
	if w == nil || w.Width <= 0 || w.Height <= 0 {
		return buf
	}
	buf.Fill(Rect{X: 0, Y: 0, W: w.Width, H: w.Height}, Cell{BG: colorBlue})
	if w.Left != nil {
		leftBuf := w.Left.Render()
		if leftBuf.Width > 0 && leftBuf.Height > 0 {
			buf.Blit(leftBuf, w.Left.X-w.X, w.Left.Y-w.Y)
		}
	}
	if w.Right != nil {
		rightBuf := w.Right.Render()
		if rightBuf.Width > 0 && rightBuf.Height > 0 {
			buf.Blit(rightBuf, w.Right.X-w.X, w.Right.Y-w.Y)
		}
	}
	if w.Width > 1 {
		dividerX := w.Left.Width
		if dividerX < 0 {
			dividerX = 0
		}
		if dividerX >= w.Width {
			dividerX = w.Width - 1
		}
		if dividerX >= 0 && dividerX < w.Width {
			for y := 0; y < w.Height; y++ {
				if dividerX-1 >= 0 {
					buf.Set(dividerX-1, y, Cell{R: ' ', FG: colorWhite, BG: colorBlue})
				}
				if dividerX+1 < w.Width {
					buf.Set(dividerX+1, y, Cell{R: ' ', FG: colorWhite, BG: colorBlue})
				}
				buf.Set(dividerX, y, Cell{R: '│', FG: colorGray, BG: colorBlue})
			}
		}
	}
	return buf
}

// HandleKey routes Tab to toggle the active pane; all other keys go to the active child.
func (w *DualPaneWidget) HandleKey(msg tea.KeyMsg) tea.Cmd {
	if w == nil {
		return nil
	}
	if msg.String() == "tab" {
		w.ToggleActivePane()
		return nil
	}
	if w.activePane == 0 && w.Left != nil {
		return w.Left.HandleKey(msg)
	}
	if w.Right != nil {
		return w.Right.HandleKey(msg)
	}
	return nil
}

func (w *DualPaneWidget) HandleMouse(msg tea.MouseMsg) tea.Cmd { return nil }

func (w *DualPaneWidget) ActivePaneIndex() int {
	if w == nil {
		return 0
	}
	return w.activePane
}

func (w *DualPaneWidget) SetActivePane(idx int) {
	if w == nil || (idx != 0 && idx != 1) {
		return
	}
	w.activePane = idx
	if w.Left != nil {
		w.Left.SetActive(idx == 0)
	}
	if w.Right != nil {
		w.Right.SetActive(idx == 1)
	}
}

func (w *DualPaneWidget) ToggleActivePane() {
	if w == nil {
		return
	}
	w.SetActivePane(1 - w.activePane)
}

// SetActivePaneBridge is kept as a compatibility shim for the app-level router.
func (w *DualPaneWidget) SetActivePaneBridge(idx int) {
	w.SetActivePane(idx)
}

// SetFSService wires a filesystem service into both child panes.
func (w *DualPaneWidget) SetFSService(s FSService) {
	if w == nil {
		return
	}
	if w.Left != nil {
		w.Left.SetService(s)
	}
	if w.Right != nil {
		w.Right.SetService(s)
	}
}

// ActivePane returns the currently focused child pane.
func (w *DualPaneWidget) ActivePane() *FilePaneWidget {
	if w == nil {
		return nil
	}
	if w.activePane == 0 {
		return w.Left
	}
	return w.Right
}

func (w *DualPaneWidget) PaneFor(p pane) *FilePaneWidget {
	if w == nil {
		return nil
	}
	switch p {
	case leftPane:
		return w.Left
	case rightPane:
		return w.Right
	default:
		return nil
	}
}

func (w *DualPaneWidget) LoadDirForPane(p pane, path string, focusName string) {
	if w == nil {
		return
	}
	path = filepath.Clean(path)
	filePane := w.PaneFor(p)
	if filePane == nil {
		return
	}

	entries, _ := os.ReadDir(path)
	items := make([]fileItem, 0, len(entries)+1)
	if filepath.Dir(path) != path {
		items = append(items, fileItem{name: "..", isDir: true})
	}
	for _, e := range entries {
		items = append(items, fileItem{name: e.Name(), isDir: e.IsDir()})
	}
	sort.Slice(items, func(i, j int) bool {
		ii, jj := items[i], items[j]
		if ii.name == ".." {
			return true
		}
		if jj.name == ".." {
			return false
		}
		if ii.isDir != jj.isDir {
			return ii.isDir
		}
		return strings.ToLower(ii.name) < strings.ToLower(jj.name)
	})

	filePane.SetDir(path)
	filePane.SetItems(items)
	filePane.Select(0)
	if focusName != "" {
		filePane.SelectByName(focusName)
	}
	filePane.Title = dirTitleName(path)
}

func (w *DualPaneWidget) MoveCursorHorizontal(dir int, height int) {
	if w == nil {
		return
	}
	filePane := w.ActivePane()
	if filePane == nil {
		return
	}
	itemsPerCol := height - 7
	if itemsPerCol <= 0 {
		return
	}
	newIdx := filePane.SelectedIndex() + (dir * itemsPerCol)
	if newIdx < 0 {
		newIdx = 0
	}
	if newIdx >= len(filePane.Items()) {
		newIdx = len(filePane.Items()) - 1
	}
	filePane.Select(newIdx)
}

// OtherPane returns the non-active child pane.
func (w *DualPaneWidget) OtherPane() *FilePaneWidget {
	if w == nil {
		return nil
	}
	if w.activePane == 0 {
		return w.Right
	}
	return w.Left
}

// OtherDir returns the directory of the non-active pane.
func (w *DualPaneWidget) OtherDir() string {
	p := w.OtherPane()
	if p == nil {
		return ""
	}
	return p.Dir()
}

func (w *DualPaneWidget) ActiveDir() string {
	if w == nil {
		return ""
	}
	if w.activePane == 0 && w.Left != nil {
		return w.Left.Dir()
	}
	if w.Right != nil {
		return w.Right.Dir()
	}
	return ""
}

func (w *DualPaneWidget) ActiveSelectedItem() (fileItem, bool) {
	if w == nil {
		return fileItem{}, false
	}
	if w.activePane == 0 && w.Left != nil {
		return w.Left.SelectedItem()
	}
	if w.Right != nil {
		return w.Right.SelectedItem()
	}
	return fileItem{}, false
}

var _ Widget = (*DualPaneWidget)(nil)
var _ InputWidget = (*DualPaneWidget)(nil)

// dirTitleName returns a display title for a directory path.
func dirTitleName(path string) string {
	path = filepath.Clean(path)
	trimmed := strings.TrimRight(path, "/\\")
	if trimmed == "" || trimmed == "." {
		// Handle root or relative current
		if path == "" || path == "." {
			return " . "
		}
		return " / "
	}
	// On Windows, filepath.Clean("C:\\") is "C:\\"
	// filepath.Base("C:\\") is "\"
	base := filepath.Base(path)
	if base == "\\" || base == "/" {
		// If it's a root, show the whole thing or just the drive
		return " " + path + " "
	}
	return " " + base + " "
}
