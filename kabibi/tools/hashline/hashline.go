// Package hashline implements anchor-verified, whitespace-tolerant line hashing
// ("LINE:HASH|content") and self-healing file editing with ±30-line relocation.
package hashline

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// EditOp specifies a single anchor-verified edit operation.
type EditOp struct {
	Op          string `json:"op" doc:"Operation: 'replace', 'insert_after', 'insert_before', or 'delete'"`
	StartAnchor string `json:"start_anchor" doc:"Start line anchor in 'LINE:HASH' format (e.g. '42:a3')"`
	EndAnchor   string `json:"end_anchor,omitempty" doc:"End line anchor (inclusive) for multi-line 'replace' or 'delete'. Omit for single-line."`
	Content     string `json:"content,omitempty" doc:"New text content (without LINE:HASH| prefixes)"`
}

// Range represents a 1-based inclusive line interval [Start, End].
type Range struct {
	Start int
	End   int
}

// EditResult contains the outcome of applying a batch of hashline edits.
type EditResult struct {
	Path        string `json:"path"`
	Tag         string `json:"tag"`
	Header      string `json:"header"`
	Snippet     string `json:"snippet"`
	AppliedHunk int    `json:"applied_hunks"`
	Relocated   int    `json:"relocated_hunks,omitempty"`
}

const (
	xxPrime64_1 uint64 = 11400714785074694791
	xxPrime64_2 uint64 = 14029467366897019727
	xxPrime64_3 uint64 = 1609587929392839161
	xxPrime64_4 uint64 = 9650029242287828579
	xxPrime64_5 uint64 = 2870177450012600261
)

func rotl64(x uint64, r uint) uint64 {
	return (x << r) | (x >> (64 - r))
}

// xxHash64 computes the canonical 64-bit xxHash (seed 0) in pure Go.
// Note that for empty input "", xxHash64("") & 0xFF == 0xef ("ef").
func xxHash64(b []byte) uint64 {
	n := len(b)
	var h64 uint64

	if n >= 32 {
		v1 := xxPrime64_1
		v1 += xxPrime64_2
		v2 := xxPrime64_2
		v3 := uint64(0)
		var z uint64
		v4 := z - xxPrime64_1
		for len(b) >= 32 {
			v1 = xxRound(v1, readU64LE(b[0:8]))
			v2 = xxRound(v2, readU64LE(b[8:16]))
			v3 = xxRound(v3, readU64LE(b[16:24]))
			v4 = xxRound(v4, readU64LE(b[24:32]))
			b = b[32:]
		}
		h64 = rotl64(v1, 1) + rotl64(v2, 7) + rotl64(v3, 12) + rotl64(v4, 18)
		h64 = xxMergeRound(h64, v1)
		h64 = xxMergeRound(h64, v2)
		h64 = xxMergeRound(h64, v3)
		h64 = xxMergeRound(h64, v4)
	} else {
		h64 = xxPrime64_5
	}

	h64 += uint64(n)

	for len(b) >= 8 {
		k1 := xxRound(0, readU64LE(b[:8]))
		h64 ^= k1
		h64 = rotl64(h64, 27)*xxPrime64_1 + xxPrime64_4
		b = b[8:]
	}
	if len(b) >= 4 {
		h64 ^= uint64(readU32LE(b[:4])) * xxPrime64_1
		h64 = rotl64(h64, 23)*xxPrime64_2 + xxPrime64_3
		b = b[4:]
	}
	for len(b) > 0 {
		h64 ^= uint64(b[0]) * xxPrime64_5
		h64 = rotl64(h64, 11) * xxPrime64_1
		b = b[1:]
	}

	h64 ^= h64 >> 33
	h64 *= xxPrime64_2
	h64 ^= h64 >> 29
	h64 *= xxPrime64_3
	h64 ^= h64 >> 32
	return h64
}

func xxRound(acc, input uint64) uint64 {
	acc += input * xxPrime64_2
	acc = rotl64(acc, 31)
	acc *= xxPrime64_1
	return acc
}

func xxMergeRound(acc, val uint64) uint64 {
	val = xxRound(0, val)
	acc ^= val
	acc = acc*xxPrime64_1 + xxPrime64_4
	return acc
}

