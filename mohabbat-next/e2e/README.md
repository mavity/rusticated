# Mohabbat-Next end-to-end performance harness

This Windows-only harness launches an executable directly in ConPTY and waits
for the Kabibi prompt marker `፧` (U+1367). It then records an idle baseline and
reproducible slow, fast, and edit/backspace typing bursts. It is an
interactive-system diagnostic, not a pass/fail test: input delivery and
observed terminal output are recorded separately, and an output chunk is not
claimed to be a verified screen repaint for a particular key.

Startup is considered ready only after the exact UTF-8 bytes for `፧` appear in
PTY output. The matcher handles the marker being split across PTY reads. This
is Kabibi's default prompt terminator; the harness then preserves the existing
2-second idle period before sending any typing input. If the marker does not
appear before `-startup-timeout`, the harness fails without sending input.

From the repository root, build the Mohabbat-Next Kabibi vegetable and run it:

```powershell
go run ./mohabbat-next/build kabibi
go -C ./mohabbat-next/e2e run .
```

The harness is its own Go module so it can consume the existing `pty` module
without changing the production builder's dependency graph.

By default, the harness writes one run directory under
`target/mohabbat-next-e2e/`. It uses ordinary process-query APIs only: it does
not start ETW/WPR or require elevated access. After the capture it terminates
the target process rather than sending interactive shutdown keys.

Important options:

| Option | Default | Description |
| --- | --- | --- |
| `-exe` | `..\..\release\dist\kabibi.exe` | Target executable, launched directly (not through a `.bat`) |
| `-arg` | none | Target argument; repeat the flag to supply multiple arguments |
| `-dir` | `..\..` | Target working directory |
| `-out-dir` | `..\..\target\mohabbat-next-e2e` | Run artifact root |
| `-cols`, `-rows` | `120`, `40` | ConPTY dimensions |
| `-startup-timeout` | `90s` | Maximum wait for the prompt marker before failing |
| `-idle` | `2s` | Idle period included in capture |
| `-slow-text`, `-slow-interval` | `slow-type-0123456789`, human-like cadence | Paced burst; positive interval overrides cadence with a fixed delay |
| `-typing-seed` | `1` | Seed for reproducible human-like inter-key delays |
| `-fast-text`, `-fast-interval` | `FAST0123456789abcdefghijklmnopqrstuvwxyz`, `20ms` | Rapid burst |
| `-edit-text` | `EDIT-12345` | Text to type and then erase with DEL bytes |
| `-sample-interval` | `100ms` | Win32 process-counter sampling interval |
| `-thread-sample-interval` | `1s` | Slower cadence for system-wide thread snapshot enumeration |
| `-drain` | `1s` | Post-typing observation interval |

The slow and edit bursts use timing ranges from the AI browser's documented
human-like typing design: alternating-hand transitions are 45–95 ms,
same-hand/unknown transitions are 110–210 ms, a word-start pause after spaces
is 80–150 ms, and sentence punctuation adds a 220–550 ms pause before the next
character. The
edit burst also spaces its DEL bytes. These are delays between bytes written to
ConPTY, not physical key-down/key-up events; the browser typing design is
documentation only and has no implementation to reuse. The fast burst keeps
its fixed interval for a controlled rapid-input comparison. A fixed seed makes
the default slow/edit schedule repeatable.

Each run contains:

* `manifest.json`: exact executable path, SHA-256 and size, arguments, working
  directory, PID, host, PTY geometry/TERM, prompt marker and timeout, workload
  schedule and seed, sampling interval, metric list, and artifact paths.
* `events.jsonl`: timestamped records with schema version, run ID, monotonically
  assigned record sequence, UTC timestamp, monotonic elapsed nanoseconds,
  phase, source, source API, PID, and event-specific data. PTY output and input
  bytes are preserved as base64; valid UTF-8 output also has a text field.
* `summary.json`: process exit information, PTY byte/chunk totals by phase,
  sample count, maximum/unique observed thread counts, total counter deltas,
  and approximate counter deltas for intervals whose adjacent samples share a
  phase.

The `process.sampling_started` event lists the Win32 source APIs. `process.sample`
events include sample start/completion times, query duration, cumulative CPU
times, I/O counts/bytes, page faults, working set, private pagefile usage,
handle count, and (on its separate cadence) sampled thread IDs. Thread IDs
first seen in a snapshot are listed separately as an observed-churn proxy;
short-lived threads between snapshots may be missed, and Windows may reuse
IDs. The system-wide snapshot's own duration is recorded because it can be
relatively expensive. Counter-query errors are attached to the sample instead
of being replaced with zero.

The current Windows PAL uses `CreateThread` for each stdio read/write and
socket send/receive worker; each worker performs one operation and returns.
This is not a persistent worker pool. A pending stdin read can keep its
one-shot worker blocked until input arrives, but fast output workers may start
and finish between thread snapshots. The unprivileged snapshots therefore
measure observed live threads and a lower bound on distinct thread IDs, not an
exact worker-creation count.

To study response timing, correlate `pty.input` records (input start/completion
and sequence) with subsequent `pty.output` chunks using `elapsed_ns` and
`chunk_sequence`. The input write duration measures delivery to ConPTY, not
guest handling. Output records preserve the raw bytes and timing but do not
assert that a particular chunk corresponds to the preceding key. Process
samples are approximate periodic observations and include their measurement
duration. These caveats are deliberately explicit so the artifacts remain
useful without overstating what the harness observes.
