// Package fabric implements the 4-tier progressive search funnel
// (Exact -> Regex -> Fuzzy -> EmbeddingGemma 2 Semantic Vector Search)
// and indentation-gravity "fabric tightness" block expansion with `…` merging.
package fabric

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"ai"
	"github.com/mavity/rusticated/kabibi/tools/hashline"
)

const (
	DefaultMaxMatches    = 25
	DefaultPerHitBudget  = 25
	DefaultTotalBudget   = 160
	SemanticSimilarityTh = 0.55
)

// SearchResult holds the matched line indices, expanded ranges, and diagnostic metadata.
type SearchResult struct {
	Ranges     []hashline.Range
	MatchLines []int  // 1-based line numbers that matched
	TierUsed   string // "plain", "regex", "fuzzy", or "semantic"
}

type chunkCacheKey struct {
	path     string
	modNano  int64
	chunkIdx int
}

var (
	embedCacheMu sync.Mutex
	embedCache   = make(map[chunkCacheKey][]float32)
)

// SearchAndExpand runs the progressive 4-tier search funnel over lines and expands
// matching anchors into indentation-coherent structural slices.
func SearchAndExpand(
	path string,
	modNano int64,
	lines []string,
	query string,
	mode string,
	embedder ai.PartEmbedder,
	totalBudget int,
) SearchResult {
	if totalBudget <= 0 {
		totalBudget = DefaultTotalBudget
	}
	q := strings.TrimSpace(query)
	if q == "" || len(lines) == 0 {
		end := len(lines)
		if end > totalBudget {
			end = totalBudget
		}
		return SearchResult{
			Ranges:   []hashline.Range{{Start: 1, End: end}},
			TierUsed: "none",
		}
	}

	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}

	var matchIndices []int // 0-based line indices
	var tier string

	switch mode {
	case "plain":
		matchIndices = matchPlain(lines, q)
		tier = "plain"

	case "regex":
		matchIndices = matchRegex(lines, q)
		tier = "regex"

	case "semantic":
		if embedder != nil {
			matchIndices = matchSemantic(path, modNano, lines, q, embedder)
			tier = "semantic"
		}
		if len(matchIndices) == 0 {
			matchIndices = matchFuzzy(lines, q)
			tier = "fuzzy"
		}

	default: // "auto": progressive escalation ladder
		// Tier 1: Exact case-sensitive -> case-insensitive
		matchIndices = matchPlain(lines, q)
		tier = "plain"

		// Tier 2: Regex if query has metacharacters or Tier 1 found 0 matches
		if len(matchIndices) == 0 && (hasRegexMeta(q) || len(matchIndices) == 0) {
			if rxMatches := matchRegex(lines, q); len(rxMatches) > 0 {
				matchIndices = rxMatches
				tier = "regex"
			}
		}

		// Tier 3: Fuzzy token + Levenshtein match when Tiers 1-2 return 0
		if len(matchIndices) == 0 {
			if fzMatches := matchFuzzy(lines, q); len(fzMatches) > 0 {
				matchIndices = fzMatches
				tier = "fuzzy"
			}
		}

		// Tier 4: EmbeddingGemma 2 semantic vector search when Tiers 1-3 return 0
		// OR when Tiers 1-3 return too many matches (> DefaultMaxMatches)
		if embedder != nil && (len(matchIndices) == 0 || len(matchIndices) > DefaultMaxMatches) {
			if semMatches := matchSemantic(path, modNano, lines, q, embedder); len(semMatches) > 0 {
				matchIndices = semMatches
				tier = "semantic"
			}
		}

		if len(matchIndices) > DefaultMaxMatches {
			matchIndices = matchIndices[:DefaultMaxMatches]
		}
	}

	if len(matchIndices) == 0 {
		return SearchResult{
			Ranges:     nil,
			MatchLines: nil,
			TierUsed:   tier,
		}
	}

	oneBased := make([]int, len(matchIndices))
	for i, idx := range matchIndices {
		oneBased[i] = idx + 1
	}

	ranges := ExpandAndMergeSlices(lines, matchIndices, DefaultPerHitBudget, totalBudget)
	return SearchResult{
		Ranges:     ranges,
		MatchLines: oneBased,
		TierUsed:   tier,
	}
}

func matchPlain(lines []string, query string) []int {
	var exact []int
	for i, l := range lines {
		if strings.Contains(l, query) {
			exact = append(exact, i)
		}
	}
	if len(exact) >= 1 && len(exact) <= DefaultMaxMatches {
		return exact
	}
	lowerQ := strings.ToLower(query)
	var ci []int
	for i, l := range lines {
		if strings.Contains(strings.ToLower(l), lowerQ) {
			ci = append(ci, i)
		}
	}
	if len(exact) > 0 && len(ci) > DefaultMaxMatches {
		return exact
	}
	return ci
}