func readU64LE(b []byte) uint64 {
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

func readU32LE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// ComputeLineHash strips '\r' and all ASCII spaces/tabs (' ', '\t') from line,
// computes xxHash64(stripped) & 0xFF, and formats it as a 2-hex-char string ("%02x").
// For blank/whitespace-only lines, returns "ef" per the hashline spec.
func ComputeLineHash(line string) string {
	buf := make([]byte, 0, len(line))
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\r' || c == ' ' || c == '\t' {
			continue
		}
		buf = append(buf, c)
	}
	if len(buf) == 0 {
		return "ef"
	}
	return fmt.Sprintf("%02x", xxHash64(buf)&0xFF)
}

// XXHash64String computes the 64-bit xxHash (seed 0) of a string.
func XXHash64String(s string) uint64 {
	return xxHash64([]byte(s))
}

// ComputeFileTag returns a 4-hex-char snapshot tag ("%04x") for raw file bytes.
func ComputeFileTag(raw []byte) string {
	return fmt.Sprintf("%04x", xxHash64(raw)&0xFFFF)
}

// SplitPathAndTag separates an optional "#TAG" suffix from a path argument
// (e.g. "main.go#8f2a" -> "main.go", "8f2a").
func SplitPathAndTag(pathWithTag string) (path string, tag string) {
	idx := strings.LastIndexByte(pathWithTag, '#')
	if idx > 0 && len(pathWithTag)-(idx+1) == 4 {
		cand := strings.ToLower(pathWithTag[idx+1:])
		isHex := true
		for i := 0; i < len(cand); i++ {
			c := cand[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				isHex = false
				break
			}
		}
		if isHex {
			return pathWithTag[:idx], cand
		}
	}
	return pathWithTag, ""
}

// SplitLines splits file text into lines (normalizing \r\n to \n).
func SplitLines(content string) []string {
	if content == "" {
		return nil
	}
	norm := strings.ReplaceAll(content, "\r\n", "\n")
	norm = strings.TrimSuffix(norm, "\n")
	return strings.Split(norm, "\n")
}

// FormatLine formats a single 1-based line number and line text in LINE:HASH|text format
// when useHashline is true, or LINE|text when false.
func FormatLine(lineNum int, line string, useHashline bool) string {
	clean := strings.TrimSuffix(line, "\r")
	if !useHashline {
		return fmt.Sprintf("%d|%s", lineNum, clean)
	}
	return fmt.Sprintf("%d:%s|%s", lineNum, ComputeLineHash(clean), clean)
}

// FormatHashlines formats selected 1-based inclusive line ranges with a `[path#TAG]` header
// and `…` (U+2026) separators between non-contiguous slices.
func FormatHashlines(displayPath string, rawBytes []byte, ranges []Range, summaryNote string, useHashline bool) string {
	lines := SplitLines(string(rawBytes))
	tag := ComputeFileTag(rawBytes)
	totalLines := len(lines)

	var sb strings.Builder
	if summaryNote != "" {
		fmt.Fprintf(&sb, "[%s#%s] (%s)\n", displayPath, tag, summaryNote)
	} else {
		fmt.Fprintf(&sb, "[%s#%s] (%d lines)\n", displayPath, tag, totalLines)
	}

	if totalLines == 0 {
		return strings.TrimSuffix(sb.String(), "\n")
	}

	if len(ranges) == 0 {
		ranges = []Range{{Start: 1, End: totalLines}}
	}

	merged := mergeRanges(ranges, totalLines)
	if len(merged) == 0 {
		return strings.TrimSuffix(sb.String(), "\n")
	}

	if merged[0].Start > 1 {
		sb.WriteString("…\n")
	}
	for i, r := range merged {
		if i > 0 {
			sb.WriteString("…\n")
		}
		for ln := r.Start; ln <= r.End; ln++ {
			sb.WriteString(FormatLine(ln, lines[ln-1], useHashline))
			sb.WriteByte('\n')
		}
	}
	if merged[len(merged)-1].End < totalLines {
		sb.WriteString("…\n")
	}

	return strings.TrimSuffix(sb.String(), "\n")
}

