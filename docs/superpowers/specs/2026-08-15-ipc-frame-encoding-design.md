# IPC Frame Encoding — Design Spec

- **Date:** 2026-08-15
- **Status:** Implemented
- **Owner:** Artjoms Stukans
- **Origin:** [`docs/roadmap/daemon-language-rewrite.md`](../../roadmap/daemon-language-rewrite.md)

Revised after an adversarial review that verified every claim against the code. Three
findings changed the design rather than the prose, and each is recorded inline where it
applies:

1. The encoder's escape guard omitted HTML escaping (`<`, `>`, `&`), which
   `encoding/json` applies by default — the byte-identity claim was false for any
   client-supplied `ID` containing one.
2. The decode fast path could not simultaneously "slice without scanning" and "fall back
   on non-conforming input", because non-conformance can live inside the unscanned span.
   Resolved by gating the fast path on `pane_output` and validating the span's braces.
3. `maxFrameSize` enforcement was missing from the fast encode path.

Two motivating claims were also wrong and are corrected: the client-side decode cost is
**not** paid on the Bubble Tea `Update` goroutine, and `-count 6` is not a benchstat
minimum.

## Problem

Every IPC frame is encoded by `ipc.EncodeFrame`, which calls `json.Marshal` on a
`Message` whose `Payload` field is a `json.RawMessage` — bytes that `json.Marshal`
produced moments earlier. `encoding/json` compacts and validates every `RawMessage` on
the way out, walking the payload through its scanner state machine byte by byte at
roughly 10 ns/byte.

For `pane_output` — the only high-frequency message type, at up to 500 frames/s/pane —
the payload is base64-encoded PTY bytes, so that second pass covers ~11 KB per 8 KB of
terminal output. Measured on the real code (Go 1.25, linux/amd64, Xeon E5-2680 v4,
`-cpu 1`):

| Step | Time (8 KB flush) | Allocs |
|---|---|---|
| `ringbuf.Write` | 0.46 µs | 0 |
| base64 of the payload | 11.3 µs | 0 |
| `json.Marshal(PaneOutputPayload)` | 17.3 µs | 2 |
| `json.Marshal(Message{Payload: raw})` | **117.0 µs** | 1 |
| **`EncodeFrame` total** | **118.5 µs** | 2 |

The re-scan costs **7× more than the base64 encode that produced the bytes**. It is 99%
of encode time and it produces no information.

The receive side is worse. A full client receive of one 8 KB `pane_output` frame —
`ReadMessage`'s `json.Unmarshal` into `Message`, then `DecodePayload` into
`PaneOutputPayload` — costs **207 µs**. It is paid off the Bubble Tea `Update`
goroutine: `ReadMessage` runs on the router's per-destination pump
(`internal/tui/router.go:186`) and `DecodePayload` on the listen `tea.Cmd` goroutine
(`internal/tui/model.go:5953`). So it does not contend with keystroke handling — but it
does bound per-connection receive throughput, since one pump drains a destination
sequentially, and it adds latency ahead of every frame `Update` eventually sees.

In CPU terms, given the coalescer's 2 ms debounce:

| Scenario | Encode cost today |
|---|---|
| One pane streaming 4 MB/s (8 KB flushes) | 5.9% of a core |
| One pane at the 64 KB coalescer cap | 49% of a core |

## Goals

1. Remove the redundant JSON scan on both encode and decode paths.
2. **Produce byte-identical wire output.** No protocol version bump, no capability
   negotiation, no compatibility gate.
3. Ship a repeatable benchmark harness so the improvement is measured, not asserted, and
   so future regressions are detectable.

## Non-Goals

- **No binary wire format.** A binary frame would reach ~1.7 µs instead of ~2.7 µs, but
  requires capability negotiation because `cmd/quil/mcp.go` performs **no version
  handshake** — an old `quil mcp` binary named by absolute path in an AI client's config
  can attach to a newer daemon, and `client.Receive()` failing to unmarshal tears down
  its read loop. The remaining ~1 µs does not justify that surface. Revisit only if
  profiling after this change says otherwise.
