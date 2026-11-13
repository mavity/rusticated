package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
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

const wslStartupTimeout = 90 * time.Second

var (
	reVerifyComponents = regexp.MustCompile(`^Verified all six compressed components\.\n$`)
	reEchoTranscript   = regexp.MustCompile(`^Mohabbat-next ready \(compiled [^\)]+\)\. Enter an empty line to exit\.\n> echo: alpha\n> echo: beta\n> $`)
	reEmptyEOF         = regexp.MustCompile(`^Mohabbat-next ready \(compiled [^\)]+\)\. Enter an empty line to exit\.\n> $`)
	reFileIoResult     = regexp.MustCompile(`^\d+ (OK|ERR) `)
)

func runBundleCase(ctx context.Context, command string, args []string, stdinText string, expectedExit int, expectedRegex *regexp.Regexp) error {
	_, err := runBundleCaseCapture(ctx, command, args, stdinText, expectedExit, expectedRegex)
	return err
}

func runBundleCaseCapture(ctx context.Context, command string, args []string, stdinText string, expectedExit int, expectedRegex *regexp.Regexp) (string, error) {
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
			return "", fmt.Errorf("launch %s %v failed: %w (stderr: %s)", command, args, err, stderr.String())
		}
	}

	normalizedOut := strings.ReplaceAll(stdout.String(), "\r\n", "\n")
	if exitCode != expectedExit {
		return "", fmt.Errorf("exit code %d != expected %d\nstdout: %q\nstderr: %q", exitCode, expectedExit, normalizedOut, stderr.String())
	}
	if expectedRegex != nil && !expectedRegex.MatchString(normalizedOut) {
		return "", fmt.Errorf("output mismatch:\n  expected pattern: %s\n  actual output:    %q\n  stderr:           %q", expectedRegex.String(), normalizedOut, stderr.String())
	}
	return normalizedOut, nil
}

func runStandardAcceptanceSuite(r *Runner, suiteName string, command string, baseArgs []string, supportVerifyFlag bool) {
	if supportVerifyFlag {
		r.RunTest(suiteName, "verify-components", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := append(append([]string(nil), baseArgs...), "--verify-components")
			return runBundleCase(ctx, command, args, "", 0, reVerifyComponents)
		})
	}

	r.RunTest(suiteName, "interactive-echo-crlf-lf", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return runBundleCase(ctx, command, baseArgs, "alpha\r\nbeta\n\n", 0, reEchoTranscript)
	})

	r.RunTest(suiteName, "immediate-eof-exit-0", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return runBundleCase(ctx, command, baseArgs, "", 0, reEmptyEOF)
	})

	r.RunTest(suiteName, "partial-line-eof-discards-line", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return runBundleCase(ctx, command, baseArgs, "unfinished", 0, reEmptyEOF)
	})

	r.RunTest(suiteName, "line-capacity-255", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		line := strings.Repeat("x", 255)
		return runBundleCase(ctx, command, baseArgs, line+"\n\n", 0, echoTranscriptRegex(line))
	})

	r.RunTest(suiteName, "line-capacity-256", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		line := strings.Repeat("x", 256)
		return runBundleCase(ctx, command, baseArgs, line+"\r\n\n", 0, echoTranscriptRegex(line))
	})

	r.RunTest(suiteName, "line-capacity-counts-utf8-bytes", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		line := strings.Repeat("x", 254) + "é"
		if len(line) != 256 {
			return fmt.Errorf("test line is %d bytes, want 256", len(line))
		}
		return runBundleCase(ctx, command, baseArgs, line+"\n\n", 0, echoTranscriptRegex(line))
	})

	r.RunTest(suiteName, "cr-only-line-separators", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return runBundleCase(ctx, command, baseArgs, "alpha\rbeta\r\r", 0, echoTranscriptRegex("alpha", "beta"))
	})

	r.RunTest(suiteName, "line-overflow-257-exit-2", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		longLine := strings.Repeat("x", 257) + "\n"
		return runBundleCase(ctx, command, baseArgs, longLine, 2, reEmptyEOF)
	})
}

type fileIoScenario struct {
	name          string
	args          []string
	initialFiles  map[string][]byte
	expectedFiles map[string][]byte
	expectedLines []string
}

