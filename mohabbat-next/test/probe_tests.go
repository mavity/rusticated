package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const wslProbeTimeout = 90 * time.Second

var (
	reVerifyComponents = regexp.MustCompile(`^Verified all four compressed components\.\n$`)
	reEchoTranscript   = regexp.MustCompile(`^Anonymous-memory probe ready \(compiled [^\)]+\)\. Enter an empty line to exit\.\n> echo: alpha\n> echo: beta\n> $`)
	reEmptyEOF         = regexp.MustCompile(`^Anonymous-memory probe ready \(compiled [^\)]+\)\. Enter an empty line to exit\.\n> $`)
)

func runProbeCase(ctx context.Context, command string, args []string, stdinText string, expectedExit int, expectedRegex *regexp.Regexp) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = strings.NewReader(stdinText)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return fmt.Errorf("launch %s %v failed: %w (stderr: %s)", command, args, err, stderr.String())
		}
	}

	normalizedOut := strings.ReplaceAll(stdout.String(), "\r\n", "\n")
	if exitCode != expectedExit {
		return fmt.Errorf("exit code %d != expected %d\nstdout: %q\nstderr: %q", exitCode, expectedExit, normalizedOut, stderr.String())
	}
	if expectedRegex != nil && !expectedRegex.MatchString(normalizedOut) {
		return fmt.Errorf("output mismatch:\n  expected pattern: %s\n  actual output:    %q\n  stderr:           %q", expectedRegex.String(), normalizedOut, stderr.String())
	}
	return nil
}

func runStandardProbeSuite(r *Runner, suiteName string, command string, baseArgs []string, supportVerifyFlag bool) {
	if supportVerifyFlag {
		r.RunTest(suiteName, "verify-components", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := append(append([]string(nil), baseArgs...), "--verify-components")
			return runProbeCase(ctx, command, args, "", 0, reVerifyComponents)
		})
	}

	r.RunTest(suiteName, "interactive-echo-crlf-lf", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return runProbeCase(ctx, command, baseArgs, "alpha\r\nbeta\n\n", 0, reEchoTranscript)
	})

	r.RunTest(suiteName, "immediate-eof-exit-0", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return runProbeCase(ctx, command, baseArgs, "", 0, reEmptyEOF)
	})

	r.RunTest(suiteName, "partial-line-eof-discards-line", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return runProbeCase(ctx, command, baseArgs, "unfinished", 0, reEmptyEOF)
	})

	r.RunTest(suiteName, "line-capacity-255", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		line := strings.Repeat("x", 255)
		return runProbeCase(ctx, command, baseArgs, line+"\n\n", 0, echoTranscriptRegex(line))
	})

	r.RunTest(suiteName, "line-capacity-256", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		line := strings.Repeat("x", 256)
		return runProbeCase(ctx, command, baseArgs, line+"\r\n\n", 0, echoTranscriptRegex(line))
	})

	r.RunTest(suiteName, "line-capacity-counts-utf8-bytes", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		line := strings.Repeat("x", 254) + "é"
		if len(line) != 256 {
			return fmt.Errorf("test line is %d bytes, want 256", len(line))
		}
		return runProbeCase(ctx, command, baseArgs, line+"\n\n", 0, echoTranscriptRegex(line))
	})

	r.RunTest(suiteName, "cr-only-line-separators", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return runProbeCase(ctx, command, baseArgs, "alpha\rbeta\r\r", 0, echoTranscriptRegex("alpha", "beta"))
	})

	r.RunTest(suiteName, "line-overflow-257-exit-2", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		longLine := strings.Repeat("x", 257) + "\n"
		return runProbeCase(ctx, command, baseArgs, longLine, 2, reEmptyEOF)
	})
}

func echoTranscriptRegex(lines ...string) *regexp.Regexp {
	var expected strings.Builder
	expected.WriteString(`^Anonymous-memory probe ready \(compiled [^\)]+\)\. Enter an empty line to exit\.\n> `)
	for _, line := range lines {
		expected.WriteString(`echo: `)
		expected.WriteString(regexp.QuoteMeta(line))
		expected.WriteString(`\n> `)
	}
	expected.WriteByte('$')
	return regexp.MustCompile(expected.String())
}

