# WASHMHOST Testing

We are targeting three specific test boundaries, each requiring distinct setup methods:

```
[ Tier 3: E2E Integration ]  --> Runs actual compiled .wasm binaries (demo, trivial) via wazero.
          │
[ Tier 2: Behavioral Contract ] -> Uses t.TempDir() & local loops to verify side-effects against real OS.
          │
[ Tier 1: Isolated Unit ]    --> In-memory tests for pointer translation and register parsing.

```

## Tier 1: Isolated Unit Testing

Make sure unit tests are fully isolated. The standard Go unit tests conventions should be followed, and the coverage should include all the ABIs and all the major functions, plus any functions that are important to be tested.

### Unit tests expected (approximate list)

Each of the ABI functions should have at least 2-5 unit tests, and the internal helper functions should have at least 1-7 unit tests. The following table lists the expected functions and their corresponding unit test targets.

| Function Name | Signature | Description | Defined In | ABI | Unit Test Target |
| :--- | :--- | :--- | :--- | :---: | :---: |
| `createAbiStat` | `(fi os.FileInfo) AbiStat` | Converts OS file info to guest `AbiStat`. | env_fs.go | No | **Yes (1-7 tests)** |
| `marshalAbiStat` | `(stat AbiStat) []byte` | Serializes `AbiStat` to little-endian bytes. | env_fs.go | No | **Yes (1-7 tests)** |
| `sys_read` | `(ctx context.Context, m api.Module, stack []uint64)` | Asynchronous file/handle read. | env_fs.go | **Yes** | **Yes (2-5 tests)** |
| `sys_write` | `(ctx context.Context, m api.Module, stack []uint64)` | Asynchronous file/handle write. | env_fs.go | **Yes** | **Yes (2-5 tests)** |
| `sys_handle_close` | `(ctx context.Context, m api.Module, stack []uint64)` | Closes a registered handle. | env_fs.go | **Yes** | **Yes (2-5 tests)** |
| `sys_path_open` | `(ctx context.Context, m api.Module, stack []uint64)` | Asynchronous file/directory open. | env_fs.go | **Yes** | **Yes (2-5 tests)** |
| `sys_dir_read` | `(ctx context.Context, m api.Module, stack []uint64)` | Asynchronous directory enumeration. | env_fs.go | **Yes** | **Yes (2-5 tests)** |
| `sys_path_stat` | `(ctx context.Context, m api.Module, stack []uint64)` | Asynchronous metadata retrieval. | env_fs.go | **Yes** | **Yes (2-5 tests)** |
| `sys_path_chmod` | `(ctx context.Context, m api.Module, stack []uint64)` | Synchronous permission update. | env_fs.go | **Yes** | **Yes (2-5 tests)** |
| `sys_get_cwd` | `(ctx context.Context, m api.Module, stack []uint64)` | Retrieves working directory. | env_fs.go | **Yes** | **Yes (2-5 tests)** |
| `sys_set_cwd` | `(ctx context.Context, m api.Module, stack []uint64)` | Updates working directory. | env_fs.go | **Yes** | **Yes (2-5 tests)** |
| `NewHostEnv` | `() *HostEnv` | Constructor and signal multiplexer. | env_impl.go | No | **Yes (1-7 tests)** |
| `IncOps` | `()` | Increment operation counter. | env_impl.go | No | No |
| `RegisterOp` | `(ovPtr uint32, handle interface{}) *OpState` | Register async operation. | env_impl.go | No | **Yes (1-7 tests)** |
| `registerOpLocked` | `(ovPtr uint32, handle interface{}) *OpState` | Mutex-locked internal registration. | env_impl.go | No | No |
| `IsOpActive` | `(ovPtr uint32, id uint64) bool` | Thread-safe operation validity check. | env_impl.go | No | **Yes (1-7 tests)** |
| `isOpActiveLocked` | `(ovPtr uint32, id uint64) bool` | Internal mutex-locked check. | env_impl.go | No | No |
| `DecOps` | `()` | Decrement counter using CAS loop. | env_impl.go | No | **Yes (1-7 tests)** |
| `PendingOps` | `() int32` | Returns active job count. | env_impl.go | No | No |
| `HasOutstandingOps` | `() bool` | Boolean check for outstanding jobs. | env_impl.go | No | No |
| `HasLiveOps` | `() bool` | Verification of uncancelled jobs. | env_impl.go | No | No |
| `CancelOp` | `(ovPtr uint32)` | Abortion of pending operations. | env_impl.go | No | **Yes (1-7 tests)** |
| `Register` | `(ctx context.Context, r wazero.Runtime) error` | Wazero host function registration. | env_impl.go | No | No |
| `Poll` | `(ctx context.Context, mod api.Module) bool` | The proactor drive loop. | env_impl.go | No | **Yes (1-7 tests)** |
| `sys_time_now` | `(ctx context.Context, m api.Module, stack []uint64)` | Timestamp retrieval. | env_os.go | **Yes** | **Yes (2-5 tests)** |
| `sys_get_time` | `(ctx context.Context, apiMod api.Module, stack []uint64)` | Wall-clock time retrieval. | env_os.go | **Yes** | **Yes (2-5 tests)** |
| `sys_get_random` | `(ctx context.Context, m api.Module, stack []uint64)` | Secure random hydration. | env_os.go | **Yes** | **Yes (2-5 tests)** |
| `sys_yield` | `(ctx context.Context, m api.Module, stack []uint64)` | Voluntary preemption. | env_os.go | **Yes** | **Yes (2-5 tests)** |
| `sys_pause` | `(ctx context.Context, m api.Module, stack []uint64)` | Parking logic. | env_os.go | **Yes** | **Yes (2-5 tests)** |
| `sys_timer_set` | `(ctx context.Context, m api.Module, stack []uint64)` | Async timer setup. | env_os.go | **Yes** | **Yes (2-5 tests)** |
| `sys_timer_cancel` | `(ctx context.Context, m api.Module, stack []uint64)` | Timer abortion. | env_os.go | **Yes** | **Yes (2-5 tests)** |
| `sys_panic` | `(ctx context.Context, m api.Module, stack []uint64)` | Immediate guest abort. | env_os.go | **Yes** | **Yes (2-5 tests)** |
| `sys_process_spawn` | `(ctx context.Context, m api.Module, stack []uint64)` | Child process execution. | env_proc.go | **Yes** | **Yes (2-5 tests)** |
| `sys_process_wait` | `(ctx context.Context, m api.Module, stack []uint64)` | Waiting for process exit. | env_proc.go | **Yes** | **Yes (2-5 tests)** |
| `sys_process_signal` | `(ctx context.Context, m api.Module, stack []uint64)` | Signals child processes. | env_proc.go | **Yes** | **Yes (2-5 tests)** |
| `sys_signal_wait` | `(ctx context.Context, m api.Module, stack []uint64)` | Parking until host signal. | env_proc.go | **Yes** | **Yes (2-5 tests)** |
| `sys_cancel` | `(ctx context.Context, m api.Module, stack []uint64)` | Entry for `CancelOp`. | env_os.go | **Yes** | **Yes (2-5 tests)** |
| `sys_process_exit` | `(ctx context.Context, m api.Module, stack []uint64)` | Instance termination. | env_proc.go | **Yes** | **Yes (2-5 tests)** |
| `sys_net_open` | `(ctx context.Context, m api.Module, stack []uint64)` | Async TCP dial/listen. | env_net.go | **Yes** | **Yes (2-5 tests)** |
| `sys_net_accept` | `(ctx context.Context, m api.Module, stack []uint64)` | Async TCP accept. | env_net.go | **Yes** | **Yes (2-5 tests)** |
| `mapErrno` | `(err error) uint32` | Translation table. | env_utils.go | No | **Yes (1-7 tests)** |
| `resolveUsableCwd` | `() (string, error)` | Fallback project root logic. | env_utils.go | No | **Yes (1-7 tests)** |
| `debugLog` | `(format string, args ...interface{})` | Atomic debug logger. | env_utils.go | No | No |
| `writeOverlapped` | `(mod api.Module, ovP uint32, err uint32, c uint64, r uint64)` | Completion signaling utility. | env_utils.go | No | **Yes (1-7 tests)** |
| `ensurePosixOutputRunnable` | `(args []string)` | Set `+x` flags utility. | main.go | No | No |
| `main` | `()` | Binary entry point. | main.go | No | No |
| `RunWasm` | `(ctx context.Context, payload []byte, args []string) (int, error)` | Runtime lifecycle drive. | runtime.go | No | **Yes (1-7 tests)** |
| `tryRecoverFunctionName` | `(err error, payload []byte) error` | Error enhancer logic. | runtime.go | No | **Yes (1-7 tests)** |
| `findFunctionNameInWasm` | `(payload []byte, targetIdx uint32) (string, bool)` | Hand-written section parser. | runtime.go | No | **Yes (1-7 tests)** |
| `readVarUint32` | `(r io.Reader) (uint32, int, error)` | LEB128 decoder utility. | runtime.go | No | **Yes (1-7 tests)** |