- **No removal of base64.** Same reason: it is part of the wire format.
- **No fast path for payload types other than `PaneOutputPayload`.** Every other message
  type runs at a few frames per second at most. Adding them is unmeasured complexity.
- No changes to queueing, backpressure, coalescing, or the ring buffer. Those are
  measured healthy (`ringbuf.Write` is 0.46 µs, zero-alloc).

## Design

Three fast paths, each with an unconditional fallback to the current `encoding/json`
implementation. The fallback is what makes every edge case a performance question rather
than a correctness question.

### 1. Encode — `appendEnvelope`

`EncodeFrame` builds the envelope by concatenation instead of marshalling it:

```
{"type":"<Type>"[,"id":"<ID>"][,"payload":<Payload>]}
```

This is exactly what `json.Marshal` emits for `Message`, given that struct's field order
and its `omitempty` tags, and given that `Payload` is already-compact JSON (it always is —
every producer builds it with `json.Marshal`). The 4-byte length prefix is written into
the same allocation, so the current separate `make` + `copy` disappears too.

**The fast path is taken only when `Type` and `ID` are plain ASCII needing no escaping:**
every byte must be in `0x20..0x7E` and none may be `"`, `\`, `<`, `>`, or `&`. Anything
else falls back to `json.Marshal`.

Each part of that guard was verified empirically against Go 1.25, not reasoned about:

- `<`, `>`, `&` are **not obvious and were a bug in the first draft of this spec.**
  `json.Marshal` HTML-escapes them by default (`SetEscapeHTML` defaults to true), so
  `ID: "a<b>c&d"` marshals to `"a<b>c&d"`. Concatenating them raw would
  produce different bytes — silently, and only for IDs containing those characters.
- Bytes above 0x7E are excluded even though valid non-ASCII passes through Go's encoder
  unescaped (`"café-日本"` stays literal, verified). The guard is not about valid UTF-8:
  it is about **invalid** UTF-8, which `json.Marshal` silently replaces with U+FFFD and
  concatenation would pass through verbatim. Rejecting all non-ASCII is the cheap way to
  be right without a validity scan.
- `"` and `\` are the ordinary JSON escapes.

This guard is load-bearing, not defensive. `Message.ID` is echoed back by `respondTo` to
whatever an IPC client sent, and any client can open the socket under the current trust
model. An `ID` containing a double quote, concatenated unescaped, produces a **malformed
frame on a length-prefixed stream** — corruption, not a dropped message.

**`Payload` is inserted verbatim, and one invariant makes that correct.** `json.Marshal`
does two things to a `RawMessage` on the way out: it compacts insignificant whitespace,
and it HTML-escapes `<`, `>`, `&` *inside* it (both verified). Concatenation does
neither. Verbatim insertion is therefore byte-identical only if every payload is already
compact and already escaped — which holds because **`NewMessage`
(`internal/ipc/protocol.go:1130`) is the sole construction site for `Message.Payload` in
the entire repo, and it always goes through `json.Marshal`.** A `grep` for
`json.RawMessage(` and `Payload:` finds no other producer. That invariant is pinned by a
test rather than left as a comment, because the fast path is silently wrong the day
someone hand-builds a payload.

The guarantee is therefore **conditional on marshal-produced payloads**, and that
condition is worth stating plainly because tests already violate it: `protocol_test.go:41`
and `overlay_lifecycle_test.go:344` build `Payload` from byte literals. Those particular
literals are compact and escape-free so they stay identical, but a hand-built payload
containing internal whitespace or a raw `<` would produce a **valid, decode-equivalent,
byte-different** frame — no failure, just a broken identity claim. The first-byte check in
*Risks* catches only a leading-whitespace or non-object payload, not that. The identity
property test therefore asserts over `NewMessage`-produced payloads, and a separate row
pins the hand-built ones as explicitly out of the guarantee.

`maxFrameSize` is enforced identically. `EncodeFrame` checks it today
(`internal/ipc/protocol.go:1155`) and the constant's own comment explains why the
producer-side check exists: to fail attributably at the producer instead of poisoning the
stream, and to bound the size arithmetic. The fast path computes the same total and
performs the same check before allocating — ghost-replay chunks and `screenshot_pane`
text are the frames that approach the limit, so this is a live path, not a formality.

