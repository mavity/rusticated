package filemanager

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type (
	Constraints   = ui.Constraints
	Size          = ui.Size
	Rect          = ui.Rect
	CellBuf       = terminal.CellBuf
	Cell          = terminal.Cell
	CursorPos     = ui.CursorPos
	RenderContext = ui.RenderContext
	Widget        = ui.Widget
)

var NewCellBuf = terminal.NewCellBuf

// PaneSide identifies the left or right pane in DualPaneWidget.
type PaneSide int

const (
	LeftPane PaneSide = iota
	RightPane

	// Aliases for legacy callers/tests
	leftPane  = LeftPane
	rightPane = RightPane
)

type pane = PaneSide

// FSEntry is a single item returned by FSService.ReadDir.
type FSEntry struct {
	Name   string
	IsDir  bool
	IsExec bool
}

// FSService abstracts filesystem access for widgets that display directory contents.
type FSService interface {
	ReadDir(path string) ([]FSEntry, error)
}

// OsFSService reads directory listings from the local filesystem.
type OsFSService struct{}

func (s *OsFSService) ReadDir(path string) ([]FSEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]FSEntry, 0, len(entries))
	for _, e := range entries {
		full := filepath.Join(path, e.Name())
		isDir := e.IsDir()
		out = append(out, FSEntry{
			Name:   e.Name(),
			IsDir:  isDir,
			IsExec: !isDir && IsExecutableFile(full),
		})
	}
	return out, nil
}

// FileItem represents a single entry inside a FilePaneWidget.
type FileItem struct {
	name     string
	isDir    bool
	isExec   bool
	selected bool
}

func (f FileItem) Name() string   { return f.name }
func (f FileItem) IsDir() bool    { return f.isDir }
func (f FileItem) IsExec() bool   { return f.isExec }
func (f FileItem) Selected() bool { return f.selected }

type fileItem = FileItem

// NewFileItem constructs a FileItem for programmatic initialization or tests.
func NewFileItem(name string, isDir bool, selected bool) FileItem {
	return FileItem{name: name, isDir: isDir, selected: selected}
}

// IsExecutableFile reports whether the file at path is executable (via mode bits,
// Windows PATHEXT / .sh extension, or a #! shebang header).
func IsExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".sh", ".exe", ".bat", ".cmd", ".com", ".ps1":
		return true
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 != 0 {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [2]byte
	if n, _ := f.Read(hdr[:]); n == 2 && bytes.Equal(hdr[:], []byte("#!")) {
		return true
	}
	return false
}

// Color constants matching classic Norton / Far Manager blue panels.
const (
	colorBlue     terminal.Color = 0x0000A8
	colorYellow   terminal.Color = 0xFFD700
	colorWhite    terminal.Color = 0xE5E5E5
	colorGray     terminal.Color = 0xB0B0B0
	colorDarkGray terminal.Color = 0x3A3A3A
	colorNavy     terminal.Color = 0x000080
	colorCyan     terminal.Color = 0x00D9FF
	colorGreen    terminal.Color = 0x00FF87
)

func makeCell(r rune, fg, bg terminal.Color, bold bool) Cell {
	var attrs terminal.CharAttributes
	if bold {
		attrs |= terminal.CharBold
	}
	return Cell{
		Grapheme: string(r),
		Style:    terminal.NewStyle(fg, bg, attrs),
	}
}

func truncateStringToWidth(s string, maxWidth int) string {
	displayWidth := runewidth.StringWidth(s)
	if displayWidth <= maxWidth {
		return s
	}
	effectiveWidth := maxWidth - 1
	if effectiveWidth <= 0 {
		return ""
	}
	width := 0
	var b strings.Builder
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if width+w > effectiveWidth {
			break
		}
		b.WriteRune(r)
		width += w
	}
	return strings.TrimRight(b.String(), " ")
}

