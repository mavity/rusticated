package filemanager

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"
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
	Size   int64
	Mode   os.FileMode
}

// FSService abstracts filesystem access for widgets that display directory contents.
type FSService interface {
	ReadDir(path string) ([]FSEntry, error)
}

// FSMetadataService provides a direct metadata lookup for the selected item.
// Services without this capability can still refresh metadata by rereading the
// directory through FSService.
type FSMetadataService interface {
	Lstat(path string) (os.FileInfo, error)
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
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("read metadata for %q: %w", full, err)
		}
		isDir := e.IsDir()
		out = append(out, FSEntry{
			Name:   e.Name(),
			IsDir:  isDir,
			IsExec: !isDir && IsExecutableFile(full),
			Size:   info.Size(),
			Mode:   info.Mode(),
		})
	}
	return out, nil
}

func (s *OsFSService) Lstat(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

// FileItem represents a single entry inside a FilePaneWidget.
type FileItem struct {
	name      string
	isDir     bool
	isExec    bool
	selected  bool
	size      int64
	mode      os.FileMode
	modeKnown bool
}

func (f FileItem) Name() string      { return f.name }
func (f FileItem) IsDir() bool       { return f.isDir }
func (f FileItem) IsExec() bool      { return f.isExec }
func (f FileItem) Selected() bool    { return f.selected }
func (f FileItem) Size() int64       { return f.size }
func (f FileItem) Mode() os.FileMode { return f.mode }

type fileItem = FileItem

// NewFileItem constructs a FileItem for programmatic initialization or tests.
func NewFileItem(name string, isDir bool, selected bool) FileItem {
	return FileItem{name: name, isDir: isDir, selected: selected}
}

func fileItemLess(ii, jj fileItem) bool {
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
}

// IsExecutableFile reports whether the file at path is executable. Unix-like
// systems use execute permission bits; Windows uses PATHEXT, with .sh treated
// as a Kabibi shell script.
func IsExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	if runtime.GOOS == "windows" {
		if ext == ".sh" {
			return true
		}
		for _, pathext := range strings.Split(os.Getenv("PATHEXT"), ";") {
			if strings.EqualFold(strings.TrimSpace(pathext), ext) {
				return true
			}
		}
		return false
	}
	return info.Mode()&0o111 != 0
}

func numberPrinterFromEnv() *message.Printer {
	for _, key := range []string{"LC_ALL", "LC_NUMERIC", "LANG"} {
		locale := strings.TrimSpace(os.Getenv(key))
		if locale == "" {
			continue
		}
		locale = strings.SplitN(locale, "@", 2)[0]
		locale = strings.SplitN(locale, ".", 2)[0]
		if strings.EqualFold(locale, "C") || strings.EqualFold(locale, "POSIX") {
			continue
		}
		locale = strings.ReplaceAll(locale, "_", "-")
		tag, err := language.Parse(locale)
		if err == nil && tag != language.Und {
			return message.NewPrinter(tag)
		}
	}
	return message.NewPrinter(language.English)
}

func (w *FilePaneWidget) formatNumber(value any) string {
	printer := w.numberPrinter
	if printer == nil {
		printer = numberPrinterFromEnv()
	}
	return printer.Sprintf("%v", number.Decimal(value))
}

