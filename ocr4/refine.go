package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"strings"

	"ai"
)

// RefineOneLine refines a single TextLine in-place using Gemma 4 + the line
// image crop. A fresh conversation is created per line to avoid state
// contamination; the correction turn carries the full page context, the line
// image crop, and the Phase 1 candidate text.
func RefineOneLine(engine *ai.Engine, pageText string, line *TextLine, img image.Image) error {
	conv, err := engine.NewConversation()
	if err != nil {
		return err
	}
	defer conv.Close()
	crop := CropRect(img, line.BBox.Rect())
	parts, err := buildLineCorrectionParts(pageText, *line, crop)
	if err != nil {
		return err
	}
	var corrected strings.Builder
	if err := conv.Send(context.Background(), parts, func(text string) {
		corrected.WriteString(text)
	}); err != nil {
		return err
	}
	if t := strings.TrimSpace(corrected.String()); t != "" {
		line.Text = t
		line.NeedsLLM = false
	}
	return nil
}

func buildPageContext(lines []TextLine) string {
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// buildLineCorrectionParts builds a multimodal prompt (image crop + text) for
// correcting one OCR line. The ai module handles JSON encoding internally.
func buildLineCorrectionParts(pageText string, line TextLine, crop image.Image) ([]ai.PromptPart, error) {
	imgBytes, err := encodeImageJPEG(crop)
	if err != nil {
		return nil, err
	}
	prompt := "You are an OCR post-processor. Correct a single line of text from a scanned document.\n\n" +
		"Full page transcript (may contain OCR errors):\n<page>\n" + pageText + "\n</page>\n\n" +
		"Phase 1 OCR candidate for the target line: " + fmt.Sprintf("%q", line.Text) + "\n\n" +
		"The attached image is a crop of that exact line. " +
		"Examine it carefully and output ONLY the corrected text for this line. " +
		"No quotes, no explanation, no prefix."

	return []ai.PromptPart{
		{Role: ai.RoleUser, ImageBase64: base64.StdEncoding.EncodeToString(imgBytes)},
		{Role: ai.RoleUser, Text: prompt},
	}, nil
}