func mergeRanges(ranges []Range, maxLine int) []Range {
	var valid []Range
	for _, r := range ranges {
		s := r.Start
		e := r.End
		if s < 1 {
			s = 1
		}
		if e > maxLine {
			e = maxLine
		}
		if s <= e {
			valid = append(valid, Range{Start: s, End: e})
		}
	}
	if len(valid) == 0 {
		return nil
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Start != valid[j].Start {
			return valid[i].Start < valid[j].Start
		}
		return valid[i].End < valid[j].End
	})
	out := []Range{valid[0]}
	for _, cur := range valid[1:] {
		last := &out[len(out)-1]
		if cur.Start <= last.End+1 {
			if cur.End > last.End {
				last.End = cur.End
			}
		} else {
			out = append(out, cur)
		}
	}
	return out
}

// ParseAnchor parses a "LINE:HASH" (or "LINE:HASH|...") string into a 1-based line number and 2-char hex hash.
func ParseAnchor(anchor string) (line int, hash string, err error) {
	s := strings.TrimSpace(anchor)
	if pipeIdx := strings.IndexByte(s, '|'); pipeIdx > 0 {
		s = s[:pipeIdx]
	}
	colonIdx := strings.IndexByte(s, ':')
	if colonIdx <= 0 || colonIdx+1 >= len(s) {
		return 0, "", fmt.Errorf("invalid anchor %q (expected 'LINE:HASH', e.g. '42:a3')", anchor)
	}
	lineNum, err := strconv.Atoi(strings.TrimSpace(s[:colonIdx]))
	if err != nil || lineNum < 1 {
		return 0, "", fmt.Errorf("invalid line number in anchor %q", anchor)
	}
	h := strings.ToLower(strings.TrimSpace(s[colonIdx+1:]))
	if len(h) != 2 {
		return 0, "", fmt.Errorf("invalid 2-char hex hash in anchor %q", anchor)
	}
	return lineNum, h, nil
}

var hashlinePrefixRe = regexp.MustCompile(`^\s*\d+:[0-9a-fA-F]{2}\|`)

// SanitizeReplacementContent strips accidental `LINE:HASH|` prefixes from replacement lines
// when the original file does not itself contain literal `LINE:HASH|` text.
func SanitizeReplacementContent(content string, origLines []string) []string {
	if content == "" {
		return nil
	}
	norm := strings.ReplaceAll(content, "\r\n", "\n")
	norm = strings.TrimSuffix(norm, "\n")
	repLines := strings.Split(norm, "\n")

	fileHasHashlineText := false
	for _, l := range origLines {
		if hashlinePrefixRe.MatchString(l) {
			fileHasHashlineText = true
			break
		}
	}
	if fileHasHashlineText {
		return repLines
	}

	cleaned := make([]string, len(repLines))
	for i, l := range repLines {
		if loc := hashlinePrefixRe.FindStringIndex(l); loc != nil && loc[0] == 0 {
			cleaned[i] = l[loc[1]:]
		} else {
			cleaned[i] = l
		}
	}
	return cleaned
}

type resolvedHunk struct {
	origIndex int
	op        string
	startLine int
	endLine   int
	newLines  []string
	relocated bool
}

const relocateWindow = 30