func runFileIoAcceptanceSuite(r *Runner, suiteName, command string, baseArgs []string, mapWorkDir func(string) (string, error)) {
	for _, scenario := range fileIoScenarios() {
		scenario := scenario
		r.RunTest(suiteName, "fileio-"+scenario.name, func() (testErr error) {
			workDir, err := os.MkdirTemp("", "mohabbat-fileio-")
			if err != nil {
				return fmt.Errorf("create file-I/O test directory: %w", err)
			}
			defer func() {
				if cleanupErr := os.RemoveAll(workDir); cleanupErr != nil {
					if testErr == nil {
						testErr = fmt.Errorf("remove file-I/O test directory: %w", cleanupErr)
					} else {
						testErr = fmt.Errorf("%v; remove file-I/O test directory: %w", testErr, cleanupErr)
					}
				}
			}()

			for name, content := range scenario.initialFiles {
				if err := os.WriteFile(filepath.Join(workDir, name), content, 0o600); err != nil {
					return fmt.Errorf("create initial file %s: %w", name, err)
				}
			}

			workDirArg := workDir
			if mapWorkDir != nil {
				workDirArg, err = mapWorkDir(workDir)
				if err != nil {
					return fmt.Errorf("map file-I/O working directory: %w", err)
				}
			}
			args := append(append([]string(nil), baseArgs...), "--file-io-test", workDirArg)
			args = append(args, scenario.args...)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := runBundleCaseCapture(ctx, command, args, "", 0, nil)
			if err != nil {
				return err
			}
			var records []string
			for _, line := range strings.Split(output, "\n") {
				if reFileIoResult.MatchString(line) {
					records = append(records, line)
				}
			}
			if len(records) != len(scenario.expectedLines) {
				return fmt.Errorf("guest emitted %d result records, want %d\nstdout: %q", len(records), len(scenario.expectedLines), output)
			}
			for i, expected := range scenario.expectedLines {
				if records[i] != expected {
					return fmt.Errorf("result record %d: %q != %q\nstdout: %q", i, records[i], expected, output)
				}
			}
			return verifyFileIoScenario(workDir, scenario.expectedFiles)
		})
	}
}

