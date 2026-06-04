package main

import (
	"context"
	"fmt"
	"os"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
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
		return 1, fmt.Errorf("failed to compile module: %w", err)
	}

	// 2. Detect Import flavor
	isRusticated := false
	isWasi := false

	for _, imp := range decoded.ImportedFunctions() {
		modName, _, _ := imp.Import()
		if modName == "env" {
			isRusticated = true
		} else if modName == "wasi_snapshot_preview1" {
			isWasi = true
		}
	}

	var hEnv *HostEnv

	if isRusticated {
		hEnv = NewHostEnv()
		if err := hEnv.Register(ctx, r); err != nil {
			return 1, fmt.Errorf("failed to register rusticated host env: %w", err)
		}
	}

	if isWasi {
		wasi_snapshot_preview1.MustInstantiate(ctx, r)
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
	if isRusticated {
		runFunc := mod.ExportedFunction("run")
		if runFunc == nil {
			return 1, fmt.Errorf("rusticated module missing 'run' export")
		}

		// Initial start if not done by instantiation
		_start := mod.ExportedFunction("_start")
		if _start != nil && !isWasi {
			_, err := _start.Call(ctx)
			if err != nil {
				return 1, fmt.Errorf("_start failed: %w", err)
			}
		} else if _start == nil {
			// Rust guest entry
			_, err = runFunc.Call(ctx)
			if err != nil {
				return 1, fmt.Errorf("initial run failed: %w", err)
			}
		}

		// Event loop: continue as long as there is outstanding work.
		for {
			// If no more outstanding ops, we are finished.
			if !hEnv.HasOutstandingOps() {
				break
			}

			// Drive the host ops polling here - this fills the queue
			hEnv.Poll(ctx, mod)

			// Re-enter the guest to process any completions
			_, err = runFunc.Call(ctx)
			if err != nil {
				return 1, fmt.Errorf("run failed: %w", err)
			}
		}

		return 0, nil
	} else if isWasi {
		// WASI runs to completion at `InstantiateModule` implicitly invoking _start if configured
		// But just in case _start wasn't invoked implicitly:
		_start := mod.ExportedFunction("_start")
		if _start != nil {
			_, err := _start.Call(ctx)
			if err != nil {
				if exitErr, ok := err.(*sys.ExitError); ok {
					return int(exitErr.ExitCode()), nil
				}
				return 1, fmt.Errorf("_start failed: %w", err)
			}
		}
		return 0, nil
	} else {
		// Try a basic run just in case
		runFunc := mod.ExportedFunction("run")
		if runFunc != nil {
			runFunc.Call(ctx)
		}
		return 0, nil
	}
}
