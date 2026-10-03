package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"sort"
)

const (
	// ConfidenceThreshold is τ: lines below this score are sent to Gemma for refinement.
	ConfidenceThreshold = 0.90

	// RecHeight is the fixed input height for the SVTR/CRNN recognizer.
	RecHeight = 64

	// CRAFTLowText is the binary threshold applied to score_text (channel 0).
	// Lowered from 0.4: terminal screenshots produce weaker CRAFT responses than natural images.
	CRAFTLowText = 0.35

	// CRAFTLinkThreshold is the binary threshold applied to score_link (channel 1).
	// Lowered from 0.4: link scores for monospace terminal fonts are weaker than natural-image text.
	CRAFTLinkThreshold = 0.20

	// CRAFTTextThreshold is the minimum peak score_text inside a component (box filter).
	// Lowered from 0.7: prevents filtering out dimly-lit coloured characters (green "Finished" etc.).
	CRAFTTextThreshold = 0.50

	// UnclipRatio is the Minkowski expansion coefficient applied after min-area rect fitting.
	// 1.6: balances adjacent-line bleed (too large at 1.8) vs edge-character loss (too tight at 1.4).
	UnclipRatio = 1.6

	// DBMinArea discards very small connected components (noise).
	DBMinArea = 30
)

// Platt scaling parameters calibrate raw C_line into a reliability score.
// Defaults are identity (a=1, b=0); tune from held-out data if needed.
var PlattA, PlattB float64 = 1.0, 0.0

// enCharset is the english_g2 EasyOCR recognizer character list (96 entries, blank at model index 0).
// Order must match the model exactly: digits, special, space, €, A-Z, a-z.
const enCharset = `0123456789!"#$%&'()*+,-./:;<=>?@[\]^_` + "`" + `{|}~ €ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz`

// BBox is an axis-aligned bounding box in image pixel coordinates.
type BBox struct{ X0, Y0, X1, Y1 int }

func (b BBox) Rect() image.Rectangle {
	return image.Rect(b.X0, b.Y0, b.X1, b.Y1)
}

// Quad is a 4-corner polygon in image pixel coordinates, ordered clockwise from top-left.
type Quad [4][2]float64

// quadAABB returns the axis-aligned bounding box of a quad.
func quadAABB(q Quad) BBox {
	x0, y0, x1, y1 := q[0][0], q[0][1], q[0][0], q[0][1]
	for _, p := range q[1:] {
		if p[0] < x0 {
			x0 = p[0]
		}
		if p[0] > x1 {
			x1 = p[0]
		}
		if p[1] < y0 {
			y0 = p[1]
		}
		if p[1] > y1 {
			y1 = p[1]
		}
	}
	return BBox{int(x0), int(y0), int(x1), int(y1)}
}

// SortByVertical sorts quads top-to-bottom (by min Y), left-to-right (by min X) as tiebreaker.
func SortByVertical(quads []Quad) {
	minF := func(a, b, c, d float64) float64 {
		m := a
		if b < m {
			m = b
		}
		if c < m {
			m = c
		}
		if d < m {
			m = d
		}
		return m
	}
	sort.Slice(quads, func(i, j int) bool {
		yi := minF(quads[i][0][1], quads[i][1][1], quads[i][2][1], quads[i][3][1])
		yj := minF(quads[j][0][1], quads[j][1][1], quads[j][2][1], quads[j][3][1])
		if yi != yj {
			return yi < yj
		}
		xi := minF(quads[i][0][0], quads[i][1][0], quads[i][2][0], quads[i][3][0])
		xj := minF(quads[j][0][0], quads[j][1][0], quads[j][2][0], quads[j][3][0])
		return xi < xj
	})
}

// TextLine is one decoded line with its confidence score.
type TextLine struct {
	BBox       BBox
	Text       string
	Confidence float64 // calibrated C_line in [0,1]
	NeedsLLM   bool    // true when Confidence < ConfidenceThreshold
}

