package perftest

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ai"
	"ai/assets"
)

const performanceVectorSize = 4915

func TestRealEmbedderPerformanceAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	embedder := openPerformanceEmbedder(t, ctx)
	defer embedder.Close()

	if !embedder.SupportsBatch() {
		t.Fatalf("real batch acceptance requires LiteRT batch inference; backend %q only supports sequential fallback", embedder.Backend())
	}
	t.Logf("embedder=%T identity=%s native_batch=true batch_api=litert_lm_embedding_engine_compute_embedding_batch",
		embedder, embedder.CacheIdentity())

	t.Run("semantic-grouping", func(t *testing.T) {
		terms := []string{"duck", "chicken", "iron", "copper"}
		vectors := make([][]float32, len(terms))
		dimension := 0
		for i, term := range terms {
			started := time.Now()
			vector, err := ai.EmbedWithContext(ctx, embedder, []ai.PromptPart{{Role: ai.RoleUser, Text: term}})
			duration := time.Since(started)
			t.Logf("term=%q duration=%s dimensions=%d", term, duration, len(vector))
			if err != nil {
				t.Fatalf("embed %q: %v", term, err)
			}
			if err := validatePerformanceVector(vector, dimension); err != nil {
				t.Fatalf("term %q: %v", term, err)
			}
			if dimension == 0 {
				dimension = len(vector)
			}
			vectors[i] = vector
		}

		t.Log("cosine-similarity matrix (rows/columns: duck, chicken, iron, copper):")
		for i := range terms {
			row := make([]float64, len(terms))
			for j := range terms {
				row[j] = performanceCosine(vectors[i], vectors[j])
			}
			t.Logf("%q: %.6f %.6f %.6f %.6f", terms[i], row[0], row[1], row[2], row[3])
		}
		if nearest := nearestOther(vectors, 0); nearest < 0 || nearest != 1 {
			if nearest < 0 {
				t.Fatal("could not determine duck's nearest other term")
			}
			t.Fatalf("duck's closest other term is %q, want chicken", terms[nearest])
		}
		if nearest := nearestOther(vectors, 1); nearest < 0 || nearest != 0 {
			if nearest < 0 {
				t.Fatal("could not determine chicken's nearest other term")
			}
			t.Fatalf("chicken's closest other term is %q, want duck", terms[nearest])
		}
		if nearest := nearestOther(vectors, 2); nearest < 0 || nearest != 3 {
			if nearest < 0 {
				t.Fatal("could not determine iron's nearest other term")
			}
			t.Fatalf("iron's closest other term is %q, want copper", terms[nearest])
		}
		if nearest := nearestOther(vectors, 3); nearest < 0 || nearest != 2 {
			if nearest < 0 {
				t.Fatal("could not determine copper's nearest other term")
			}
			t.Fatalf("copper's closest other term is %q, want iron", terms[nearest])
		}
	})

	t.Run("warm-single-input-latency", func(t *testing.T) {
		terms := []string{"London", "Paris", "Tokyo", "Lagos"}
		for i, term := range terms {
			started := time.Now()
			vector, err := ai.EmbedWithContext(ctx, embedder, []ai.PromptPart{{Role: ai.RoleUser, Text: term}})
			duration := time.Since(started)
			t.Logf("term=%q duration=%s dimensions=%d", term, duration, len(vector))
			if err != nil {
				t.Fatalf("embed %q: %v", term, err)
			}
			if err := validatePerformanceVector(vector, 0); err != nil {
				t.Fatalf("term %q: %v", term, err)
			}
			if i > 0 && duration >= time.Second {
				t.Errorf("warm embedding %q took %s, want under 1s", term, duration)
			}
		}
	})

	t.Run("real-batches", func(t *testing.T) {
		batches := [][]string{
			{"dollar", "euro", "yen", "pound"},
			{"red", "blue", "green", "yellow"},
		}
		dimension := 0
		for batchIndex, terms := range batches {
			parts := make([]ai.PromptPart, len(terms))
			for i, term := range terms {
				parts[i] = ai.PromptPart{Role: ai.RoleModel, Text: term}
			}
			started := time.Now()
			vectors, err := ai.EmbedBatchWithContext(ctx, embedder, parts)
			duration := time.Since(started)
			t.Logf("batch=%d terms=%v duration=%s vector_count=%d", batchIndex+1, terms, duration, len(vectors))
			if err != nil {
				t.Fatalf("embed batch %d: %v", batchIndex+1, err)
			}
			if len(vectors) != len(terms) {
				t.Fatalf("batch %d returned %d vectors, want %d", batchIndex+1, len(vectors), len(terms))
			}
			for i, vector := range vectors {
				if err := validatePerformanceVector(vector, dimension); err != nil {
					t.Fatalf("batch %d term %q: %v", batchIndex+1, terms[i], err)
				}
				if dimension == 0 {
					dimension = len(vector)
				}
			}
			if batchIndex == 1 && duration >= 3*time.Second {
				t.Errorf("warm color batch took %s, want under 3s", duration)
			}
		}
	})

	t.Run("long-input-latency", func(t *testing.T) {
		source, err := os.ReadFile(filepath.Join("..", "backend.go"))
		if err != nil {
			t.Fatalf("read Go source fixture: %v", err)
		}
		if len(source) < 2*performanceVectorSize {
			t.Fatalf("Go source fixture has %d bytes, need at least %d", len(source), 2*performanceVectorSize)
		}
		inputs := [][]byte{
			source[:performanceVectorSize],
			source[performanceVectorSize : 2*performanceVectorSize],
		}
		for i, input := range inputs {
			if len(input) != performanceVectorSize {
				t.Fatalf("input %d has %d bytes, want %d", i+1, len(input), performanceVectorSize)
			}
			started := time.Now()
			vector, err := ai.EmbedWithContext(ctx, embedder, []ai.PromptPart{{Role: ai.RoleModel, Text: string(input)}})
			duration := time.Since(started)
			t.Logf("source_slice=%d bytes=%d duration=%s dimensions=%d", i+1, len(input), duration, len(vector))
			if err != nil {
				t.Fatalf("embed source slice %d: %v", i+1, err)
			}
			if err := validatePerformanceVector(vector, 0); err != nil {
				t.Fatalf("source slice %d: %v", i+1, err)
			}
			if i == 1 && duration >= 3*time.Second {
				t.Errorf("warm 4,915-byte source embedding took %s, want under 3s", duration)
			}
		}
	})
}