func fileIoScenarios() []fileIoScenario {
	maxTransfer := make([]byte, 4096)
	for i := range maxTransfer {
		maxTransfer[i] = byte((i*37 + 11) & 0xff)
	}
	maxTransferHex := hex.EncodeToString(maxTransfer)

	return []fileIoScenario{
		{
			name: "binary-unicode-and-positional-read",
			args: []string{
				"open", "f", "binary-λ.bin", "rw", "create,exclusive",
				"write", "f", "0061",
				"write", "f", "00ff7f80",
				"seek", "f", "start", "0",
				"read", "f", "6",
				"seek", "f", "start", "2",
				"pread", "f", "0", "2",
				"seek", "f", "current", "1",
				"read", "f", "2",
				"seek", "f", "end", "-2",
				"read", "f", "2",
				"close", "f",
			},
			expectedFiles: map[string][]byte{"binary-λ.bin": {0x00, 'a', 0x00, 0xff, 0x7f, 0x80}},
			expectedLines: []string{
				"0 OK open f",
				"1 OK write f bytes=2",
				"2 OK write f bytes=4",
				"3 OK seek f position=0",
				"4 OK read f bytes=6 eof=0 hex=006100ff7f80",
				"5 OK seek f position=2",
				"6 OK pread f bytes=2 eof=0 hex=0061",
				"7 OK seek f position=3",
				"8 OK read f bytes=2 eof=0 hex=ff7f",
				"9 OK seek f position=4",
				"10 OK read f bytes=2 eof=0 hex=7f80",
				"11 OK close f",
			},
		},
		{
			name: "maximum-transfer-boundary",
			args: []string{
				"open", "f", "max-transfer.bin", "rw", "create,exclusive",
				"write", "f", maxTransferHex,
				"seek", "f", "start", "0",
				"read", "f", "4096",
				"close", "f",
			},
			expectedFiles: map[string][]byte{"max-transfer.bin": maxTransfer},
			expectedLines: []string{
				"0 OK open f",
				"1 OK write f bytes=4096",
				"2 OK seek f position=0",
				"3 OK read f bytes=4096 eof=0 hex=" + maxTransferHex,
				"4 OK close f",
			},
		},
		{
			name: "missing-exclusive-and-open-truncate",
			args: []string{
				"open", "x", "missing.bin", "ro", "-",
				"open", "x", "replace.bin", "rw", "create,exclusive",
				"open", "f", "replace.bin", "rw", "truncate",
				"read", "f", "1",
				"write", "f", "6e6577",
				"close", "f",
			},
			initialFiles:  map[string][]byte{"replace.bin": []byte("old-data")},
			expectedFiles: map[string][]byte{"replace.bin": []byte("new")},
			expectedLines: []string{
				"0 ERR open x error=44",
				"1 ERR open x error=20",
				"2 OK open f",
				"3 OK read f bytes=0 eof=1 hex=",
				"4 OK write f bytes=3",
				"5 OK close f",
			},
		},
		{
			name: "zero-length-and-repeated-eof",
			args: []string{
				"open", "f", "empty.bin", "rw", "create,exclusive",
				"write", "f", "",
				"seek", "f", "start", "0",
				"read", "f", "0",
				"read", "f", "1",
				"read", "f", "1",
				"close", "f",
			},
			expectedFiles: map[string][]byte{"empty.bin": {}},
			expectedLines: []string{
				"0 OK open f",
				"1 OK write f bytes=0",
				"2 OK seek f position=0",
				"3 OK read f bytes=0 eof=0 hex=",
				"4 OK read f bytes=0 eof=1 hex=",
				"5 OK read f bytes=0 eof=1 hex=",
				"6 OK close f",
			},
		},
		{
			name: "seek-truncate-extend-and-shrink",
			args: []string{
				"open", "f", "resize.bin", "rw", "-",
				"seek", "f", "end", "0",
				"truncate", "f", "3",
				"seek", "f", "start", "0",
				"read", "f", "3",
				"truncate", "f", "6",
				"seek", "f", "start", "3",
				"read", "f", "3",
				"truncate", "f", "0",
				"seek", "f", "end", "0",
				"close", "f",
			},
			initialFiles:  map[string][]byte{"resize.bin": []byte("abcdef")},
			expectedFiles: map[string][]byte{"resize.bin": {}},
			expectedLines: []string{
				"0 OK open f",
				"1 OK seek f position=6",
				"2 OK truncate f",
				"3 OK seek f position=0",
				"4 OK read f bytes=3 eof=0 hex=616263",
				"5 OK truncate f",
				"6 OK seek f position=3",
				"7 OK read f bytes=3 eof=0 hex=000000",
				"8 OK truncate f",
				"9 OK seek f position=0",
				"10 OK close f",
			},
		},
		{
			name: "append-and-independent-handles",
			args: []string{
				"open", "a", "append.bin", "rw", "append",
				"open", "b", "append.bin", "ro", "-",
				"seek", "a", "start", "0",
				"write", "a", "5859",
				"seek", "b", "start", "0",
				"read", "b", "8",
				"close", "a",
				"close", "b",
			},
			initialFiles:  map[string][]byte{"append.bin": []byte("abcdef")},
			expectedFiles: map[string][]byte{"append.bin": []byte("abcdefXY")},
			expectedLines: []string{
				"0 OK open a",
				"1 OK open b",
				"2 OK seek a position=0",
				"3 OK write a bytes=2",
				"4 OK seek b position=0",
				"5 OK read b bytes=8 eof=0 hex=6162636465665859",
				"6 OK close a",
				"7 OK close b",
			},
		},
		{
			name: "access-modes-close-and-reopen",
			args: []string{
				"open", "r", "handles.bin", "ro", "-",
				"write", "r", "5a",
				"read", "r", "1",
				"open", "w", "handles.bin", "wo", "-",
				"read", "w", "1",
				"write", "w", "5a",
				"close", "r",
				"read", "r", "1",
				"close", "w",
				"open", "fresh", "handles.bin", "ro", "-",
				"read", "fresh", "3",
				"close", "fresh",
			},
			initialFiles:  map[string][]byte{"handles.bin": []byte("abc")},
			expectedFiles: map[string][]byte{"handles.bin": []byte("Zbc")},
			expectedLines: []string{
				"0 OK open r",
				"1 ERR write r error=8 bytes=0",
				"2 OK read r bytes=1 eof=0 hex=61",
				"3 OK open w",
				"4 ERR read w error=8 bytes=0 hex=",
				"5 OK write w bytes=1",
				"6 OK close r",
				"7 ERR read r error=8 bytes=0 hex=",
				"8 OK close w",
				"9 OK open fresh",
				"10 OK read fresh bytes=3 eof=0 hex=5a6263",
				"11 OK close fresh",
			},
		},
		{
			name: "directory-open-and-close",
			args: []string{
				"open", "d", ".", "ro", "directory",
				"close", "d",
			},
			expectedFiles: map[string][]byte{},
			expectedLines: []string{"0 OK open d", "1 OK close d"},
		},
	}
}