// DetectLines runs the DB detector on img and returns rotated bounding quads.
// interp must be loaded with the DB .tflite model (input [1,608,800,3] NHWC for EasyOCR).
func DetectLines(interp *TFLiteInterpreter, img image.Image) ([]Quad, error) {
	b := img.Bounds()
	origW, origH := b.Dx(), b.Dy()

	// EasyOCR detector expects fixed 608x800 input in NHWC layout.
	const modelH, modelW = 608, 800
	var inH, inW int

	if origW > origH {
		inW = modelW
		inH = int(float64(origH) * float64(modelW) / float64(origW))
	} else {
		inH = modelH
		inW = int(float64(origW) * float64(modelH) / float64(origH))
	}

	// Pad to 608x800 with black borders.
	resized := ResizeNearest(img, inW, inH)
	square := image.NewNRGBA(image.Rect(0, 0, modelW, modelH))
	// Fill with black (padding)
	for y := 0; y < modelH; y++ {
		for x := 0; x < modelW; x++ {
			square.Set(x, y, color.RGBA{0, 0, 0, 255})
		}
	}
	// Paste resized image centered
	offsetX := (modelW - inW) / 2
	offsetY := (modelH - inH) / 2
	for y := 0; y < inH; y++ {
		for x := 0; x < inW; x++ {
			square.Set(offsetX+x, offsetY+y, resized.At(x, y))
		}
	}

	// Build NHWC [modelH×modelW×3] tensor with raw [0,1] values.
	// The exported TFLite detector applies ImageNet normalization internally.
	nhwcTensor := make([]float32, modelH*modelW*3)
	for y := 0; y < modelH; y++ {
		for x := 0; x < modelW; x++ {
			r, g, b, _ := square.At(x, y).RGBA()
			nhwcTensor[(y*modelW+x)*3+0] = float32(r) / 65535.0
			nhwcTensor[(y*modelW+x)*3+1] = float32(g) / 65535.0
			nhwcTensor[(y*modelW+x)*3+2] = float32(b) / 65535.0
		}
	}

	if err := interp.SetInput(0, nhwcTensor); err != nil {
		return nil, err
	}
	if err := interp.Invoke(); err != nil {
		return nil, err
	}

	probMap, shape, err := interp.GetOutput(0)
	if err != nil {
		return nil, err
	}

	var mapH, mapW, mapC int
	switch len(shape) {
	case 4:
		mapH, mapW, mapC = int(shape[1]), int(shape[2]), int(shape[3])
	case 3:
		mapH, mapW, mapC = int(shape[0]), int(shape[1]), int(shape[2])
	default:
		mapH, mapW, mapC = modelH, modelW, 1
	}

	// If the model exports the two CRAFT channels as separate output tensors (index 0 and 1)
	// rather than one interleaved tensor with C=2, splice them together.
	if mapC == 1 {
		if linkMap, linkShape, linkErr := interp.GetOutput(1); linkErr == nil {
			var lH, lW int
			switch len(linkShape) {
			case 4:
				lH, lW = int(linkShape[1]), int(linkShape[2])
			case 3:
				lH, lW = int(linkShape[0]), int(linkShape[1])
			}
			if lH == mapH && lW == mapW && len(linkMap) == mapH*mapW {
				// Interleave: combined[i*2+0]=textScore, combined[i*2+1]=linkScore.
				combined := make([]float32, mapH*mapW*2)
				for i := 0; i < mapH*mapW; i++ {
					combined[i*2+0] = probMap[i]
					combined[i*2+1] = linkMap[i]
				}
				probMap = combined
				mapC = 2
				fmt.Printf("[DBG] Detector: split-output model detected; spliced 2 channels\n")
			}
		}
	}

	// Print per-channel stats so we can verify the model is producing meaningful scores.
	if mapC >= 1 && len(probMap) > 0 {
		var minT, maxT, sumT float64
		minT = float64(probMap[0])
		for i := 0; i < len(probMap); i += mapC {
			v := float64(probMap[i])
			if v < minT {
				minT = v
			}
			if v > maxT {
				maxT = v
			}
			sumT += v
		}
		n := float64(len(probMap) / mapC)
		fmt.Printf("[DBG] score_text: min=%.3f max=%.3f mean=%.4f\n", minT, maxT, sumT/n)
		if mapC >= 2 {
			var minL, maxL, sumL float64
			minL = float64(probMap[1])
			for i := 1; i < len(probMap); i += mapC {
				v := float64(probMap[i])
				if v < minL {
					minL = v
				}
				if v > maxL {
					maxL = v
				}
				sumL += v
			}
			fmt.Printf("[DBG] score_link: min=%.3f max=%.3f mean=%.4f\n", minL, maxL, sumL/n)
		}
	}

	quads := extractQuads(probMap, mapH, mapW, mapC, origW, origH, inW, inH, offsetX, offsetY)
	quads = mergeLineQuads(quads)
	fmt.Printf("[DBG] Detector: mapC=%d shape=%v, %d quad(s) after merge (lowText=%.2f linkThr=%.2f textThr=%.2f)\n", mapC, shape, len(quads), CRAFTLowText, CRAFTLinkThreshold, CRAFTTextThreshold)
	for i, q := range quads {
		bb := quadAABB(q)
		fmt.Printf("  quad[%d] x=%d..%d y=%d..%d\n", i, bb.X0, bb.X1, bb.Y0, bb.Y1)
	}
	return quads, nil
}