// Color constants matching classic Norton / Far Manager blue panels.
const (
	colorBlue       terminal.Color = 0x0000A8
	colorYellow     terminal.Color = 0xFFD700
	colorWhite      terminal.Color = 0xE5E5E5
	colorGray       terminal.Color = 0xB0B0B0
	colorDarkGray   terminal.Color = 0x3A3A3A
	colorNavy       terminal.Color = 0x000080
	colorCyan       terminal.Color = 0x00D9FF
	colorBrightCyan terminal.Color = 0x55FFFF
	colorGreen      terminal.Color = 0x00FF87
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
// It owns navigation state, handles keyboard input, and returns ANSI frames.
// Title is structural state passed by the parent; operational state (items,
// selection, scroll) is managed internally.
type FilePaneWidget struct {
	Title           string            // display name shown in the title bar
	OnOpenFile      func(path string) // called when the user presses Enter on a regular file
	OnChangeDir     func(path string) // called when the pane changes its directory
	OnMetadataError func(path string, err error)

	dir             string
	items           []fileItem
	selectedIdx     int
	firstVisibleCol int
	service         FSService
	searchText      []rune
	searchPos       int
	searchStart     int
	searching       bool
	searchMatch     bool
	numberPrinter   *message.Printer

	Width  int
	Height int
	X      int
	Y      int
}

func NewFilePaneWidget(title string) *FilePaneWidget {
	return &FilePaneWidget{
		Title:         title,
		service:       &OsFSService{},
		numberPrinter: numberPrinterFromEnv(),
	}
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
			size:   e.Size,
			mode:   e.Mode,
		})
	}
	// Sort: ".." first, then dirs before files, then alphabetically.
	sort.Slice(w.items, func(i, j int) bool { return fileItemLess(w.items[i], w.items[j]) })
	w.selectedIdx = 0
	w.firstVisibleCol = 0
	if focusName != "" {
		for i, item := range w.items {
			if item.name == focusName {
				w.selectedIdx = i
				break
			}
		}
	}
	if w.OnChangeDir != nil {
		w.OnChangeDir(path)
	}
	if item, ok := w.SelectedItem(); ok && item.name == ".." {
		if _, ok := svc.(FSMetadataService); ok {
			w.refreshSelectedMetadata()
		}
	}
	return nil
}

