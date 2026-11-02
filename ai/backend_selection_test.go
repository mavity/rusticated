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
