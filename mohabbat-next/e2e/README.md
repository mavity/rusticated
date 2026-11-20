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
`target/mohabbat-next-e2e/`. Use `-wpr <trace.etl>` to additionally collect
Windows Performance Recorder data during the capture window. WPR starts after
the prompt marker appears and stops after the typing/drain phases, before
shutdown. It requests the installed `GeneralProfile`, `CPU`, and
`VirtualAllocation` profiles. If WPR refuses the request (for example due to
permissions or another active recording), the harness reports the command and
does not silently continue as though an ETL had been collected.

Important options:

| Option | Default | Description |
| --- | --- | --- |
| `-exe` | `..\..\release\dist\kabibi.exe` | Target executable, launched directly (not through a `.bat`) |
| `-arg` | none | Target argument; repeat the flag to supply multiple arguments |
| `-dir` | `..\..` | Target working directory |
| `-out-dir` | `..\..\target\mohabbat-next-e2e` | Run artifact root |
| `-wpr` | none | Optional WPR ETL output path |
| `-cols`, `-rows` | `120`, `40` | ConPTY dimensions |
| `-startup-timeout` | `90s` | Maximum wait for the prompt marker before failing |
| `-idle` | `2s` | Idle period included in capture |
| `-slow-text`, `-slow-interval` | `slow-type-0123456789`, `200ms` | Paced burst |
| `-fast-text`, `-fast-interval` | `FAST0123456789abcdefghijklmnopqrstuvwxyz`, `20ms` | Rapid burst |
| `-edit-text` | `EDIT-12345` | Text to type and then erase with DEL bytes |
| `-sample-interval` | `100ms` | Win32 process-counter sampling interval |
| `-thread-sample-interval` | `1s` | Slower cadence for system-wide thread snapshot enumeration |
| `-drain` | `1s` | Post-typing observation interval |
| `-exit-grace` | `4s` | Grace period after the capture for Ctrl-C shutdown |

Each run contains:

* `manifest.json`: exact executable path, SHA-256 and size, arguments, working
  directory, PID, host, PTY geometry/TERM, prompt marker and timeout, workload
  schedule, sampling interval, metric list, WPR profile selection, and artifact
  paths.
* `events.jsonl`: timestamped records with schema version, run ID, monotonically
  assigned record sequence, UTC timestamp, monotonic elapsed nanoseconds,
  phase, source, source API, PID, and event-specific data. PTY output and input
  bytes are preserved as base64; valid UTF-8 output also has a text field.
* `summary.json`: process exit information, PTY byte/chunk totals by phase,
  sample count, maximum/unique observed thread counts, and counter deltas from
  the first to last process sample.
* The requested WPR ETL, if enabled and successfully captured.

`process.sample` events identify the Win32 source API for each metric and
include sample start/completion times, query duration, cumulative CPU times,
I/O counts/bytes, page faults, working set, private pagefile usage, handle
count, and (on its separate cadence) sampled thread IDs. Thread IDs first seen
in a snapshot are listed separately as an observed-churn proxy; short-lived
threads between snapshots may be missed, and Windows may reuse IDs. The
system-wide snapshot's own duration is recorded because it can be relatively
expensive. Counter-query errors are attached to the sample instead of being
replaced with zero.

To study response timing, correlate `pty.input` records (input start/completion
and sequence) with subsequent `pty.output` chunks using `elapsed_ns` and
`chunk_sequence`. The input write duration measures delivery to ConPTY, not
guest handling. Output records preserve the raw bytes and timing but do not
assert that a particular chunk corresponds to the preceding key. Process
samples are approximate periodic observations and include their measurement
duration. These caveats are deliberately explicit so the artifacts remain
useful without overstating what the harness observes.