### 2. Decode — `parseEnvelope`, `pane_output` only

`ReadMessage` pattern-matches the envelope shape above and slices `Payload` out without
running the JSON scanner over its contents. Anything that does not match falls back to
`json.Unmarshal`.

**The fast path is gated on `Type == MsgPaneOutput`.** This is a correctness requirement
first and a scope reduction second.

*Why it is required.* "Slice the payload without scanning it" and "fall back on anything
that doesn't match" are contradictory for one input class, because non-conformance can
live **inside** the payload span — exactly the bytes not being scanned. A frame like
`{"type":"a","payload":{},"x":{}}` matches the prefix grammar; the naive slice
(everything between `,"payload":` and the final `}`) yields `{},"x":{}` rather than `{}`.
`json.Unmarshal` accepts that frame today — key order is free and duplicate keys are
last-wins — so the fast path would change behaviour for a frame the daemon currently
handles, under the same "any client can open the socket" trust model that motivates the
encoder's ID guard.

*Why gating fixes it.* A `pane_output` payload has a known, flat shape:
`{"pane_id":"…","data":"…"[,"ghost":…]}`. Neither field can contain a brace — base64's
alphabet has none, and pane IDs are `pane-` plus hex. So the span is validated in full
with two SIMD-fast `IndexByte` calls rather than a parser: the span must begin with `{`,
end with `}`, and contain **exactly one** of each. `{},"x":{}` fails that (two `}`), and
falls back. Cost is ~0.4 µs over 11 KB against the ~131 µs being removed.

*Why the gate costs nothing.* The envelope scan is expensive only in proportion to the
payload, and `pane_output` is the only message type with a large one. For
`list_panes_resp` or `workspace_state` the payload is small and `json.Unmarshal` is
already microseconds — which the `ClientReceive_ListPanesResp` control benchmark exists
to keep honest.

Two further hardenings on the string scan, both fallback triggers:

- Any byte `< 0x20` inside the `Type` or `ID` string. `json.Unmarshal` rejects raw
  control bytes in JSON strings; without this check the fast decoder would be **more
  lenient than the encoder's own guard**, accepting frames the fallback rejects.