## Tier 2: Behavioral Contract Testing

These tests execute your underlying filesystem manipulation methods (`sys_open`, `sys_readdir`) against the real underlying OS system call layer to verify path validation logic and state synchronization.

### No major changes

The tested production code is NOT expected to undergo any major changes or refactoring. The tests are there to test, not to modify production code.

### Behavioural tests expected (approximate list)

- **Verify_Sandbox_Escapement_Jailing**: Exercises `ResolvePhysicalPath` against a real filesystem using `t.TempDir()`, validating that `../`, `/etc/passwd`, and UNC paths are strictly blocked.
- **Verify_Overlapped_Async_Writeback**: Confirms the host can write `result_ext` and `continued` values back to a specific `Overlapped` structure in guest memory while the worker is active.
- **Verify_Read_Write_Proactor_Lifecycle**: Full-trip behavioral check for file creation (`sys_open`), writing data, and closing handles using the async operation queue.
- **Verify_Directory_Scan_Stateful_Contract**: Tests `sys_readdir` behavior, ensuring it correctly handles directory paging and preserves the `DirScan` state across multiple continuation calls.
- **Verify_Async_Operation_Concurrency_Pressure**: Spawns thousands of concurrent I/O operations to verify the host-side `fileOpsQueue` and completion dispatcher stability.
- **Verify_Physical_Timer_Deadline_Precision**: Measures the drift between guest-requested `sched_pause` nanoseconds and actual host re-entry timing.
- **Verify_File_Metadata_Attribute_Persistence**: Validates that setting `AbiStat` fields (modified_ns, created_ns) via the host ABI correctly updates the underlying OS metadata.
- **Verify_Stdout_Stderr_Stream_Capture**: Confirms that synchronous and asynchronous writes to handles `1` and `2` are correctly buffered and flushed to the host's physical streams.


