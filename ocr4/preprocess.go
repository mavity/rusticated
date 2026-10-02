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
