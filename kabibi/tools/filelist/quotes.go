package filelist

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"ai"
)

const maxQuoteChunkBytes = 1500

type quoteLine struct {
	source string
	input  string
}

type quoteChunk struct {
	lines []quoteLine
	input string
}

func enrichSemanticQuotes(ctx context.Context, entries []fileEntry, queryVector []float32, embedder *concurrentEmbedder, progress *progressTracker, quoteLimit int) error {
	if embedder == nil || len(queryVector) == 0 {
		return nil
	}
	for index := 0; index < min(len(entries), quoteLimit); index++ {
		if !entries[index].hasScore {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		stats := progress.current()
		if err := progress.update("quote", entries[index].relPath, stats.Viewed, stats.Total); err != nil {
			return err
		}
		quote, err := relevantQuote(ctx, entries[index], queryVector, embedder, progress)
		if err != nil {
			return fmt.Errorf("find relevant quote in %q: %w", entries[index].relPath, err)
		}
		entries[index].quote = quote
		if err := progress.updateQuote(entries[index].relPath, quote); err != nil {
			return err
		}
	}
	return nil
}

func relevantQuote(ctx context.Context, entry fileEntry, queryVector []float32, embedder *concurrentEmbedder, progress *progressTracker) (string, error) {
	if entry.isDir {
		return "", nil
	}
	file, err := os.Open(entry.absPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, maxQuoteChunkBytes*2)
	prefix, peekErr := reader.Peek(512)
	if peekErr != nil && peekErr != io.EOF {
		return "", peekErr
	}
	if isBinaryBlob(prefix) {
		return "", nil
	}

	var pending []quoteChunk
	var bestLines []quoteLine
	bestScore := -1.0
	processPending := func() error {
		if len(pending) == 0 {
			return nil
		}
		var chunks []quoteChunk
		var inputs []string
		for _, chunk := range pending {
			if len(entry.scoreVector) > 0 && entry.scoreText == chunk.input {
				score, err := quoteSimilarity(queryVector, entry.scoreVector)
				if err != nil {
					return fmt.Errorf("reuse scored quote chunk: %w", err)
				}
				if score > bestScore {
					bestLines = append([]quoteLine(nil), chunk.lines...)
					bestScore = score
				}
				continue
			}
			chunks = append(chunks, chunk)
			inputs = append(inputs, chunk.input)
		}
		if len(inputs) > 0 {
			embedStarted := time.Now()
			progress.logVerbose("embedding start phase=quote path=%q chunks=%d", entry.relPath, len(inputs))
			vectors, err := embedQuoteInputs(ctx, embedder, inputs)
			if err != nil {
				progress.logVerbose("embedding failed phase=quote path=%q chunks=%d duration=%s error=%q",
					entry.relPath, len(inputs), time.Since(embedStarted), err)
				return err
			}
			progress.logVerbose("embedding finish phase=quote path=%q chunks=%d duration=%s",
				entry.relPath, len(inputs), time.Since(embedStarted))
			for i, vector := range vectors {
				score, err := quoteSimilarity(queryVector, vector)
				if err != nil {
					return fmt.Errorf("score quote chunk: %w", err)
				}
				if score > bestScore {
					bestLines = append([]quoteLine(nil), chunks[i].lines...)
					bestScore = score
				}
			}
		}
		pending = pending[:0]
		return nil
	}
	if err := scanQuoteChunks(ctx, reader, func(chunk quoteChunk) error {
		pending = append(pending, chunk)
		if len(pending) == 16 {
			return processPending()
		}
		return nil
	}); err != nil {
		return "", err
	}
	if err := processPending(); err != nil {
		return "", err
	}
	if len(bestLines) == 0 {
		return "", nil
	}
	for len(bestLines) > 1 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		middle := len(bestLines) / 2
		left, right := bestLines[:middle], bestLines[middle:]
		embedStarted := time.Now()
		progress.logVerbose("embedding start phase=quote-bisect path=%q chunks=2", entry.relPath)
		vectors, err := embedQuoteInputs(ctx, embedder, []string{joinQuoteLines(left), joinQuoteLines(right)})
		if err != nil {
			progress.logVerbose("embedding failed phase=quote-bisect path=%q duration=%s error=%q",
				entry.relPath, time.Since(embedStarted), err)
			return "", err
		}
		progress.logVerbose("embedding finish phase=quote-bisect path=%q duration=%s",
			entry.relPath, time.Since(embedStarted))
		leftScore, err := quoteSimilarity(queryVector, vectors[0])
		if err != nil {
			return "", fmt.Errorf("score left bisection: %w", err)
		}
		rightScore, err := quoteSimilarity(queryVector, vectors[1])
		if err != nil {
			return "", fmt.Errorf("score right bisection: %w", err)
		}
		if leftScore >= rightScore {
			bestLines = left
		} else {
			bestLines = right
		}
	}
	return strings.TrimSpace(bestLines[0].source), nil
}

