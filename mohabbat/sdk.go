package mohabbat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveGoroot finds the correct GOROOT using a priority chain that does not
// require the `go` binary to be in PATH — critical for vegetable (WASM brain) mode.
// Go 1.21+ toolchain forwarding sets GOROOT to a path inside GOMODCACHE when
// the parent `go` binary delegates to a newer toolchain. Overlay replacements
// beneath GOMODCACHE are forbidden since Go 1.26, so any candidate pointing
// there Must be rejected.
// Build GOMODCACHE prefix for rejecting toolchain-forwarded roots.
func resolveGoroot(ws string) (string, string, error) {
	// Build GOMODCACHE prefix for rejecting toolchain-forwarded roots.
	gomodcache := os.Getenv("GOMODCACHE")
	if gomodcache == "" {
		if h, err := os.UserHomeDir(); err == nil {
			gomodcache = filepath.Join(h, "go", "pkg", "mod")
		}
	}
	isUnderModCache := func(p string) bool {
		if gomodcache == "" {
			return false
		}
		return strings.HasPrefix(filepath.ToSlash(p), filepath.ToSlash(gomodcache))
	}

	// Determine go version from go.mod — needed for SDK lookup.
	ver := ""
	goModPath := filepath.Join(ws, "mohabbat", "go.mod")
	if f, err := os.Open(goModPath); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "go ") {
				ver = strings.TrimSpace(strings.TrimPrefix(line, "go "))
				break
			}
		}
		f.Close()
	}

	// Priority 1: $HOME/sdk/go{ver} — the cleanest source, not inside GOMODCACHE.
	if ver != "" {
		homes := uniqueStrings([]string{
			func() string { h, _ := os.UserHomeDir(); return h }(),
			os.Getenv("USERPROFILE"),
			os.Getenv("HOME"),
		})
		for _, home := range homes {
			if home == "" {
				continue
			}
			sdkPath := filepath.Join(home, "sdk", "go"+ver)
			if _, err := os.Stat(sdkPath); err == nil {
				return sdkPath, "sdk v" + ver, nil
			}
		}
	}

	// Priority 2: GOROOT env var — but reject GOMODCACHE paths (toolchain forwarding).
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		if !isUnderModCache(goroot) {
			if _, err := os.Stat(goroot); err == nil {
				return goroot, "env", nil
			}
		}
	}

	if ver != "" {
		// Priority 3: run `go{ver} env GOROOT`
		if out, err := exec.Command("go"+ver, "env", "GOROOT").Output(); err == nil {
			p := strings.TrimSpace(string(out))
			if !isUnderModCache(p) {
				if _, err := os.Stat(p); err == nil {
					return p, "go" + ver + " env GOROOT", nil
				}
			}
		}
	}

	// Priority 4: `go env GOROOT`
	if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
		p := strings.TrimSpace(string(out))
		if !isUnderModCache(p) {
			if _, err := os.Stat(p); err == nil {
				return p, "go env GOROOT", nil
			}
		}
	}
	return "", "", fmt.Errorf("could not find a usable GOROOT (all candidates were inside GOMODCACHE or missing)")
}

// gorootFromOverlay reads target/overlay.json and extracts GOROOT from the
// source paths embedded in it. The keys are absolute paths of the form
// {GOROOT}/src/runtime/os_wasip1.go — so GOROOT is everything before /src/.
// This requires no subprocess and works inside the WASM vegetable sandbox.
func gorootFromOverlay(ws string) string {
	overlayPath := filepath.Join(ws, "target", "overlay.json")
	data, err := os.ReadFile(overlayPath)
	if err != nil {
		return ""
	}
	var v struct {
		Replace map[string]string `json:"Replace"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return ""
	}
	// Determine GOMODCACHE so we can reject candidates inside it.
	// Go 1.26+ forbids overlay replacements for files under GOMODCACHE.
	gomodcache := os.Getenv("GOMODCACHE")
	if gomodcache == "" {
		gomodcache = filepath.Join(func() string { h, _ := os.UserHomeDir(); return h }(), "go", "pkg", "mod")
	}
	gomodcache = filepath.ToSlash(gomodcache)
	for src := range v.Replace {
		// Normalize to forward slashes for consistent searching.
		srcFwd := filepath.ToSlash(src)
		if idx := strings.Index(srcFwd, "/src/runtime/"); idx >= 0 {
			candidate := filepath.FromSlash(srcFwd[:idx])
			candidateFwd := filepath.ToSlash(candidate)
			if strings.HasPrefix(candidateFwd, gomodcache) {
				continue
			}
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}

// goBinFromRoot returns the absolute path to the `go` binary inside goroot.
// Falls back to "go" (PATH lookup) if the binary doesn't exist there.
// Probes both "go.exe" and "go" because runtime.GOOS is "wasip1" when
// this code runs as the WASM brain, not the actual host OS.
func goBinFromRoot(goroot string) string {
	if goroot == "" {
		return "go"
	}
	// Try .exe first (Windows host), then no extension (Linux/macOS host).
	for _, ext := range []string{".exe", ""} {
		bin := filepath.Join(goroot, "bin", "go"+ext)
		if _, err := os.Stat(bin); err == nil {
			return bin
		}
	}
	return "go"
}

