package main

import (
	"bytes"
	"image"
	"image/jpeg"
)

// encodeImageJPEG encodes img to JPEG bytes at quality 85.
func encodeImageJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