func quoteSimilarity(query, candidate []float32) (float64, error) {
	if err := validateQuoteVector(query, candidate); err != nil {
		return 0, err
	}
	score := cosineSim(query, candidate)
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0, fmt.Errorf("quote similarity is not finite")
	}
	return score, nil
}

func scanQuoteChunks(ctx context.Context, reader *bufio.Reader, yield func(quoteChunk) error) error {
	var current quoteChunk
	currentBytes := 0
	flush := func() error {
		if len(current.lines) == 0 {
			return nil
		}
		current.input = joinQuoteLines(current.lines)
		if err := yield(current); err != nil {
			return err
		}
		current = quoteChunk{}
		currentBytes = 0
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, ok, err := readBoundedQuoteLine(ctx, reader)
		if ok && strings.TrimSpace(line) != "" {
			input := truncateUTF8(line, maxQuoteChunkBytes)
			lineBytes := len(input) + 1
			if len(current.lines) > 0 && currentBytes+lineBytes > maxQuoteChunkBytes {
				if flushErr := flush(); flushErr != nil {
					return flushErr
				}
			}
			current.lines = append(current.lines, quoteLine{source: line, input: input})
			currentBytes += lineBytes
		}
		if err == io.EOF {
			return flush()
		}
		if err != nil {
			return err
		}
	}
}

func readBoundedQuoteLine(ctx context.Context, reader *bufio.Reader) (string, bool, error) {
	var line []byte
	for {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		fragment, prefix, err := reader.ReadLine()
		remaining := maxQuoteChunkBytes - len(line)
		if remaining > 0 {
			line = append(line, fragment[:min(len(fragment), remaining)]...)
		}
		if err == io.EOF {
			if len(line) > 0 {
				return truncateUTF8(string(line), maxQuoteChunkBytes), true, nil
			}
			return "", false, io.EOF
		}
		if err != nil {
			return "", false, err
		}
		if !prefix {
			return truncateUTF8(string(line), maxQuoteChunkBytes), true, nil
		}
	}
}

func validateQuoteVector(query, candidate []float32) error {
	if len(candidate) == 0 {
		return fmt.Errorf("quote embedding is empty")
	}
	if len(candidate) != len(query) {
		return fmt.Errorf("quote embedding has %d dimensions; query embedding has %d", len(candidate), len(query))
	}
	return nil
}

func embedQuoteInputs(ctx context.Context, embedder *concurrentEmbedder, inputs []string) ([][]float32, error) {
	parts := make([]ai.PromptPart, len(inputs))
	for i, input := range inputs {
		parts[i] = ai.PromptPart{Role: ai.RoleModel, Text: input}
	}
	return embedScheduledBatch(ctx, embedder, parts, true)
}

func splitQuoteChunks(content string) []quoteChunk {
	var chunks []quoteChunk
	reader := bufio.NewReader(strings.NewReader(content))
	_ = scanQuoteChunks(context.Background(), reader, func(chunk quoteChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	return chunks
}

func joinQuoteLines(lines []quoteLine) string {
	inputs := make([]string, len(lines))
	for i, line := range lines {
		inputs[i] = line.input
	}
	return strings.Join(inputs, "\n")
}

func truncateUTF8(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