- Any `\` in either string (an escape we would otherwise return un-decoded).

The residual divergence is stated rather than hidden: a frame whose payload span passes
the brace check but is not what a conforming producer emits will mis-slice and then fail
in `DecodePayload`. No producer in this repo can emit one — `EncodeFrame` writes
`payload` last, guaranteed by `Message`'s field order — and the failure mode is a dropped
message, not corruption. A **differential fuzz test** (below) is the guard, because a
generated-corpus property test only ever produces well-formed messages and can never find
this class.

**`Payload` becomes a slice aliasing the frame buffer rather than a copy**, which is a
real change in lifetime semantics. Today `json.RawMessage.UnmarshalJSON` copies
(`*m = append((*m)[0:0], data...)`), so a decoded `Payload` is independent of the read
buffer. After this change it is not.

Traced against every consumer in the repo, and it is safe — but for a narrower reason
than "nothing retains it":

- Most consumers call `DecodePayload` into a struct and drop the raw bytes.
- **One consumer retains a `RawMessage` from the wire for the daemon's lifetime**:
  `handleUpdateLayout` does `tab.Layout = payload.Layout`
  (`internal/daemon/daemon.go:2473`), one per tab. That is safe **only because
  `UpdateLayoutPayload` is decoded through `json.Unmarshal`, whose `RawMessage` handling
  copies.** It is not covered by the `Message.Payload` aliasing, which stops at the
  envelope.
- `Tab.Layout`'s other producer (`daemon.go:746`, restore) builds from
  `json.Marshal(layoutRaw)` off the snapshot file, so it never aliases a frame.

The router's pump does carry a `*Message` across goroutines
(`internal/tui/router.go:210` → `r.in` → `Update`), but the frame buffer is never reused,
so the aliasing holds there too.

Four consequences the implementation must carry structurally or as comments, not as
tribal knowledge:

1. **Slice with the full slice expression `data[i:j:j]`.** Capping capacity means a
   future `append` to a retained `RawMessage` allocates instead of overwriting the
   frame's trailing bytes. This is compiler-enforced where a comment is not.
2. `ReadMessage` allocates `data` fresh per call and must continue to. If it ever adopts
   a pooled or reused read buffer, the aliasing becomes a use-after-reuse bug surfacing
   as intermittently corrupted payloads. The same prohibition applies to `EncodeFrame`,
   whose freshly-allocated return value `Broadcast` fans out to every conn read-only
   (`internal/ipc/server.go:610`) — **no `sync.Pool` on either side.** Pooling is the
   next optimization someone reaches for and it would break the broadcast fan-out and
   the decode aliasing simultaneously.
3. **The fast and fallback decode paths now have different lifetime semantics** — the
   fallback copies via `RawMessage.UnmarshalJSON`, the fast path aliases. Document at
   both, because a reader who checks only one will draw the wrong conclusion.
4. **Do not extend the payload fast path to `UpdateLayoutPayload` without copying.** A
   slicing fast path there would pin one wire frame buffer per tab in the daemon forever
   — a slow leak proportional to tab count, invisible in any test.

### 3. Payload — `DecodePayload` type switch

`(*Message).DecodePayload` gains a type switch on `*PaneOutputPayload` that
base64-decodes the `data` field directly instead of running the JSON scanner over the
encoded string, with the same shape-match-or-fall-back structure.

Putting it inside `DecodePayload` rather than adding a new method means **no call site
changes** — `internal/tui/model.go` and every other consumer keep calling what they call
today.

### Measured result

Shipped numbers, `benchstat bench/before.txt bench/after.txt`, n=6, `-cpu 1`:

| Benchmark | Before | After | Change |
|---|---|---|---|
| `EncodeFrame_PaneOutput8K` | 112.97 µs | **3.20 µs** | −97.2% (35×) |
| `EncodeFrame_PaneOutput64K` | 858.53 µs | **27.20 µs** | −96.8% (32×) |
| `EncodeFrame_ListPanesResp` | 3.49 µs | **1.95 µs** | −44% (1.8×) |
| `ReadMessage_PaneOutput8K` | 117.38 µs | **3.62 µs** | −96.9% (32×) |
| `DecodePayload_PaneOutput8K` | 103.83 µs | **18.73 µs** | −82.0% (5.5×) |
| `ClientReceive_PaneOutput8K` | 226.72 µs | **22.73 µs** | −90.0% (10×) |
| `ClientReceive_ListPanesResp` | 13.77 µs | 13.04 µs | ~ (control, no regression) |

Allocations: encode 2 → 1 on every type; a full client receive 17 → 7.

Both acceptance thresholds are met with margin (≥30× encode, ≥8× client receive).

**`EncodeFrame_ListPanesResp` is the one number that got worse than an earlier draft of
this change**, from 17× down to 1.8×, and deliberately: `payloadInlinable` now runs
`json.Valid` on every payload that is not a `pane_output` span. That closes the envelope
injection described below, and small payloads are the only ones paying for it — measured
at 1.4 µs for a two-pane `list_panes_resp`, on a message type that appears a few times a
second at most. `pane_output`, the type that actually runs hot, skips validation entirely
via the structural check and keeps its 35×. Paying ~1.5 µs on cold-path messages to
remove a way for a payload to forge an envelope is not a close call.

Measurement note: these came from a run with the benchmark process pinned to four CPUs.
An unpinned run on the same machine, with unrelated containers active, produced ±69–106%
variance and showed the *unchanged* component benchmarks moving 73–88% — a reminder to
check the controls before believing a delta.

### Unplanned consequence: the encoder was supplying backpressure

The encoder's slowness was load-bearing in two resilience tests, which nobody knew.
`TestBroadcast_SlowConnDoesNotBlockFastConn` and
`TestBroadcast_ContinuesAfterSlowConnDisconnects` both used an `ipc.Client.Receive` loop
as their "fast" client. That reader costs ~320 µs per frame on their escape-heavy 24 KiB
payloads — never fast, merely faster than a 372 µs encoder. With the encoder at 25 µs the
producer outran it ~13×, both fast clients filled their own 64-slot critical queues, and
the overflow policy closed them: two deterministic failures asserting the opposite of
their names, the second with only a ~30% margin under `-race`.

Both now drain raw frames, so "fast" means "drains its socket" — the layer the broadcast
fan-out actually couples to — and neither depends on either side's incidental speed.
`SlowConnDoesNotBlockFastConn` was also tightened from 50 of 200 frames to all 200, since
the critical queue has no drop path for a conn that keeps up.

**Production risk was assessed separately and is low**, for a reason independent of the
tests: `pane_output` is the only genuinely high-frequency type and it routes to the
*droppable* queue, where it can never trip the overflow close. The critical queue carries
state, responses and lifecycle, which are event-driven and emitted one frame per handled
event; the two known >64-frame bursts (ghost replay, attach event replay) are both on
`SendBlocking` paths that are structurally immune. The 13× is also inflated by those
tests' pathological payload — 4000 NUL bytes escape to ~6 wire bytes each, making the
envelope scan enormous relative to the payload marshal, which is unchanged. Real critical
frames see closer to 2–5×.

## Benchmark harness

`internal/ipc/frame_bench_test.go` — a committed, permanent benchmark covering encode and
decode at 8 KB and 64 KB, the combined client-receive path, a small structured control
payload (`list_panes_resp`, carrying an ID) to catch a fast path that helps `pane_output`
and regresses everything else, and the component breakdown (base64, payload marshal,
envelope marshal) that made the original diagnosis possible.

`./scripts/dev.sh bench [label] [pkg]` runs it in the existing Docker image with
`-run '^$' -bench . -benchmem -count 6 -cpu 1` and writes `bench/<label>.txt` (gitignored).
When a `bench/before.txt` exists and the label is not `before`, it also runs `benchstat`
to print the comparison; `benchstat` is fetched through the persisted module volume and
its absence degrades to a message rather than failing the command, so a machine without
network still gets its raw numbers.

`-count 6` gives benchstat headroom for a useful confidence interval — it is a choice,
not a floor (its Mann-Whitney U test can reach p<0.05 at n=4). `-cpu 1` keeps runs
comparable, since the benchmarked code is single-threaded and GOMAXPROCS otherwise varies
with whatever else the machine is doing. `-run '^$'` skips tests so a slow suite does not
pad the timing run. `QUIL_BENCH_BASE` overrides which file the comparison runs against,
so a baseline captured under a label other than `before` is not silently skipped — and
when the baseline is missing the command says so rather than quietly producing no
comparison, since success criterion 1 depends on it. The `benchstat` version is **pinned**
rather than `@latest`: `@latest` re-resolves against the module proxy on every run, which
both requires network and can change the measuring tool midway through a comparison.

**This harness already exists on the branch** (`internal/ipc/frame_bench_test.go`,
`scripts/dev.sh`) and the baseline is captured. It is described here because it is part of
the change, not because it is still to be built.

**Baseline captured on unmodified code** (`bench/before.txt`, same hardware as the
Problem section, median of 6):

| Benchmark | ns/op | allocs/op |
|---|---|---|
| `EncodeFrame_PaneOutput8K` | ~112 000 | 2 |
| `ClientReceive_PaneOutput8K` | ~221 000 | 17 |
| `Component_MarshalEnvelope8K` | ~104 000 | 1 |
| `Component_MarshalPayload8K` | ~17 800 | 2 |
| `Component_Base64Encode8K` | ~12 000 | 0 |

The envelope line being ~6× the payload line, on the same bytes, is the whole finding.

## Testing

| Concern | Test |
|---|---|
| Byte identity | Property test over generated `Message` values: `appendEnvelope` output must equal `json.Marshal` output for every case, including nil payload, empty-slice payload, literal `null` payload, empty `Type`, absent ID, and every `Msg*` constant as `Type`. (Verified by hand: nil and empty-slice both omit the field; `null` emits `"payload":null`.) |
| Escape fallback | Table test: `Type`/`ID` containing `"`, `\`, `<`, `>`, `&`, newline, tab, DEL, valid non-ASCII, and **invalid UTF-8** must each produce output identical to `json.Marshal` via the fallback, and must round-trip. The `<>&` and invalid-UTF-8 rows are the ones that catch the two mistakes this spec already made. |
| Payload invariant | A test asserting `NewMessage` is the only path that sets `Message.Payload`, and that its output is compact and HTML-escaped — i.e. that verbatim insertion stays correct. |
| Round trip | `parseEnvelope(appendEnvelope(m)) == m` for the same generated corpus, plus the reverse for frames produced by `json.Marshal`. |
| Cross-compatibility | Frames produced by the fast encoder must decode with `json.Unmarshal`, and frames produced by `json.Marshal` must decode with the fast decoder. This is what "no version bump needed" means, so it is tested explicitly rather than argued. |
| **Differential fuzz** | `go test -fuzz` over arbitrary frame bytes: the fast decoder must either produce exactly what `json.Unmarshal` produces, or have declined to the fallback. **This is the only test that can find the finding-2 class** — a generated-corpus property test emits only well-formed messages and never probes keys-after-payload, duplicate keys, or raw control bytes in strings. Seed it with the mis-slice cases named in the design. |
| Payload fast path | `PaneOutputPayload` round-trip including binary data with NUL and 0xFF bytes, `ghost` true / false / absent, empty `data`. |
| Payload failure shapes | Each must match `encoding/json`'s behaviour exactly: `"data":null`; **invalid base64 (must ERROR, not return a short slice)**; duplicate keys (→ fallback); an unknown extra key (`encoding/json` ignores it, so the pattern match must fall back, not error); and nil/empty `Payload`. The last is load-bearing: today it yields "unexpected end of JSON input" and `internal/tui/model.go:5955` **ignores that error**, so a fast path returning nil-success would silently change behaviour. |
| Struct drift | A reflection test over `ipc.Message` pinning field **order** and json **tags**, not merely the field set. Declaration order is what makes `type,id,payload` the wire order, and `Origin`'s `json:"-"` (`internal/ipc/protocol.go:197`) is load-bearing — it is why `Origin` must never reach the wire. The hand-built encoder silently drops a field a contributor adds, and the compiler cannot catch it. |
| Frame size | A payload just over `maxFrameSize`: both the fast path and the fallback must reject it, with the same error. |
| Mutation check | Deliberately break each fast path and confirm the identity tests fail. A byte-identity test that passes against a broken encoder is worth nothing, and this repo has shipped that mistake before (`.claude/rules` records two). |
| Regression | The full existing suite unchanged, plus `go test -race ./...` — the CI command, not `dev.sh test`. |