// extractQuads binarises probMap, applies 2×2 dilation, finds connected components,
// scores each with a two-stage box_thresh filter, fits rotated min-area rects,
// and applies Minkowski unclip. Returns quads in original image coordinates.
func extractQuads(probMap []float32, mapH, mapW, mapC, origW, origH, inW, inH, offsetX, offsetY int) []Quad {
	if len(probMap) != mapH*mapW*mapC {
		return nil
	}

	// Build combined binary map from score_text (ch0) and score_link (ch1).
	// This is the EasyOCR/CRAFT canonical combination:
	//   text_score_comb = clip(score_text > lowText, 0,1) | (score_link > linkThr)
	binary := make([]bool, mapH*mapW)
	for i := 0; i < mapH*mapW; i++ {
		textScore := float64(probMap[i*mapC+0])
		linkScore := float64(0)
		if mapC >= 2 {
			linkScore = float64(probMap[i*mapC+1])
		}
		binary[i] = textScore > CRAFTLowText || linkScore > CRAFTLinkThreshold
	}

	// 2×2 morphological dilation bridges gaps in sparse strokes (e.g. "i", "j", punctuation).
	binary = dilate2x2(binary, mapW, mapH)

	// Coordinate scale: output map is half-res relative to model input.
	scaleW := float64(origW) / float64(inW)
	scaleH := float64(origH) / float64(inH)

	// Count hot pixels (both channels combined) for debugging.
	var hotPixels int
	for _, v := range binary {
		if v {
			hotPixels++
		}
	}
	fmt.Printf("[DBG] Binary map: %d/%d hot pixels (%.2f%%)\n", hotPixels, mapH*mapW, 100.0*float64(hotPixels)/float64(mapH*mapW))

	components := findComponents(binary, mapW, mapH)
	fmt.Printf("[DBG] Components: %d total\n", len(components))
	var nTooSmall, nLowPeak int
	var quads []Quad
	for _, comp := range components {
		if len(comp) < DBMinArea {
			nTooSmall++
			continue
		}
		// CRAFT box filter: maximum score_text in the component must exceed text_threshold.
		// (EasyOCR: if np.max(textmap[labels==k]) < text_threshold: continue)
		var maxTextScore float64
		for _, px := range comp {
			if s := float64(probMap[(px[1]*mapW+px[0])*mapC]); s > maxTextScore {
				maxTextScore = s
			}
		}
		if maxTextScore < CRAFTTextThreshold {
			nLowPeak++
			continue
		}
		pts := componentBoundaryPts(comp)
		if len(pts) < 2 {
			continue
		}
		hull := convexHullPts(pts)
		if len(hull) < 2 {
			continue
		}
		rect := minAreaRectFromHull(hull)

		w := dist2D(rect[0], rect[1])
		h := dist2D(rect[1], rect[2])
		if w < 1 || h < 1 {
			continue
		}
		// Ensure the longer side is rect[0]→rect[1] (the "width" axis).
		if h > w {
			rect[0], rect[1], rect[2], rect[3] = rect[3], rect[0], rect[1], rect[2]
			w, h = h, w
		}
		// Discard vertical stripes: long axis is predominantly vertical.
		ux := (rect[1][0] - rect[0][0]) / w
		uy := (rect[1][1] - rect[0][1]) / w
		if math.Abs(uy) > math.Abs(ux) {
			continue
		}

		// Minkowski unclip: canonical DB expansion proportional to area/perimeter.
		rect = unclipRect(rect, UnclipRatio)

		// Convert map coords → original image coords (×2 undo half-res, subtract letterbox offset, scale).
		var q Quad
		for i, p := range rect {
			ox := (p[0]*2 - float64(offsetX)) * scaleW
			oy := (p[1]*2 - float64(offsetY)) * scaleH
			if ox < 0 {
				ox = 0
			} else if ox > float64(origW) {
				ox = float64(origW)
			}
			if oy < 0 {
				oy = 0
			} else if oy > float64(origH) {
				oy = float64(origH)
			}
			q[i] = [2]float64{ox, oy}
		}
		quads = append(quads, q)
	}
	fmt.Printf("[DBG] Filtered: %d too-small, %d low-peak; %d quads passed\n", nTooSmall, nLowPeak, len(quads))
	return quads
}

