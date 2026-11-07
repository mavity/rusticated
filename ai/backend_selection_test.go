package ai

import (
	"reflect"
	"testing"
)

func TestResolveLMBackendCandidatesPrefersNPUThenGPU(t *testing.T) {
	t.Setenv("LITERTLM_BACKEND", "")

	for _, test := range []struct {
		backend string
		want    []string
	}{
		{backend: "npu", want: []string{"npu", "gpu", "cpu"}},
		{backend: "auto", want: []string{"npu", "gpu", "cpu"}},
		{backend: "gpu", want: []string{"gpu", "cpu"}},
	} {
		t.Run(test.backend, func(t *testing.T) {
			if got := resolveLMBackendCandidates(test.backend); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("resolveLMBackendCandidates(%q) = %v, want %v", test.backend, got, test.want)
			}
		})
	}
}

func TestResolveEmbedderBackendCandidatesPrefersGPUByDefault(t *testing.T) {
	t.Setenv("LITERTLM_BACKEND", "")
	if got, want := resolveEmbedderBackendCandidates("auto"), []string{"gpu", "npu", "cpu"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveEmbedderBackendCandidates(auto) = %v, want %v", got, want)
	}
	if got, want := resolveEmbedderBackendCandidates("npu"), []string{"npu", "gpu", "cpu"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveEmbedderBackendCandidates(npu) = %v, want %v", got, want)
	}
	t.Setenv("LITERTLM_BACKEND", "cpu")
	if got, want := resolveEmbedderBackendCandidates("auto"), []string{"cpu"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveEmbedderBackendCandidates(auto with cpu override) = %v, want %v", got, want)
	}
}

func TestEmbedderBackendReportsSelectedBackend(t *testing.T) {
	if got := (*Embedder)(nil).Backend(); got != "" {
		t.Fatalf("nil embedder Backend() = %q, want empty", got)
	}
	if got := (&Embedder{}).Backend(); got != "" {
		t.Fatalf("uninitialized embedder Backend() = %q, want empty", got)
	}
	embedder := &Embedder{impl: &embedderImpl{backendName: "gpu"}}
	if got := embedder.Backend(); got != "gpu" {
		t.Fatalf("embedder Backend() = %q, want gpu", got)
	}
}
