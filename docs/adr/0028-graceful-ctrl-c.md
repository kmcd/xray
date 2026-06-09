## Status

Accepted. v0.3.x. Closes [#86](https://github.com/kmcd/xray/issues/86).

## Context

`xray run` may execute for an hour against production systems. Default Go
signal handling kills the process immediately, leaks `/tmp/xray-*` clone
directories, and leaves the operator without a phase-named summary. The
`docs/spec.md` Connectors section requires "clean cancellation" support, so
the seam wants explicit testing rather than implicit Go defaults.

## Decision

`cmd/xray/main.go` replaces `signal.NotifyContext` with an explicit
`signal.Notify` loop driving a two-state machine in `watchSignals`: first
signal calls the run's `context.CancelFunc`, prints a one-line stderr notice,
and continues listening; any subsequent signal logs
`force exit; temp dir <path> not cleaned` and `os.Exit(130)`, bypassing all
`defer`s.

`internal/run/Run` adds an `inflightTracker` mutex-guarded map updated around
every worker's `j.conn.Extract` call so the dispatcher can snapshot what was
running at the moment `ctx.Done()` fired. `Result` gains `Interrupted`,
`InterruptedPhase` ("clone" / "extract" / "postprocess"), `InflightJobs`, and
`TempDir`.

The temp-dir path flows from `internal/run.Run` to the signal handler via an
`Options.OnTempDir` callback that writes an `atomic.Pointer[string]` in
`cmd/xray/main.go`.

**A. Always-force on the second signal, not 5-seconds-windowed.** A windowed
implementation would surprise the operator whose second Ctrl-C "doesn't work"
because they waited too long. Always-force is strictly safer.

**B. Inflight snapshot inside `run.Run`, not via the progress sink.** The TTY
sink tracks display state, not live execution state. A separate
`inflightTracker` (`sync.Mutex` around a `map[inflightKey]struct{}`) is
small, isolated to `internal/run/interrupt.go`, and has no compile-time
coupling to the sink layer.

**C. Temp-dir callback rather than channel or globals.** The callback wins on
least coupling and is naturally test-isolatable.

**D. Exit code 130 separate from the 0/1/2/3 contract.** 130 is the POSIX
convention for "process killed by SIGINT" (128 + 2). It does not compete with
the existing 0/1/2/3 contract.

## Consequences

**Positive.** Operators can safely interrupt long runs. The phase-named
summary tells them what was running. Temp dir is cleaned on graceful
cancellation; its path is reported on force-exit.

**Negative.** `archive.WriteTarGz` cancellation is out of scope — the archive
step is a tiny window and the operator's recourse is the double-tap.

**Neutral.** SIGTERM is treated identically to SIGINT.

## How to apply

New: `internal/run/interrupt.go` (inflightTracker + `interruptedResult`
helper), `internal/run/interrupt_test.go`, `cmd/xray/main_test.go`
(signal-handler state machine). Modified: `internal/run/options.go` adds
`OnTempDir func(string)`; `internal/run/run.go` installs the tracker, calls
`OnTempDir` after `os.MkdirTemp`, snapshots on `ctx.Done()`, populates new
`Result` fields; `internal/run/summary.go` adds `InterruptSummary`;
`cmd/xray/main.go` switches from `signal.NotifyContext` to `signal.Notify` +
`watchSignals` + `atomic.Pointer[string] tmpDirRef`; `cmd/xray/run.go` passes
`OnTempDir` callback, detects `context.Canceled`, emits interrupt summary;
`docs/spec.md` appends a "Cancellation" subsection and adds 130 to the
exit-code list.
