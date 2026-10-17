// Package ocr provides Phase 1 TFLite (CRAFT detector + SVTR recognizer) OCR
// for reading text from image files (.png, .jpg, .jpeg, .webp, .gif) inside read_file.
package ocr

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"ai"
	"ai/assets"
)

const (
	easyOCRTFLiteZipURL = "https://qaihub-public-assets.s3.us-west-2.amazonaws.com/qai-hub-models/models/easyocr/releases/v0.63.0/easyocr-tflite-float.zip"
	craftLowText        = 0.35
	craftLinkThreshold  = 0.20
	craftTextThreshold  = 0.50
	unclipRatio         = 1.6
	dbMinArea           = 30
	enCharset           = `0123456789!"#$%&'()*+,-./:;<=>?@[\]^_` + "`" + `{|}~ €ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz`
)

type quad [4][2]float64

type bbox struct{ x0, y0, x1, y1 int }

var (
	ocrMu      sync.Mutex
	detInterp  *ai.Interpreter
	recInterp  *ai.Interpreter
	ocrInitErr error
)

// ExtractImageLines decodes rawBytes as an image, runs Phase 1 TFLite OCR if models are cached
// or available via ai/assets, and falls back to image metadata if TFLite models are offline/unavailable.
func ExtractImageLines(ctx context.Context, absPath string, rawBytes []byte, allowDownload bool) []string {
	img, format, err := image.Decode(bytes.NewReader(rawBytes))
	if err != nil {
		return []string{fmt.Sprintf("[image: %s | size: %d bytes | decode error: %v]", filepath.Base(absPath), len(rawBytes), err)}
	}
	b := img.Bounds()
	header := fmt.Sprintf("[image: %s | format: %s | dimensions: %dx%d | size: %d bytes]",
		filepath.Base(absPath), format, b.Dx(), b.Dy(), len(rawBytes))

	lines, ocrErr := runPhase1TFLiteOCR(ctx, img, allowDownload)
	if ocrErr != nil || len(lines) == 0 {
		return []string{header}
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, header)
	out = append(out, lines...)
	return out
}

func runPhase1TFLiteOCR(ctx context.Context, img image.Image, allowDownload bool) ([]string, error) {
	ocrMu.Lock()
	defer ocrMu.Unlock()

	if detInterp == nil || recInterp == nil {
		cacheDir, err := assets.DefaultCacheDir()
		if err != nil {
			return nil, err
		}
		detPath := filepath.Join(cacheDir, "detector.tflite")
		recPath := filepath.Join(cacheDir, "recognizer.tflite")

		if !allowDownload {
			if _, err := os.Stat(detPath); err != nil {
				return nil, err
			}
			if _, err := os.Stat(recPath); err != nil {
				return nil, err
			}
		}

		opts := assets.FetchOptions{CacheDir: cacheDir}
		libPath, err := assets.EnsureRuntime(ctx, opts)
		if err != nil {
			return nil, err
		}
		models, err := assets.EnsureZipModels(ctx, easyOCRTFLiteZipURL, []string{"detector.tflite", "recognizer.tflite"}, opts)
		if err != nil {
			return nil, err
		}

		d, err := ai.NewInterpreter(libPath, models["detector.tflite"])
		if err != nil {
			return nil, err
		}
		r, err := ai.NewInterpreter(libPath, models["recognizer.tflite"])
		if err != nil {
			d.Close()
			return nil, err
		}
		detInterp = d
		recInterp = r
	}

	quads, err := detectQuads(detInterp, img)
	if err != nil || len(quads) == 0 {
		return nil, err
	}
	return recognizeQuads(recInterp, img, quads)
}

