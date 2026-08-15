# Rewriting the Daemon in Rust or Zig

| Field | Value |
|-------|-------|
| Priority | — |
| Effort | Very Large |
| Impact | Negative (at current measurements) |
| Status | **Investigated — rejected** |
| Investigated | 2026-08-15 |
| Depends on | — |

## Question

Would rewriting `quild` in Rust or Zig buy meaningful efficiency over the current Go
implementation?

## Verdict

**No.** The daemon's hot path is bottlenecked by a *wire protocol* decision, not by the
language. The dominant cost is `encoding/json` re-scanning a payload the daemon itself
just produced — and that is removable in Go, on the same wire format, for a 42× encode
and 11.5× decode improvement. See
[`docs/superpowers/specs/2026-08-15-ipc-frame-encoding-design.md`](../superpowers/specs/2026-08-15-ipc-frame-encoding-design.md).

Revisit this document only if the conditions under [What would change the answer](#what-would-change-the-answer)
become true.

## Evidence

Microbenchmarks against the real `internal/ipc` and `internal/ringbuf` code
(Go 1.25, linux/amd64, Xeon E5-2680 v4, `-cpu 1`). No benchmarks existed in the repo
before this investigation; the ones that came out of it live in
`internal/ipc/frame_bench_test.go` and are runnable via `./scripts/dev.sh bench`.

Cost of encoding one coalesced 8 KB `pane_output` flush:

| Step | Time | Throughput | Allocs |
|---|---|---|---|
| `ringbuf.Write` | 0.46 µs | 18 GB/s | 0 |
| base64 of the payload bytes | 11.3 µs | 725 MB/s | 0 |
| `json.Marshal(PaneOutputPayload)` | 17.3 µs | 474 MB/s | 2 |
| `json.Marshal(Message{Payload: raw})` | **117.0 µs** | 70 MB/s | 1 |
| **`EncodeFrame` total** | **118.5 µs** | 69 MB/s | 2 |
| *hand-built binary frame, for scale* | 1.7 µs | 4.8 GB/s | 1 |

Two conclusions follow directly:

1. **99% of encode time is `encoding/json` validating already-valid JSON.**
   `EncodeFrame` marshals a `Message` whose `Payload` is a `json.RawMessage` — bytes
   `json.Marshal` produced moments earlier. `encoding/json` compacts every `RawMessage`
   on the way out, walking the ~11 KB of base64 through its scanner state machine at
   roughly 10 ns/byte. That re-scan costs **7× more than the base64 encode that
   generated it**.

2. **The parts a systems language traditionally wins are already optimal.**
   `ringbuf.Write` is 0.46 µs with zero allocations — the fixed-backing-array design
   noted in `internal/ringbuf/ringbuf.go` did its job, and there is nothing left on the
   table. `Broadcast` marshals once and fans the shared frame out to all conns
   (`internal/ipc/server.go`), so cost is per-frame, not per-client. Go's GC does not
   appear in the hot path.

Translated into CPU, given the coalescer's 2 ms debounce (≤500 flushes/s/pane):

| Scenario | Today | After the encoding fix |
|---|---|---|
| One pane streaming 4 MB/s (8 KB flushes) | 5.9% of a core | 0.13% |
| One pane at the 64 KB coalescer cap | 49% of a core | 1.0% |

The client side is larger still: a full receive of one 8 KB `pane_output` frame
(`json.Unmarshal` into `Message`, then `DecodePayload` into `PaneOutputPayload`) costs
**207 µs**, and it is the same root cause. It is paid off the Bubble Tea `Update`
goroutine — `ReadMessage` runs on the router's per-destination pump
(`internal/tui/router.go:186`) and `DecodePayload` on the listen `tea.Cmd` goroutine
(`internal/tui/model.go:5953`) — so it does not contend with keystroke handling. What it
does bound is per-connection receive throughput, since one pump drains a destination
sequentially, and it adds latency ahead of every frame `Update` eventually sees.

## What a rewrite would cost

| Component | LOC (prod + test) | Rust | Zig |
|---|---|---|---|
| `internal/daemon` | 10.7k + 15.6k | rewrite | rewrite |
| `internal/ipc` | 2.0k + 2.2k | becomes a cross-language contract | same |
| `internal/pty` (ConPTY + unix) | 1.3k + 0.6k | `portable-pty` (wezterm) | hand-roll Win32 |
| VT emulator (`screenshot_pane`) | dependency | `vt100` / `alacritty_terminal` / `termwiz` | none |
| MCP server + bridge | ~3k of `cmd/quil` | `rmcp` (official SDK) | hand-roll JSON-RPC |
| `internal/notify` (WinRT COM) | 1.8k + 0.9k | `windows` crate — better than today | `std.os.windows` |
| plugin + config (TOML) | 2.0k | `toml` / `serde` | sparse |
| hooks, sessions, persist, update | ~2.5k | comparable | comparable |

Roughly **26k LOC** daemon-side. Three costs dominate, and none of them are the typing:

**The tests are the asset, not the overhead.** Those 15.6k test lines encode at least six
production incidents — the 2026-06-11/12 PTY-write wedge, the 64-slot critical-queue
overflow at 33 tabs, keystroke transposition under load, resize-guard data races, the
2026-08-11 write-deadline misdiagnosis, blocking-FS-call pool exhaustion. A rewrite
discards that hardening and re-earns it in production, on a user who runs Quil as a daily
driver.

**`internal/ipc` stops being a shared type.** It is imported by 21 files in
`internal/tui`, 12 in `internal/daemon`, 10 in `cmd/quil`. Today the protocol *is* Go
structs: one source of truth, both sides checked by the compiler. Split the languages and
you need a schema plus two implementations kept in sync by discipline. Every
"`omitempty` keeps older clients working" comment in `protocol.go` becomes a manual
invariant maintained across a language boundary.

**Two toolchains, permanently.** The TUI is 33k LOC of Bubble Tea and is not part of this
question. A Rust daemon means two build systems, two CI matrices, two cross-compilation
stories for five platforms, and a `dev.sh` that drives both.

### Zig specifically

Zig is the weaker of the two candidates here, for reasons unrelated to the language's
merits: it is pre-1.0 with breaking changes per release, and the ecosystem has no mature
PTY, VT-emulator, MCP, or TOML libraries. Quil is a Windows-first project whose hardest
platform code is ConPTY interop and WinRT toast delivery — exactly the surface where Rust
has good crates and Zig has none. A Zig rewrite is a Go rewrite plus writing the
dependency tree.

## What would change the answer

Revisit if any of these becomes true:

- **The encoding fix lands and a bottleneck remains.** If profiling after the fix shows
  time in the Go runtime rather than in application logic, the premise changes. Measure
  first; this document exists because the intuition was wrong once already.
- **A hard binary-size or RSS target appears** (e.g. shipping into a container base image
  where 3 MB vs 15 MB matters). Go's floor is ~10–15 MB RSS; Rust's is ~2–3 MB. Today
  that difference is noise on a developer workstation.
- **The TUI is being rewritten anyway.** The two-toolchain objection and the shared-`ipc`
  objection both dissolve if the whole product is moving. It would still be a very large
  project.
- **Hard real-time guarantees become a requirement.** No current or planned feature needs
  them.

None of these hold as of 2026-08-15.

## Related

- [`browser-ui.md`](./browser-ui.md) — the other half of the same investigation.
- [`docs/superpowers/specs/2026-08-15-ipc-frame-encoding-design.md`](../superpowers/specs/2026-08-15-ipc-frame-encoding-design.md)
  — the fix this investigation recommends instead.
