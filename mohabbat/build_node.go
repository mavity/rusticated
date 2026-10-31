package mohabbat

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
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
	if err := writeFileIfChanged(outHostPath, hostBytes); err != nil {
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

	if err := writeFileIfChanged(outStarterPath, combined); err != nil {
		return fmt.Errorf("write brot-node.js: %w", err)
	}
	return buildNodePtyArtifacts(ws, buildDir, verbose)
}

func buildNodePtyArtifacts(ws, buildDir string, verbose bool) error {
	return buildNodePtyArtifactsFor(ws, buildDir, verbose, "")
}

func nodePtyArtifactName(platform, arch string) string {
	switch {
	case platform == "linux" && arch == "x64":
		return "linux-x64"
	case platform == "linux" && arch == "arm64":
		return "linux-arm64"
	case platform == "darwin" && arch == "arm64":
		return "darwin-arm64"
	case platform == "win32" && arch == "x64":
		return "win32-x64"
	case platform == "win32" && arch == "arm64":
		return "win32-arm64"
	default:
		return ""
	}
}

func buildNodePtyArtifactsFor(ws, buildDir string, verbose bool, artifactName string) error {
	brotStubsDir := filepath.Join(ws, "target", "brot-stubs")
	if err := ensureBrotStubs(brotStubsDir); err != nil {
		return err
	}
	darwinStubsDir := filepath.Join(ws, "target", "darwin-stubs")
	if err := ensureDarwinStubs(darwinStubsDir); err != nil {
		return err
	}

	type target struct {
		name      string
		triple    string
		source    string
		rustflags []string
		sdkRoot   string
	}
	targets := []target{
		{
			name:   "linux-x64",
			triple: "x86_64-unknown-linux-gnu",
			source: "libnodepty.so",
			rustflags: []string{
				"'-C'", "'linker=rust-lld'",
				"'-C'", "'linker-flavor=ld.lld'",
				"'-C'", "'link-arg=--allow-shlib-undefined'",
			},
		},
		{
			name:   "linux-arm64",
			triple: "aarch64-unknown-linux-gnu",
			source: "libnodepty.so",
			rustflags: []string{
				"'-C'", "'linker=rust-lld'",
				"'-C'", "'linker-flavor=ld.lld'",
				"'-C'", "'link-arg=--allow-shlib-undefined'",
			},
		},
		{
			name:   "darwin-arm64",
			triple: "aarch64-apple-darwin",
			source: "libnodepty.dylib",
			rustflags: []string{
				"'-C'", "'linker=rust-lld'",
				"'-C'", "'linker-flavor=darwin-lld'",
				"'-Z'", "'unstable-options'",
				"'-C'", "'link-arg=-undefined'",
				"'-C'", "'link-arg=dynamic_lookup'",
				"'-C'", fmt.Sprintf("'link-arg=-L%s'", filepath.ToSlash(darwinStubsDir)),
			},
			sdkRoot: darwinStubsDir,
		},
		{
			name:   "win32-x64",
			triple: "x86_64-pc-windows-gnullvm",
			source: "nodepty.dll",
			rustflags: []string{
				"'-C'", "'linker=rust-lld'",
				"'-C'", "'linker-flavor=ld.lld'",
				"'-C'", fmt.Sprintf("'link-arg=-L%s'", filepath.ToSlash(brotStubsDir)),
			},
		},
		{
			name:   "win32-arm64",
			triple: "aarch64-pc-windows-gnullvm",
			source: "nodepty.dll",
			rustflags: []string{
				"'-C'", "'linker=rust-lld'",
				"'-C'", "'linker-flavor=ld.lld'",
				"'-C'", fmt.Sprintf("'link-arg=-L%s'", filepath.ToSlash(brotStubsDir)),
			},
		},
	}

	for _, target := range targets {
		if artifactName != "" && target.name != artifactName {
			continue
		}
		args := []string{
			"build",
			"--manifest-path", filepath.Join(ws, "mohabbat", "nodepty", "Cargo.toml"),
			"--release",
			"--target", target.triple,
			"--config", fmt.Sprintf("target.%s.rustflags=[%s]", target.triple, strings.Join(target.rustflags, ", ")),
		}
		cmd := exec.Command("cargo", args...)
		cmd.Dir = ws
		cmd.Env = os.Environ()
		if target.sdkRoot != "" {
			cmd.Env = upsertEnv(cmd.Env, "SDKROOT", target.sdkRoot)
		}
		if err := runCmd(cmd, fmt.Sprintf("cargo build nodepty for %s", target.name), verbose); err != nil {
			return fmt.Errorf("build nodepty for %s: %w", target.name, err)
		}

		source := filepath.Join(ws, "target", target.triple, "release", target.source)
		data, err := os.ReadFile(source)
		if err != nil {
			return fmt.Errorf("read nodepty for %s: %w", target.name, err)
		}
		destination := filepath.Join(buildDir, "nodepty-"+target.name+".node")
		if err := writeFileIfChangedMode(destination, data, 0o755); err != nil {
			return fmt.Errorf("write nodepty for %s: %w", target.name, err)
		}
	}
	return nil
}