## Tier 3: Integration Testing

This tier builds your existing guest demo targets directly into WebAssembly format, configures a clean host runtime environment, and validates correct execution loops.

### Validate Dependencies

Before execution, your test pipeline must build the required project to ensure that the compiled `.wasm` targets exist. You can leverage a Go `TestMain` entry point or use raw shell scripting inside the test code via `os/exec` to compile the targets on demand.

### Integration tests expected (approximate list)

- **TestIntegration_Rusticated_Bootstrap**: Verifies the Go "Cold Boot" entry point (`initialized == 0`), ensuring the runtime hydrates memory and runs `init()` functions correctly before the first return.
- **TestIntegration_Async_Reentry_State_Machine**: Executes a full E2E cycle where a guest goroutine calls `gopark`, the host performs I/O, and then re-enters the guest via the `run` export to resume.
- **TestIntegration_Interactive_CLI_Echo**: Boots the production `demo-go` target, pipes in test strings to stdin, and verifies the resulting stdout/stderr interactive feedback loop.
- **TestIntegration_Preemption_Constraint_Enforcement**: Confirms the host proactor can strictly terminate a guest session that exceeds configured time or memory limits.
- **TestIntegration_Memory_Growth_Stability**: Executes a memory-intensive guest application to verify that `memory.grow` events are handled without corrupting the host's pointer tracking.
- **TestIntegration_Panic_Diagnostic_Propagation**: Verifies that a guest-side Go `panic` or unhandled exception is caught, formatted, and results in a non-zero host exit code.
- **TestIntegration_Shared_Memory_Completion_Ordering**: Rigorous check to ensure the host flips `FLAG_COMPLETED` only after all payload data has been fully synchronized to guest memory.
- **TestIntegration_Environment_Integrity**: Broad check for `ARGS`, `ENV`, and root-level Preopened directory visibility inside the guest standard library.


## Detailed Implementation Steps

To deploy this test suite systematically without disrupting ongoing runtime changes, follow this execution sequence:

1. **Step 1: Code Splitting Prerequisites** The washmhost-go project is already split and segmented as required, and NO further changes are expected. Write the tests to respect that.
2. **Step 2: Establish the Unit Tests** Add unit test files. Run `go test -v` to verify they are running.
3. **Step 3: Establish Behavioral Checks** Add behavioural test files. Hook it into a subdirectory inside target directory IN THE ROOT OF REPOSITORY (not target inside washmhost-go).
4. **Step 4: Connect the E2E Pipeline** Add integration test files. Verify that local Go or Rust compiler toolchains are present in your test environment path variables so the execution routines can build the `.wasm` binaries correctly.
5. **Step 5: Run the Unified Verification Script** Execute the entire pipeline seamlessly using standard Go tooling:

```bash
go -C washmhost-go test -v
```

