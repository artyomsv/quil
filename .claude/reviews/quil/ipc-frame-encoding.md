# Code Review State: quil / ipc-frame-encoding

Last reviewed: 2026-08-15
Rounds completed: 1

## Resolved (fixed in code; do not re-raise)
- [security/LOW-1] dev.sh bench interpolated an unvalidated label into `sh -c` and used it as a path — label/base now validated against [A-Za-z0-9._-] — round 1
- [security/LOW-2] parseEnvelope's payload span was in range only via an unstated suffix invariant; a reorder would panic on a goroutine with no recover — explicit length guard added and the invariant documented — round 1
- [security/LOW-3] read-buffer aliasing contract documented at the producer only — ban mirrored into .claude/rules/daemon-lifecycle.md and pinned by TestReadMessage_PayloadsDoNotShareBackingArrays — round 1
- [qa/mutation-b] dropping paneOutputSpan's `{"pane_id":"` prefix check survived the suite — pinned by TestParseEnvelope_GatesAreIndependentlyPinned/payload_prefix_check — round 1
- [qa/mutation-d] removing parseEnvelope's `typ != MsgPaneOutput` gate survived the suite and 4.4M fuzz execs — pinned by TestParseEnvelope_GatesAreIndependentlyPinned/type_gate; the code comment's "correctness requirement" claim was corrected, since paneOutputSpan is what actually gates acceptance — round 1
- [qa/coverage] CR/LF-in-base64 decline was a real fuzzer find with no regression test — pinned by TestDecodePaneOutput_DeclinesLineBreaksInData — round 1
- [qa/coverage] payloadInlinable false-literal and numeric branches, fastString exhaustion, parseEnvelope and decodePaneOutput decline branches all unreachable from tests — closed; fastframe.go now 100% on six of seven functions, 96% on parseEnvelope — round 1
- [self/pin] paneOutputSpan and decodePaneOutput hard-code PaneOutputPayload's wire shape; a field or tag change would silently disengage the fast path with nothing failing — pinned by TestPaneOutputPayload_FastPathStaysEngaged (reflection + behavioural round trip) — round 1

## Dismissed (acknowledged, will not fix; agents may escalate with explicit justification)
- [rules/LOW] commit subject `test(ipc): make the broadcast resilience tests measure draining, not decoding` is 77 chars vs the 72-char convention — the repo squash-merges PRs, so the subject that reaches master is the PR title (66 chars); rewriting 5 commits to fix a subject that never lands was not judged worth the force-push (round 1)
- [rules/verification-gap] internal/ipc/protocol.go fails gofmt — pre-existing on master, entirely in message-type constant-block alignment this PR never touched; fixing it here would be unrelated churn on lines the diff does not own (round 1)
- [security/INFO] decode leniency: parseEnvelope accepts a pane_output frame whose span is brace-flat and pane_id-prefixed but internally invalid, where json.Unmarshal rejects it. Accepted as designed — closing it needs json.Valid, measured at 47us on an 8 KB payload, which gives back most of the win. The error is MOVED not swallowed (DecodePayload still fails) and TestFastPaths_KnownValidityLimits pins the boundary (round 1)

## Round 2 additions (same review round, code-quality agent reported late)
- [code-quality/1] TestBroadcast_SlowConnDoesNotBlockFastConn left flaky by the encoder speedup — unpaced producer vs 64 slots + one socket buffer; failed 3x plain / 0x on master while passing -race every time — producer now paced, only Broadcast calls timed — round 1
- [code-quality/2] payloadInlinable permitted ENVELOPE KEY INJECTION: payload `{},"type":"shutdown","x":{}` produced a frame decoding cleanly as Type "shutdown". The documented residual risk ("malformed frame / error is MOVED") was measurably false and a test asserted it — payloadInlinable now requires one complete JSON value (paneOutputSpan free on the hot path, json.Valid elsewhere) — round 1
- [code-quality/3] payloadInlinable's number branch accepted 11 shapes json.Marshal never emits — subsumed by the json.Valid fix — round 1
- [code-quality/4] the spec's required payload-invariant test was never written — added TestNewMessage_IsTheOnlyPayloadProducer, a go/parser walk over cmd/ and internal/, mutation-verified — round 1
- [code-quality/5] FuzzAppendEnvelopeRawPayload bare-returned in its out-of-contract branch, which is why ~18M execs missed the injection — now asserts the envelope survived, injection shapes seeded — round 1
- [code-quality/low] EncodeFrame(nil) panicked where json.Marshal emitted null — nil check added — round 1
