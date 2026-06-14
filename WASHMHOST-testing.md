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

- list
- goes
- here

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