// ApplyHashlineEdits validates and atomically applies a list of EditOp operations to the file at absPath.
// Supports ±30-line unique hash relocation and returns fresh anchors for the modified region.
func ApplyHashlineEdits(absPath string, displayPath string, edits []EditOp) (*EditResult, error) {
	if len(edits) == 0 {
		return nil, fmt.Errorf("no edits provided")
	}

	rawBytes, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	rawStr := string(rawBytes)
	useCRLF := strings.Contains(rawStr, "\r\n")
	hadTrailingNewline := strings.HasSuffix(rawStr, "\n") || len(rawBytes) == 0

	lines := SplitLines(rawStr)
	lineHashes := make([]string, len(lines))
	for i, l := range lines {
		lineHashes[i] = ComputeLineHash(l)
	}

	resolved := make([]resolvedHunk, 0, len(edits))
	totalRelocated := 0

	for idx, e := range edits {
		op := strings.ToLower(strings.TrimSpace(e.Op))
		switch op {
		case "replace", "insert_after", "insert_before", "delete":
		default:
			return nil, fmt.Errorf("edit[%d]: unsupported op %q (expected replace, insert_after, insert_before, or delete)", idx, e.Op)
		}

		reqStart, startHash, err := ParseAnchor(e.StartAnchor)
		if err != nil {
			return nil, fmt.Errorf("edit[%d]: %w", idx, err)
		}

		reqEnd := reqStart
		endHash := startHash
		hasEnd := strings.TrimSpace(e.EndAnchor) != "" && (op == "replace" || op == "delete")
		if hasEnd {
			reqEnd, endHash, err = ParseAnchor(e.EndAnchor)
			if err != nil {
				return nil, fmt.Errorf("edit[%d] end_anchor: %w", idx, err)
			}
			if reqEnd < reqStart {
				return nil, fmt.Errorf("edit[%d]: end_anchor line %d is before start_anchor line %d", idx, reqEnd, reqStart)
			}
		}

		actualStart, actualEnd, relocated, matchErr := resolveAnchorSpan(
			lines, lineHashes, reqStart, startHash, reqEnd, endHash,
		)
		if matchErr != nil {
			snippet := buildRecoverySnippet(displayPath, rawBytes, lines, reqStart)
			return nil, fmt.Errorf("edit[%d] (%s at %s): %v\n\nCurrent lines around line %d:\n%s",
				idx, op, e.StartAnchor, matchErr, reqStart, snippet)
		}
		if relocated {
			totalRelocated++
		}

		var repLines []string
		if op != "delete" {
			repLines = SanitizeReplacementContent(e.Content, lines)
		}

		resolved = append(resolved, resolvedHunk{
			origIndex: idx,
			op:        op,
			startLine: actualStart,
			endLine:   actualEnd,
			newLines:  repLines,
			relocated: relocated,
		})
	}

	// Sort bottom-up (by startLine descending) so earlier edits never shift line numbers for later ones.
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].startLine != resolved[j].startLine {
			return resolved[i].startLine > resolved[j].startLine
		}
		return resolved[i].origIndex > resolved[j].origIndex
	})

	// Check for overlapping hunks
	for i := 0; i+1 < len(resolved); i++ {
		upper := resolved[i]   // higher line number
		lower := resolved[i+1] // lower or equal line number
		if lower.endLine > upper.startLine || (lower.endLine == upper.startLine && lower.op != "insert_before" && upper.op != "insert_after") {
			return nil, fmt.Errorf("overlapping edit hunks around lines %d..%d and %d..%d",
				lower.startLine, lower.endLine, upper.startLine, upper.endLine)
		}
	}

	minTouched := len(lines) + 1
	maxTouched := 1
	lineDelta := 0

	for _, h := range resolved {
		if h.startLine < minTouched {
			minTouched = h.startLine
		}
		if h.endLine > maxTouched {
			maxTouched = h.endLine
		}

		switch h.op {
		case "replace":
			before := lines[:h.startLine-1]
			after := lines[h.endLine:]
			oldCount := h.endLine - h.startLine + 1
			lineDelta += len(h.newLines) - oldCount
			next := make([]string, 0, len(before)+len(h.newLines)+len(after))
			next = append(next, before...)
			next = append(next, h.newLines...)
			next = append(next, after...)
			lines = next

		case "delete":
			before := lines[:h.startLine-1]
			after := lines[h.endLine:]
			oldCount := h.endLine - h.startLine + 1
			lineDelta -= oldCount
			next := make([]string, 0, len(before)+len(after))
			next = append(next, before...)
			next = append(next, after...)
			lines = next

		case "insert_after":
			before := lines[:h.startLine]
			after := lines[h.startLine:]
			lineDelta += len(h.newLines)
			next := make([]string, 0, len(before)+len(h.newLines)+len(after))
			next = append(next, before...)
			next = append(next, h.newLines...)
			next = append(next, after...)
			lines = next

		case "insert_before":
			before := lines[:h.startLine-1]
			after := lines[h.startLine-1:]
			lineDelta += len(h.newLines)
			next := make([]string, 0, len(before)+len(h.newLines)+len(after))
			next = append(next, before...)
			next = append(next, h.newLines...)
			next = append(next, after...)
			lines = next
		}
	}

	sep := "\n"
	if useCRLF {
		sep = "\r\n"
	}
	newContent := strings.Join(lines, sep)
	if hadTrailingNewline && len(lines) > 0 {
		newContent += sep
	}
	newBytes := []byte(newContent)

	if err := os.WriteFile(absPath, newBytes, 0o644); err != nil {
		return nil, err
	}

	newTag := ComputeFileTag(newBytes)
	ctxStart := minTouched - 3
	if ctxStart < 1 {
		ctxStart = 1
	}
	ctxEnd := maxTouched + lineDelta + 3
	if ctxEnd > len(lines) {
		ctxEnd = len(lines)
	}
	if ctxEnd < ctxStart && len(lines) > 0 {
		ctxEnd = len(lines)
	}

	note := fmt.Sprintf("updated %d hunk(s)", len(resolved))
	if totalRelocated > 0 {
		note = fmt.Sprintf("updated %d hunk(s), %d auto-relocated", len(resolved), totalRelocated)
	}
	snippet := FormatHashlines(displayPath, newBytes, []Range{{Start: ctxStart, End: ctxEnd}}, note, true)

	return &EditResult{
		Path:        absPath,
		Tag:         newTag,
		Header:      fmt.Sprintf("[%s#%s]", displayPath, newTag),
		Snippet:     snippet,
		AppliedHunk: len(resolved),
		Relocated:   totalRelocated,
	}, nil
}