// Refresh reloads the current directory while preserving the selected entry name
// or, if it disappeared, its position in the sorted list.
func (w *FilePaneWidget) Refresh() error {
	if w == nil || w.dir == "" {
		return nil
	}
	prevItem, ok := w.SelectedItem()
	if !ok {
		return w.LoadDir(w.dir)
	}
	if err := w.LoadDirFocus(w.dir, prevItem.name); err != nil {
		return err
	}
	for _, item := range w.items {
		if item.name == prevItem.name {
			w.ensureSelectedColumnVisible()
			return nil
		}
	}
	if len(w.items) == 0 {
		return nil
	}
	// Find where the missing entry would be inserted among the refreshed items.
	w.selectedIdx = sort.Search(len(w.items), func(i int) bool {
		return !fileItemLess(w.items[i], prevItem)
	})
	if w.selectedIdx == len(w.items) {
		w.selectedIdx--
	}
	w.ensureSelectedColumnVisible()
	return nil
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

func (w *FilePaneWidget) fileTotals() (int, uint64) {
	if w == nil {
		return 0, 0
	}
	count := 0
	var total uint64
	for _, item := range w.items {
		if item.name == ".." || item.isDir {
			continue
		}
		count++
		if item.size > 0 {
			total += uint64(item.size)
		}
	}
	return count, total
}

func (w *FilePaneWidget) selectIndex(idx int) {
	if len(w.items) == 0 {
		w.selectedIdx = 0
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(w.items) {
		idx = len(w.items) - 1
	}
	w.selectedIdx = idx
	w.ensureSelectedColumnVisible()
	w.refreshSelectedMetadata()
}

func (w *FilePaneWidget) gridMetrics() (numCols, itemsPerCol int) {
	numCols = (w.Width - 2) / 18
	if numCols < 1 {
		numCols = 1
	}
	if w.Height <= 0 {
		return numCols, 10
	}
	innerH := w.Height - 4
	if innerH < 1 {
		innerH = 1
	}
	return numCols, innerH
}

func (w *FilePaneWidget) clampFirstVisibleCol(numCols, itemsPerCol int) {
	if itemsPerCol < 1 {
		itemsPerCol = 1
	}
	totalCols := (len(w.items) + itemsPerCol - 1) / itemsPerCol
	maxFirstCol := totalCols - numCols
	if maxFirstCol < 0 {
		maxFirstCol = 0
	}
	if w.firstVisibleCol < 0 {
		w.firstVisibleCol = 0
	} else if w.firstVisibleCol > maxFirstCol {
		w.firstVisibleCol = maxFirstCol
	}
}

func (w *FilePaneWidget) ensureSelectedColumnVisible() {
	numCols, itemsPerCol := w.gridMetrics()
	if itemsPerCol < 1 {
		itemsPerCol = 1
	}
	selectedCol := w.selectedIdx / itemsPerCol
	w.clampFirstVisibleCol(numCols, itemsPerCol)
	if selectedCol < w.firstVisibleCol {
		w.firstVisibleCol = selectedCol
	} else if selectedCol >= w.firstVisibleCol+numCols {
		w.firstVisibleCol = selectedCol - numCols + 1
	}
	w.clampFirstVisibleCol(numCols, itemsPerCol)
}

func (w *FilePaneWidget) refreshSelectedMetadata() {
	item, ok := w.SelectedItem()
	if !ok || w.dir == "" {
		return
	}
	previousIsDir := item.isDir
	svc := w.service
	if svc == nil {
		svc = &OsFSService{}
		w.service = svc
	}
	path := filepath.Join(w.dir, item.name)
	if item.name == ".." {
		path = filepath.Dir(w.dir)
	}
	var size int64
	var mode os.FileMode
	if statSvc, ok := svc.(FSMetadataService); ok {
		info, err := statSvc.Lstat(path)
		if err != nil {
			if item.name == ".." {
				if w.OnMetadataError != nil {
					w.OnMetadataError(path, err)
				}
			} else {
				w.handleMetadataError(path, err)
			}
			return
		}
		size, mode = info.Size(), info.Mode()
	} else {
		if item.name == ".." {
			return
		}
		entries, err := w.service.ReadDir(w.dir)
		if err != nil {
			w.handleMetadataError(path, err)
			return
		}
		found := false
		for _, entry := range entries {
			if entry.Name == item.name {
				size, mode = entry.Size, entry.Mode
				item.isDir, item.isExec = entry.IsDir, entry.IsExec
				found = true
				break
			}
		}
		if !found {
			if err := w.Refresh(); err != nil {
				w.handleMetadataError(path, err)
			}
			return
		}
	}
	item.size = size
	item.mode = mode
	item.modeKnown = item.name == ".."
	if _, ok := svc.(FSMetadataService); ok {
		item.isDir = mode.IsDir()
		item.isExec = !item.isDir && IsExecutableFile(path)
	}
	w.items[w.selectedIdx] = item
	if item.isDir != previousIsDir {
		sort.Slice(w.items, func(i, j int) bool { return fileItemLess(w.items[i], w.items[j]) })
		for i, current := range w.items {
			if current.name == item.name {
				w.selectedIdx = i
				break
			}
		}
		w.ensureSelectedColumnVisible()
	}
}

func (w *FilePaneWidget) handleMetadataError(path string, err error) {
	if os.IsNotExist(err) {
		if refreshErr := w.Refresh(); refreshErr == nil {
			return
		} else {
			err = refreshErr
		}
	}
	if w.OnMetadataError != nil {
		w.OnMetadataError(path, err)
	}
}

// SearchActive reports whether this pane's instant-search editor is open.
func (w *FilePaneWidget) SearchActive() bool {
	return w != nil && w.searching
}

// BeginSearch activates instant search from an Alt+printable key event.
func (w *FilePaneWidget) BeginSearch(ev input.KeyPressEvent) bool {
	if w == nil || !ev.Mod.Contains(input.ModAlt) {
		return false
	}
	rn := ev.Code
	if ev.ShiftedCode != 0 {
		rn = ev.ShiftedCode
	}
	text := ev.Text
	if unicode.IsPrint(rn) {
		// Option/Alt can attach a composed glyph to Text; instant search uses the key.
		text = string(rn)
	}
	if text == "" {
		return false
	}
	w.searching = true
	w.searchStart = w.selectedIdx
	w.searchText = nil
	w.searchPos = 0
	w.searchMatch = true
	w.insertSearchText(text)
	return true
}

// CloseSearch closes instant search without changing the current selection.
func (w *FilePaneWidget) CloseSearch() {
	if w == nil || !w.searching {
		return
	}
	w.searching = false
	w.searchText = nil
	w.searchPos = 0
	w.searchMatch = false
}

// HandleSearchEvent edits the active instant-search query. It returns false
// when the event should be handled by the surrounding file manager instead.
func (w *FilePaneWidget) HandleSearchEvent(ev input.KeyPressEvent) bool {
	if w == nil || !w.searching || ev.Mod.Contains(input.ModCtrl) || ev.Mod.Contains(input.ModMeta) {
		return false
	}
	switch ev.String() {
	case "left":
		if w.searchPos > 0 {
			w.searchPos--
		}
		return true
	case "right":
		if w.searchPos < len(w.searchText) {
			w.searchPos++
		}
		return true
	case "home":
		w.searchPos = 0
		return true
	case "end":
		w.searchPos = len(w.searchText)
		return true
	case "backspace":
		if w.searchPos > 0 {
			w.searchText = append(w.searchText[:w.searchPos-1], w.searchText[w.searchPos:]...)
			w.searchPos--
			w.updateSearchMatch()
		}
		return true
	case "delete":
		if w.searchPos < len(w.searchText) {
			w.searchText = append(w.searchText[:w.searchPos], w.searchText[w.searchPos+1:]...)
			w.updateSearchMatch()
		}
		return true
	}
	if ev.Text != "" {
		w.insertSearchText(ev.Text)
		return true
	}
	if unicode.IsPrint(ev.Code) {
		w.insertSearchText(string(ev.Code))
		return true
	}
	return false
}

func (w *FilePaneWidget) insertSearchText(text string) {
	runes := []rune(text)
	if len(runes) == 0 {
		return
	}
	w.searchText = append(w.searchText[:w.searchPos], append(runes, w.searchText[w.searchPos:]...)...)
	w.searchPos += len(runes)
	w.updateSearchMatch()
}

func (w *FilePaneWidget) updateSearchMatch() {
	if len(w.searchText) == 0 {
		w.selectIndex(w.searchStart)
		w.searchMatch = true
		return
	}
	query := strings.ToLower(string(w.searchText))
	if idx := w.findSearchMatch(func(name string) bool {
		return strings.HasPrefix(strings.ToLower(name), query)
	}); idx >= 0 {
		w.selectIndex(idx)
		w.searchMatch = true
		return
	}
	if idx := w.findSearchMatch(func(name string) bool {
		return matchesSearchSubsequence(name, query)
	}); idx >= 0 {
		w.selectIndex(idx)
		w.searchMatch = true
		return
	}
	w.searchMatch = false
}

func (w *FilePaneWidget) findSearchMatch(matches func(string) bool) int {
	if len(w.items) == 0 {
		return -1
	}
	start := w.selectedIdx + 1
	if start < 0 || start >= len(w.items) {
		start = 0
	}
	for offset := 0; offset < len(w.items); offset++ {
		idx := (start + offset) % len(w.items)
		item := w.items[idx]
		if item.name != ".." && matches(item.name) {
			return idx
		}
	}
	return -1
}

func matchesSearchSubsequence(name, query string) bool {
	nameRunes := []rune(strings.ToLower(name))
	queryRunes := []rune(query)
	if len(queryRunes) == 0 || len(nameRunes) == 0 || nameRunes[0] != queryRunes[0] {
		return false
	}
	next := 1
	for _, rn := range nameRunes[1:] {
		if next < len(queryRunes) && rn == queryRunes[next] {
			next++
		}
		if next == len(queryRunes) {
			return true
		}
	}
	return next == len(queryRunes)
}

// Select sets the highlighted index; no-op if out of range.
func (w *FilePaneWidget) Select(idx int) {
	if w == nil {
		return
	}
	w.selectIndex(idx)
}

// SelectByName scrolls to the first item whose name matches; no-op if not found.
func (w *FilePaneWidget) SelectByName(name string) {
	if w == nil {
		return
	}
	for i, fi := range w.items {
		if fi.name == name {
			w.selectIndex(i)
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
	w.ensureSelectedColumnVisible()
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
	w.firstVisibleCol = 0
	w.ensureSelectedColumnVisible()
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
	w.ensureSelectedColumnVisible()
}

func (w *FilePaneWidget) Render(r Rect, ctx RenderContext) string {
	if r.W > 0 || r.H > 0 {
		w.Layout(r)
	}
	return ui.ANSIFrame(w.renderBuf(ctx.Focused), CursorPos{})
}

func (w *FilePaneWidget) RenderBuf() CellBuf {
	return w.renderBuf(false)
}

func (w *FilePaneWidget) renderBuf(focused bool) CellBuf {
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
		if focused {
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

	listHeight := innerH
	if innerH >= 2 {
		listHeight -= 2
	}
	if listHeight > 0 {
		numCols, itemsPerCol := w.gridMetrics()
		itemsPerCol = listHeight
		w.ensureSelectedColumnVisible()
		selectedIdx := w.selectedIdx
		if selectedIdx < 0 {
			selectedIdx = 0
		}
		startIdx := w.firstVisibleCol * itemsPerCol
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
					buf.Set(cellX+colOff, cellY, paneCell(rn, item, idx == selectedIdx && focused, item.selected))
					colOff += runewidth.RuneWidth(rn)
				}
			}
		}
	}
	if innerH >= 2 {
		w.renderSelectedDetails(&buf)
	}
	if w.searching {
		w.renderSearch(buf)
	} else {
		w.renderTotal(&buf)
	}
	return buf
}

func (w *FilePaneWidget) renderSelectedDetails(buf *CellBuf) {
	separatorY := w.Height - 3
	detailY := w.Height - 2
	for x := 1; x < w.Width-1; x++ {
		buf.Set(x, separatorY, makeCell('─', colorGray, colorBlue, false))
		buf.Set(x, detailY, makeCell(' ', colorWhite, colorBlue, false))
	}
	if w.Width > 1 {
		buf.Set(0, separatorY, makeCell('├', colorGray, colorBlue, false))
		buf.Set(w.Width-1, separatorY, makeCell('┤', colorGray, colorBlue, false))
	}
	item, ok := w.SelectedItem()
	if !ok {
		return
	}
	displayName := item.name
	if item.name == ".." && w.dir != "" {
		displayName = strings.TrimSpace(dirTitleName(filepath.Dir(w.dir)))
	}
	flags := make([]string, 0, 4)
	if (item.name != ".." || item.modeKnown) && item.mode.Perm()&0o222 == 0 {
		flags = append(flags, "R")
	}
	if item.name != ".." && strings.HasPrefix(item.name, ".") {
		flags = append(flags, "H")
	}
	if item.isExec {
		flags = append(flags, "X")
	}
	suffix := ""
	if len(flags) > 0 {
		suffix = fmt.Sprintf("[%s]", strings.Join(flags, ","))
	}
	if item.name != ".." || item.modeKnown {
		if suffix != "" {
			suffix += " "
		}
		suffix += item.mode.Perm().String()
	}
	sizeStart := -1
	if !item.isDir {
		if suffix != "" {
			sizeStart = runewidth.StringWidth(suffix) + 1
			suffix += " "
		} else {
			sizeStart = 0
		}
		suffix += w.formatNumber(item.size) + "b"
	}
	availableWidth := w.Width - 2
	if suffixWidth := runewidth.StringWidth(suffix); suffixWidth > availableWidth {
		suffix = truncateStringToWidth(suffix, availableWidth)
	}
	suffixWidth := runewidth.StringWidth(suffix)
	suffixX := w.Width - 1 - suffixWidth
	nameWidth := suffixX - 2
	if nameWidth < 0 {
		nameWidth = 0
	}
	name := truncateStringToWidth(displayName, nameWidth)
	col := 1
	for _, rn := range name {
		if col >= w.Width-1 {
			break
		}
		buf.Set(col, detailY, makeCell(rn, colorWhite, colorBlue, false))
		col += runewidth.RuneWidth(rn)
	}
	col = suffixX
	suffixOffset := 0
	for _, rn := range suffix {
		if col >= w.Width-1 {
			break
		}
		fg := colorGray
		if suffixOffset >= sizeStart && unicode.IsDigit(rn) {
			fg = colorBrightCyan
		}
		buf.Set(col, detailY, makeCell(rn, fg, colorBlue, false))
		runeWidth := runewidth.RuneWidth(rn)
		col += runeWidth
		suffixOffset += runeWidth
	}
}

func (w *FilePaneWidget) renderTotal(buf *CellBuf) {
	count, total := w.fileTotals()
	text := fmt.Sprintf("[%s]──%sb", w.formatNumber(int64(count)), w.formatNumber(total))
	y := w.Height - 1
	col := 4
	for _, rn := range text {
		if col >= w.Width-1 {
			break
		}
		fg := colorGray
		if unicode.IsDigit(rn) {
			fg = colorBrightCyan
		}
		buf.Set(col, y, makeCell(rn, fg, colorBlue, false))
		col += runewidth.RuneWidth(rn)
	}
}

func (w *FilePaneWidget) renderSearch(buf CellBuf) {
	if w.Width < 4 || w.Height < 2 {
		return
	}
	var display strings.Builder
	for i, rn := range w.searchText {
		if i == w.searchPos {
			display.WriteRune('|')
		}
		display.WriteRune(rn)
	}
	if w.searchPos == len(w.searchText) {
		display.WriteRune('|')
	}
	content := display.String()
	available := w.Width - 4
	if runewidth.StringWidth(content) > available {
		content = truncateStringToWidth(content, available)
	}
	color := colorCyan
	if w.searchMatch {
		color = colorYellow
	}
	fieldW := runewidth.StringWidth(content) + 2
	startX := w.Width - 1 - fieldW
	y := w.Height - 1
	buf.Set(startX, y, makeCell('[', color, colorNavy, true))
	col := startX + 1
	for _, rn := range content {
		if col >= w.Width-2 {
			break
		}
		buf.Set(col, y, makeCell(rn, color, colorNavy, true))
		col += runewidth.RuneWidth(rn)
	}
	buf.Set(w.Width-2, y, makeCell(']', color, colorNavy, true))
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
	_, itemsPerCol := w.gridMetrics()
	return itemsPerCol
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
		w.selectIndex(w.selectedIdx - 1)
		return true
	case "down":
		w.selectIndex(w.selectedIdx + 1)
		return true
	case "left":
		step := w.itemsPerCol()
		w.selectIndex(w.selectedIdx - step)
		return true
	case "right":
		step := w.itemsPerCol()
		w.selectIndex(w.selectedIdx + step)
		return true
	case "pgup":
		numCols, itemsPerCol := w.gridMetrics()
		step := itemsPerCol * numCols
		w.firstVisibleCol -= numCols
		w.selectIndex(w.selectedIdx - step)
		return true
	case "pgdown":
		numCols, itemsPerCol := w.gridMetrics()
		step := itemsPerCol * numCols
		w.firstVisibleCol += numCols
		w.selectIndex(w.selectedIdx + step)
		return true
	case "home":
		w.selectIndex(0)
		return true
	case "end":
		w.selectIndex(len(w.items) - 1)
		return true
	case "insert":
		w.MarkToggle(w.selectedIdx)
		if w.selectedIdx+1 < len(w.items) {
			w.selectIndex(w.selectedIdx + 1)
		} else {
			w.refreshSelectedMetadata()
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

func (w *DualPaneWidget) Render(r Rect, ctx RenderContext) string {
	if r.W > 0 || r.H > 0 {
		w.Layout(r)
	}
	return ui.ANSIFrame(w.renderBuf(ctx.Focused), CursorPos{})
}

func (w *DualPaneWidget) RenderBuf() CellBuf {
	return w.renderBuf(false)
}

func (w *DualPaneWidget) renderBuf(focused bool) CellBuf {
	buf := NewCellBuf(w.Width, w.Height)
	if w == nil || w.Width <= 0 || w.Height <= 0 {
		return buf
	}
	buf.Fill(Rect{X: 0, Y: 0, W: w.Width, H: w.Height}, makeCell(' ', colorWhite, colorBlue, false))
	if w.Left != nil {
		leftBuf := w.Left.renderBuf(focused && w.activePane == 0)
		if leftBuf.Width > 0 && leftBuf.Height > 0 {
			buf.Blit(leftBuf, w.Left.X-w.X, w.Left.Y-w.Y)
		}
	}
	if w.Right != nil {
		rightBuf := w.Right.renderBuf(focused && w.activePane == 1)
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
				switch y {
				case 0:
					if dividerX-1 >= 0 {
						buf.Set(dividerX-1, y, makeCell('─', colorGray, colorBlue, false))
					}
					if dividerX+1 < w.Width {
						buf.Set(dividerX+1, y, makeCell('─', colorGray, colorBlue, false))
					}
					buf.Set(dividerX, y, makeCell('┬', colorGray, colorBlue, false))
				case w.Height - 1:
					if dividerX-1 >= 0 {
						buf.Set(dividerX-1, y, makeCell('─', colorGray, colorBlue, false))
					}
					if dividerX+1 < w.Width {
						buf.Set(dividerX+1, y, makeCell('─', colorGray, colorBlue, false))
					}
					buf.Set(dividerX, y, makeCell('┴', colorGray, colorBlue, false))
				case w.Height - 3:
					if dividerX-1 >= 0 {
						buf.Set(dividerX-1, y, makeCell('─', colorGray, colorBlue, false))
					}
					if dividerX+1 < w.Width {
						buf.Set(dividerX+1, y, makeCell('─', colorGray, colorBlue, false))
					}
					buf.Set(dividerX, y, makeCell('┼', colorGray, colorBlue, false))
				default:
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