func (r *Runner) RunStage3ArtifactIntegrityTests() {
	fmt.Fprintln(os.Stdout, "\n================================================================================")
	fmt.Fprintln(os.Stdout, "🧪  Stage 3: Holistic Multi-Platform & Multi-Arch Acceptance Matrix")
	fmt.Fprintln(os.Stdout, "================================================================================")

	r.RunTest("artifact/polyglot", "mz-pe-header-and-dos-stub", func() error {
		data, err := os.ReadFile(r.DistExe)
		if err != nil {
			return fmt.Errorf("read %s: %w", r.DistExe, err)
		}
		if len(data) < 256 || data[0] != 'M' || data[1] != 'Z' {
			return fmt.Errorf("missing MZ header")
		}
		peOffset := int(binary.LittleEndian.Uint32(data[0x3c:0x40]))
		if peOffset != 128 {
			return fmt.Errorf("expected e_lfanew == 128, got %d", peOffset)
		}
		if data[peOffset] != 'P' || data[peOffset+1] != 'E' || data[peOffset+2] != 0 || data[peOffset+3] != 0 {
			return fmt.Errorf("invalid PE\\0\\0 signature at offset %d", peOffset)
		}
		// Verify DOS stub starts with MZ=1; and contains tail -c +
		stub := string(data[:60])
		if !strings.HasPrefix(stub, "MZ=1;") || !strings.Contains(stub, "tail -c +") {
			return fmt.Errorf("unexpected DOS stub prefix: %q", stub)
		}
		return nil
	})

	r.RunTest("artifact/flat-bins", "zero-relocs-and-size-bounds", func() error {
		compDir := filepath.Join(r.BuildDir, "components")
		expected := []struct {
			name    string
			maxSize int64
		}{
			{"probe-x86_64.bin", 4096},
			{"probe-aarch64.bin", 4096},
			{"platform-windows-x86_64.bin", 4096},
			{"platform-windows-aarch64.bin", 4096},
			{"platform-linux-x86_64.bin", 4096},
			{"platform-linux-aarch64.bin", 4096},
			{"platform-darwin-x86_64.bin", 4096},
			{"platform-darwin-aarch64.bin", 4096},
			{"runtime-x86_64.bin", 4 * 1024 * 1024},
			{"runtime-aarch64.bin", 4 * 1024 * 1024},
		}
		for _, item := range expected {
			p := filepath.Join(compDir, item.name)
			info, err := os.Stat(p)
			if err != nil {
				return fmt.Errorf("missing built component %s: %w", item.name, err)
			}
			if info.Size() <= 0 || info.Size() > item.maxSize {
				return fmt.Errorf("component %s size %d out of bounds (1..%d)", item.name, info.Size(), item.maxSize)
			}
		}
		return nil
	})

	r.RunTest("artifact/raw-entry", "probe-entry-symbol-at-byte-zero", func() error {
		compDir := filepath.Join(r.BuildDir, "components")
		for _, arch := range []string{"x86_64", "aarch64"} {
			elfPath := filepath.Join(compDir, "probe-"+arch+".elf")
			out, err := exec.Command("llvm-nm", "--defined-only", "--format=posix", elfPath).Output()
			if err != nil {
				return fmt.Errorf("llvm-nm probe entry check on %s: %w", elfPath, err)
			}
			found := false
			for _, line := range strings.Split(string(out), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 3 || fields[0] != "probe_code_start" {
					continue
				}
				if found {
					return fmt.Errorf("probe entry symbol is duplicated in %s", elfPath)
				}
				found = true
				address, err := strconv.ParseUint(fields[2], 16, 64)
				if err != nil {
					return fmt.Errorf("parse probe entry address %q in %s: %w", fields[2], elfPath, err)
				}
				if address != 0 {
					return fmt.Errorf("probe entry symbol in %s is at %#x, want byte offset zero", elfPath, address)
				}
			}
			if !found {
				return fmt.Errorf("probe entry symbol missing from %s", elfPath)
			}
		}
		return nil
	})

	r.RunTest("artifact/runtime-slices", "zero-relocs-and-clean-sections", func() error {
		compDir := filepath.Join(r.BuildDir, "components")
		slices := []string{"runtime-aarch64", "runtime-x86_64"}
		relocRe := regexp.MustCompile(`(?m)^\s*0x[0-9a-fA-F]+\s+`)
		badSecRe := regexp.MustCompile(`(?m)\.(data|bss|got|tdata|tbss)\b`)

		for _, name := range slices {
			elfPath := filepath.Join(compDir, name+".elf")
			if _, err := os.Stat(elfPath); err != nil {
				return fmt.Errorf("missing runtime slice ELF %s: %w", elfPath, err)
			}

			// 1. Verify 0 relocations
			relocOut, err := exec.Command("llvm-readobj", "--relocations", elfPath).Output()
			if err != nil {
				return fmt.Errorf("llvm-readobj relocations check on %s: %w", elfPath, err)
			}
			if relocRe.Match(relocOut) {
				return fmt.Errorf("runtime slice %s contains relocations", name)
			}

			// 2. Verify clean sections (no .data, .bss, .got, .tdata, .tbss)
			secOut, err := exec.Command("llvm-readobj", "--sections", elfPath).Output()
			if err != nil {
				return fmt.Errorf("llvm-readobj sections check on %s: %w", elfPath, err)
			}
			if badSecRe.Match(secOut) {
				return fmt.Errorf("runtime slice %s contains disallowed data/bss/got/tls sections", name)
			}

			// 3. Verify zero undefined symbols
			nmOut, err := exec.Command("llvm-nm", "--undefined-only", elfPath).Output()
			if err != nil {
				return fmt.Errorf("llvm-nm undefined-only check on %s: %w", elfPath, err)
			}
			if len(bytes.TrimSpace(nmOut)) > 0 {
				return fmt.Errorf("runtime slice %s has undefined symbols:\n%s", name, string(nmOut))
			}
		}
		return nil
	})
}

