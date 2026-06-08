package main

import (
	"context"
	"fmt"
	"os"
	"runtime"

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

		// 1. Initial run to boot the guest and start the main future.
		res, err := runFunc.Call(ctx)
		if err != nil {
			if exitErr, ok := err.(*sys.ExitError); ok {
				return int(exitErr.ExitCode()), nil
			}
			return 1, fmt.Errorf("initial run failed: %w", err)
		}

		// 2. Event loop: poll for completions, then re-enter the guest.
		fmt.Printf("HOST: entering event loop\n")
		for {
			// If the guest indicated it's done (main future finished), we exit.
			if len(res) > 0 && res[0] != 0 {
				fmt.Printf("HOST: guest done\n")
				break
			}

			// If we have active host operations, we must wait for them.
			if hEnv.HasLiveOps() {
				hEnv.Poll(ctx, mod)
			} else if len(res) > 0 && res[0] == 0 {
				// No host ops, but guest is not done? This typically means
				// the guest is stuck or we have a race.
				// For now, let's keep running but maybe add a small yield to avoid 100% CPU
				// if both sides are waiting for each other.
				runtime.Gosched()
			} else {
				// We don't block in Poll here; we just re-run the guest.
			}

			res, err = runFunc.Call(ctx)
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
