package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/sys"
)

func RunWasm(ctx context.Context, payload []byte, args []string) (int, error) {
	if len(payload) == 0 {
		return 1, fmt.Errorf("payload is empty")
	}

	// 1. Setup Wazero using Compiler.
	rConfig := wazero.NewRuntimeConfigCompiler()
	r := wazero.NewRuntimeWithConfig(ctx, rConfig)
	defer r.Close(ctx)

	// We decode earlier than module compilation to detect imports
	decoded, err := r.CompileModule(ctx, payload)
	if err != nil {
		recoveredErr := tryRecoverFunctionName(err, payload)
		return 1, fmt.Errorf("failed to compile module: %w", recoveredErr)
	}

	// 2. Register Host Environment
	hEnv := NewHostEnv()
	defer hEnv.Close()
	if err := hEnv.Register(ctx, r); err != nil {
		return 1, fmt.Errorf("failed to register rusticated host env: %w", err)
	}

	// 3. Instantiate
	// Apply args directly to Wazero Config
	cfg := wazero.NewModuleConfig().
		WithArgs(args...).
		WithStdout(os.Stdout).
		WithStderr(os.Stderr).
		WithStdin(os.Stdin).
		WithFSConfig(wazero.NewFSConfig().WithDirMount(".", "/").WithDirMount("C:\\", "C:\\"))

	// Since we provide rusticated ABI bindings via `hEnv.Register`, Wazero will resolve imports
	mod, err := r.InstantiateModule(ctx, decoded, cfg)
	if err != nil {
		// Module might exit with specific exit code (e.g. WASI exit)
		if exitErr, ok := err.(*sys.ExitError); ok {
			return int(exitErr.ExitCode()), nil // Normal exit flow
		}
		return 1, fmt.Errorf("failed to instantiate module: %w", err)
	}

	// 4. Drive completion
	runFunc := mod.ExportedFunction("run")
	if runFunc == nil {
		return 1, fmt.Errorf("rusticated module missing 'run' export")
	}

	// 2. Event loop: poll for completions, then re-enter the guest.
	fmt.Printf("HOST: entering event loop\n")
	var res []uint64
	for {
		res, err = runFunc.Call(ctx)
		if err != nil {
			if exitErr, ok := err.(*sys.ExitError); ok {
				return int(exitErr.ExitCode()), nil
			}
			return 1, fmt.Errorf("run failed: %w", err)
		}

		// The only indicator should be outstanding continuation count.
		if !hEnv.HasOutstandingOps() {
			fmt.Printf("HOST: guest done (no outstanding ops)\n")
			break
		}

		// If we have active host operations, we must wait for them.
		if hEnv.HasLiveOps() {
			hEnv.Poll(ctx, mod)
		} else {
			// No host ops, but guest is not done? This typically means
			// the guest is stuck or we have a race.
			// For now, let's keep running but maybe add a small yield to avoid 100% CPU
			// if both sides are waiting for each other.
			runtime.Gosched()
		}
	}

	exitCode := 0
	if len(res) > 0 {
		exitCode = int(res[0])
	}
	return exitCode, nil
}

func tryRecoverFunctionName(err error, payload []byte) error {
	re := regexp.MustCompile(`invalid function\[(\d+)\]`)
	matches := re.FindStringSubmatch(err.Error())
	if len(matches) < 2 {
		return err
	}

	funcIdx, parseErr := strconv.Atoi(matches[1])
	if parseErr != nil {
		return err
	}

	name, found := findFunctionNameInWasm(payload, uint32(funcIdx))
	if found {
		return fmt.Errorf("%v (function name: %s)", err, name)
	}
	return err
}

func findFunctionNameInWasm(payload []byte, targetIdx uint32) (string, bool) {
	if len(payload) < 8 {
		return "", false
	}
	// Skip magic and version
	pos := 8
	for pos < len(payload) {
		sectionID := payload[pos]
		pos++
		size, n, err := readVarUint32(bytes.NewReader(payload[pos:]))
		if err != nil {
			break
		}
		pos += n
		sectionEnd := pos + int(size)
		if sectionEnd > len(payload) {
			break
		}

		if sectionID == 0 { // Custom section
			nameLen, n2, err := readVarUint32(bytes.NewReader(payload[pos:]))
			if err == nil {
				pos2 := pos + n2
				if pos2+int(nameLen) <= sectionEnd {
					sectionName := string(payload[pos2 : pos2+int(nameLen)])
					if sectionName == "name" {
						// Name section!
						pos3 := pos2 + int(nameLen)
						for pos3 < sectionEnd {
							if pos3 >= len(payload) {
								break
							}
							subID := payload[pos3]
							pos3++
							subSize, n3, err := readVarUint32(bytes.NewReader(payload[pos3:]))
							if err != nil {
								break
							}
							pos3 += n3
							subEnd := pos3 + int(subSize)
							if subEnd > sectionEnd || subEnd > len(payload) {
								break
							}

							if subID == 1 { // Function names subsection
								count, n4, err := readVarUint32(bytes.NewReader(payload[pos3:]))
								if err == nil {
									pos4 := pos3 + n4
									for i := uint32(0); i < count; i++ {
										if pos4 >= len(payload) {
											break
										}
										idx, n5, err := readVarUint32(bytes.NewReader(payload[pos4:]))
										if err != nil {
											break
										}
										pos4 += n5
										strLen, n6, err := readVarUint32(bytes.NewReader(payload[pos4:]))
										if err != nil {
											break
										}
										pos4 += n6
										if int(pos4+int(strLen)) > subEnd || int(pos4+int(strLen)) > len(payload) {
											break
										}
										if idx == targetIdx {
											return string(payload[pos4 : pos4+int(strLen)]), true
										}
										pos4 += int(strLen)
									}
								}
							}
							pos3 = subEnd
						}
					}
				}
			}
		}
		pos = sectionEnd
	}
	return "", false
}

func readVarUint32(r io.Reader) (uint32, int, error) {
	var res uint32
	var shift uint
	var count int
	for {
		var b [1]byte
		if n, err := r.Read(b[:]); err != nil || n == 0 {
			return 0, count, errors.New("unexpected EOF reading LEB128")
		}
		count++
		res |= uint32(b[0]&0x7F) << shift
		if b[0]&0x80 == 0 {
			break
		}
		shift += 7
		if shift >= 32 {
			return 0, count, errors.New("LEB128 overflow")
		}
	}
	return res, count, nil
}
