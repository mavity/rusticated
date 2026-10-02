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
	// ConfidenceThreshold is τ from the design document.
	ConfidenceThreshold = 0.80

	// RecHeight is the fixed input height for the SVTR/CRNN recognizer.
	RecHeight = 64

	// DBThreshold is the binarisation threshold on the DB probability map.
	DBThreshold = 0.15

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

// SortByVertical sorts bounding boxes top-to-bottom (by Y0), left-to-right (by X0) as tiebreaker.
func SortByVertical(boxes []BBox) {
	sort.Slice(boxes, func(i, j int) bool {
		if boxes[i].Y0 != boxes[j].Y0 {
			return boxes[i].Y0 < boxes[j].Y0
		}
		return boxes[i].X0 < boxes[j].X0
	})
}

// TextLine is one decoded line with its confidence score.
type TextLine struct {
	BBox       BBox
	Text       string
	Confidence float64 // calibrated C_line in [0,1]
	NeedsLLM   bool    // true when Confidence < ConfidenceThreshold
}

// DetectLines runs the DB detector on img and returns bounding boxes.
// interp must be loaded with the DB .tflite model (input [1,608,800,3] NHWC for EasyOCR).
func DetectLines(interp *TFLiteInterpreter, img image.Image) ([]BBox, error) {
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

	boxes := extractBBoxes(probMap, mapH, mapW, mapC, origW, origH, inW, inH, offsetX, offsetY)
	fmt.Printf("[DBG] Detector: %d box(es) after merge (threshold=%.2f)\n", len(boxes), DBThreshold)
	for i, b := range boxes {
		fmt.Printf("  box[%d] x=%d..%d y=%d..%d (w=%d h=%d)\n", i, b.X0, b.X1, b.Y0, b.Y1, b.X1-b.X0, b.Y1-b.Y0)
	}
	return boxes, nil
}

// extractBBoxes binarises probMap and returns bounding boxes in original image coordinates.
// The output map is half-resolution: map coord × 2 gives padded-model-input coord.
// offsetX/offsetY are the padding offsets applied when fitting orig into the model input.
func extractBBoxes(probMap []float32, mapH, mapW, mapC, origW, origH, inW, inH, offsetX, offsetY int) []BBox {
	if len(probMap) != mapH*mapW*mapC {
		return []BBox{}
	}
	binary := make([]bool, mapH*mapW)
	for i := 0; i < mapH*mapW; i++ {
		// First channel is the text probability map.
		v := probMap[i*mapC+0]
		binary[i] = v > DBThreshold
	}

	visited := make([]bool, mapH*mapW)
	var boxes []BBox
	// Map from output-map coordinates to original image coordinates.
	// Output map is half-res relative to model input (×2), then subtract padding, then scale.
	scaleWOrig := float64(origW) / float64(inW)
	scaleHOrig := float64(origH) / float64(inH)

	for startY := 0; startY < mapH; startY++ {
		for startX := 0; startX < mapW; startX++ {
			idx := startY*mapW + startX
			if !binary[idx] || visited[idx] {
				continue
			}
			// BFS flood-fill to find connected component.
			minX, minY, maxX, maxY := startX, startY, startX, startY
			area := 0
			queue := []int{idx}
			visited[idx] = true
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				cy, cx := cur/mapW, cur%mapW
				area++
				if cx < minX {
					minX = cx
				}
				if cx > maxX {
					maxX = cx
				}
				if cy < minY {
					minY = cy
				}
				if cy > maxY {
					maxY = cy
				}
				for _, n := range neighbors4(cx, cy, mapW, mapH) {
					if !visited[n] && binary[n] {
						visited[n] = true
						queue = append(queue, n)
					}
				}
			}
			if area < DBMinArea {
				continue
			}
			// ×2: undo half-res; subtract offset: undo letterbox; ×scale: undo resize.
			// Extra ±4/±5 px: DB tight-boxes the stroke; pad to capture full character cells.
			x0 := int(math.Round((float64(minX)*2-float64(offsetX))*scaleWOrig)) - 4
			y0 := int(math.Round((float64(minY)*2-float64(offsetY))*scaleHOrig)) - 5
			x1 := int(math.Round((float64(maxX+1)*2-float64(offsetX))*scaleWOrig)) + 4
			y1 := int(math.Round((float64(maxY+1)*2-float64(offsetY))*scaleHOrig)) + 5
			if x0 < 0 {
				x0 = 0
			}
			if y0 < 0 {
				y0 = 0
			}
			if x1 > origW {
				x1 = origW
			}
			if y1 > origH {
				y1 = origH
			}
			boxes = append(boxes, BBox{x0, y0, x1, y1})
		}
	}
	return groupByTextLine(boxes)
}