func resolveAnchorSpan(
	lines []string,
	lineHashes []string,
	reqStart int,
	startHash string,
	reqEnd int,
	endHash string,
) (actualStart int, actualEnd int, relocated bool, err error) {
	n := len(lines)
	if n == 0 {
		return 0, 0, false, fmt.Errorf("file is empty")
	}
	spanOffset := reqEnd - reqStart

	// 1. Check exact line numbers first
	if reqStart >= 1 && reqEnd <= n {
		if lineHashes[reqStart-1] == startHash && lineHashes[reqEnd-1] == endHash {
			return reqStart, reqEnd, false, nil
		}
	}

	// 2. Scan ±30 lines around reqStart for a unique matching span
	winStart := reqStart - relocateWindow
	if winStart < 1 {
		winStart = 1
	}
	winEnd := reqStart + relocateWindow
	if winEnd+spanOffset > n {
		winEnd = n - spanOffset
	}

	var candidates []int
	for cand := winStart; cand <= winEnd; cand++ {
		if lineHashes[cand-1] == startHash && lineHashes[cand+spanOffset-1] == endHash {
			candidates = append(candidates, cand)
		}
	}

	if len(candidates) == 1 {
		c := candidates[0]
		return c, c + spanOffset, true, nil
	}
	if len(candidates) > 1 {
		return 0, 0, false, fmt.Errorf("anchor %d:%s shifted and matches %d ambiguous lines in ±%d window",
			reqStart, startHash, len(candidates), relocateWindow)
	}

	if reqStart >= 1 && reqStart <= n {
		return 0, 0, false, fmt.Errorf("anchor hash mismatch at line %d: expected %s, found %s (%q)",
			reqStart, startHash, lineHashes[reqStart-1], lines[reqStart-1])
	}
	return 0, 0, false, fmt.Errorf("line %d is out of range (file has %d lines)", reqStart, n)
}

func buildRecoverySnippet(displayPath string, rawBytes []byte, lines []string, centerLine int) string {
	if len(lines) == 0 {
		return fmt.Sprintf("[%s#%s] (empty file)", displayPath, ComputeFileTag(rawBytes))
	}
	s := centerLine - 5
	if s < 1 {
		s = 1
	}
	e := centerLine + 5
	if e > len(lines) {
		e = len(lines)
	}
	return FormatHashlines(displayPath, rawBytes, []Range{{Start: s, End: e}}, fmt.Sprintf("lines %d-%d of %d", s, e, len(lines)), true)
}
