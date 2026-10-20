//go:build wasip1

package ai

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// EvalJS evaluates jsCode in the browser host's global scope, parking the calling
// Go goroutine if jsCode returns a Promise, and returns the resolved string.
func EvalJS(jsCode string) (string, error) {
	if jsCode == "" {
		return "", nil
	}
	f, err := os.OpenFile("/dev/eval", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("open /dev/eval: %w", err)
	}
	defer f.Close()

	if _, err := f.Write([]byte(jsCode)); err != nil {
		return "", fmt.Errorf("write /dev/eval: %w", err)
	}

	resp, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read /dev/eval: %w", err)
	}
	if len(resp) == 0 {
		return "", nil
	}
	if resp[0] != 0 {
		return "", errors.New(string(resp[1:]))
	}
	return string(resp[1:]), nil
}