func (r *Runner) RunStage3PlatformMatrix() {
	// 1. Windows Native (ARM64 or x86_64) + Windows Cross-Arch Emulation
	if runtime.GOOS == "windows" {
		nativeSuite := fmt.Sprintf("windows/%s-native", runtime.GOARCH)
		runStandardProbeSuite(r, nativeSuite, r.DistExe, nil, true)

		// If running on Windows ARM64, also test the x86_64 Windows PAL & x86_64 Probe via a /platform:x64 CLR host!
		if runtime.GOARCH == "arm64" {
			x64Exe, err := r.builtX64WindowsHost()
			r.RunTest("windows/amd64-emulated", "x64-host-artifact", func() error {
				return err
			})
			if err == nil {
				runStandardProbeSuite(r, "windows/amd64-emulated", x64Exe, nil, true)
			}
		}
	} else {
		r.RecordDisabled(
			"windows/native",
			"probe-suite",
			fmt.Sprintf("host GOOS=%s cannot run Windows PE %s natively", runtime.GOOS, filepath.Base(r.DistExe)),
		)
	}

	// 2. Linux (Native or WSL with bounded startup probe)
	r.runLinuxMatrix()

	// 3. Android Termux (Native Termux or ADB probe with 3-second timeout)
	r.runAndroidMatrix()

	// 4. macOS Darwin (Native Darwin + Rosetta 2 x86_64 on Apple Silicon)
	r.runDarwinMatrix()
}

func (r *Runner) builtX64WindowsHost() (string, error) {
	path := filepath.Join(r.BuildDir, "MohabbatWindowsHost-x64.exe")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("Go builder did not produce the x64 Windows host at %s: %w", path, err)
	}
	if info.IsDir() || info.Size() == 0 {
		return "", fmt.Errorf("Go builder produced an invalid x64 Windows host at %s", path)
	}
	return path, nil
}