func hasRegexMeta(q string) bool {
	return strings.ContainsAny(q, `.*+?^$[](){}|\`)
}

func matchRegex(lines []string, query string) []int {
	re, err := regexp.Compile("(?i)" + query)
	if err != nil {
		return nil
	}
	var out []int
	for i, l := range lines {
		if re.MatchString(l) {
			out = append(out, i)
		}
	}
	return out
}

func tokenizeWords(s string) []string {
	var tokens []string
	var cur strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			tok := cur.String()
			if len(tok) >= 2 {
				tokens = append(tokens, tok)
			}
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		tok := cur.String()
		if len(tok) >= 2 {
			tokens = append(tokens, tok)
		}
	}
	return tokens
}

func matchFuzzy(lines []string, query string) []int {
	qTokens := tokenizeWords(query)
	if len(qTokens) == 0 {
		return nil
	}

	type scoredLine struct {
		idx   int
		score float64
	}
	var scored []scoredLine

	for i, line := range lines {
		lTokens := tokenizeWords(line)
		if len(lTokens) == 0 {
			continue
		}
		matched := 0.0
		for _, qt := range qTokens {
			best := 0.0
			for _, lt := range lTokens {
				if lt == qt {
					best = 1.0
					break
				}
				if strings.Contains(lt, qt) || strings.Contains(qt, lt) {
					if best < 0.85 {
						best = 0.85
					}
					continue
				}
				dist := levenshtein(qt, lt)
				maxLen := len(qt)
				if len(lt) > maxLen {
					maxLen = len(lt)
				}
				if maxLen >= 4 && dist <= 2 {
					sim := 1.0 - float64(dist)/float64(maxLen)
					if sim > best {
						best = sim
					}
				}
			}
			matched += best
		}
		ratio := matched / float64(len(qTokens))
		if ratio >= 0.60 {
			scored = append(scored, scoredLine{idx: i, score: ratio})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].idx < scored[j].idx
	})

	limit := 10
	if len(scored) < limit {
		limit = len(scored)
	}
	out := make([]int, limit)
	for i := 0; i < limit; i++ {
		out[i] = scored[i].idx
	}
	sort.Ints(out)
	return out
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func matchSemantic(path string, modNano int64, lines []string, query string, embedder ai.PartEmbedder) []int {
	qVec, err := embedder.Embed([]ai.PromptPart{{Text: query}})
	if err != nil || len(qVec) == 0 {
		return nil
	}

	const chunkLines = 20
	const stride = 12
	type scoredChunk struct {
		centerIdx int
		sim       float64
	}
	var results []scoredChunk

	chunkIdx := 0
	for start := 0; start < len(lines); start += stride {
		end := start + chunkLines
		if end > len(lines) {
			end = len(lines)
		}
		key := chunkCacheKey{path: path, modNano: modNano, chunkIdx: chunkIdx}
		chunkIdx++

		embedCacheMu.Lock()
		vec, ok := embedCache[key]
		embedCacheMu.Unlock()

		if !ok {
			chunkText := strings.Join(lines[start:end], "\n")
			if strings.TrimSpace(chunkText) == "" {
				continue
			}
			vec, err = embedder.Embed([]ai.PromptPart{{Text: chunkText}})
			if err != nil || len(vec) == 0 {
				continue
			}
			embedCacheMu.Lock()
			if len(embedCache) > 4096 {
				embedCache = make(map[chunkCacheKey][]float32)
			}
			embedCache[key] = vec
			embedCacheMu.Unlock()
		}

		sim := cosineSimilarity(qVec, vec)
		if sim >= SemanticSimilarityTh {
			center := (start + end - 1) / 2
			results = append(results, scoredChunk{centerIdx: center, sim: sim})
		}
		if end == len(lines) {
			break
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].sim > results[j].sim
	})
	limit := 5
	if len(results) < limit {
		limit = len(results)
	}
	out := make([]int, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, results[i].centerIdx)
	}
	sort.Ints(out)
	return out
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ComputeIndentationGravity computes the indentation column profile I(k) for each line,
// interpolating blank lines from their nearest non-blank neighbors.
func ComputeIndentationGravity(lines []string) ([]int, []bool) {
	n := len(lines)
	indents := make([]int, n)
	isBlank := make([]bool, n)

	for i, l := range lines {
		col := 0
		blank := true
		for _, r := range l {
			if r == ' ' {
				col++
			} else if r == '\t' {
				col += 4
			} else if r == '\r' {
				continue
			} else {
				blank = false
				break
			}
		}
		if blank {
			isBlank[i] = true
		} else {
			indents[i] = col
		}
	}

	// Interpolate blank lines: I(k) = min(I(prev_non_blank), I(next_non_blank))
	prevIndent := make([]int, n)
	last := 0
	for i := 0; i < n; i++ {
		if !isBlank[i] {
			last = indents[i]
		}
		prevIndent[i] = last
	}
	nextIndent := make([]int, n)
	last = 0
	for i := n - 1; i >= 0; i-- {
		if !isBlank[i] {
			last = indents[i]
		}
		nextIndent[i] = last
	}

	for i := 0; i < n; i++ {
		if isBlank[i] {
			p := prevIndent[i]
			nx := nextIndent[i]
			if nx < p {
				p = nx
			}
			indents[i] = p
		}
	}
	return indents, isBlank
}

// ExpandAndMergeSlices expands each 0-based anchor index in matchIndices using
// indentation gravity into structural blocks and merges overlapping intervals.
func ExpandAndMergeSlices(lines []string, matchIndices []int, perHitBudget int, totalBudget int) []hashline.Range {
	n := len(lines)
	if n == 0 || len(matchIndices) == 0 {
		return nil
	}
	if perHitBudget <= 0 {
		perHitBudget = DefaultPerHitBudget
	}
	if totalBudget <= 0 {
		totalBudget = DefaultTotalBudget
	}

	effectivePerHit := totalBudget / len(matchIndices)
	if effectivePerHit > perHitBudget {
		effectivePerHit = perHitBudget
	}
	if effectivePerHit < 6 {
		effectivePerHit = 6
	}

	indents, isBlank := ComputeIndentationGravity(lines)
	var ranges []hashline.Range

	for _, m := range matchIndices {
		if m < 0 || m >= n {
			continue
		}
		s, e := expandSingleAnchor(lines, indents, isBlank, m, effectivePerHit)
		ranges = append(ranges, hashline.Range{Start: s + 1, End: e + 1})
	}

	// Merge overlapping or adjacent 1-based ranges
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start != ranges[j].Start {
			return ranges[i].Start < ranges[j].Start
		}
		return ranges[i].End < ranges[j].End
	})

	var merged []hashline.Range
	usedLines := 0
	for _, r := range ranges {
		if len(merged) > 0 && r.Start <= merged[len(merged)-1].End+1 {
			prev := &merged[len(merged)-1]
			if r.End > prev.End {
				added := r.End - prev.End
				if usedLines+added > totalBudget {
					prev.End = prev.End + (totalBudget - usedLines)
					break
				}
				usedLines += added
				prev.End = r.End
			}
			continue
		}
		span := r.End - r.Start + 1
		if usedLines+span > totalBudget {
			rem := totalBudget - usedLines
			if rem >= 3 {
				merged = append(merged, hashline.Range{Start: r.Start, End: r.Start + rem - 1})
			}
			break
		}
		usedLines += span
		merged = append(merged, r)
	}
	return merged
}

func expandSingleAnchor(lines []string, indents []int, isBlank []bool, m int, budget int) (startIdx int, endIdx int) {
	n := len(lines)
	startIdx = m
	endIdx = m
	currMin := indents[m]
	upBudget := (budget * 2) / 5
	if upBudget < 3 {
		upBudget = 3
	}

	// 1. Walk upward to capture siblings and enclosing header
	consecutiveBlanks := 0
	for k := m - 1; k >= 0 && (m-startIdx) < upBudget; k-- {
		if isBlank[k] {
			consecutiveBlanks++
			if consecutiveBlanks >= 2 && currMin == 0 {
				break
			}
			startIdx = k
			continue
		}
		consecutiveBlanks = 0
		ik := indents[k]
		if ik < currMin {
			currMin = ik
			startIdx = k
			if currMin == 0 {
				// Include immediately preceding comment lines (// or #)
				for c := k - 1; c >= 0 && (m-startIdx) < upBudget; c-- {
					trimmed := strings.TrimSpace(lines[c])
					if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
						startIdx = c
					} else {
						break
					}
				}
				break
			}
		} else if ik >= currMin {
			startIdx = k
		}
	}

	// 2. Walk downward while I(k) >= enclosing indent (plus matching closing delimiter)
	remBudget := budget - (m - startIdx + 1)
	if remBudget < 2 {
		remBudget = 2
	}
	enclosingIndent := currMin
	consecutiveBlanks = 0
	for k := m + 1; k < n && (endIdx-m) < remBudget; k++ {
		if isBlank[k] {
			consecutiveBlanks++
			if consecutiveBlanks >= 2 {
				break
			}
			endIdx = k
			continue
		}
		consecutiveBlanks = 0
		ik := indents[k]
		trimmed := strings.TrimSpace(lines[k])
		isCloser := strings.HasPrefix(trimmed, "}") || strings.HasPrefix(trimmed, ")") || strings.HasPrefix(trimmed, "]") || trimmed == "end"
		if ik > enclosingIndent {
			endIdx = k
		} else if ik == enclosingIndent && isCloser {
			endIdx = k
			break
		} else if ik == indents[m] {
			endIdx = k
		} else {
			break
		}
	}

	// Trim leading/trailing blank lines on the slice
	for startIdx < m && isBlank[startIdx] {
		startIdx++
	}
	for endIdx > m && isBlank[endIdx] {
		endIdx--
	}
	return startIdx, endIdx
}

// FormatMatchNote formats the human-readable summary note for FormatHashlines.
func FormatMatchNote(totalLines int, res SearchResult, query string) string {
	if query == "" {
		return fmt.Sprintf("lines 1-%d", totalLines)
	}
	return fmt.Sprintf("lines 1-%d, %d matches for %q [%s]", totalLines, len(res.MatchLines), query, res.TierUsed)
}
