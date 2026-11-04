# Ejecting a Running Command into the Background

Status: implemented in `kabibi/ui/shell` and the generated mvdan/sh JIT patch.
The remaining behavioral limits and open UI decisions are listed below.

## 1. Goal

The agent's `exec` tool runs inline in the user's interactive shell. Its `cd`,
`export`, `alias`, `pushd`, function definitions and option changes affect the
user exactly as if the user had typed the command.

When an agent command lingers past its timeout, it is **ejected**:

1. The command keeps running unchanged, but in the background.
2. The interactive shell becomes idle at once and accepts the next command.
3. The new foreground shell looks exactly like the old one did at the moment of
   ejection.
4. Nothing the background job does afterwards affects the interactive shell.
5. The job is reachable only by its id, through `term_read`, `term_input` and
   `term_kill`. It is never reused by a later `exec`.

## 2. Principle: move the interpreter out, seed a copy in

mvdan/sh cannot move a running script from one `Runner` to another. So ejection
goes the other way round:

1. The `Runner` that is executing the command (`es.runner`) leaves the session
   whole and becomes the background job's interpreter. Its goroutine is never
   interrupted, cancelled or stopped.
2. `es.runner` is replaced by a copy of the old interpreter's user-visible state,
   taken with `Runner.Subshell()`.

In the `rusticated-jit` build of mvdan, `Subshell()` makes an independent copy:
variables flattened, functions, aliases, the `pushd` stack, options, `$?` and
positional parameters
(`mohabbat/rusticated-jit/mvdan.cc/sh/v3/interp/api.go`, `subshell(true)`). It
takes no locks and writes nothing back to the parent, so it cannot deadlock.

The copy is made **only when an eject happens**, never for ordinary commands. The
per-command cost is one flag write under `runMu`.

## 3. When the copy is safe and correct

A copy of `es.runner` is safe only while nothing is modifying it, and correct only
when its state is what the user would see after the command. Both hold when:

1. **The interpreter is at a handler safe point.** It is inside our exec handler,
   waiting for an external command or a Kabibi builtin, or inside the call
   handler before a simple command. It touches no state until the handler
   returns.
2. **The call is on a synchronous chain from `es.runner`.** A pipeline stage,
   `&` job or process substitution can run concurrently with `es.runner`, so a
   call on that chain cannot safely copy it. Synchronous subshells such as
   `( … )` and `$( … )` do leave `es.runner` paused, even though the handler runs
   on another `Runner`.
3. **Temporary command and function state is removed from the copy.** For
   `FOO=x cmd`, mvdan temporarily changes `FOO` and restores it after the call.
   Inside a function it also temporarily changes positional parameters and
   creates a local-variable scope. A copy made mid-call must represent the
   top-level state, not those temporary values.

These conditions require a small JIT patch (§5): the public API does not expose
the interpreter ancestry or the temporary state needed to make this decision
and construct the right copy.

## 4. Run state and admission (rework of the current `beginRun`/`endRun`)

1. `beginRun(desc)` returns a `*runState` token instead of only setting
   `es.running`. The token holds:
   - `desc` and the job id, if any;
   - `away bool`;
   - `ejectRequested bool`;
   - `ejected bool`;
   - the job's output target;
   - a cancel function for the job's context.
2. `es.current` points at the admitted token. `running` is equivalent to
   `es.current != nil`.
3. `endRun(st)` clears `es.current` only if it still equals `st`.
   - An ejected run must never clear the admission of the command that now owns
     the foreground.
   - This is a correctness fix that the current code lacks.
4. Every field of the token, `es.current`, `es.runner`, `es.streamOut` and
   `es.ttyIn` is read and written under `runMu`. `runMu` is only ever held for a
   few assignments or for the single `Subshell()` copy, never while a command
   runs.
5. Re-entry still fails fast with `ErrShellBusy`.
6. The token reaches the handlers through the context passed to `Runner.Run`, the
   same way `withExecSession` passes the session today. After an eject, the
   handlers of the background job can therefore tell that they belong to an
   ejected job.

## 5. JIT patch to mvdan (`patchMvdanEjectInMemory`)

Add it in `mohabbat/patch_go_deps.go` next to `patchMvdanKillBuiltinInMemory` and
`patchMvdanUmaskBuiltinInMemory`, following the same rule: return an error, so the
build fails, if a pattern does not match exactly once.

1. **Track interpreter ancestry.** Add `parent *Runner` and `concurrent bool` to
   `Runner`. In `subshell(background)`, set the child's parent to `r` and its
   `concurrent` flag to `background`. Add
   `func (hc HandlerContext) PausedIn(r *Runner) bool` in a new
   `interp/kabibi_eject.go`. It walks from `hc.runner` toward its parents:
   - return true if it reaches `r` without crossing a concurrent child;
   - return false if it encounters a concurrent child first or reaches the end.

   This distinguishes a synchronous nested subshell from a child that may be
   running concurrently. It does not expose the private runner pointer to Kabibi.