## Risks

**A payload can forge envelope keys — this was real, and the mitigation described in the
first draft of this spec did not bound it.** The draft said an invalid payload "reaches
the peer as a malformed frame" and proposed "a cheap first-byte check". Both were wrong,
and code review caught it by measurement:

	payload  {},"type":"shutdown","x":{}
	frame    {"type":"pane_output","payload":{},"type":"shutdown","x":{}}
	decodes  cleanly, as Type "shutdown"

The payload is concatenated between `,"payload":` and the closing brace, so trailing
content becomes sibling envelope keys and `encoding/json` resolves duplicates last-wins.
A first/last-byte check cannot see it. The outcome is not a moved error; it is a
different, well-formed message.

`payloadInlinable` now requires the payload to be one complete JSON value:
`paneOutputSpan` (free, and injection-proof by construction — exactly one `{` and one
`}`, at the two ends) for the hot path, `json.Valid` for everything else. That also
subsumes a hand-rolled number branch which accepted eleven shapes `json.Marshal` never
emits, several of which cannot begin a JSON value at all.

It establishes validity, not compactness: `json.Valid` accepts `[ ]` where `json.Marshal`
emits `[]`. So byte-identity still holds only for marshal-produced payloads — every
production payload — and semantic equivalence holds for the rest, which is what "no
version negotiation needed" actually rests on. Leading and trailing whitespace is
rejected outright to keep byte-identity broader for two comparisons.

