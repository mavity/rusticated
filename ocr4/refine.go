package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"strings"
)

// RefineLines uses Gemma 4 to correct low-confidence Phase 1 lines.
// Each flagged line is corrected with a single, self-contained turn that includes
// the full page context, the line image crop, and the Phase 1 candidate text.
// A fresh conversation is created per line to avoid state contamination.
// RefineOneLine refines a single TextLine in-place using Gemma 4 + the line image crop.
func RefineOneLine(engine *LMEngine, pageText string, line *TextLine, img image.Image) error {
	conv, err := engine.NewConversation()
	if err != nil {
		return err
	}
	defer conv.Close()
	crop := CropRect(img, line.BBox.Rect())
	payload, err := buildLineCorrectionPayload(pageText, *line, crop)
	if err != nil {
		return err
	}
	var corrected strings.Builder
	if err := conv.SendMessageStream(payload, func(raw string) {
		corrected.WriteString(extractTokenText(raw))
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

// buildLineCorrectionPayload builds a LiteRT-LM multimodal message.
// Format per litert-lm source (_messages.py): content is an array of typed parts;
// images use {"type":"image","blob":"<base64>"} and text uses {"type":"text","text":"..."}.
func buildLineCorrectionPayload(pageText string, line TextLine, crop image.Image) (string, error) {
	imgBytes, err := encodeImageJPEG(crop)
	if err != nil {
		return "", err
	}
	prompt := "You are an OCR post-processor. Correct a single line of text from a scanned document.\n\n" +
		"Full page transcript (may contain OCR errors):\n<page>\n" + pageText + "\n</page>\n\n" +
		"Phase 1 OCR candidate for the target line: " + fmt.Sprintf("%q", line.Text) + "\n\n" +
		"The attached image is a crop of that exact line. " +
		"Examine it carefully and output ONLY the corrected text for this line. " +
		"No quotes, no explanation, no prefix."

	type contentPart = map[string]string
	msg := map[string]any{
		"role": "user",
		"content": []contentPart{
			{"type": "image", "blob": base64.StdEncoding.EncodeToString(imgBytes)},
			{"type": "text", "text": prompt},
		},
	}
	out, err := json.Marshal(msg)
	return string(out), err
}

// extractTokenText parses a raw streaming token from litert_lm and returns just the text.
// Raw tokens are JSON objects like {"role":"assistant","content":[{"type":"text","text":"..."}]}.
func extractTokenText(raw string) string {
	var msg struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return raw
	}
	if len(msg.Content) > 0 {
		return msg.Content[0].Text
	}
	return raw
}
