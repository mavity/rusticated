package widgets

import (
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type TrackKind int

const (
	TrackAuto TrackKind = iota
	TrackFixed
	TrackFraction
)

type Track struct {
	Kind  TrackKind
	Value int
}

type GridChild struct {
	Row     int
	Column  int
	RowSpan int
	ColSpan int
	Child   ui.Widget
}

type Grid struct {
	Rows     []Track
	Columns  []Track
	Children []GridChild
	Focused  int

	compositor *terminal.Terminal
}

type resolvedGridChild struct {
	index int
	rect  Rect
}

type resolvedGridLayout struct {
	colWidths  []int
	rowHeights []int
	children   []resolvedGridChild
	size       Size
}

func (g *Grid) Measure(c Constraints) Size {
	if g == nil {
		return Size{}
	}
	return g.resolve(c).size
}

func (g *Grid) Render(r Rect, ctx RenderContext) string {
	width, height := max(0, r.W), max(0, r.H)
	buf := terminal.NewCellBuf(width, height)
	if g == nil || width == 0 || height == 0 {
		return ui.ANSIFrame(buf, CursorPos{})
	}
	layout := g.resolve(Constraints{MaxW: width, MaxH: height})
	var cursor CursorPos
	for _, rc := range layout.children {
		ch := g.Children[rc.index].Child
		if ch == nil || rc.rect.W <= 0 || rc.rect.H <= 0 {
			continue
		}
		childCtx := ctx.ForChild(rc.index == g.Focused)
		childANSI := ch.Render(Rect{W: rc.rect.W, H: rc.rect.H}, childCtx)
		childCursor := childCtx.CompositeANSI(&buf, g.ansiTerminal(), rc.rect, childANSI)
		if childCursor.Visible {
			cursor = childCursor
		}
	}
	return ui.ANSIFrame(buf, cursor)
}

func (g *Grid) HandleEvent(event input.Event) bool {
	if g == nil || len(g.Children) == 0 {
		return false
	}
	if g.Focused >= 0 && g.Focused < len(g.Children) {
		if child := g.Children[g.Focused].Child; child != nil {
			if child.HandleEvent(event) {
				return true
			}
		}
	}
	return false
}

func (g *Grid) SetFocusedWidget(target ui.Widget) bool {
	if g == nil {
		return false
	}
	g.Focused = -1
	if target == nil {
		return false
	}
	found := false
	for i, ch := range g.Children {
		if ch.Child == nil {
			continue
		}
		matched := ch.Child == target
		if setFocusedDescendant(ch.Child, target) {
			matched = true
		}
		if matched && !found {
			g.Focused = i
			found = true
		}
	}
	return found
}

func setFocusedDescendant(node, target ui.Widget) bool {
	switch container := node.(type) {
	case *Grid:
		return container.SetFocusedWidget(target)
	case *Frame:
		return container.SetFocusedWidget(target)
	default:
		return false
	}
}

func (g *Grid) ansiTerminal() *terminal.Terminal {
	if g.compositor == nil {
		g.compositor = terminal.NewTerminal(1, 1)
	}
	return g.compositor
}

func (g *Grid) resolve(c Constraints) resolvedGridLayout {
	numRows := len(g.Rows)
	numCols := len(g.Columns)
	for _, ch := range g.Children {
		r := max(0, ch.Row) + max(1, ch.RowSpan)
		col := max(0, ch.Column) + max(1, ch.ColSpan)
		if r > numRows {
			numRows = r
		}
		if col > numCols {
			numCols = col
		}
	}
	if numRows == 0 || numCols == 0 {
		return resolvedGridLayout{}
	}

	cols := make([]Track, numCols)
	if len(g.Columns) == 0 && numCols == 1 {
		cols[0] = Track{Kind: TrackFraction, Value: 1}
	} else {
		for i := 0; i < numCols; i++ {
			if i < len(g.Columns) {
				cols[i] = g.Columns[i]
			} else {
				cols[i] = Track{Kind: TrackAuto}
			}
			if cols[i].Kind == TrackFraction && cols[i].Value <= 0 {
				cols[i].Value = 1
			}
		}
	}

	rows := make([]Track, numRows)
	for i := 0; i < numRows; i++ {
		if i < len(g.Rows) {
			rows[i] = g.Rows[i]
		} else {
			rows[i] = Track{Kind: TrackAuto}
		}
		if rows[i].Kind == TrackFraction && rows[i].Value <= 0 {
			rows[i].Value = 1
		}
	}

	availW := c.MaxW
	hasMaxW := availW > 0
	availH := c.MaxH
	hasMaxH := availH > 0

	colWidths := make([]int, numCols)
	fixedW := 0
	for col := 0; col < numCols; col++ {
		if cols[col].Kind == TrackFixed {
			colWidths[col] = max(0, cols[col].Value)
			fixedW += colWidths[col]
		}
	}

	provisionalMaxW := availW
	if hasMaxW {
		provisionalMaxW = max(1, availW-fixedW)
	}
	for _, ch := range g.Children {
		if ch.Child == nil {
			continue
		}
		col := max(0, ch.Column)
		cSpan := max(1, ch.ColSpan)
		if cSpan != 1 {
			continue
		}
		if cols[col].Kind == TrackAuto || (!hasMaxW && cols[col].Kind == TrackFraction) {
			sz := ch.Child.Measure(Constraints{MaxW: provisionalMaxW, MaxH: availH})
			if sz.W > colWidths[col] {
				colWidths[col] = sz.W
			}
		}
	}

	for _, ch := range g.Children {
		if ch.Child == nil {
			continue
		}
		col := max(0, ch.Column)
		cSpan := max(1, ch.ColSpan)
		if cSpan <= 1 {
			continue
		}
		hasFractionInSpan := false
		autoTracks := 0
		for k := col; k < col+cSpan && k < numCols; k++ {
			if cols[k].Kind == TrackFraction {
				hasFractionInSpan = true
			} else if cols[k].Kind == TrackAuto {
				autoTracks++
			}
		}
		if (!hasFractionInSpan || !hasMaxW) && autoTracks > 0 {
			sz := ch.Child.Measure(Constraints{MaxW: provisionalMaxW, MaxH: availH})
			currentSpanW := spanSum(colWidths, col, cSpan)
			if sz.W > currentSpanW {
				distributeSpanDeficit(colWidths, cols, col, cSpan, sz.W-currentSpanW, TrackAuto)
			}
		}
	}

	if hasMaxW {
		nonFracW := sumTracks(colWidths)
		if nonFracW <= availW {
			distributeFractions(colWidths, cols, availW-nonFracW)
		} else {
			squeezeAutoTracks(colWidths, cols, max(0, availW-fixedW))
		}
	}

	for _, ch := range g.Children {
		if ch.Child == nil {
			continue
		}
		col := max(0, ch.Column)
		cSpan := max(1, ch.ColSpan)
		if cSpan <= 1 {
			continue
		}
		spanW := spanSum(colWidths, col, cSpan)
		sz := ch.Child.Measure(Constraints{MaxW: spanW, MaxH: availH})
		if sz.W > spanW && (!hasMaxW || sumTracks(colWidths) < availW) {
			extra := sz.W - spanW
			if hasMaxW {
				extra = min(extra, availW-sumTracks(colWidths))
			}
			distributeSpanDeficit(colWidths, cols, col, cSpan, extra, TrackAuto)
		}
	}

	rowHeights := make([]int, numRows)
	fixedH := 0
	for row := 0; row < numRows; row++ {
		if rows[row].Kind == TrackFixed {
			rowHeights[row] = max(0, rows[row].Value)
			fixedH += rowHeights[row]
		}
	}

	for _, ch := range g.Children {
		if ch.Child == nil {
			continue
		}
		row := max(0, ch.Row)
		rSpan := max(1, ch.RowSpan)
		if rSpan != 1 || rows[row].Kind == TrackFixed {
			continue
		}
		childW := spanSum(colWidths, max(0, ch.Column), max(1, ch.ColSpan))
		if rows[row].Kind == TrackAuto || (!hasMaxH && rows[row].Kind == TrackFraction) {
			sz := ch.Child.Measure(Constraints{MaxW: childW, MaxH: availH})
			if sz.H > rowHeights[row] {
				rowHeights[row] = sz.H
			}
		}
	}

	for _, ch := range g.Children {
		if ch.Child == nil {
			continue
		}
		row := max(0, ch.Row)
		rSpan := max(1, ch.RowSpan)
		if rSpan <= 1 {
			continue
		}
		childW := spanSum(colWidths, max(0, ch.Column), max(1, ch.ColSpan))
		sz := ch.Child.Measure(Constraints{MaxW: childW, MaxH: availH})
		curSpanH := spanSum(rowHeights, row, rSpan)
		if sz.H > curSpanH {
			distributeSpanDeficit(rowHeights, rows, row, rSpan, sz.H-curSpanH, TrackAuto)
		}
	}

	if hasMaxH {
		nonFracH := sumTracks(rowHeights)
		if nonFracH <= availH {
			distributeFractions(rowHeights, rows, availH-nonFracH)
		} else {
			g.squeezeRowHeights(rowHeights, rows, colWidths, max(0, availH-fixedH))
		}
	}

	colX := prefixOffsets(colWidths)
	rowY := prefixOffsets(rowHeights)
	resolvedChildren := make([]resolvedGridChild, 0, len(g.Children))
	for i, ch := range g.Children {
		row := max(0, ch.Row)
		col := max(0, ch.Column)
		rSpan := max(1, ch.RowSpan)
		cSpan := max(1, ch.ColSpan)
		x := colX[col]
		y := rowY[row]
		w := spanSum(colWidths, col, cSpan)
		h := spanSum(rowHeights, row, rSpan)
		if hasMaxW && x+w > availW {
			w = max(0, availW-x)
		}
		if hasMaxH && y+h > availH {
			h = max(0, availH-y)
		}
		resolvedChildren = append(resolvedChildren, resolvedGridChild{
			index: i,
			rect:  Rect{X: x, Y: y, W: w, H: h},
		})
	}

	totalW := sumTracks(colWidths)
	if hasMaxW && totalW > availW {
		totalW = availW
	}
	totalH := sumTracks(rowHeights)
	if hasMaxH && totalH > availH {
		totalH = availH
	}
	return resolvedGridLayout{
		colWidths:  colWidths,
		rowHeights: rowHeights,
		children:   resolvedChildren,
		size:       Size{W: totalW, H: totalH},
	}
}

func (g *Grid) squeezeRowHeights(rowHeights []int, rows []Track, _ []int, targetAutoH int) {
	currentAutoH := 0
	for r, tr := range rows {
		if tr.Kind == TrackAuto {
			currentAutoH += rowHeights[r]
		}
	}
	if currentAutoH <= targetAutoH {
		return
	}
	for currentAutoH > targetAutoH {
		reduced := false
		for r := 0; r < len(rows) && currentAutoH > targetAutoH; r++ {
			if rows[r].Kind == TrackAuto && rowHeights[r] > 1 {
				rowHeights[r]--
				currentAutoH--
				reduced = true
			}
		}
		if !reduced {
			break
		}
	}
	for r := 0; r < len(rows) && currentAutoH > targetAutoH; r++ {
		if rows[r].Kind == TrackAuto && rowHeights[r] > 0 {
			take := min(rowHeights[r], currentAutoH-targetAutoH)
			rowHeights[r] -= take
			currentAutoH -= take
		}
	}
}

func distributeFractions(sizes []int, tracks []Track, remaining int) {
	if remaining <= 0 {
		return
	}
	totalWeight := 0
	for i, tr := range tracks {
		if tr.Kind == TrackFraction {
			totalWeight += max(1, tr.Value)
			sizes[i] = 0
		}
	}
	if totalWeight <= 0 {
		return
	}
	allocated := 0
	for i, tr := range tracks {
		if tr.Kind == TrackFraction {
			weight := max(1, tr.Value)
			share := remaining * weight / totalWeight
			sizes[i] = share
			allocated += share
		}
	}
	leftover := remaining - allocated
	for i, tr := range tracks {
		if leftover <= 0 {
			break
		}
		if tr.Kind == TrackFraction {
			sizes[i]++
			leftover--
		}
	}
}

func distributeSpanDeficit(sizes []int, tracks []Track, start, span, deficit int, kind TrackKind) {
	if deficit <= 0 {
		return
	}
	end := min(len(tracks), start+span)
	count := 0
	for i := start; i < end; i++ {
		if tracks[i].Kind == kind {
			count++
		}
	}
	if count == 0 {
		return
	}
	each := deficit / count
	rem := deficit % count
	for i := start; i < end; i++ {
		if tracks[i].Kind == kind {
			sizes[i] += each
			if rem > 0 {
				sizes[i]++
				rem--
			}
		}
	}
}

func squeezeAutoTracks(sizes []int, tracks []Track, targetAuto int) {
	currentAuto := 0
	for i, tr := range tracks {
		if tr.Kind == TrackAuto {
			currentAuto += sizes[i]
		}
	}
	for currentAuto > targetAuto {
		maxIdx := -1
		maxVal := 0
		for i, tr := range tracks {
			if tr.Kind == TrackAuto && sizes[i] > maxVal {
				maxVal = sizes[i]
				maxIdx = i
			}
		}
		if maxIdx < 0 || maxVal == 0 {
			break
		}
		sizes[maxIdx]--
		currentAuto--
	}
}

func spanSum(sizes []int, start, span int) int {
	end := min(len(sizes), start+span)
	sum := 0
	for i := max(0, start); i < end; i++ {
		sum += sizes[i]
	}
	return sum
}

func sumTracks(sizes []int) int {
	sum := 0
	for _, s := range sizes {
		sum += s
	}
	return sum
}

func prefixOffsets(sizes []int) []int {
	offsets := make([]int, len(sizes)+1)
	for i, s := range sizes {
		offsets[i+1] = offsets[i] + s
	}
	return offsets
}
