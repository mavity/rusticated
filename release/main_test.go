package main

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIndexWithEmbeddedFavicons(t *testing.T) {
	svg, err := os.ReadFile(filepath.Join("..", "kabibi", "kabibi.svg"))
	if err != nil {
		t.Fatal(err)
	}
	template := []byte("<head>\n  " + sourceFaviconLink + "\n</head>")

	index, err := buildIndexWithEmbeddedFavicons(template, svg)
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	if strings.Contains(html, sourceFaviconLink) || strings.Contains(html, "kabibi/kabibi.svg") {
		t.Fatal("staged HTML still references the external Kabibi SVG")
	}

	svgPrefix := `href="data:image/svg+xml,`
	svgStart := strings.Index(html, svgPrefix)
	if svgStart < 0 {
		t.Fatal("staged HTML has no embedded SVG favicon")
	}
	svgStart += len(svgPrefix)
	svgEnd := strings.IndexByte(html[svgStart:], '"')
	if svgEnd < 0 {
		t.Fatal("embedded SVG favicon href is not closed")
	}
	decodedSVG, err := url.PathUnescape(html[svgStart : svgStart+svgEnd])
	if err != nil {
		t.Fatalf("decode embedded SVG URI: %v", err)
	}
	if strings.ContainsAny(decodedSVG, "\r\n") {
		t.Fatal("embedded SVG is not a single line")
	}
	if decodedSVG != strings.Join(strings.Fields(string(svg)), " ") {
		t.Fatal("embedded SVG does not match the whitespace-normalized source")
	}

	pngPrefix := `href="data:image/png;base64,`
	pngStart := strings.Index(html, pngPrefix)
	if pngStart < 0 {
		t.Fatal("staged HTML has no embedded Apple touch icon")
	}
	pngStart += len(pngPrefix)
	pngEnd := strings.IndexByte(html[pngStart:], '"')
	if pngEnd < 0 {
		t.Fatal("embedded PNG href is not closed")
	}
	pngBytes, err := base64.StdEncoding.DecodeString(html[pngStart : pngStart+pngEnd])
	if err != nil {
		t.Fatalf("decode embedded PNG: %v", err)
	}
	icon, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode rendered PNG: %v", err)
	}
	if icon.Bounds().Dx() != faviconPNGSize || icon.Bounds().Dy() != faviconPNGSize {
		t.Fatalf("PNG size = %v, want %dx%d", icon.Bounds(), faviconPNGSize, faviconPNGSize)
	}
	if _, _, _, alpha := icon.At(0, 0).RGBA(); alpha != 0 {
		t.Fatalf("PNG background alpha = %d, want transparent", alpha)
	}
	if _, _, _, alpha := icon.At(faviconPNGSize/2, faviconPNGSize/2).RGBA(); alpha == 0 {
		t.Fatal("rendered logo has no visible pixels at its center")
	}
}

func TestBuildIndexWithEmbeddedFaviconsRequiresOneSourceLink(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`)
	for _, template := range []string{
		"<head></head>",
		"<head>" + sourceFaviconLink + sourceFaviconLink + "</head>",
	} {
		if _, err := buildIndexWithEmbeddedFavicons([]byte(template), svg); err == nil {
			t.Errorf("buildIndexWithEmbeddedFavicons(%q) succeeded, want an error", template)
		}
	}
}