// mergeLineQuads groups word-level quads into line-level quads using two passes.
//
// Pass 1 — y-band grouping: sort by y-centre; assign each quad to the current
//
//	band if its y-centre is within shortHeight/3 of the last quad in that band.
//	This collects ALL quads for a given text row before doing any x-merging,
//	so a "foreign" quad at a large x-distance cannot break the chain.
//
// Pass 2 — horizontal merge: within each y-band, sort by x-centre and
//
//	merge adjacent quads whose gap is ≤ 4 × shortHeight.
func mergeLineQuads(quads []Quad) []Quad {
	if len(quads) == 0 {
		return quads
	}
	type box struct{ x0, y0, x1, y1 float64 }
	yc := func(b box) float64 { return (b.y0 + b.y1) / 2 }
	xc := func(b box) float64 { return (b.x0 + b.x1) / 2 }
	h := func(b box) float64 { return b.y1 - b.y0 }
	shortH := func(a, b box) float64 {
		ha, hb := h(a), h(b)
		if ha < hb {
			return ha
		}
		return hb
	}

	boxes := make([]box, len(quads))
	for i, q := range quads {
		bb := quadAABB(q)
		boxes[i] = box{float64(bb.X0), float64(bb.Y0), float64(bb.X1), float64(bb.Y1)}
	}
	sort.Slice(boxes, func(i, j int) bool {
		yci, ycj := yc(boxes[i]), yc(boxes[j])
		if yci != ycj {
			return yci < ycj
		}
		return xc(boxes[i]) < xc(boxes[j])
	})

	// Pass 1: group into y-bands.
	var bands [][]box
	var curBand []box
	for _, b := range boxes {
		if len(curBand) == 0 {
			curBand = append(curBand, b)
			continue
		}
		last := curBand[len(curBand)-1]
		if math.Abs(yc(last)-yc(b)) < shortH(last, b)/2.5 {
			curBand = append(curBand, b)
		} else {
			bands = append(bands, curBand)
			curBand = []box{b}
		}
	}
	bands = append(bands, curBand)

	// Pass 2: within each band sort by x-centre and merge horizontally.
	var merged []box
	for _, band := range bands {
		sort.Slice(band, func(i, j int) bool { return xc(band[i]) < xc(band[j]) })
		cur := band[0]
		for _, b := range band[1:] {
			sh := shortH(cur, b)
			hGap := b.x0 - cur.x1
			if hGap <= sh*4 {
				if b.x1 > cur.x1 {
					cur.x1 = b.x1
				}
				if b.x0 < cur.x0 {
					cur.x0 = b.x0
				}
				if b.y0 < cur.y0 {
					cur.y0 = b.y0
				}
				if b.y1 > cur.y1 {
					cur.y1 = b.y1
				}
			} else {
				merged = append(merged, cur)
				cur = b
			}
		}
		merged = append(merged, cur)
	}

	// Sort merged quads top-to-bottom, left-to-right before returning.
	sort.Slice(merged, func(i, j int) bool {
		yci, ycj := yc(merged[i]), yc(merged[j])
		if yci != ycj {
			return yci < ycj
		}
		return xc(merged[i]) < xc(merged[j])
	})

	fmt.Printf("[DBG] mergeLineQuads: %d → %d quads\n", len(quads), len(merged))

	result := make([]Quad, len(merged))
	for i, m := range merged {
		result[i] = Quad{
			{m.x0, m.y0}, {m.x1, m.y0},
			{m.x1, m.y1}, {m.x0, m.y1},
		}
	}
	return result
}

// dilate2x2 applies a 2×2 morphological dilation: each true pixel also sets right, below, diagonal neighbours.
func dilate2x2(binary []bool, w, h int) []bool {
	out := make([]bool, len(binary))
	copy(out, binary)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !binary[y*w+x] {
				continue
			}
			if x+1 < w {
				out[y*w+x+1] = true
			}
			if y+1 < h {
				out[(y+1)*w+x] = true
			}
			if x+1 < w && y+1 < h {
				out[(y+1)*w+x+1] = true
			}
		}
	}
	return out
}

