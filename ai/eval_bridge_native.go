//go:build !wasip1

package ai

import "errors"

// EvalJS is not supported outside wasip1 / browser environments.
func EvalJS(jsCode string) (string, error) {
	return "", errors.New("EvalJS is only supported in browser wasm runtime")
}
