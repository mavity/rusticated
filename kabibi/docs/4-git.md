# Dual Git Architecture in Kabibi

At this point, it is a **purely technical, bounded engineering task** rather than an architectural or research gamble.

All the foundational hard problems in Kabibi are already solved and running at alpha stability at least:

* **WASM / OS Portability:** `rusticated-go` (on Go 1.27.1) already maps goroutines, file I/O, and network sockets cleanly onto the async `washmhost` / `mohab.bat` runtime.


* **Shell & Builtin Dispatch:** `kabibi/ui/shell` already runs a persistent `mvdan.cc/sh/v3/interp.Runner` with `u-root` coreutils builtins (`moreinterp/coreutils`) and `ClassifyInput` command routing.


* **PTY & Virtual Terminal Rendering:** `rustica/pty` (ConPTY, `/dev/ptmx`, and `pty_rusticated.go`) and the unified `ui.Host` / `terminal.CellBuf` diff engine (`terminal.FromANSI` / `ToANSI`) are already live.


* **AI & Embeddings:** `ai.Session`, declarative tool schema compilation with auto-injected `"explanation"`, `deliberate` (via `litert_lm_conversation_clone`), and `EmbeddingGemma 2` background vector indexing are all in place.



Because those pillars exist, implementing the dual Git layer (and the rest of the upgraded tool suite) is straightforward Go plumbing with zero new FFI or WASM ABI changes required.

---

### How the Dual Git Architecture Fits Together

You only pull in **`[github.com/go-git/go-git/v5](https://github.com/go-git/go-git/v5)`** once, and wrap it in a shared internal package (`kabibi/gitcore`) that serves two distinct frontends:

```text
                   ┌─────────────────────────────────────────┐
                   │             kabibi/gitcore              │
                   │  (Pure-Go go-git/v5 + go-billy osfs)    │
                   │  • Fast index/worktree status & diff    │
                   │  • Commit, stage, branch, log, blame    │
                   │  • SSH / HTTPS transport via net/http   │
                   └───────────┬─────────────────┬───────────┘
                               │                 │
         ┌─────────────────────┘                 └─────────────────────┐
         ▼                                                             ▼
┌─────────────────────────────────────────┐   ┌─────────────────────────────────────────┐
│ 1. Shell Builtin: `git` (Human & Script)│   │ 2. AI Tools: `git_inspect` & `git_mutate`│
│ • Registered in `mvdan.cc/sh` builtin   │   │ • Registered in `ai.SessionConfig.Tools`│
│   table alongside u-root coreutils      │   │ • High-semantics, low-token formatting  │
│ • Standard CLI flags (`status -s`,      │   │ • Injects `LINE:HASH|` anchors directly │
│   `diff`, `log -n 5`, `commit -am ...`) │   │   into diff/blame for `edit_file`       │
│ • Streams ANSI to `hc.Stdout/Stderr`    │   │ • Uses EmbeddingGemma 2 for log/diff    │
│ • 100% portable when host git is absent │   │   concept search                        │
└─────────────────────────────────────────┘   └─────────────────────────────────────────┘

```

---

### The Only 4 Technical Details to Watch During Implementation

While there are no architectural blockers, there are four technical nuances to handle cleanly so the implementation feels rock-solid:

1. **Mounting `es.Dir()` Correctly in `go-git`:**
In `mvdan.cc/sh`, `cd` changes `runner.Dir` (tracked by `es.Dir()`), not the OS process's global working directory. `gitcore.OpenRepo(es.Dir())` must walk upward from `es.Dir()` to find `.git` and open the repository using `osfs.New(repoRoot)` so both the `git` builtin and the AI tools always respect the shell's live working directory.