func detectQuads(interp *ai.Interpreter, img image.Image) ([]quad, error) {
	b := img.Bounds()
	origW, origH := b.Dx(), b.Dy()
	if origW <= 0 || origH <= 0 {
		return nil, nil
	}

	const modelH, modelW = 608, 800
	var inH, inW int
	if origW > origH {
		inW = modelW
		inH = int(float64(origH) * float64(modelW) / float64(origW))
	} else {
		inH = modelH
		inW = int(float64(origW) * float64(modelH) / float64(origH))
	}
	if inW < 1 {
		inW = 1
	}
	if inH < 1 {
		inH = 1
	}

	resized := resizeNearest(img, inW, inH)
	square := image.NewNRGBA(image.Rect(0, 0, modelW, modelH))
	for y := 0; y < modelH; y++ {
		for x := 0; x < modelW; x++ {
			square.Set(x, y, color.RGBA{0, 0, 0, 255})
		}
	}
	offsetX := (modelW - inW) / 2
	offsetY := (modelH - inH) / 2
	for y := 0; y < inH; y++ {
		for x := 0; x < inW; x++ {
			square.Set(offsetX+x, offsetY+y, resized.At(x, y))
		}
	}

	nhwcTensor := make([]float32, modelH*modelW*3)
	for y := 0; y < modelH; y++ {
		for x := 0; x < modelW; x++ {
			r, g, bl, _ := square.At(x, y).RGBA()
			nhwcTensor[(y*modelW+x)*3+0] = float32(r) / 65535.0
			nhwcTensor[(y*modelW+x)*3+1] = float32(g) / 65535.0
			nhwcTensor[(y*modelW+x)*3+2] = float32(bl) / 65535.0
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
				combined := make([]float32, mapH*mapW*2)
				for i := 0; i < mapH*mapW; i++ {
					combined[i*2+0] = probMap[i]
					combined[i*2+1] = linkMap[i]
				}
				probMap = combined
				mapC = 2
			}
		}
	}

	quads := extractQuads(probMap, mapH, mapW, mapC, origW, origH, inW, inH, offsetX, offsetY)
	return mergeLineQuads(quads), nil
}