// FilePaneWidget is the complete model for a single file-browser pane.
// It owns navigation state, handles keyboard input, and renders cells.
// Title is structural state passed by the parent; operational state (items,
// selection, scroll) is managed internally.
type FilePaneWidget struct {
	Title       string            // display name shown in the title bar
	OnOpenFile  func(path string) // called when the user presses Enter on a regular file
	OnChangeDir func(path string) // called when the pane changes its directory

	active      bool
	dir         string
	items       []fileItem
	selectedIdx int
	service     FSService
	ctx         RenderContext

	Width  int
	Height int
	X      int
	Y      int
}

func NewFilePaneWidget(title string) *FilePaneWidget {
	return &FilePaneWidget{
		Title:   title,
		service: &OsFSService{},
	}
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
	return w.LoadDirFocus(path, "")
}

// LoadDirFocus loads path and optionally selects focusName if present.
func (w *FilePaneWidget) LoadDirFocus(path string, focusName string) error {
	if w == nil {
		return nil
	}
	svc := w.service
	if svc == nil {
		svc = &OsFSService{}
		w.service = svc
	}
	path = filepath.Clean(path)
	entries, err := svc.ReadDir(path)
	if err != nil {
		return err
	}
	w.dir = path
	w.Title = strings.TrimSpace(dirTitleName(path))
	w.items = make([]fileItem, 0, len(entries)+1)
	if filepath.Dir(path) != path {
		w.items = append(w.items, fileItem{name: "..", isDir: true})
	}
	for _, e := range entries {
		w.items = append(w.items, fileItem{
			name:   e.Name,
			isDir:  e.IsDir,
			isExec: e.IsExec,
		})
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
	w.selectedIdx = 0
	if focusName != "" {
		w.SelectByName(focusName)
	}
	if w.OnChangeDir != nil {
		w.OnChangeDir(path)
	}
	return nil
}

// Refresh reloads the current directory while preserving the selected entry name if it still exists.
func (w *FilePaneWidget) Refresh() error {
	if w == nil || w.dir == "" {
		return nil
	}
	var prevName string
	if fi, ok := w.SelectedItem(); ok {
		prevName = fi.name
	}
	return w.LoadDirFocus(w.dir, prevName)
}

// SelectedItem returns the currently highlighted item.
func (w *FilePaneWidget) SelectedItem() (fileItem, bool) {
	if w == nil || w.selectedIdx < 0 || w.selectedIdx >= len(w.items) {
		return fileItem{}, false
	}
	return w.items[w.selectedIdx], true
}

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
	if w == nil || len(w.items) == 0 {
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(w.items) {
		idx = len(w.items) - 1
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

func (w *FilePaneWidget) Render(r Rect, ctx RenderContext) (CellBuf, CursorPos) {
	if r.W > 0 || r.H > 0 {
		w.Layout(r)
	}
	w.ctx = ctx
	return w.RenderBuf(), CursorPos{Visible: false}
}

func (w *FilePaneWidget) RenderBuf() CellBuf {
	buf := NewCellBuf(w.Width, w.Height)
	if w.Width <= 0 || w.Height <= 0 {
		return buf
	}

	bg := colorBlue
	buf.Fill(Rect{X: 0, Y: 0, W: w.Width, H: w.Height}, makeCell(' ', colorWhite, bg, false))

	if w.Width >= 2 && w.Height >= 2 {
		for x := 1; x < w.Width-1; x++ {
			buf.Set(x, 0, makeCell('─', colorGray, bg, false))
			buf.Set(x, w.Height-1, makeCell('─', colorGray, bg, false))
		}
		for y := 1; y < w.Height-1; y++ {
			buf.Set(0, y, makeCell('│', colorGray, bg, false))
			buf.Set(w.Width-1, y, makeCell('│', colorGray, bg, false))
		}
		buf.Set(0, 0, makeCell('┌', colorGray, bg, false))
		buf.Set(w.Width-1, 0, makeCell('┐', colorGray, bg, false))
		buf.Set(0, w.Height-1, makeCell('└', colorGray, bg, false))
		buf.Set(w.Width-1, w.Height-1, makeCell('┘', colorGray, bg, false))
	}

	title := strings.TrimSpace(w.Title)
	if title == "" {
		title = "."
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
		colOff := 0
		for _, rn := range label {
			if start+colOff >= w.Width-1 {
				break
			}
			buf.Set(start+colOff, 0, makeCell(rn, titleFG, titleBG, true))
			colOff += runewidth.RuneWidth(rn)
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
			if item.isExec && !item.isDir && name != ".." {
				name = name + "*"
			}
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
			colOff := 0
			for _, rn := range name {
				if cellX+colOff >= w.Width-1 {
					break
				}
				buf.Set(cellX+colOff, cellY, paneCell(rn, item, idx == selectedIdx && w.active, item.selected))
				colOff += runewidth.RuneWidth(rn)
			}
		}
	}
	return buf
}

func paneCell(rn rune, item fileItem, isSelected, marked bool) Cell {
	bg := colorBlue
	if isSelected {
		if marked {
			return makeCell(rn, colorNavy, colorYellow, true)
		}
		if item.isDir {
			return makeCell(rn, colorDarkGray, colorYellow, false)
		}
		return makeCell(rn, colorNavy, colorYellow, false)
	}
	if marked {
		return makeCell(rn, colorYellow, bg, true)
	}
	if item.isDir {
		return makeCell(rn, colorWhite, bg, false)
	}
	if item.isExec {
		return makeCell(rn, colorGreen, bg, false)
	}
	return makeCell(rn, colorCyan, bg, false)
}

func (w *FilePaneWidget) itemsPerCol() int {
	h := w.Height - 2
	if h < 1 {
		h = 10
	}
	return h
}

// ActivateSelected steps into the selected directory (focusing the previous child
// directory name when ascending via "..") or invokes OnOpenFile for regular files.
func (w *FilePaneWidget) ActivateSelected() bool {
	if w == nil {
		return false
	}
	item, ok := w.SelectedItem()
	if !ok {
		return false
	}
	if item.isDir {
		if item.name == ".." {
			prevBase := filepath.Base(w.dir)
			target := filepath.Dir(w.dir)
			_ = w.LoadDirFocus(target, prevBase)
		} else {
			target := filepath.Join(w.dir, item.name)
			_ = w.LoadDirFocus(target, "")
		}
		return true
	}
	if w.OnOpenFile != nil {
		w.OnOpenFile(filepath.Join(w.dir, item.name))
		return true
	}
	return false
}

// HandleEvent processes canonical x/input.Event navigation and actions.
func (w *FilePaneWidget) HandleEvent(e input.Event) bool {
	if w == nil {
		return false
	}
	var ev input.KeyPressEvent
	switch k := e.(type) {
	case input.KeyPressEvent:
		ev = k
	case *input.KeyPressEvent:
		if k == nil {
			return false
		}
		ev = *k
	default:
		return false
	}

	switch ev.String() {
	case "up":
		if w.selectedIdx > 0 {
			w.selectedIdx--
		}
		return true
	case "down":
		if w.selectedIdx < len(w.items)-1 {
			w.selectedIdx++
		}
		return true
	case "left":
		step := w.itemsPerCol()
		if w.selectedIdx-step >= 0 {
			w.selectedIdx -= step
		} else {
			w.selectedIdx = 0
		}
		return true
	case "right":
		step := w.itemsPerCol()
		if len(w.items) > 0 {
			if w.selectedIdx+step < len(w.items) {
				w.selectedIdx += step
			} else {
				w.selectedIdx = len(w.items) - 1
			}
		}
		return true
	case "pgup":
		step := w.itemsPerCol() * 2
		if w.selectedIdx-step >= 0 {
			w.selectedIdx -= step
		} else {
			w.selectedIdx = 0
		}
		return true
	case "pgdown":
		step := w.itemsPerCol() * 2
		if len(w.items) > 0 {
			if w.selectedIdx+step < len(w.items) {
				w.selectedIdx += step
			} else {
				w.selectedIdx = len(w.items) - 1
			}
		}
		return true
	case "home":
		w.selectedIdx = 0
		return true
	case "end":
		if len(w.items) > 0 {
			w.selectedIdx = len(w.items) - 1
		}
		return true
	case "insert":
		w.MarkToggle(w.selectedIdx)
		if w.selectedIdx+1 < len(w.items) {
			w.selectedIdx++
		}
		return true
	case "enter":
		return w.ActivateSelected()
	case "backspace":
		if w.dir != "" {
			prevBase := filepath.Base(w.dir)
			_ = w.LoadDirFocus(filepath.Dir(w.dir), prevBase)
			return true
		}
	}
	return false
}

var _ Widget = (*FilePaneWidget)(nil)

// DualPaneWidget owns two FilePaneWidgets and routes keyboard input to the active one.
type DualPaneWidget struct {
	Left   *FilePaneWidget
	Right  *FilePaneWidget
	Width  int
	Height int
	X      int
	Y      int

	activePane int // 0=left, 1=right
	ctx        RenderContext
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
	leftW := 0
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

func (w *DualPaneWidget) Render(r Rect, ctx RenderContext) (CellBuf, CursorPos) {
	if r.W > 0 || r.H > 0 {
		w.Layout(r)
	}
	w.ctx = ctx
	return w.RenderBuf(), CursorPos{Visible: false}
}

func (w *DualPaneWidget) RenderBuf() CellBuf {
	buf := NewCellBuf(w.Width, w.Height)
	if w == nil || w.Width <= 0 || w.Height <= 0 {
		return buf
	}
	buf.Fill(Rect{X: 0, Y: 0, W: w.Width, H: w.Height}, makeCell(' ', colorWhite, colorBlue, false))
	if w.Left != nil {
		leftBuf := w.Left.RenderBuf()
		if leftBuf.Width > 0 && leftBuf.Height > 0 {
			buf.Blit(leftBuf, w.Left.X-w.X, w.Left.Y-w.Y)
		}
	}
	if w.Right != nil {
		rightBuf := w.Right.RenderBuf()
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
					buf.Set(dividerX-1, y, makeCell(' ', colorWhite, colorBlue, false))
				}
				if dividerX+1 < w.Width {
					buf.Set(dividerX+1, y, makeCell(' ', colorWhite, colorBlue, false))
				}
				buf.Set(dividerX, y, makeCell('│', colorGray, colorBlue, false))
			}
		}
	}
	return buf
}

// HandleEvent routes Tab to toggle the active pane; all other events go to the active child.
func (w *DualPaneWidget) HandleEvent(e input.Event) bool {
	if w == nil {
		return false
	}
	var ev input.KeyPressEvent
	switch k := e.(type) {
	case input.KeyPressEvent:
		ev = k
	case *input.KeyPressEvent:
		if k == nil {
			return false
		}
		ev = *k
	default:
		return false
	}

	if ev.String() == "tab" {
		w.ToggleActivePane()
		return true
	}
	if w.activePane == 0 && w.Left != nil {
		return w.Left.HandleEvent(ev)
	}
	if w.Right != nil {
		return w.Right.HandleEvent(ev)
	}
	return false
}

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
	filePane := w.PaneFor(p)
	if filePane == nil {
		return
	}
	_ = filePane.LoadDirFocus(path, focusName)
}

// RefreshBoth reloads both Left and Right panes from disk, preserving their current selection names.
func (w *DualPaneWidget) RefreshBoth() {
	if w == nil {
		return
	}
	if w.Left != nil {
		_ = w.Left.Refresh()
	}
	if w.Right != nil {
		_ = w.Right.Refresh()
	}
}

func (w *DualPaneWidget) MoveCursorHorizontal(dir int, height int) {
	if w == nil {
		return
	}
	filePane := w.ActivePane()
	if filePane == nil || len(filePane.Items()) == 0 {
		return
	}
	itemsPerCol := height - 2
	if itemsPerCol <= 0 {
		itemsPerCol = 1
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

// dirTitleName returns a display title for a directory path.
func dirTitleName(path string) string {
	path = filepath.Clean(path)
	trimmed := strings.TrimRight(path, "/\\")
	if trimmed == "" || trimmed == "." {
		if path == "" || path == "." {
			return " . "
		}
		return " / "
	}
	base := filepath.Base(path)
	if base == "\\" || base == "/" {
		return " " + path + " "
	}
	return " " + base + " "
}
