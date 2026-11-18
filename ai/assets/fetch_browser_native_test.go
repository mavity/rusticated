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
` + browserAssetBootstrapJS + `
// Match /dev/eval: await the result, then coerce it to a string.
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
