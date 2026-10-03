package main

import (
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
)

// ImageNet normalization constants used by DB detector.
var (
	dbMean = [3]float32{0.485, 0.456, 0.406}
	dbStd  = [3]float32{0.229, 0.224, 0.225}
)

// Recognition normalization: zero-mean, unit-range.
var (
	recMean = [3]float32{0.5, 0.5, 0.5}
	recStd  = [3]float32{0.5, 0.5, 0.5}
)

// LoadImage decodes a JPEG or PNG file into an RGBA image.
func LoadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// ResizeNearest resizes img to (w, h) using nearest-neighbour interpolation.
// We keep a simple implementation to avoid external dependencies.
func ResizeNearest(img image.Image, w, h int) *image.NRGBA {
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

// ResizeHeight resizes img so that the height equals targetH, preserving aspect ratio.
func ResizeHeight(img image.Image, targetH int) *image.NRGBA {
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
	return ResizeNearest(img, targetW, targetH)
}

// ResizeHeightAndWidth resizes img so that height equals targetH and width is capped at maxW,
// preserving aspect ratio. If the result would be wider than maxW, crops to fit.
func ResizeHeightAndWidth(img image.Image, targetH, maxW int) *image.NRGBA {
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
	return ResizeNearest(img, targetW, targetH)
}

// CropRect crops img to r, clamped to the image bounds.
func CropRect(img image.Image, r image.Rectangle) *image.NRGBA {
	b := img.Bounds()
	r = r.Intersect(b)
	out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			out.Set(x-r.Min.X, y-r.Min.Y, img.At(x, y))
		}
	}
	return out
}

// ToNCHWTensor converts img to a [1, 3, H, W] float32 NCHW tensor.
// Each channel is normalised with the given mean and std.
func ToNCHWTensor(img image.Image, mean, std [3]float32) []float32 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]float32, 3*h*w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			rf := float32(r>>8) / 255.0
			gf := float32(g>>8) / 255.0
			bf := float32(bl>>8) / 255.0
			out[0*h*w+y*w+x] = (rf - mean[0]) / std[0]
			out[1*h*w+y*w+x] = (gf - mean[1]) / std[1]
			out[2*h*w+y*w+x] = (bf - mean[2]) / std[2]
		}
	}
	return out
}

// ToNCHWTensorRaw converts img to a [1, 3, H, W] float32 NCHW tensor with ImageNet normalization.
// ImageNet normalization: mean=[0.485, 0.456, 0.406], std=[0.229, 0.224, 0.225]
// Formula: (pixel/255 - mean) / std
func ToNCHWTensorRaw(img image.Image) []float32 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]float32, 3*h*w)

	// ImageNet normalization parameters
	means := [3]float32{0.485, 0.456, 0.406} // R, G, B
	stds := [3]float32{0.229, 0.224, 0.225}  // R, G, B

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// Convert from uint16 to [0, 1] by dividing by 65535
			rf := float32(r) / 65535.0
			gf := float32(g) / 65535.0
			bf := float32(bl) / 65535.0
			// Apply ImageNet normalization
			out[0*h*w+y*w+x] = (rf - means[0]) / stds[0]
			out[1*h*w+y*w+x] = (gf - means[1]) / stds[1]
			out[2*h*w+y*w+x] = (bf - means[2]) / stds[2]
		}
	}
	return out
}

// ToGrayscaleHWC converts img to [H, W, 1] grayscale HWC tensor (float32, [0,1]).
// For text recognition, uses luminance weighting and applies recognition normalization.
// Normalization: mean=[0.5, 0.5, 0.5], std=[0.5, 0.5, 0.5]
// Formula: (x - mean) / std = (x - 0.5) / 0.5
func ToGrayscaleHWC(img image.Image) []float32 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]float32, h*w*1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			gray := GrayscaleAt(img.At(b.Min.X+x, b.Min.Y+y))
			// Normalize: (gray - 0.5) / 0.5 where gray is in [0, 1]
			normalized := (gray - 0.5) / 0.5
			out[y*w*1+x*1+0] = normalized
		}
	}
	return out
}

// GrayscaleAt returns the luminance [0,1] of a pixel.
func GrayscaleAt(c color.Color) float32 {
	r, g, b, _ := c.RGBA()
	return float32(0.299*float64(r)+0.587*float64(g)+0.114*float64(b)) / 65535.0
}

// PerspectiveCrop extracts a dewarped rectangular crop from img using a backward
// perspective warp, so that rotated text quads are straightened before recognition.
func PerspectiveCrop(img image.Image, q Quad) *image.NRGBA {
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
	// Auto-rotate tall crops to landscape orientation.
	if dstH > dstW {
		dstW, dstH = dstH, dstW
		q[0], q[1], q[2], q[3] = q[1], q[2], q[3], q[0]
	}

	// Backward homography: for each output pixel (u,v), find source (x,y) in img.
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

			// Bilinear interpolation; RGBA() returns [0,65535] values.
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

// solvePerspective computes the 8-DOF homography H mapping dst corners to src corners.
// Used for backward warping: each output pixel (u,v) maps to source (x,y) via H.
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

// gaussElim8 solves an 8×8 linear system Ax=b via Gaussian elimination with partial pivoting.
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
