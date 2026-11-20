//go:build !wasip1

package assets

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEstimateBrowserEmbeddingDownload(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required to exercise the browser asset adapter")
	}

	for _, tt := range []struct {
		name string
		hit  bool
		want int64
	}{
		{name: "uncached", want: DefaultEmbeddingModelSize},
		{name: "cached from mirror", hit: true, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				browserModelSizesMu.Lock()
				delete(browserModelSizes, DefaultEmbeddingModelURL)
				browserModelSizesMu.Unlock()
			})
			eval := func(code string) (string, error) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, node, "-")
				cmd.Stdin = strings.NewReader(`
globalThis.caches = {
  open: async () => ({
    match: async (url) => {
      if (` + strconv.FormatBool(tt.hit) + ` &&
          url === 'https://assets.kabi.bi/embeddinggemma-2-text-270m.litertlm') {
        return { headers: new Map([['content-length', '` + strconv.Itoa(DefaultEmbeddingModelSize) + `']]) };
      }
    }
  })
};
` + browserAssetBootstrapJS + `// Match /dev/eval: await the result, then coerce it to a string.
(async () => {
  process.stdout.write(String(await (0, eval)(` + strconv.Quote(code) + `) ?? ''));
})().catch(err => {
  console.error(err);
  process.exitCode = 1;
});
`)
				out, err := cmd.CombinedOutput()
				if err != nil {
					return "", fmt.Errorf("run browser adapter: %w: %s", err, out)
				}
				return string(out), nil
			}
			got, err := estimateBrowserModelDownload(eval, DefaultEmbeddingModelURL, DefaultEmbeddingModelSize)
			if err != nil || got != tt.want {
				t.Fatalf("embedding estimate = %d, %v; want %d, nil", got, err, tt.want)
			}
		})
	}
}

func TestEnsureModelBrowserChecksCacheBeforeStartingDownload(t *testing.T) {
	for _, tt := range []struct {
		name       string
		cached     bool
		wantCalls  int
		wantEvents int
	}{
		{name: "cache hit", cached: true, wantCalls: 1},
		{name: "cache miss", wantCalls: 2, wantEvents: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				browserModelSizesMu.Lock()
				delete(browserModelSizes, DefaultEmbeddingModelURL)
				browserModelSizesMu.Unlock()
			})

			var calls []string
			var progress []string
			eval := func(code string) (string, error) {
				calls = append(calls, code)
				switch {
				case strings.Contains(code, ".checkCached("):
					time.Sleep(10 * time.Millisecond)
					if tt.cached {
						return `{"cached":true,"size":42}`, nil
					}
					return `{"cached":false,"size":0}`, nil
				case strings.Contains(code, ".startDownload("):
					return `{"done":true,"cached":false,"received":10,"total":10,"percent":100}`, nil
				default:
					return "", fmt.Errorf("unexpected browser eval: %s", code)
				}
			}

			got, err := ensureModelBrowserWithEval(
				context.Background(),
				DefaultEmbeddingModelURL,
				FetchOptions{OnProgress: func(_, details string, percent int) {
					progress = append(progress, fmt.Sprintf("%s:%d", details, percent))
				}},
				eval,
			)
			if err != nil {
				t.Fatalf("ensureModelBrowserWithEval() error = %v", err)
			}
			if got != DefaultEmbeddingModelURL {
				t.Fatalf("ensureModelBrowserWithEval() = %q, want %q", got, DefaultEmbeddingModelURL)
			}
			if len(calls) != tt.wantCalls {
				t.Fatalf("browser eval calls = %d, want %d", len(calls), tt.wantCalls)
			}
			if !strings.Contains(calls[0], ".checkCached(") {
				t.Fatalf("first browser eval = %q, want cache check", calls[0])
			}
			if tt.cached {
				if len(progress) != 0 {
					t.Fatalf("cache-hit progress = %v, want none", progress)
				}
			} else {
				if !strings.Contains(calls[1], ".startDownload(") {
					t.Fatalf("second browser eval = %q, want download start after cache miss", calls[1])
				}
				if len(progress) != tt.wantEvents {
					t.Fatalf("cache-miss progress = %v, want %d event", progress, tt.wantEvents)
				}
			}
		})
	}
}