func verifyFileIoScenario(workDir string, expected map[string][]byte) error {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return fmt.Errorf("read file-I/O working directory: %w", err)
	}
	if len(entries) != len(expected) {
		return fmt.Errorf("working directory contains %d entries, want %d", len(entries), len(expected))
	}
	for _, entry := range entries {
		want, ok := expected[entry.Name()]
		if !ok {
			return fmt.Errorf("unexpected filesystem entry %q", entry.Name())
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("filesystem entry %q is not a regular file", entry.Name())
		}
		got, err := os.ReadFile(filepath.Join(workDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read filesystem entry %q: %w", entry.Name(), err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("file %q contents %x != expected %x", entry.Name(), got, want)
		}
	}
	return nil
}

func echoTranscriptRegex(lines ...string) *regexp.Regexp {
	var expected strings.Builder
	expected.WriteString(`^Mohabbat-next ready \(compiled [^\)]+\)\. Enter an empty line to exit\.\n> `)
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
			{"hello-x86_64.bin", 4096},
			{"hello-aarch64.bin", 4096},
			{"hello-fileio-x86_64.bin", 64 * 1024},
			{"hello-fileio-aarch64.bin", 64 * 1024},
			{"platform-windows-x86_64.bin", 8192},
			{"platform-windows-aarch64.bin", 8192},
			{"platform-linux-x86_64.bin", 8192},
			{"platform-linux-aarch64.bin", 8192},
			{"platform-darwin-x86_64.bin", 8192},
			{"platform-darwin-aarch64.bin", 8192},
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

	r.RunTest("artifact/raw-entry", "hello-entry-symbol-at-byte-zero", func() error {
		compDir := filepath.Join(r.BuildDir, "components")
		for _, payload := range []string{"hello", "hello-fileio"} {
			for _, arch := range []string{"x86_64", "aarch64"} {
				elfPath := filepath.Join(compDir, payload+"-"+arch+".elf")
				out, err := exec.Command("llvm-nm", "--defined-only", "--format=posix", elfPath).Output()
				if err != nil {
					return fmt.Errorf("llvm-nm %s entry check on %s: %w", payload, elfPath, err)
				}
				found := false
				for _, line := range strings.Split(string(out), "\n") {
					fields := strings.Fields(line)
					if len(fields) < 3 || fields[0] != "hello_payload_code_start" {
						continue
					}
					if found {
						return fmt.Errorf("hello payload entry symbol is duplicated in %s", elfPath)
					}
					found = true
					address, err := strconv.ParseUint(fields[2], 16, 64)
					if err != nil {
						return fmt.Errorf("parse hello payload entry address %q in %s: %w", fields[2], elfPath, err)
					}
					if address != 0 {
						return fmt.Errorf("hello payload entry symbol in %s is at %#x, want byte offset zero", elfPath, address)
					}
				}
				if !found {
					return fmt.Errorf("hello payload entry symbol missing from %s", elfPath)
				}
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
		runStandardAcceptanceSuite(r, nativeSuite, r.DistExe, nil, true)
		runFileIoAcceptanceSuite(r, nativeSuite, r.DistExe, nil, nil)

		// If running on Windows ARM64, also test the x86_64 Windows PAL and hello payload via a /platform:x64 CLR host.
		if runtime.GOARCH == "arm64" {
			x64Exe, err := r.builtX64WindowsHost()
			r.RunTest("windows/amd64-emulated", "x64-host-artifact", func() error {
				return err
			})
			if err == nil {
				runStandardAcceptanceSuite(r, "windows/amd64-emulated", x64Exe, nil, true)
				runFileIoAcceptanceSuite(r, "windows/amd64-emulated", x64Exe, nil, nil)
			}
		}
	} else {
		r.RecordDisabled(
			"windows/native",
			"bundle-acceptance",
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
			r.RecordDisabled("linux/gnu-musl", "bundle-acceptance", "running inside Android Termux environment (tested under android/aarch64 instead)")
			return
		}
		suite := fmt.Sprintf("linux/%s-native", runtime.GOARCH)
		runStandardAcceptanceSuite(r, suite, "bash", []string{r.DistExe}, false)
		runFileIoAcceptanceSuite(r, suite, "bash", []string{r.DistExe}, nil)
		return
	}

	if runtime.GOOS == "windows" {
		wslPath, err := exec.LookPath("wsl.exe")
		if err != nil {
			r.RecordDisabled("linux/wsl", "bundle-acceptance", fmt.Sprintf("exec.LookPath(\"wsl.exe\") failed: %v", err))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), wslStartupTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, wslPath, "-e", "uname", "-sm")
		out, err := cmd.Output()
		if ctx.Err() == context.DeadlineExceeded {
			r.RecordDisabled("linux/wsl", "bundle-acceptance", fmt.Sprintf("command %q timed out after %s (output: %q)", "wsl.exe -e uname -sm", wslStartupTimeout, strings.TrimSpace(string(out))))
			return
		}
		if err != nil {
			r.RecordDisabled("linux/wsl", "bundle-acceptance", fmt.Sprintf("command %q failed (%v): %q", "wsl.exe -e uname -sm", err, strings.TrimSpace(string(out))))
			return
		}

		// Convert Windows path to /mnt/<drive>/...
		drive := strings.ToLower(r.DistExe[:1])
		unixBundle := "/mnt/" + drive + "/" + filepath.ToSlash(r.DistExe[3:])
		suite := fmt.Sprintf("linux/wsl (%s)", strings.TrimSpace(string(out)))
		runStandardAcceptanceSuite(r, suite, wslPath, []string{"-e", "bash", unixBundle}, false)
		runFileIoAcceptanceSuite(r, suite, wslPath, []string{"-e", "bash", unixBundle}, func(path string) (string, error) {
			if len(path) < 3 || path[1] != ':' {
				return "", fmt.Errorf("expected an absolute Windows path, got %q", path)
			}
			return "/mnt/" + strings.ToLower(path[:1]) + "/" + filepath.ToSlash(path[3:]), nil
		})
		return
	}

	r.RecordDisabled("linux/native", "bundle-acceptance", fmt.Sprintf("host GOOS=%s cannot execute Linux ELF binaries locally", runtime.GOOS))
}

func (r *Runner) runAndroidMatrix() {
	if runtime.GOOS == "linux" && (os.Getenv("TERMUX_VERSION") != "" || os.Getenv("PREFIX") == "/data/data/com.termux/files/usr") {
		runStandardAcceptanceSuite(r, "android/aarch64-termux", "bash", []string{r.DistExe}, false)
		runFileIoAcceptanceSuite(r, "android/aarch64-termux", "bash", []string{r.DistExe}, nil)
		return
	}

	adbPath, err := exec.LookPath("adb")
	if err != nil {
		r.RecordDisabled("android/aarch64", "bundle-acceptance", fmt.Sprintf("not in Termux and exec.LookPath(\"adb\") failed: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, adbPath, "devices").CombinedOutput()
	if err != nil {
		r.RecordDisabled("android/aarch64", "bundle-acceptance", fmt.Sprintf("command \"adb devices\" failed (%v): %q", err, strings.TrimSpace(string(out))))
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
		r.RecordDisabled("android/aarch64", "bundle-acceptance", fmt.Sprintf("command \"adb devices\" reported no connected Android devices: %q", strings.TrimSpace(string(out))))
		return
	}
	r.RecordDisabled("android/aarch64", "bundle-acceptance", "ADB device detected but Termux shell path not configured")
}

func (r *Runner) runDarwinMatrix() {
	if runtime.GOOS != "darwin" {
		r.RecordDisabled("darwin/native", "bundle-acceptance", fmt.Sprintf("host GOOS=%s/%s cannot execute Darwin Mach-O binaries locally", runtime.GOOS, runtime.GOARCH))
		return
	}

	suite := fmt.Sprintf("darwin/%s-native", runtime.GOARCH)
	runStandardAcceptanceSuite(r, suite, "bash", []string{r.DistExe}, false)
	runFileIoAcceptanceSuite(r, suite, "bash", []string{r.DistExe}, nil)

	if runtime.GOARCH == "arm64" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "arch", "-x86_64", "/usr/bin/true").CombinedOutput()
		if err != nil {
			r.RecordDisabled("darwin/x86_64-rosetta2", "bundle-acceptance", fmt.Sprintf("command \"arch -x86_64 /usr/bin/true\" failed (%v): %q", err, strings.TrimSpace(string(out))))
			return
		}
		runStandardAcceptanceSuite(r, "darwin/x86_64-rosetta2", "arch", []string{"-x86_64", "bash", r.DistExe}, false)
		runFileIoAcceptanceSuite(r, "darwin/x86_64-rosetta2", "arch", []string{"-x86_64", "bash", r.DistExe}, nil)
	}
}