// groupByTextLine clusters sub-word detection boxes into one bounding box per text line.
// Boxes whose Y-center falls within lineGapY pixels of the running group center are merged.
// lineGapY=6 separates terminal text rows spaced ~12px apart while grouping same-row fragments.
func groupByTextLine(boxes []BBox) []BBox {
	if len(boxes) == 0 {
		return boxes
	}
	// Sort by Y-center for stable sequential grouping.
	sort.Slice(boxes, func(i, j int) bool {
		ci := (boxes[i].Y0 + boxes[i].Y1) / 2
		cj := (boxes[j].Y0 + boxes[j].Y1) / 2
		return ci < cj
	})
	const lineGapY = 6
	type group struct {
		merged  BBox
		centerY int
	}
	var groups []group
	for _, b := range boxes {
		centerY := (b.Y0 + b.Y1) / 2
		matched := -1
		for i := range groups {
			if absInt(centerY-groups[i].centerY) <= lineGapY {
				matched = i
				break
			}
		}
		if matched >= 0 {
			groups[matched].merged = unionBox(groups[matched].merged, b)
			groups[matched].centerY = (groups[matched].merged.Y0 + groups[matched].merged.Y1) / 2
		} else {
			groups = append(groups, group{b, centerY})
		}
	}
	result := make([]BBox, len(groups))
	for i, g := range groups {
		result[i] = g.merged
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Y0 < result[j].Y0 })
	return result
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// neighbors4 returns the 4-connected neighbours of (x,y) within bounds.
func neighbors4(x, y, w, h int) []int {
	var ns []int
	if x > 0 {
		ns = append(ns, y*w+(x-1))
	}
	if x < w-1 {
		ns = append(ns, y*w+(x+1))
	}
	if y > 0 {
		ns = append(ns, (y-1)*w+x)
	}
	if y < h-1 {
		ns = append(ns, (y+1)*w+x)
	}
	return ns
}

func unionBox(a, b BBox) BBox {
	return BBox{min(a.X0, b.X0), min(a.Y0, b.Y0), max(a.X1, b.X1), max(a.Y1, b.Y1)}
}

// RecognizeLines runs the SVTR/CRNN recognizer on each bounding box crop.
// interp must be loaded with the recognizer .tflite model (input [1, 64, 800, 1] NHWC).
func RecognizeLines(interp *TFLiteInterpreter, img image.Image, boxes []BBox) ([]TextLine, error) {
	// EasyOCR CTCLabelConverter: index 0 = blank, indices 1..K-1 = characters.
	vocab := []rune(enCharset)
	const blankIdx = 0
	lines := make([]TextLine, 0, len(boxes))

	for _, box := range boxes {
		crop := CropRect(img, box.Rect())
		// Recognizer expects [1, 64, 800, 1] NHWC (grayscale, not RGB)
		// Resize to height 64, width up to 800
		strip := ResizeHeightAndWidth(crop, 64, 800)
		stripW := strip.Bounds().Dx()

		// Build raw [0,1] grayscale tensor [64×800], left-aligned.
		// The exported TFLite recognizer applies (x-0.5)/0.5 normalization internally.
		const recH, recW = 64, 800
		bgVal := float32(GrayscaleAt(strip.At(0, 0)))
		tensor := make([]float32, recH*recW)
		for i := range tensor {
			tensor[i] = bgVal
		}
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

		// Output shape [1,T,K] or [T,K]: T timesteps, K classes (blank+chars).
		var T, K int
		switch len(shape) {
		case 3:
			T, K = int(shape[1]), int(shape[2])
		case 2:
			T, K = int(shape[0]), int(shape[1])
		default:
			fmt.Fprintf(os.Stderr, "[WARN] unexpected recognizer output shape %v, skipping box\n", shape)
			continue
		}

		// Log first box's recognizer output for diagnostics.
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
			BBox:       box,
			Text:       text,
			Confidence: calibrated,
			NeedsLLM:   calibrated < ConfidenceThreshold,
		})
	}
	return lines, nil
}

// ctcGreedyDecode decodes a [T×K] logit matrix.
// Blank token is at model index 0; characters map as vocab[bestK-1] for bestK>=1.
// Confidence is the geometric mean of per-character softmax probabilities, per spec.
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
	// Geometric mean per spec: C_line = exp(mean(ln p_i))
	logSum := 0.0
	for _, p := range probs {
		if p > 0 {
			logSum += math.Log(p)
		}
	}
	cLine := math.Exp(logSum / float64(len(probs)))
	return string(chars), cLine
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
