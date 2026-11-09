package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func (r *Runner) RunStage2UnitTests() {
	fmt.Fprintln(os.Stdout, "\n================================================================================")
	fmt.Fprintln(os.Stdout, "🧪  Stage 2: Package-Level Unit Tests (Go, Rust, C#)")
	fmt.Fprintln(os.Stdout, "================================================================================")

	// 1. Go Unit Tests (mohabbat-next/build)
	r.RunTest("unit/go", "mohabbat-next/build", func() error {
		cmd := exec.Command("go", "test", "-v", "./build")
		cmd.Dir = r.NextDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})

	// 2. Rust Unit Tests: probe workspace (payload, platform/windows, platform/unix)
	r.RunTest("unit/rust", "probe-workspace (payload + windows/unix PAL)", func() error {
		manifest := filepath.Join(r.ProbeDir, "Cargo.toml")
		targetDir := filepath.Join(r.BuildDir, "rust", "unit-probe")
		cmd := exec.Command("cargo", "test", "--manifest-path", manifest, "--workspace", "--target-dir", targetDir, "--", "--nocapture")
		cmd.Dir = r.ProbeDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})

	// 3. Rust Unit Tests: Unix Brot micro-launcher (intrinsics + zstd decompressor)
	r.RunTest("unit/rust", "mohabbat-next-brot (unix host)", func() error {
		manifest := filepath.Join(r.ProbeDir, "hosts", "unix", "Cargo.toml")
		targetDir := filepath.Join(r.BuildDir, "rust", "unit-brot")
		cmd := exec.Command("cargo", "test", "--manifest-path", manifest, "--target-dir", targetDir, "--", "--nocapture")
		cmd.Dir = filepath.Join(r.ProbeDir, "hosts", "unix")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})

	// 4. Rust Unit Tests: Movable Cranelift Runtime Slice (Overlapped/PAL layouts + CLIF codegen + WASM JIT)
	r.RunTest("unit/rust", "mohabbat-next-runtime (cranelift JIT)", func() error {
		manifest := filepath.Join(r.NextDir, "runtime", "Cargo.toml")
		targetDir := filepath.Join(r.BuildDir, "rust", "unit-runtime")
		cmd := exec.Command("cargo", "test", "--manifest-path", manifest, "--target-dir", targetDir, "--", "--nocapture")
		cmd.Dir = filepath.Join(r.NextDir, "runtime")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})

	// 5. C# Unit Tests (MohabbatProbe.exe --unit-test)
	if runtime.GOOS == "windows" {
		r.RunTest("unit/csharp", "WindowsHost (--unit-test)", func() error {
			cmd := exec.Command(r.DistExe, "--unit-test")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		})
	} else {
		r.RecordDisabled(
			"unit/csharp",
			"WindowsHost (--unit-test)",
			fmt.Sprintf("host GOOS=%s cannot execute Windows CLR PE directly (%s --unit-test)", runtime.GOOS, r.DistExe),
		)
	}
}