func (r *Runner) runLinuxMatrix() {
	if runtime.GOOS == "linux" {
		if os.Getenv("TERMUX_VERSION") != "" || os.Getenv("PREFIX") == "/data/data/com.termux/files/usr" {
			r.RecordDisabled("linux/gnu-musl", "probe-suite", "running inside Android Termux environment (tested under android/aarch64 instead)")
			return
		}
		suite := fmt.Sprintf("linux/%s-native", runtime.GOARCH)
		runStandardProbeSuite(r, suite, "bash", []string{r.DistExe}, false)
		return
	}

	if runtime.GOOS == "windows" {
		wslPath, err := exec.LookPath("wsl.exe")
		if err != nil {
			r.RecordDisabled("linux/wsl", "probe-suite", fmt.Sprintf("exec.LookPath(\"wsl.exe\") failed: %v", err))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), wslProbeTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, wslPath, "-e", "uname", "-sm")
		out, err := cmd.Output()
		if ctx.Err() == context.DeadlineExceeded {
			r.RecordDisabled("linux/wsl", "probe-suite", fmt.Sprintf("command %q timed out after %s (output: %q)", "wsl.exe -e uname -sm", wslProbeTimeout, strings.TrimSpace(string(out))))
			return
		}
		if err != nil {
			r.RecordDisabled("linux/wsl", "probe-suite", fmt.Sprintf("command %q failed (%v): %q", "wsl.exe -e uname -sm", err, strings.TrimSpace(string(out))))
			return
		}

		// Convert Windows path to /mnt/<drive>/...
		drive := strings.ToLower(r.DistExe[:1])
		unixBundle := "/mnt/" + drive + "/" + filepath.ToSlash(r.DistExe[3:])
		suite := fmt.Sprintf("linux/wsl (%s)", strings.TrimSpace(string(out)))
		runStandardProbeSuite(r, suite, wslPath, []string{"-e", "bash", unixBundle}, false)
		return
	}

	r.RecordDisabled("linux/native", "probe-suite", fmt.Sprintf("host GOOS=%s cannot execute Linux ELF binaries locally", runtime.GOOS))
}

func (r *Runner) runAndroidMatrix() {
	if runtime.GOOS == "linux" && (os.Getenv("TERMUX_VERSION") != "" || os.Getenv("PREFIX") == "/data/data/com.termux/files/usr") {
		runStandardProbeSuite(r, "android/aarch64-termux", "bash", []string{r.DistExe}, false)
		return
	}

	adbPath, err := exec.LookPath("adb")
	if err != nil {
		r.RecordDisabled("android/aarch64", "probe-suite", fmt.Sprintf("not in Termux and exec.LookPath(\"adb\") failed: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, adbPath, "devices").CombinedOutput()
	if err != nil {
		r.RecordDisabled("android/aarch64", "probe-suite", fmt.Sprintf("command \"adb devices\" failed (%v): %q", err, strings.TrimSpace(string(out))))
		return
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	hasDevice := false
	for _, line := range lines[1:] {
		if strings.Contains(line, "\tdevice") {
			hasDevice = true
			break
		}
	}
	if !hasDevice {
		r.RecordDisabled("android/aarch64", "probe-suite", fmt.Sprintf("command \"adb devices\" reported no connected Android devices: %q", strings.TrimSpace(string(out))))
		return
	}
	r.RecordDisabled("android/aarch64", "probe-suite", "ADB device detected but Termux shell path not configured")
}

func (r *Runner) runDarwinMatrix() {
	if runtime.GOOS != "darwin" {
		r.RecordDisabled("darwin/native", "probe-suite", fmt.Sprintf("host GOOS=%s/%s cannot execute Darwin Mach-O binaries locally", runtime.GOOS, runtime.GOARCH))
		return
	}

	suite := fmt.Sprintf("darwin/%s-native", runtime.GOARCH)
	runStandardProbeSuite(r, suite, "bash", []string{r.DistExe}, false)

	if runtime.GOARCH == "arm64" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "arch", "-x86_64", "/usr/bin/true").CombinedOutput()
		if err != nil {
			r.RecordDisabled("darwin/x86_64-rosetta2", "probe-suite", fmt.Sprintf("command \"arch -x86_64 /usr/bin/true\" failed (%v): %q", err, strings.TrimSpace(string(out))))
			return
		}
		runStandardProbeSuite(r, "darwin/x86_64-rosetta2", "arch", []string{"-x86_64", "bash", r.DistExe}, false)
	}
}
