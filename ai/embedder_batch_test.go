package ai

import (
	"context"
	"testing"
)

type recordingBatchEmbedderBackend struct {
	batchCalls  int
	singleCalls int
}

func (b *recordingBatchEmbedderBackend) ComputeEmbedding([]embedInput) ([]float32, error) {
	b.singleCalls++
	return []float32{0}, nil
}

func (b *recordingBatchEmbedderBackend) ComputeEmbeddingsBatch(inputs [][]embedInput) ([][]float32, error) {
	b.batchCalls++
	vectors := make([][]float32, len(inputs))
	for i := range inputs {
		vectors[i] = []float32{float32(i + 1)}
	}
	return vectors, nil
}

func (*recordingBatchEmbedderBackend) Close() error { return nil }

func TestEmbedderBatchUsesBackendBatchOperation(t *testing.T) {
	backend := &recordingBatchEmbedderBackend{}
	embedder := &Embedder{
		impl: &embedderImpl{backend: backend},
	}
	if !embedder.SupportsBatch() {
		t.Fatal("SupportsBatch() = false, want true")
	}

	vectors, err := embedder.EmbedBatchContext(context.Background(), []PromptPart{
		{Role: RoleUser, Text: "first"},
		{Role: RoleUser, Text: "second"},
	})
	if err != nil {
		t.Fatalf("EmbedBatchContext() error = %v", err)
	}
	if backend.batchCalls != 1 || backend.singleCalls != 0 {
		t.Fatalf("backend calls = batch:%d single:%d, want 1/0", backend.batchCalls, backend.singleCalls)
	}
	if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][0] != 2 {
		t.Fatalf("EmbedBatchContext() = %v, want ordered vectors [[1] [2]]", vectors)
	}
}
