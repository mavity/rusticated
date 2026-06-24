package mohabbat

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed brot/js/node/starter.js
var jsStarterTemplate string

func buildNodeSlot(ws, buildDir string, verbose bool) error {
	// Read JS Host source
	jsHostPath := filepath.Join(ws, "mohabbat", "washmhost", "js", "node", "index.js")
	hostBytes, err := os.ReadFile(jsHostPath)
	if err != nil {
		return fmt.Errorf("read washmhost js: %w", err)
	}

	vStr := "false"
	if verbose {
		vStr = "true"
	}
	hostBytes = bytes.ReplaceAll(hostBytes, []byte("// {{VERBOSE}}"), []byte(vStr))

	// Write washmhost-node.js to buildDir
	outHostPath := filepath.Join(buildDir, "washmhost-node.js")
	if err := os.WriteFile(outHostPath, hostBytes, 0644); err != nil {
		return fmt.Errorf("write washmhost-node.js: %w", err)
	}

	// Read brotli.wasm
	wasmPath := filepath.Join(buildDir, "brotli.wasm")
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return fmt.Errorf("read brotli.wasm: %w", err)
	}

	outStarterPath := filepath.Join(buildDir, "brot-node.js")
	starterBytes := []byte(strings.ReplaceAll(jsStarterTemplate, "{{VERBOSE}}", vStr))

	// We must separate the text length to communicate it to package.go
	// But package.go constructs `slot` from a fixed array.
	// We can't easily mutate `slots` slice fields in parallel.
	// Let's pass the textLen back or store it.
	combined := append(starterBytes, wasmBytes...)

	if err := os.WriteFile(outStarterPath, combined, 0644); err != nil {
		return fmt.Errorf("write brot-node.js: %w", err)
	}
	return nil
}