2. **Record prefix-assignment restorations.** In the `CallExpr` branch around
   `r.call(...)`, push the existing `restores` slice onto a new
   `transientRestores` stack before the call and pop it afterwards. Move
   `restoreVar` from its current function-local declaration to package scope so
   the stack can use it. When making the foreground copy, apply pending
   restorations in reverse order to that copy. For example, ejecting during
   `GOOS=js go build` must not leave the temporary `GOOS=js` in the new shell.
3. **Record function frames.** Before `call()` replaces `Params` and adds the
   function's local-variable overlay, push the old `Params` and `writeEnv` on a
   frame stack; pop them after the function body returns. If eject happens
   inside a function, construct the new foreground copy from the outermost
   frame's saved parameters and environment. Global changes made by the function
   remain visible through that environment; its locals and function arguments
   do not leak into the new shell.
4. **Add `func (r *Runner) Detach() *Runner`.** It makes the independent
   `subshell(true)` copy from the state selected by items 2 and 3, plus:
   - copy `callbackExit` and `callbackErr` (traps), which `subshell` drops;
   - fill `Vars` from the copy's environment (`maps.Insert(r2.Vars,
     r2.writeEnv.Each)`), so `EnvMap()` and command resolution see the full
     state before the copy has run;
   - clear the copy's parent/ancestry metadata, since it becomes the new
     foreground root.
5. **Patch tests** must fail the build if the expected `subshell(bool)`,
   `CallExpr` restoration block, function-call scope setup, or trap fields no
   longer match. Tests must also verify `PausedIn` for nested synchronous and
   concurrent children.

## 6. Eject protocol

### 6.1 Going away (exec handler)

The outermost exec middleware wraps every external command and every Kabibi
builtin:

1. If the handler context belongs to no admitted token (for example
   `ExecuteOneShotAITool`), run the inner handler unchanged.
2. Otherwise, under `runMu`, set `st.away = true` only if all of these hold:
   - `hc.PausedIn(es.runner)`;
   - `!st.ejected`.
3. Run the inner handler, which is the real command, without holding any lock.
4. Under `runMu`, set `st.away = false`.
   - If the token was ejected meanwhile, nothing else changes: the job simply
     continues on the old `Runner`, which no longer belongs to the session.
   - If it was not ejected, the interpreter carries on as the foreground shell.

Step 4 must take `runMu`. That ordering guarantees the interpreter cannot resume
while an eject is copying it.

### 6.2 Eject (`es.eject(st)`, called from the `exec` timeout)

Under `runMu`:

1. If `st` is not `es.current` (it already finished or was already ejected), do
   nothing.
2. If `st.away` is set, perform the swap (6.3) immediately.
3. Otherwise set `st.ejectRequested = true` and return. The swap happens at the
   next safe point (6.4).

`eject` never waits.

### 6.3 The swap (always under `runMu`)

1. `copy := es.runner.Detach()`.
2. Give the copy fresh stdio through the public `interp.StdIO`:
   - a new closed-pipe stdin, which becomes the new `es.ttyIn`;
   - a new `dynamicWriter`, which becomes the new `es.streamOut`.
3. Point the old `dynamicWriter`, still held by the job and its child processes, at
   the job's output buffer (`ManagedPTY.appendOutput`).
4. Set `es.runner = copy`.
5. Set `st.ejected = true` and `es.current = nil`. The shell is now idle.
6. Notify the UI that the shell prompt is free and that the block now belongs to
   background job `#id`.

`StreamTTY` and `IsUnredirectedTTY` already compare stream pointers, so after the
swap they correctly report the job as no longer attached to the user's terminal.
They must read `es.ttyIn` and `es.streamOut` under `runMu` (or atomically), since
those pointers can now change.

### 6.4 Requested eject (call handler)

The call handler runs before every simple command, including builtins and
function calls. If `st.ejectRequested` is set, it does the following under
`runMu`:

1. If `hc.PausedIn(es.runner)`, perform the swap (6.3). The current call handler
   is a safe point: the interpreter is waiting for it to return.
2. Otherwise leave the request pending.

When a pending eject actually takes effect:
- **Inside `$( … )` or `( … )`:** it can take effect in that subshell; the copy
  is made from the paused `es.runner`, so the subshell's private changes are
  excluded.
- **Inside a function:** it can take effect there; `Detach()` uses the saved
  outer function frame to exclude locals and function positional parameters.
- **During `wait` on `&` jobs:** after the wait returns.
- **In a loop with no simple commands**, such as `while ((1)); do ((i++)); done`:
  never, until the loop runs a simple command. These cases need an `stop(ctx)`
  hook in the JIT patch; that is left out for now.