// findComponents returns all 4-connected components as slices of (x,y) pixel coordinates.
func findComponents(binary []bool, w, h int) [][][2]int {
	visited := make([]bool, w*h)
	var comps [][][2]int
	for sy := 0; sy < h; sy++ {
		for sx := 0; sx < w; sx++ {
			if !binary[sy*w+sx] || visited[sy*w+sx] {
				continue
			}
			var pixels [][2]int
			queue := [][2]int{{sx, sy}}
			visited[sy*w+sx] = true
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				pixels = append(pixels, cur)
				cx, cy := cur[0], cur[1]
				for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
					nx, ny := cx+d[0], cy+d[1]
					if nx < 0 || nx >= w || ny < 0 || ny >= h {
						continue
					}
					ni := ny*w + nx
					if !visited[ni] && binary[ni] {
						visited[ni] = true
						queue = append(queue, [2]int{nx, ny})
					}
				}
			}
			comps = append(comps, pixels)
		}
	}
	return comps
}

// componentBoundaryPts returns the left/right extremes per row and top/bottom per column,
// giving a compact point set sufficient for an accurate convex hull.
func componentBoundaryPts(pixels [][2]int) [][2]float64 {
	if len(pixels) == 0 {
		return nil
	}
	minX, maxX, minY, maxY := pixels[0][0], pixels[0][0], pixels[0][1], pixels[0][1]
	for _, p := range pixels {
		if p[0] < minX {
			minX = p[0]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	numRows := maxY - minY + 1
	numCols := maxX - minX + 1
	rowMinX := make([]int, numRows)
	rowMaxX := make([]int, numRows)
	colMinY := make([]int, numCols)
	colMaxY := make([]int, numCols)
	for i := range rowMinX {
		rowMinX[i] = maxX + 1
		rowMaxX[i] = minX - 1
	}
	for i := range colMinY {
		colMinY[i] = maxY + 1
		colMaxY[i] = minY - 1
	}
	for _, p := range pixels {
		r := p[1] - minY
		if p[0] < rowMinX[r] {
			rowMinX[r] = p[0]
		}
		if p[0] > rowMaxX[r] {
			rowMaxX[r] = p[0]
		}
		c := p[0] - minX
		if p[1] < colMinY[c] {
			colMinY[c] = p[1]
		}
		if p[1] > colMaxY[c] {
			colMaxY[c] = p[1]
		}
	}
	pts := make([][2]float64, 0, 2*numRows+2*numCols)
	for r := range rowMinX {
		if rowMinX[r] <= maxX {
			pts = append(pts, [2]float64{float64(rowMinX[r]), float64(r + minY)})
			if rowMaxX[r] != rowMinX[r] {
				pts = append(pts, [2]float64{float64(rowMaxX[r]), float64(r + minY)})
			}
		}
	}
	for c := range colMinY {
		if colMinY[c] <= maxY {
			pts = append(pts, [2]float64{float64(c + minX), float64(colMinY[c])})
			if colMaxY[c] != colMinY[c] {
				pts = append(pts, [2]float64{float64(c + minX), float64(colMaxY[c])})
			}
		}
	}
	return pts
}

// cross2D returns the 2D cross product of vectors OA and OB.
func cross2D(O, A, B [2]float64) float64 {
	return (A[0]-O[0])*(B[1]-O[1]) - (A[1]-O[1])*(B[0]-O[0])
}

// convexHullPts computes the convex hull using Andrew's monotone chain (CCW order).
func convexHullPts(pts [][2]float64) [][2]float64 {
	n := len(pts)
	if n < 2 {
		return pts
	}
	sort.Slice(pts, func(i, j int) bool {
		if pts[i][0] != pts[j][0] {
			return pts[i][0] < pts[j][0]
		}
		return pts[i][1] < pts[j][1]
	})
	hull := make([][2]float64, 0, 2*n)
	for _, p := range pts {
		for len(hull) >= 2 && cross2D(hull[len(hull)-2], hull[len(hull)-1], p) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, p)
	}
	lo := len(hull) + 1
	for i := n - 1; i >= 0; i-- {
		for len(hull) >= lo && cross2D(hull[len(hull)-2], hull[len(hull)-1], pts[i]) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, pts[i])
	}
	return hull[:len(hull)-1]
}

