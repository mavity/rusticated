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
	fmt.Fprintln(os.Stdout, "🧪  Stage 2: Package-Level Unit Tests (Go, Zig, C#)")
	fmt.Fprintln(os.Stdout, "================================================================================")

	// 1. Go Unit Tests (mohabbat-next/build)
	r.RunTest("unit/go", "mohabbat-next/build", func() error {
		cmd := exec.Command("go", "test", "-v", "./build")
		cmd.Dir = r.NextDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})

	palAbi := filepath.Join(r.NextDir, "pal", "abi", "guest.zig")
	zwasmRoot := filepath.Join(r.NextDir, "runtime", "zwasm", "src", "zwasm.zig")
	zwasmOpts := filepath.Join(r.NextDir, "runtime", "src", "build_options.zig")

	// 2. Zig Unit Tests: ABI, Payloads, Unix Brot, PALs, zwasm Runtime
	r.RunTest("unit/zig", "pal/abi/guest.zig", func() error {
		cmd := exec.Command("zig", "test", palAbi)
		cmd.Dir = r.NextDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})

	r.RunTest("unit/zig", "payload/hello.zig & fileio.zig", func() error {
		for _, f := range []string{"hello.zig", "fileio.zig"} {
			cmd := exec.Command("zig", "test", "--dep", "pal_abi", "-Mroot="+filepath.Join(r.NextDir, "payload", f), "-Mpal_abi="+palAbi)
			cmd.Dir = r.NextDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return err
			}
		}
		return nil
	})

	r.RunTest("unit/zig", "brot/unix (pool + zstd)", func() error {
		cmd := exec.Command("zig", "test", filepath.Join(r.NextDir, "brot", "unix", "src", "main.zig"))
		cmd.Dir = r.NextDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})

	r.RunTest("unit/zig", "pal/windows & pal/unix", func() error {
		for _, f := range []string{
			filepath.Join(r.NextDir, "pal", "windows", "platform.zig"),
			filepath.Join(r.NextDir, "pal", "unix", "platform_linux.zig"),
		} {
			cmd := exec.Command("zig", "test", "--dep", "pal_abi", "-Mroot="+f, "-Mpal_abi="+palAbi)
			cmd.Dir = r.NextDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return err
			}
		}
		return nil
	})

	r.RunTest("unit/zig", "runtime/zwasm", func() error {
		cmd := exec.Command(
			"zig", "test",
			"--dep", "pal_abi",
			"--dep", "zwasm",
			"-Mroot="+filepath.Join(r.NextDir, "runtime", "src", "main.zig"),
			"-Mpal_abi="+palAbi,
			"--dep", "build_options",
			"-Mzwasm="+zwasmRoot,
			"-Mbuild_options="+zwasmOpts,
		)
		cmd.Dir = r.NextDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	})

	// 3. C# Unit Tests (MohabbatNext.exe --unit-test)
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