2. **Defining the Builtin `git` Subcommand Surface (`adrianliechti/go-git` + Custom Extensions):**
Full upstream C `git` has 140+ flags and obscure plumbing switches, whereas `go-git` covers the core 90% used daily. For the built-in `git` CLI command, we should support the essential daily driver subcommands natively in pure Go:
* `status` (`-s`, `--short`, `--porcelain`)
* `diff` (`--cached`, `--stat`, `<path>`, `<commit>..`)
* `log` (`-n <N>`, `--oneline`, `--stat`, `<path>`)
* `add` (`-A`, `.`, `<paths>`)
* `commit` (`-m`, `-a`, `--amend`)
* `branch` (`-a`, `-d`, `<name>`) & `checkout` / `switch` (`-b`, `<branch/paths>`)
* `reset` (`HEAD`, `--soft`, `--mixed`, `--hard`) & `restore` (`--staged`, `<paths>`)
* `clone`, `fetch`, `pull`, `push`, `remote` (`-v`, `add`, `remove`)
* `stash` (can be implemented cleanly over `go-git` by creating a detached commit reference under `refs/stash`!)
* *Hybrid Option:* Should the builtin `git` always handle commands in-process (for speed and portability), or should it have a flag/fallback to invoke host `git` if the user passes an esoteric flag like `git rebase -i` or `git bisect` when host `git` happens to be installed?


3. **Git Config & Author Identity Fallback:**
When committing on a fresh machine or container without `~/.gitconfig`, `go-git` requires a `CommitOptions.Author` (`Name`, `Email`, `When`). Our wrapper should read `.git/config` $\rightarrow$ `~/.gitconfig` $\rightarrow$ environment variables (`GIT_AUTHOR_NAME` / `EMAIL`) $\rightarrow$ and finally fall back gracefully to `os/user` (`<username>@kabibi.local`) so `git commit` and `git_mutate(action="commit")` **never fail** due to unconfigured global git identity.
4. **UI Cache Sync Hook (`OnCommandFinished`):**
Whether a commit or file checkout happens via the `git` shell builtin or via the AI's `git_mutate` tool, triggering the existing `OnCommandFinished(es.Dir())` callback ensures the dual blue file panels (`Ctrl+O`) and the `፧` prompt refresh immediately.



---

### Concrete Engineering Effort Breakdown

Every piece of this upgrade can be built and unit-tested modularly inside `kabibi`:

| Module | Scope | Complexity |
| --- | --- | --- |
| **1. `kabibi/tools/hashline**` | `LINE:HASH|` formatter, `[path#TAG]` header, whitespace-stripped `xxhash`, and `ApplyHashlineEdits` with $$\pm 3$-line self-healing relocation.

 | **Low** (~250 LOC, pure algorithmic Go) |
| **2. `kabibi/tools/fabric**` | 4-tier progressive search (`plain` $\rightarrow$ `regex` $\rightarrow$ `fuzzy` $\rightarrow$ `EmbeddingGemma 2`), indentation-gravity block expansion, `…` slice merging, and TFLite OCR fallback for images.

 | **Medium** (~350 LOC, wires into `Session.Embedder` & `ocr4`) |
| **3. `kabibi/tools/filelist**` | `doublestar` globbing + `.gitignore` filtering + fuzzy/embedding ranking + compact `d/x/l/-` POSIX/Windows formatter.

 | **Low–Medium** (~250 LOC) |
| **4. `kabibi/gitcore` + AI Git Tools + `git` Builtin** | Shared `go-git` repo resolver; `git_inspect` (with hashline diffs + semantic log search) & `git_mutate` for AI; in-process `git` CLI builtin registered in `ClassifyInput` / `moreinterp`. | **Medium** (~550 LOC total across AI tools + CLI subcommand router) |
| **5. `kabibi/ui/shell` PTY Gutter & Lifecycle** | `Cols: W - 2` PTY launch, 2-column `┃ ` gutter blit in `renderBlock`, `CellBuf` color-stripped text extraction, timeout promotion (`bgProcs`), and `term_read` / `term_input` / `term_kill`.

 | **Medium** (~400 LOC, builds directly on `pty` and `terminal.CellBuf`) |