While an eject is pending, the shell stays busy. The `ErrShellBusy` message says
`exec #N is being moved to the background`.

### 6.5 Finishing at the same moment as eject

`Run` returning and `eject` both decide under `runMu`, so exactly one wins:

1. **The run finished first:** `endRun(st)` clears `es.current`, and `eject`
   finds `st != es.current` and does nothing. The command's effects stay in the
   shell, exactly as for a command that completed within the timeout.
2. **The eject won:** the code after `Run` in `RunCommand` and
   `runManagedExecution` sees `st.ejected`. It must then:
   - not call `es.streamOut.setTarget(nil)` on the session's new writer;
   - not append a foreground history record;
   - finish the job with `markExited` and record its final cwd from the job's own
     `Runner.Dir` (read on the job goroutine), not `es.Dir()`.

## 7. The direct PTY path

`runManagedExecution` can run a single simple command directly in a PTY,
bypassing the interpreter.

1. The interpreter is not involved, so ejecting needs no copy: the swap only clears
   `es.current`. `es.runner` is already the foreground state.
2. **Fix:** today `endRun()` is called before the process starts, so the user can
   type while an agent's foreground command runs. Admission must be held until the
   process exits or is ejected.

## 8. Cancellation (`term_kill`)

1. Each admitted run gets its own cancellable context, stored in the token.
   `runManagedExecution` stops using `context.Background()`.
2. `ManagedPTY.Kill` cancels that context:
   - mvdan checks `ctx.Err()` at every statement, command and loop iteration;
   - `DefaultExecHandler` sends SIGINT, then SIGKILL, to external processes.
3. Kabibi builtins must honour `ctx`. A builtin that ignores cancellation remains
   unkillable; that is a defect in the builtin.
4. Killing does not require ejecting. Killing a job in the foreground ends it, and
   its effects up to that point stay in the shell.

## 9. Rules for Kabibi builtins

Builtins run while the interpreter is away, so after an eject they belong to the
background job:

1. They must take cwd, environment and stdio from `hc` (`hc.Dir`, `hc.Env`,
   `hc.Stdout`), never from `es.Dir()`, `es.EnvMap()` or the UI sink. Audit:
   - `aiBuiltinMiddleware`;
   - `git`;
   - `web`/`cdp`;
   - `git_mutate`, which calls `es.RunCommand` and already fails with
     `ErrShellBusy` when re-entered.
2. They must not call `hc.Builtin` for state-changing builtins while away. Today
   Kabibi never calls `hc.Builtin`.

## 10. Known limitations

1. **Loops with no simple commands:** a requested eject waits until the loop runs
   one (6.4).
2. **Conservative ancestry check:** a concurrent child anywhere in the caller's
   ancestry makes `PausedIn` return false, even if an enclosing synchronous
   subshell currently has the main interpreter blocked. For example, an eject
   from the first stage of a pipeline inside `( … )` is deferred; the last stage
   still runs on the synchronous chain and can service the request. A blocking
   builtin such as `read` or `wait` has no handler point while it is blocked, so
   an eject request cannot take effect until it returns.
3. **Background `&` jobs started by the command** stay with the ejected job,
   matching bash job semantics.
4. **`$!`, `jobs` and `wait`** in the new foreground shell do not see `&` jobs
   started before the eject.

## 11. Open decisions

1. **Who can eject.** This spec covers only the agent `exec` timeout. Letting the
   user eject their own long command with a key works the same way (`es.eject` on
   the current token). Should it be included now?
2. **The background job's output in the UI.** Should an ejected agent job's block
   stay in the scrollback, marked as a background job, or move to a jobs panel?

## 12. Tests

Behavior-level coverage lives in `kabibi/ui/shell/exec_session_eject_test.go`;
the patch generator has a separate test in `mohabbat/patch_go_deps_test.go`.
The tests cover interpreter ancestry across functions, synchronous subshells,
command substitutions, pipelines, process substitutions and background jobs;
detached state for prefix assignments, function locals/parameters, traps,
aliases, directory stack, options and global variables; and actual ejection
during a function, synchronous subshell, command substitution and unsafe
pipeline wait. They also
verify that the foreground accepts a new command, the ejected job continues
with its own state, `term_kill` cancels an interpreted loop, and direct-PTY
admission is released on eject.

The helper-process and PTY integration tests require a native OS and skip under
`wasip1`. Process-substitution ancestry is skipped there because mvdan/sh's
non-Unix implementation does not support creating the required FIFO. The
hot-loop `term_kill` test runs under Rusticated too; the JIT patch makes the
interpreter yield to the async timer periodically on `wasip1`, allowing timeout
and cancellation to be observed without blocking I/O. The finish-versus-eject
race still lacks deterministic test hooks; the pipeline and admission tests
exercise the observable behavior but do not force both race orderings.