func openPerformanceEmbedder(t *testing.T, ctx context.Context) *ai.Embedder {
	t.Helper()
	cacheDir, err := assets.DefaultCacheDir()
	if err != nil {
		t.Fatalf("resolve embedding cache: %v", err)
	}
	runtimeRemaining, err := assets.RuntimeDownloadSize(ctx, cacheDir)
	if err != nil {
		t.Fatalf("inspect cached runtime: %v", err)
	}
	modelRemaining, err := assets.EmbeddingModelDownloadSize(cacheDir)
	if err != nil {
		t.Fatalf("inspect cached embedding model: %v", err)
	}
	if runtimeRemaining != 0 || modelRemaining != 0 {
		t.Fatalf("performance acceptance requires cached real embedder assets (runtime remaining=%d, model remaining=%d)", runtimeRemaining, modelRemaining)
	}
	libPath, err := assets.EnsureRuntime(ctx, assets.FetchOptions{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("load cached LiteRT runtime: %v", err)
	}
	modelPath, err := assets.EnsureDefaultEmbeddingModel(ctx, assets.FetchOptions{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("load cached embedding model: %v", err)
	}
	embedder, err := ai.NewEmbedder(libPath, modelPath, "auto")
	if err != nil {
		t.Fatalf("initialize real embedder: %v", err)
	}
	t.Logf("real embedder model=%s requested_backend=auto selected_backend=%s LITERTLM_BACKEND=%q",
		embedder.CacheIdentity(), embedder.Backend(), os.Getenv("LITERTLM_BACKEND"))
	return embedder
}

func validatePerformanceVector(vector []float32, expectedDimension int) error {
	if len(vector) == 0 {
		return fmt.Errorf("empty vector")
	}
	if expectedDimension > 0 && len(vector) != expectedDimension {
		return fmt.Errorf("dimension %d, want %d", len(vector), expectedDimension)
	}
	for i, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("non-finite component at index %d: %v", i, value)
		}
	}
	return nil
}

func performanceCosine(left, right []float32) float64 {
	if len(left) != len(right) || len(left) == 0 {
		return math.NaN()
	}
	var dot, leftNorm, rightNorm float64
	for i := range left {
		dot += float64(left[i]) * float64(right[i])
		leftNorm += float64(left[i]) * float64(left[i])
		rightNorm += float64(right[i]) * float64(right[i])
	}
	if leftNorm == 0 || rightNorm == 0 {
		return math.NaN()
	}
	return dot / math.Sqrt(leftNorm*rightNorm)
}

func nearestOther(vectors [][]float32, index int) int {
	nearest := -1
	best := math.Inf(-1)
	for candidate := range vectors {
		if candidate == index {
			continue
		}
		score := performanceCosine(vectors[index], vectors[candidate])
		if score > best {
			best = score
			nearest = candidate
		}
	}
	return nearest
}