The single-producer invariant is now enforced rather than asserted: a `go/parser` walk
over `cmd/` and `internal/` fails if any production site outside `NewMessage` sets
`Message.Payload`. This spec required that test in its first draft and it was not
written; review caught that too.

**Aliasing.** Covered above. Mitigated by comments at the `ReadMessage` allocation site
and on the `Payload` field, and by the round-trip tests. The residual risk is a *future*
change (pooled read buffers, or a slicing fast path for `UpdateLayoutPayload`), which is
why both are called out as prohibitions rather than as background.

**Divergence over time.** If `Message` gains a field, or a tag changes, the hand-built
encoder is silently wrong and the wire loses data. The compiler cannot catch this — a
hand-built encoder has no reference to the fields it fails to write. The struct-drift
reflection test is the only guard, which is why it is listed as required rather than
nice-to-have.

**What fuzzing actually found.** All four differential targets failed on their first run,
which is the strongest argument for having required them. Three were real defects, now
fixed: `fastString` returned invalid UTF-8 raw where `encoding/json` substitutes U+FFFD
(different `PaneID` for the same input); `parseEnvelope` accepted `{"payload":{0}}`,
brace-flat but not JSON; and `decodePaneOutput` accepted a CR inside the base64 field,
which `encoding/json` rejects but `base64.Decode` silently skips — CR and LF turn out to
be the entire divergence set, so two `IndexByte` calls close it.