// dist2D returns the Euclidean distance between two points.
func dist2D(a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	return math.Sqrt(dx*dx + dy*dy)
}

// minAreaRectFromHull computes the minimum-area enclosing rectangle of a convex hull
// using rotating calipers. Returns 4 corners in order matching the edge directions of the hull.
func minAreaRectFromHull(hull [][2]float64) [4][2]float64 {
	n := len(hull)
	if n == 0 {
		return [4][2]float64{}
	}
	if n == 1 {
		return [4][2]float64{hull[0], hull[0], hull[0], hull[0]}
	}
	if n == 2 {
		mx, my := (hull[0][0]+hull[1][0])/2, (hull[0][1]+hull[1][1])/2
		return [4][2]float64{{mx, my}, hull[1], hull[1], hull[0]}
	}
	minArea := math.Inf(1)
	var best [4][2]float64
	for i := 0; i < n; i++ {
		dx := hull[(i+1)%n][0] - hull[i][0]
		dy := hull[(i+1)%n][1] - hull[i][1]
		edgeLen := math.Sqrt(dx*dx + dy*dy)
		if edgeLen < 1e-10 {
			continue
		}
		ux, uy := dx/edgeLen, dy/edgeLen
		vx, vy := -uy, ux
		minU, maxU := math.Inf(1), math.Inf(-1)
		minV, maxV := math.Inf(1), math.Inf(-1)
		for _, p := range hull {
			u := p[0]*ux + p[1]*uy
			v := p[0]*vx + p[1]*vy
			if u < minU {
				minU = u
			}
			if u > maxU {
				maxU = u
			}
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
		area := (maxU - minU) * (maxV - minV)
		if area < minArea {
			minArea = area
			best[0] = [2]float64{minU*ux + minV*vx, minU*uy + minV*vy}
			best[1] = [2]float64{maxU*ux + minV*vx, maxU*uy + minV*vy}
			best[2] = [2]float64{maxU*ux + maxV*vx, maxU*uy + maxV*vy}
			best[3] = [2]float64{minU*ux + maxV*vx, minU*uy + maxV*vy}
		}
	}
	return best
}

// unclipRect expands a 4-point rectangle outward using the canonical DB Minkowski expansion:
// distance = area * ratio / perimeter, applied uniformly on all sides.
func unclipRect(rect [4][2]float64, ratio float64) [4][2]float64 {
	w := dist2D(rect[0], rect[1])
	h := dist2D(rect[1], rect[2])
	area := w * h
	perimeter := 2 * (w + h)
	if perimeter < 1e-10 {
		return rect
	}
	d := area * ratio / perimeter
	cx := (rect[0][0] + rect[1][0] + rect[2][0] + rect[3][0]) / 4
	cy := (rect[0][1] + rect[1][1] + rect[2][1] + rect[3][1]) / 4
	var ux, uy, vx, vy float64
	if w > 1e-10 {
		ux = (rect[1][0] - rect[0][0]) / w
		uy = (rect[1][1] - rect[0][1]) / w
	}
	if h > 1e-10 {
		vx = (rect[2][0] - rect[1][0]) / h
		vy = (rect[2][1] - rect[1][1]) / h
	}
	hw := (w + 2*d) / 2
	hh := (h + 2*d) / 2
	return [4][2]float64{
		{cx - hw*ux - hh*vx, cy - hw*uy - hh*vy},
		{cx + hw*ux - hh*vx, cy + hw*uy - hh*vy},
		{cx + hw*ux + hh*vx, cy + hw*uy + hh*vy},
		{cx - hw*ux + hh*vx, cy - hw*uy + hh*vy},
	}
}

// RecognizeLines runs the SVTR/CRNN recognizer on each quad crop.
// interp must be loaded with the recognizer .tflite model (input [1, 64, 800, 1] NHWC).
func RecognizeLines(interp *TFLiteInterpreter, img image.Image, quads []Quad) ([]TextLine, error) {
	vocab := []rune(enCharset)
	const blankIdx = 0
	lines := make([]TextLine, 0, len(quads))

	for _, q := range quads {
		crop := PerspectiveCrop(img, q)
		strip := ResizeHeightAndWidth(crop, 64, 800)
		stripW := strip.Bounds().Dx()

		// Build raw [0,1] grayscale tensor [64×800], left-aligned.
		// Remainder padded with 0.0 (maps to -1.0 in (x-0.5)/0.5 space — canonical empty).
		const recH, recW = 64, 800
		tensor := make([]float32, recH*recW)
		for y := 0; y < recH; y++ {
			for x := 0; x < stripW; x++ {
				tensor[y*recW+x] = float32(GrayscaleAt(strip.At(x, y)))
			}
		}

		if err := interp.SetInput(0, tensor); err != nil {
			return nil, err
		}
		if err := interp.Invoke(); err != nil {
			return nil, err
		}
		logits, shape, err := interp.GetOutput(0)
		if err != nil {
			return nil, err
		}

		var T, K int
		switch len(shape) {
		case 3:
			T, K = int(shape[1]), int(shape[2])
		case 2:
			T, K = int(shape[0]), int(shape[1])
		default:
			fmt.Fprintf(os.Stderr, "[WARN] unexpected recognizer output shape %v, skipping quad\n", shape)
			continue
		}

		if len(lines) == 0 {
			fmt.Printf("[DBG] Recognizer shape=%v T=%d K=%d\n", shape, T, K)
			minL, maxL := logits[0], logits[0]
			for _, v := range logits {
				if v < minL {
					minL = v
				}
				if v > maxL {
					maxL = v
				}
			}
			fmt.Printf("[DBG] Logit range: %g..%g\n", minL, maxL)
		}

		text, conf := ctcGreedyDecode(logits, T, K, blankIdx, vocab)
		calibrated := plattScale(conf)

		lines = append(lines, TextLine{
			BBox:       quadAABB(q),
			Text:       text,
			Confidence: calibrated,
			NeedsLLM:   calibrated < ConfidenceThreshold,
		})
	}
	return lines, nil
}

// ctcGreedyDecode decodes a [T×K] logit matrix.
// Blank token is at model index 0; characters map as vocab[bestK-1] for bestK>=1.
// Confidence is the arithmetic mean of per-character softmax probabilities.
func ctcGreedyDecode(logits []float32, T, K, blankIdx int, vocab []rune) (string, float64) {
	var chars []rune
	var probs []float64
	prev := -1

	for t := 0; t < T; t++ {
		row := logits[t*K : t*K+K]
		// Softmax.
		maxV := float32(math.Inf(-1))
		for _, v := range row {
			if v > maxV {
				maxV = v
			}
		}
		sum := float64(0)
		exp := make([]float64, K)
		for k, v := range row {
			exp[k] = math.Exp(float64(v - maxV))
			sum += exp[k]
		}
		// Argmax.
		bestK, bestP := 0, 0.0
		for k, e := range exp {
			p := e / sum
			if p > bestP {
				bestK, bestP = k, p
			}
		}
		if bestK == blankIdx || bestK == prev {
			prev = bestK
			continue
		}
		prev = bestK
		// blank at model index 0; chars at 1..K-1 → vocab[bestK-1]
		if bestK >= 1 && bestK-1 < len(vocab) {
			chars = append(chars, vocab[bestK-1])
			probs = append(probs, bestP)
		}
	}
	if len(probs) == 0 {
		return "", 0
	}
	// Arithmetic mean: less sensitive to individual low-confidence characters than geometric mean.
	sum := 0.0
	for _, p := range probs {
		sum += p
	}
	return string(chars), sum / float64(len(probs))
}

// plattScale applies the Platt (logistic) calibration transform.
func plattScale(rawScore float64) float64 {
	return 1.0 / (1.0 + math.Exp(-(PlattA*rawScore + PlattB)))
}

func roundToMultiple(v, m int) int {
	return ((v + m - 1) / m) * m
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// convertNCHWtoNHWC converts a tensor from NCHW layout to NHWC layout.
// Input: NCHW tensor of shape [N, C, H, W]
// Output: NHWC tensor of shape [N, H, W, C]
func convertNCHWtoNHWC(nchw []float32, n, c, h, w int) []float32 {
	nhwc := make([]float32, len(nchw))
	for i := 0; i < n; i++ {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				for ch := 0; ch < c; ch++ {
					// NCHW: [i*C*H*W + ch*H*W + y*W + x]
					// NHWC: [i*H*W*C + y*W*C + x*C + ch]
					nchwIdx := i*c*h*w + ch*h*w + y*w + x
					nhwcIdx := i*h*w*c + y*w*c + x*c + ch
					nhwc[nhwcIdx] = nchw[nchwIdx]
				}
			}
		}
	}
	return nhwc
}
