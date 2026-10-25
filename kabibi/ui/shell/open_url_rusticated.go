//go:build wasip1

package shell

import (
	"fmt"
	"strconv"
	"strings"

	"ai"
)

func openExternalURL(target string) error {
	script := `(() => {
  if (typeof window === 'undefined' || typeof window.open !== 'function') {
    throw new Error('browser window.open is unavailable');
  }
  const opened = window.open(` + strconv.Quote(target) + `, '_blank');
  if (!opened) return 'blocked';
  opened.opener = null;
  return 'opened';
})()`
	result, err := ai.EvalJS(script)
	if err != nil {
		return fmt.Errorf("open browser tab: %w", err)
	}
	if strings.TrimSpace(result) == "blocked" {
		return fmt.Errorf("browser blocked the new tab; allow pop-ups for this page")
	}
	return nil
}