func recognizeQuads(interp *ai.Interpreter, img image.Image, quads []quad) ([]string, error) {
	vocab := []rune(enCharset)
	var out []string

	for _, q := range quads {
		crop := perspectiveCrop(img, q)
		strip := resizeHeightAndWidth(crop, 64, 800)
		stripW := strip.Bounds().Dx()

		const recH, recW = 64, 800
		tensor := make([]float32, recH*recW)
		for y := 0; y < recH; y++ {
			for x := 0; x < stripW; x++ {
				tensor[y*recW+x] = grayscaleAt(strip.At(x, y))
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
			continue
		}

		text := strings.TrimSpace(ctcGreedyDecode(logits, T, K, vocab))
		if text != "" {
			out = append(out, text)
		}
	}
	return out, nil
}

func ctcGreedyDecode(logits []float32, T, K int, vocab []rune) string {
	var chars []rune
	prev := -1
	for t := 0; t < T; t++ {
		row := logits[t*K : t*K+K]
		bestK := 0
		bestV := row[0]
		for k := 1; k < K; k++ {
			if row[k] > bestV {
				bestV = row[k]
				bestK = k
			}
		}
		if bestK == 0 || bestK == prev {
			prev = bestK
			continue
		}
		prev = bestK
		if bestK >= 1 && bestK-1 < len(vocab) {
			chars = append(chars, vocab[bestK-1])
		}
	}
	return string(chars)
}

func extractQuads(probMap []float32, mapH, mapW, mapC, origW, origH, inW, inH, offsetX, offsetY int) []quad {
	if len(probMap) != mapH*mapW*mapC {
		return nil
	}
	binary := make([]bool, mapH*mapW)
	for i := 0; i < mapH*mapW; i++ {
		textScore := float64(probMap[i*mapC+0])
		linkScore := float64(0)
		if mapC >= 2 {
			linkScore = float64(probMap[i*mapC+1])
		}
		binary[i] = textScore > craftLowText || linkScore > craftLinkThreshold
	}
	binary = dilate2x2(binary, mapW, mapH)
	scaleW := float64(origW) / float64(inW)
	scaleH := float64(origH) / float64(inH)

	components := findComponents(binary, mapW, mapH)
	var quads []quad
	for _, comp := range components {
		if len(comp) < dbMinArea {
			continue
		}
		var maxTextScore float64
		for _, px := range comp {
			if s := float64(probMap[(px[1]*mapW+px[0])*mapC]); s > maxTextScore {
				maxTextScore = s
			}
		}
		if maxTextScore < craftTextThreshold {
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
		if h > w {
			rect[0], rect[1], rect[2], rect[3] = rect[3], rect[0], rect[1], rect[2]
			w, h = h, w
		}
		ux := (rect[1][0] - rect[0][0]) / w
		uy := (rect[1][1] - rect[0][1]) / w
		if math.Abs(uy) > math.Abs(ux) {
			continue
		}
		rect = unclipRect(rect, unclipRatio)
		var q quad
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
	return quads
}

func quadAABB(q quad) bbox {
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
	return bbox{int(x0), int(y0), int(x1), int(y1)}
}

func mergeLineQuads(quads []quad) []quad {
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
		boxes[i] = box{float64(bb.x0), float64(bb.y0), float64(bb.x1), float64(bb.y1)}
	}
	sort.Slice(boxes, func(i, j int) bool {
		yci, ycj := yc(boxes[i]), yc(boxes[j])
		if yci != ycj {
			return yci < ycj
		}
		return xc(boxes[i]) < xc(boxes[j])
	})

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

	var merged []box
	for _, band := range bands {
		sort.Slice(band, func(i, j int) bool { return xc(band[i]) < xc(band[j]) })
		cur := band[0]
		for _, b := range band[1:] {
			sh := shortH(cur, b)
			if b.x0-cur.x1 <= sh*4 {
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

	sort.Slice(merged, func(i, j int) bool {
		yci, ycj := yc(merged[i]), yc(merged[j])
		if yci != ycj {
			return yci < ycj
		}
		return xc(merged[i]) < xc(merged[j])
	})

	result := make([]quad, len(merged))
	for i, m := range merged {
		result[i] = quad{
			{m.x0, m.y0}, {m.x1, m.y0},
			{m.x1, m.y1}, {m.x0, m.y1},
		}
	}
	return result
}

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

func cross2D(O, A, B [2]float64) float64 {
	return (A[0]-O[0])*(B[1]-O[1]) - (A[1]-O[1])*(B[0]-O[0])
}

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

func dist2D(a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	return math.Sqrt(dx*dx + dy*dy)
}

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

func resizeNearest(img image.Image, w, h int) *image.NRGBA {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		srcY := b.Min.Y + y*srcH/h
		for x := 0; x < w; x++ {
			srcX := b.Min.X + x*srcW/w
			out.Set(x, y, img.At(srcX, srcY))
		}
	}
	return out
}

func resizeHeightAndWidth(img image.Image, targetH, maxW int) *image.NRGBA {
	b := img.Bounds()
	srcH := b.Dy()
	if srcH == 0 {
		return image.NewNRGBA(image.Rect(0, 0, 1, targetH))
	}
	scale := float64(targetH) / float64(srcH)
	targetW := int(math.Round(float64(b.Dx()) * scale))
	if targetW < 1 {
		targetW = 1
	}
	if targetW > maxW {
		targetW = maxW
	}
	return resizeNearest(img, targetW, targetH)
}

func grayscaleAt(c color.Color) float32 {
	r, g, b, _ := c.RGBA()
	return float32(0.299*float64(r)+0.587*float64(g)+0.114*float64(b)) / 65535.0
}

func perspectiveCrop(img image.Image, q quad) *image.NRGBA {
	w1 := dist2D(q[0], q[1])
	w2 := dist2D(q[3], q[2])
	h1 := dist2D(q[0], q[3])
	h2 := dist2D(q[1], q[2])
	dstW := int(math.Round(math.Max(w1, w2)))
	dstH := int(math.Round(math.Max(h1, h2)))
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}
	if dstH > dstW {
		dstW, dstH = dstH, dstW
		q[0], q[1], q[2], q[3] = q[1], q[2], q[3], q[0]
	}

	dst := [4][2]float64{
		{0, 0}, {float64(dstW), 0},
		{float64(dstW), float64(dstH)}, {0, float64(dstH)},
	}
	h := solvePerspective(dst, q)

	bounds := img.Bounds()
	imgW, imgH := bounds.Dx(), bounds.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))

	for v := 0; v < dstH; v++ {
		for u := 0; u < dstW; u++ {
			uf, vf := float64(u)+0.5, float64(v)+0.5
			denom := h[6]*uf + h[7]*vf + h[8]
			if math.Abs(denom) < 1e-10 {
				continue
			}
			sx := (h[0]*uf + h[1]*vf + h[2]) / denom
			sy := (h[3]*uf + h[4]*vf + h[5]) / denom

			x0 := int(sx)
			y0 := int(sy)
			x1 := x0 + 1
			y1 := y0 + 1
			fx := sx - float64(x0)
			fy := sy - float64(y0)

			if x0 < 0 {
				x0 = 0
			} else if x0 >= imgW {
				x0 = imgW - 1
			}
			if x1 < 0 {
				x1 = 0
			} else if x1 >= imgW {
				x1 = imgW - 1
			}
			if y0 < 0 {
				y0 = 0
			} else if y0 >= imgH {
				y0 = imgH - 1
			}
			if y1 < 0 {
				y1 = 0
			} else if y1 >= imgH {
				y1 = imgH - 1
			}

			r00, g00, b00, a00 := img.At(bounds.Min.X+x0, bounds.Min.Y+y0).RGBA()
			r10, g10, b10, a10 := img.At(bounds.Min.X+x1, bounds.Min.Y+y0).RGBA()
			r01, g01, b01, a01 := img.At(bounds.Min.X+x0, bounds.Min.Y+y1).RGBA()
			r11, g11, b11, a11 := img.At(bounds.Min.X+x1, bounds.Min.Y+y1).RGBA()

			blerp := func(v00, v10, v01, v11 uint32) uint8 {
				f := float64(v00)*(1-fx)*(1-fy) + float64(v10)*fx*(1-fy) +
					float64(v01)*(1-fx)*fy + float64(v11)*fx*fy
				return uint8(f / 257)
			}
			out.SetNRGBA(u, v, color.NRGBA{
				R: blerp(r00, r10, r01, r11),
				G: blerp(g00, g10, g01, g11),
				B: blerp(b00, b10, b01, b11),
				A: blerp(a00, a10, a01, a11),
			})
		}
	}
	return out
}

func solvePerspective(dst, src [4][2]float64) [9]float64 {
	var A [8][8]float64
	var b [8]float64
	for i := 0; i < 4; i++ {
		u, v := dst[i][0], dst[i][1]
		x, y := src[i][0], src[i][1]
		A[2*i] = [8]float64{u, v, 1, 0, 0, 0, -u * x, -v * x}
		b[2*i] = x
		A[2*i+1] = [8]float64{0, 0, 0, u, v, 1, -u * y, -v * y}
		b[2*i+1] = y
	}
	h8 := gaussElim8(A, b)
	return [9]float64{h8[0], h8[1], h8[2], h8[3], h8[4], h8[5], h8[6], h8[7], 1}
}

func gaussElim8(A [8][8]float64, b [8]float64) [8]float64 {
	var M [8][9]float64
	for i := range A {
		copy(M[i][:8], A[i][:])
		M[i][8] = b[i]
	}
	for col := 0; col < 8; col++ {
		pivot := col
		for row := col + 1; row < 8; row++ {
			if math.Abs(M[row][col]) > math.Abs(M[pivot][col]) {
				pivot = row
			}
		}
		M[col], M[pivot] = M[pivot], M[col]
		if math.Abs(M[col][col]) < 1e-12 {
			continue
		}
		for row := col + 1; row < 8; row++ {
			f := M[row][col] / M[col][col]
			for j := col; j <= 8; j++ {
				M[row][j] -= f * M[col][j]
			}
		}
	}
	var x [8]float64
	for i := 7; i >= 0; i-- {
		x[i] = M[i][8]
		for j := i + 1; j < 8; j++ {
			x[i] -= M[i][j] * x[j]
		}
		if math.Abs(M[i][i]) > 1e-12 {
			x[i] /= M[i][i]
		}
	}
	return x
}
