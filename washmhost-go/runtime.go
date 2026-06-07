package main

import (
	"context"
	"fmt"
	"os"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/sys"
)

func RunWasm(ctx context.Context, payload []byte, args []string) (int, error) {
	if len(payload) == 0 {
		return 1, fmt.Errorf("payload is empty")
	}

	// 1. Setup Wazero using Compiler.
	rConfig := wazero.NewRuntimeConfigCompiler()
	// rConfig = rConfig.WithHostLogging(logging.LogScopeAll)
	r := wazero.NewRuntimeWithConfig(ctx, rConfig)
	defer r.Close(ctx)

	// We decode earlier than module compilation to detect imports
	decoded, err := r.CompileModule(ctx, payload)
	if err != nil {
		return 1, fmt.Errorf("failed to compile module: %w", err)
	}

	// 2. Detect Import flavor
	isRusticated := false

	for _, imp := range decoded.ImportedFunctions() {
		modName, _, _ := imp.Import()
		if modName == "env" {
			isRusticated = true
			break
		}
	}

	for i := 0; i < 10; i++ {
		// Just a hack to see if we can get info
	}
	// Note: wazero.CompiledModule doesn't expose table info easily here.
	// But we can check after instantiation.

	var hEnv *HostEnv

	if isRusticated {
		hEnv = NewHostEnv()
		if err := hEnv.Register(ctx, r); err != nil {
			return 1, fmt.Errorf("failed to register rusticated host env: %w", err)
		}
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

		// Boot the guest. For Go guests compiled with //go:wasmexport,
		// calling "run" initialises the Go runtime, starts main, and returns
		// when beforeIdle fires pause(). For Rust guests, "run" is the normal
		// entry point. Never call _start: it would run the whole program
		// synchronously and deadlock the host's event loop.
		_, err = runFunc.Call(ctx)
		if err != nil {
			if exitErr, ok := err.(*sys.ExitError); ok {
				return int(exitErr.ExitCode()), nil
			}
			return 1, fmt.Errorf("initial run failed: %w", err)
		}

		// Event loop: poll for completions, then re-enter the guest.
		// Terminates when there are no more outstanding ops (all I/O done
		// and all sched_pause wakeups consumed) or when the guest calls
		// process_exit (which calls os.Exit directly).
		for {
			if !hEnv.HasOutstandingOps() {
				break
			}

			hEnv.Poll(ctx, mod)

			_, err = runFunc.Call(ctx)
			if err != nil {
				if exitErr, ok := err.(*sys.ExitError); ok {
					return int(exitErr.ExitCode()), nil
				}
				return 1, fmt.Errorf("run failed: %w", err)
			}
		}

		return 0, nil
	}

	return 1, fmt.Errorf("module is not rusticated (missing 'env' imports)")
}
