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
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/sys"
)

func canonicalGuestEnvKey(key string) string {
	if runtime.GOOS == "windows" {
		switch strings.ToUpper(key) {
		case "PATH":
			return "PATH"
		case "PATHEXT":
			return "PATHEXT"
		case "LOCALAPPDATA":
			return "LocalAppData"
		case "APPDATA":
			return "AppData"
		case "USERPROFILE":
			return "UserProfile"
		case "TEMP":
			return "Temp"
		case "TMP":
			return "Tmp"
		case "HOMEDRIVE":
			return "HomeDrive"
		case "HOMEPATH":
			return "HomePath"
		case "ALLUSERSPROFILE":
			return "AllUsersProfile"
		case "PROGRAMFILES":
			return "ProgramFiles"
		case "PROGRAMFILES(X86)":
			return "ProgramFiles(x86)"
		case "COMMONPROGRAMFILES":
			return "CommonProgramFiles"
		case "COMMONPROGRAMFILES(X86)":
			return "CommonProgramFiles(x86)"
		case "COMSPEC":
			return "ComSpec"
		case "SYSTEMROOT":
			return "SystemRoot"
		}
	}
	return key
}

func lookupGuestEnvValue(env map[string]string, key string) (string, bool) {
	if env != nil {
		if v, ok := env[key]; ok {
			return v, true
		}
		for k, v := range env {
			if strings.EqualFold(k, key) {
				return v, true
			}
		}
	}
	return "", false
}

func expandWindowsGuestEnv(value string, env map[string]string) string {
	if value == "" {
		return value
	}
	for i := 0; i < 8; i++ {
		changed := false
		for start := 0; start < len(value); {
			begin := strings.Index(value[start:], "%")
			if begin < 0 {
				break
			}
			begin += start
			end := strings.Index(value[begin+1:], "%")
			if end < 0 {
				break
			}
			end += begin + 1
			name := value[begin+1 : end]
			if name == "" {
				start = end + 1
				continue
			}
			if replacement, ok := lookupGuestEnvValue(env, name); ok {
				value = value[:begin] + replacement + value[end+1:]
				changed = true
				start = begin + len(replacement)
				continue
			}
			start = end + 1
		}
		if !changed {
			break
		}
	}
	return value
}

func guestEnvForWasm(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	ordered := make([]string, 0, len(env))
	values := map[string]string{}
	seen := map[string]bool{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		key := canonicalGuestEnvKey(k)
		if !seen[key] {
			seen[key] = true
			ordered = append(ordered, key)
		}
		values[key] = v
	}
	for i := 0; i < 8; i++ {
		changed := false
		for _, key := range ordered {
			expanded := expandWindowsGuestEnv(values[key], values)
			if expanded != values[key] {
				values[key] = expanded
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	out := make([]string, 0, len(ordered))
	for _, key := range ordered {
		out = append(out, key+"="+values[key])
	}
	return out
}

func RunWasm(ctx context.Context, payload []byte, args []string) (int, error) {
	if len(payload) == 0 {
		return 1, fmt.Errorf("payload is empty")
	}

	// 1. Setup Wazero using default config (picks best engine).
	rConfig := wazero.NewRuntimeConfig()
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
	hEnv.args = args
	if err := hEnv.Register(ctx, r); err != nil {
		return 1, fmt.Errorf("failed to register rusticated host env: %w", err)
	}

	// 3. Instantiate
	// Apply args and environment directly to Wazero Config.
	// Windows often exposes PATH/PATHEXT as Path/Pathext, but the guest runtime
	// expects canonical names and uses shell semantics that are case-insensitive
	// there; normalize them so command lookup receives the expected values.
	cfg := wazero.NewModuleConfig().
		WithArgs(args...).
		WithStdout(os.Stdout).
		WithStderr(os.Stderr).
		WithStdin(os.Stdin)

	for _, env := range guestEnvForWasm(os.Environ()) {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 && parts[0] != "" {
			cfg = cfg.WithEnv(parts[0], parts[1])
		}
	}

	cfg = cfg.WithFSConfig(wazero.NewFSConfig().
		// TODO: WASI mounting is redundant, remove it.
		WithDirMount(".", "/").
		WithDirMount("C:\\", "C:\\").
		WithDirMount(os.TempDir(), "/tmp"))

	// Since we provide rusticated ABI bindings via `hEnv.Register`, Wazero will resolve imports
	mod, err := r.InstantiateModule(ctx, decoded, cfg)
	if err != nil {
		// Module might exit with specific exit code (e.g. WASI exit)
		if exitErr, ok := err.(*sys.ExitError); ok {
			return int(exitErr.ExitCode()), nil // Normal exit flow
		}
		return 1, fmt.Errorf("failed to instantiate module: %w", err)
	}
	hEnv.mod = mod

	// 4. Drive completion
	runFunc := mod.ExportedFunction("run")
	if runFunc == nil {
		return 1, fmt.Errorf("rusticated module missing 'run' export")
	}

	// 2. Event loop: poll for completions, then re-enter the guest.
	var res []uint64
	for {
		for _, mgr := range hEnv.DylibManagersForPolling() {
			mgr.BeforeRun(ctx, mod)
		}
		res, err = runFunc.Call(ctx)
		if err != nil {
			if exitErr, ok := err.(*sys.ExitError); ok {
				return int(exitErr.ExitCode()), nil
			}
			return 1, fmt.Errorf("run failed: %w", err)
		}
		for _, mgr := range hEnv.DylibManagersForPolling() {
			mgr.AfterRun(ctx, mod)
		}

		hEnv.mu.Lock()
		forcedCode := hEnv.forcedExitCode
		hEnv.mu.Unlock()

		// 1. Immediate override via process_exit
		if forcedCode != -1 {
			return int(forcedCode), nil
		}

		// 2. Derive completion fact entirely from host state (no active operations)
		if !hEnv.HasActiveOps() {
			code := int32(0)
			if len(res) > 0 {
				code = int32(res[0])
				if code == -1 {
					// We are finishing because there are no more ops,
					// so we treat "pending" as "finished with success"
					// in the absence of any other information.
					code = 0
				}
			}
			return int(code), nil
		}

		hEnv.Poll(ctx, mod)
	}
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