The fourth was a contract correction rather than a defect, and it narrows a claim made
above. Byte-identity holds for payloads `json.Marshal` produces — every production
payload — but not for arbitrary ones: `json.Marshal` compacts a `RawMessage`, so a
hand-built `[ ]` re-encodes to `[]` while the fast path inlines it unchanged. The
resulting frame is semantically identical and decodes the same, which is what "no version
negotiation needed" actually rests on. The fuzz targets are split accordingly — one
asserts byte-identity over marshal-produced payloads, one asserts semantic equivalence
over arbitrary ones.

**A benchmark that measures the wrong thing.** The harness must exercise `EncodeFrame`
and `ReadMessage` themselves, not reimplementations of them, or a future regression in
the real functions passes unnoticed. `internal/ipc/frame_bench_test.go` calls the
production functions and constructs its input through `NewMessage`.

## Rollout

No feature flag. The change is byte-identical on the wire and the fallback covers every
shape the fast path declines; a flag would double the paths under test to protect against
a difference the tests assert does not exist. It ships in a normal release.

## Success criteria

1. `./scripts/dev.sh bench after` reports ≥30× on `EncodeFrame_PaneOutput8K` and ≥8× on
   `ClientReceive_PaneOutput8K` against the committed `bench/before.txt` baseline, and no
   regression on `EncodeFrame_ListPanesResp` / `ClientReceive_ListPanesResp`.
2. Byte-identity, cross-compatibility, frame-size and struct-drift tests pass, and each
   fails under deliberate mutation. The fuzz target runs clean for at least 60 s.
3. `go test -race ./...` green — the CI command, not `dev.sh test`.
4. A dev daemon passes this checklist, which replaces "no visible change in behaviour"
   with something that can fail. **All items verified 2026-08-15** against a dev daemon
   in the worktree's own `.quil/` (production untouched), driven by a throwaway IPC
   client that attaches exactly as the TUI does:
   - ✅ attach → `workspace_state` decodes, pane visible
   - ✅ input → PTY → `pane_output` → decode: a marker string typed into the pane came
     back byte-intact over the socket (7 frames, 1215 bytes)
   - ✅ daemon restart: workspace restored the same pane id, ghost replay delivered
     1428 bytes from `ghostsnap` and decoded cleanly
   - ✅ MCP `initialize` + two `tools/call` round-trips through `quil mcp`, exercising the
     `Message.ID` request/response path through the new encoder
   - ✅ `quild.log` contains zero unmarshal, decode, panic, malformed-frame or
     frame-too-large entries across both daemon runs

   Not reachable headlessly and therefore not covered here: the TUI's own rendering
   (split borders, preview crop, sidebar compositing). Those are `View()`-side and
   untouched by this change, which stops at the `ipc` package boundary.
