package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ensureGoBrainOverlay(repoRoot, workDir string) (string, error) {
	existing := filepath.Join(repoRoot, "target", "overlay.json")
	if _, err := os.Stat(existing); err == nil {
		return existing, nil
	}

	gorootOut, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOROOT: %w", err)
	}
	goroot := strings.TrimSpace(string(gorootOut))
	overlayDir := filepath.Join(repoRoot, "rusticated-go")
	genDir := filepath.Join(workDir, "go-overlay")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		return "", err
	}

	canon := func(p string) string {
		abs, err := filepath.EvalSymlinks(p)
		if err != nil {
			abs = p
		}
		return filepath.ToSlash(abs)
	}

	// Patch internal/filepathlite/path_unix.go to avoid redeclaring Separator/ListSeparator
	genLiteUnix := filepath.Join(genDir, "internal_filepathlite_path_unix.go")
	liteUnixSrc := `//go:build unix || (js && wasm) || wasip1

package filepathlite

func IsPathSeparator(c uint8) bool { return CoreIsPathSeparator(c) }
func IsAbs(path string) bool { return CoreIsAbs(path) }
func Join(elem []string) string { return CoreJoin(elem) }
func volumeNameLen(path string) int { return CoreVolumeNameLen(path) }
func isLocal(path string) bool { return CoreIsLocal(path) }
func localize(path string) (string, error) { return CoreLocalize(path) }
`
	if err := os.WriteFile(genLiteUnix, []byte(liteUnixSrc), 0o644); err != nil {
		return "", err
	}

	replacements := [][2]string{
		{"src/runtime/lock_wasip1.go", canon(filepath.Join(overlayDir, "runtime/lock_rusticated.go"))},
		{"src/runtime/os_wasip1.go", canon(filepath.Join(overlayDir, "runtime/os_rusticated.go"))},
		{"src/runtime/netpoll_wasip1.go", canon(filepath.Join(overlayDir, "runtime/netpoll_rusticated.go"))},
		{"src/runtime/stubs_wasm.go", canon(filepath.Join(overlayDir, "runtime/stubs_rusticated.go"))},
		{"src/runtime/rt0_wasip1_wasm.s", canon(filepath.Join(overlayDir, "runtime/rt0_wasip1_wasm.s"))},
		{"src/syscall/fs_wasip1.go", canon(filepath.Join(overlayDir, "syscall/fs_rusticated.go"))},
		{"src/syscall/syscall_wasip1.go", canon(filepath.Join(overlayDir, "syscall/syscall_rusticated.go"))},
		{"src/syscall/net_wasip1.go", canon(filepath.Join(overlayDir, "syscall/net_rusticated.go"))},
		{"src/syscall/os_wasip1.go", canon(filepath.Join(overlayDir, "syscall/os_rusticated.go"))},
		{"src/syscall/log_rusticated.go", canon(filepath.Join(overlayDir, "syscall/log_rusticated.go"))},
		{"src/syscall/log_rusticated_verbose.go", canon(filepath.Join(overlayDir, "syscall/log_rusticated_verbose.go"))},
		{"src/internal/syscall/unix/at_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/at_rusticated.go"))},
		{"src/internal/syscall/unix/utimes_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/utimes_rusticated.go"))},
		{"src/internal/syscall/unix/nonblocking_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/nonblocking_rusticated.go"))},
		{"src/internal/syscall/unix/fcntl_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/fcntl_rusticated.go"))},
		{"src/internal/syscall/unix/net_wasip1.go", canon(filepath.Join(overlayDir, "internal/syscall/unix/net_rusticated.go"))},
		{"src/os/file_open_wasip1.go", canon(filepath.Join(overlayDir, "os_overlay/file_open_rusticated.go"))},
		{"src/os/pipe_wasm.go", canon(filepath.Join(overlayDir, "os/pipe_rusticated.go"))},
		{"src/os/exec/lp_wasm.go", canon(filepath.Join(overlayDir, "os/exec_rusticated.go"))},
		{"src/internal/filepathlite/path_nonwindows.go", canon(filepath.Join(overlayDir, "internal/filepathlite/path_nonwindows.go"))},
		{"src/internal/filepathlite/path_unix.go", canon(genLiteUnix)},
		{"src/net/net_rusticated.go", canon(filepath.Join(overlayDir, "net/net_rusticated.go"))},
		{"src/crypto/x509/root_rusticated.go", canon(filepath.Join(overlayDir, "crypto/x509/root_rusticated.go"))},
	}

	var entries strings.Builder
	first := true
	for _, r := range replacements {
		if _, err := os.Stat(r[1]); err != nil {
			continue
		}
		srcStr := canon(filepath.Join(goroot, r[0]))
		if !first {
			entries.WriteString(",\n")
		}
		first = false
		fmt.Fprintf(&entries, "    %q: %q", srcStr, r[1])
	}

	outPath := filepath.Join(genDir, "overlay.json")
	overlayJSON := fmt.Sprintf("{\n  \"Replace\": {\n%s\n  }\n}\n", entries.String())
	if err := os.WriteFile(outPath, []byte(overlayJSON), 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}
