# claudesessions fallback gaps — design

**Date:** 2026-08-20
**Status:** Draft, awaiting review
**Branch:** `fix/claudesessions-fallback-gaps` (off `775576f`, v1.62.5)
**Origin:** Three findings deferred at #174's merge, plus one defect #174 introduced
**Supersedes:** the first draft of this file, whose evidence section was measured on
the wrong population and is retracted in full — see *Retractions*.

## Summary

Four changes in `internal/claudesessions`, all consequences of the transcript
fallbacks added in #174 (`9e6d4a9`, shipped in v1.62.5):

1. `TranscriptPath` became dead exported production code when `ReadDetailIn`
   inlined the join it used to own.
2. The `ai-title` title pass carries a comment that is measurably false, and the
   guard a reviewer proposed for it would make the pass dead code.
3. `readDetail`'s second pass is gated on "found no typed prompt" rather than on
   the transcript's schema, so a recording transcript with no typed prompt
   re-reads the whole file to find nothing.
4. **The `promptSource == ""` guard in both fallback paths is a per-LINE check
   documented as a per-FILE claim.** On transcripts that record the field, it
   admits entries that are not typed prompts at all. Three sessions on this
   machine are titled `a`, `main-agent`, and a directory path *today*.

§4 is a live user-visible defect introduced in #174. §1–§3 are corrections to
code and prose from the same PR.

## Retractions

The first draft of this spec drew its evidence from 25 transcripts selected with
`ls -S` (largest first). Both halves of that were wrong:

- **Wrong sort.** Transcript size correlates almost perfectly with how much the
  user typed, so sampling the largest files drew from the population *least*
  likely to exhibit "no typed prompt" — the exact variable under test. The draft
  concluded §3 had zero incidence. It has four.
- **Wrong population, in the opposite direction.** A reviewer re-measured over
  6,029 files and concluded §3's target case never occurs. That corpus is
  `find`-recursive; 5,836 of those files are subagent transcripts (see
  *Population*) that this package never opens.

Every rate in the first draft is retracted. The mechanism arguments survived; the
frequencies did not. Rates below are stated with their population, and all come
from one machine and one user — see *Risks*.

## Population

`ListIn` resolves a project directory and calls **`os.ReadDir`**
(`internal/claudesessions/claudesessions.go:212`), filtering on a `.jsonl` suffix
(`:240`) behind a regular-file guard. It does not descend. So the population this
package can ever see is exactly the files one level under a project directory:

```
~/.claude/projects/*/*.jsonl                      →   193   ← the population
~/.claude/projects/**/subagents/agent-*.jsonl     → 5,836   ← never opened
```

The nested files are subagent and workflow transcripts
(`<project>/<session-uuid>/subagents/…`). They have no typed human prompts by
construction, so including them biases every measurement in this document's
subject area. **All figures below are over the 193.**

## Evidence

Field-presence and leading-token greps only; no transcript body was read beyond
the first 40 characters of entries quoted as defects.

**`promptSource` takes five values.**

| value | lines | value | lines |
|---|---|---|---|
| `system` | 3735 | `queued` | 47 |
| `typed` | 2892 | `sdk` | 3 |
| `suggestion_accepted` | 429 | | |

**24 of 193 transcripts have zero `"promptSource":"typed"` file-wide** — these are
the ones where `readDetail`'s second pass runs and `readTitle` falls past its
first pass. They split:

- **20 record no `promptSource` at all.** These do NOT, as an earlier draft
  claimed, give the second pass real work. Broken down:
  - **5 contain no `"type":"user"` entry at all** — their types are
    `attachment`, `last-prompt`, `mode`, `permission-mode`. They are not
    conversation transcripts; `ListIn` lists them as sessions regardless, which
    is a separate matter recorded under *Non-goals*.
  - **15 have user entries whose first string-content entry is machinery** —
    `<local-command-caveat>` ×13, `<bash-input>` ×2.

  **Zero of the 20 contains a real user prompt.** The second pass recovers
  nothing on this machine; it only ever counts machinery. The two transcripts
  that do gain a correct title from the fallback path (`fc8e3889`, `fd72815d`)
  are *recording* files with old-schema heads — a different population.
- **4 record `promptSource`** — §3's target case. Named, with sizes:

  | transcript | size | `promptSource` in head? |
  |---|---|---|
  | `E--Projects-Stukans-monorepo/332fec5d…` | 108,167 B | yes |
  | `…-fix-running-indicator/47acfe01…` | 56,550 B | yes |
  | `…-fix-running-indicator/b384c502…` | 54,134 B | yes |
  | `…-fix-running-indicator/87eb2ea8…` | 41,363 B | yes |

**The head is not representative of its own file.** 6 of 25 sampled large
transcripts record `promptSource` file-wide but not within the first
`titleScanBytes`. These are long sessions that outlived a Claude upgrade —
old-schema head, new-schema tail. Any claim of the form "this transcript's schema
is X", derived from the head, is false for roughly a quarter of long sessions.
This kills the uniformity argument the first draft used to justify §3.

**`ai-title` is not an old-schema marker, and is stable.** 21 of the 25 sampled
large transcripts carry `ai-title` entries *alongside* `promptSource`. Each
transcript carries exactly **one distinct `aiTitle` value**, repeated up to 3,116
times. The entry shape is `{"type":"ai-title","aiTitle":…,"sessionId":…}`, which
is what `readTitle`'s fallback 1 parses. #174's spec recorded this shape as
*"transcribed from the fork's test fixtures, not from a transcript anyone here
has inspected"* — that caveat is discharged, and the inference drawn from it was
wrong.

**What the §4 defect produces today.** For the four recording zero-typed
transcripts, `readTitle` currently yields:

```
47acfe01  →  "a"
b384c502  →  "main-agent"
332fec5d  →  "E:/Projects/Stukans/monorepo/.claude/wor…"
87eb2ea8  →  (nothing; falls through to the bare UUID)
```

and separately, on transcripts whose *head* is old-schema, it yields command
machinery:

```
e57ea64e, 4b56614d, 8f8c8498  →  "<command-name>/goal</command-name> <command-message>goal…"
9a540121                      →  "<local-command-caveat>Caveat: The messages below were generated…"
```

Two others (`fc8e3889`, `fd72815d`, both April-dated heads) yield **correct**
titles from the same pass. Any fix must keep those.

**Two structural markers separate machinery from prompts.** These were found
after the first rewrite and they change §4's design; they are the reason it no
longer needs a schema gate.

| marker | on typed prompts | what it identifies |
|---|---|---|
| `toolUseResult` present | **0 / 2,698** | a tool RESULT whose content is a bare string |
| `isMeta: true` | **0 / 2,698** | Claude-injected meta text in the user turn |

`toolUseResult` is the one that matters most, because it falsifies the premise
`contentIsString` encodes. Of user entries with **string** `message.content`,
tool results are not the exception — every untagged non-prompt observed (`a`,
`main-agent`, a directory path, `ls` and `git status` output) is a tool result
carrying this field. The content *shape* was never the discriminant; the sibling
field is.

**The machinery tag census, anchored correctly.** An earlier count matched
`"content":"<tag` anywhere on the line, which also matches the `content` key
*nested inside* a `tool_result` block in an ARRAY-content entry —
`contentIsString` (`:613`) rejects those before any denylist could see them.
Anchoring on `"message":{"role":"user","content":"` gives the reachable set:

| tag | entries | `isMeta` | `toolUseResult` |
|---|---|---|---|
| `task-notification` | 2541 | 0 | 0 |
| `command-name` | 375 | 0 | 0 |
| `local-command-stdout` | 360 | 0 | 0 |
| `local-command-caveat` | 218 | **218** | 0 |
| `system-reminder` | 39 | **39** | 0 |
| `command-message` | 31 | 0 | 0 |
| `bash-stdout` | 17 | 0 | 0 |
| `bash-input` | 17 | 0 | 0 |

Three tags from the earlier list — `tool_use_error` (391), `persisted-output`
(71), `retrieval_status` (67) — occur **only** inside array content and are
unreachable by the guarded pass. They are dropped. `system-reminder` was
inflated 186 → 39 by the same error.

**One untagged machinery form exists**, and only `isMeta` catches it: an injected
`"A session-scoped Stop hook is now active…"` entry, which carries `isMeta:true`,
no tag, and no `toolUseResult`. Without the `isMeta` test it becomes the title of
three sessions.

**Three of the four recording zero-typed transcripts carry
`"promptSource":"sdk"`** — `47acfe01`, `b384c502`, `87eb2ea8` are the only three
`sdk` entries in the whole population. They are SDK/agent-driven sessions, not
human conversations, which is why `main-agent` is plumbing rather than a memory
hook. The fourth, `332fec5d`, carries one `"promptSource":"queued"` entry — real
typed text that no pass promotes, because fallback 2 considers only entries with
no `promptSource` at all. See *Non-goals*.

## Goals

1. Remove the parallel join and restore the `…In` symmetry the package has (§1).
2. Make the `ai-title` pass's comment true, and justify its lack of a guard on
   grounds that survive §4 (§2).
3. Skip `readDetail`'s second pass when the transcript's schema proves it cannot
   contribute (§3).
4. Stop both fallback paths promoting non-prompts to titles and prompt counts,
   without costing the old-schema transcripts their correct titles (§4).

## Non-goals

- **No change to `readTitle`'s three-pass structure.** Extracting the passes into
  named helpers was raised in review; it is a readability change with no defect
  behind it and would obscure the corrections §2 and §4 make.
- **No `syncPaneMeta`/`pluginCaps` work.** Separate concern, separate PR.
- **No change to which `promptSource` values count as prompts.**
  `suggestion_accepted` (429) and `queued` (47) are real user inputs that the
  typed pass excludes from `UserPrompts`. Whether that exclusion was chosen or
  inherited is a separate question, recorded here and deliberately not answered.

  **Accepted fallout, stated because it is otherwise invisible:** `332fec5d`
  holds exactly one real typed prompt, recorded as `"promptSource":"queued"`.
  The typed pass skips it by this non-goal, and §4 removes the tool-result text
  that currently stands in for a title, so that session goes from a wrong title
  to no title. Titling it correctly requires widening the accepted value set —
  which is this non-goal, and a separate change.

- **No change to which files `ListIn` treats as sessions.** 5 of the 193 hold no
  `"type":"user"` entry at all (`attachment`, `last-prompt`, `mode`,
  `permission-mode`) and are listed as sessions regardless. Filtering them is a
  distinct defect with a distinct fix; §4 merely stops them being titled from
  machinery.

---

## §1 — `TranscriptPath` is dead production code

### Problem

`TranscriptPath` (`claudesessions.go:175`) is exported with **no production
caller**. Verified independently twice: every other repo hit is
`claudehook.SessionRecord.TranscriptPath` or `claudehook`'s local
`refreshTranscriptPath` (`internal/claudehook/runhook.go:145`, `:493`) — a
different type and a different function — or a test.

It died in #174, when `ReadDetailIn` inlined the join it needed under an explicit
config dir. `TestTranscriptPath_BuildsJSONLPath` now certifies a join nothing
calls, while the join that *is* called has no direct test.

### Design

Add the `…In` variant the package already has elsewhere, and route both spellings
through it:

```go
// TranscriptPathIn returns one session's transcript path under an explicit
// config dir. Takes the directory for the same reason ProjectDirIn does: a
// caller that already resolved one must not be at the mercy of a concurrent
// Setenv, and tests need no environment at all.
func TranscriptPathIn(configDir, cwd, sessionID string) string {
	dir := ProjectDirIn(configDir, cwd)
	if dir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(dir, sessionID+".jsonl")
}

// TranscriptPath returns the absolute path of one session's transcript, or ""
// when the config directory cannot be resolved or either argument is empty.
func TranscriptPath(cwd, sessionID string) string {
	return TranscriptPathIn(ConfigDir(), cwd, sessionID)
}
```

`ReadDetailIn` calls `TranscriptPathIn` and checks the result for `""`, replacing
both the inlined join and the separate `dir == ""` check. The existing error text
(`"no transcript path for this session"`) is preserved — the TUI renders it.

**Kept, not deleted.** It is the natural public spelling in a package documented
as standalone and importable. What is fixed is that it is no longer a *parallel
implementation*: it delegates, so the two cannot drift.

### Success criteria

- `grep -rn 'filepath.Join(dir, sessionID' internal/claudesessions/` returns
  exactly one site, inside `TranscriptPathIn`.
- A test asserts `ReadDetailIn` reads the path `TranscriptPathIn` produces.
- `TranscriptPath` and `TranscriptPathIn` agree when the environment is unset.

---

## §2 — The `ai-title` pass keeps no schema guard; its comment is corrected

### Problem

`readTitle`'s fallback 1 (`claudesessions.go:550`) claims:

> *"a `{"type":"ai-title"}` entry, emitted by builds that do not record
> promptSource. […] an install whose transcripts carry both is unaffected."*

Both sentences are false. Current builds emit `ai-title` **alongside**
`promptSource` (21/25 sampled), and such an install **is** affected whenever a
transcript has no typed prompt.

A reviewer proposed adding the `promptSource`-absent guard fallback 2 carries, to
restore #174's "strictly additive" claim. **That would make this pass dead code
on every current transcript.**

### Design

Keep the pass ungated. Rewrite the comment to say what it does and why it needs
no guard — **without contrasting it against fallback 2**, whose guard §4 is
concurrently fixing. Justifying one pass by appeal to a sibling's safety would
write a second false comment into the twenty lines this PR exists to de-falsify.

```go
	// Fallback 1 — a {"type":"ai-title"} entry. Reached only when the typed
	// pass above found nothing.
	//
	// Deliberately NOT gated on the transcript's schema. Measured 2026-08-20:
	// current Claude builds emit ai-title entries AND promptSource, so gating
	// on the field's absence would make this pass dead code. It fires for any
	// transcript with no typed prompt, whatever its schema.
	//
	// A guard would buy nothing anyway, because of what this pass reads.
	// aiTitle is a dedicated title field: the only values it can yield are a
	// title or nothing. Measured: one distinct value per transcript, however
	// many times the entry repeats. Contrast the passes that promote message
	// CONTENT, which must first establish that the content is a prompt at all.
```

### The accepted behaviour change

A session with an `ai-title` and no typed prompt now shows that title where a
bare UUID appeared before. This departs from #174's "strictly additive" promise,
accepted deliberately: an AI-generated title beats a UUID, and the field cannot
carry anything else. Observed once in 25 (`b61c1509`).

### Why no input validation is needed

Checked, so the next reader need not: `sanitizeTitle` (`:728`) maps `\n\r\t` to
spaces, drops `unicode.IsControl` and `unicode.Cf` (which covers the bidi
overrides `internal/tui/remotetext.go` exists to defend against), collapses
whitespace, and truncates to `MaxTitleRunes`. The empty result is rejected by the
`text != ""` check at the call site. Stale, hostile, over-long and control-bearing
inputs are all already handled.

### Success criteria

- A test proves the pass fires on a transcript carrying **both** `ai-title` and
  `promptSource` with no typed prompt — the exact case the proposed guard would
  have killed. This is the regression test against someone re-adding it.
- Existing sidechain and non-`ai-title`-entry tests pass unchanged.
- `docs/superpowers/specs/2026-08-19-claude-session-seam-design.md` is amended:
  its "not from a transcript anyone here has inspected" note is discharged, and
  the inference drawn from it was wrong.

---

## §3 — Gate `readDetail`'s second pass on schema

### Problem

`readDetail`'s second pass (`:439`) is entered on `d.UserPrompts == 0` — "the
typed pass found nothing", which is not "this transcript has no `promptSource`".
A recording transcript with no typed prompt re-reads the entire file and
unmarshals every `"type":"user"` line, discarding all of it.

Incidence: **4 of 193** (17% of the 24 that reach the pass).

### Design

Before entering the pass, scan the first `titleScanBytes` for `"promptSource"`:

```go
	// Skipped when the transcript RECORDS promptSource. See recordsPromptSource
	// for why that is a correctness improvement and not only a speed one.
	if d.UserPrompts == 0 && !recordsPromptSource(f) {
		…existing pass…
	}
```

**Operand order is load-bearing.** `&&` short-circuits, so `recordsPromptSource`
runs only on transcripts that already found no typed prompt — the rare path.
Reversing it puts a seek and a 64 KiB read on *every* detail read.

```go
// recordsPromptSource reports whether the transcript's head mentions
// promptSource, i.e. whether the build that wrote it records the field. Reads
// the same titleScanBytes window readTitle uses, then seeks back so the pass's
// own Seek(0) is unaffected. A seek or read error answers false, falling
// through to the pass exactly as today.
//
// This is a CORRECTNESS gate that also saves work, not a pure optimisation.
// On a transcript that records promptSource, an entry lacking the field is not
// a typed prompt — whatever else it may be — so skipping the pass cannot lose
// a prompt. It can only suppress entries the per-line filters would
// misclassify, which is what §4 addresses at the other end.
//
// The two error directions are NOT symmetric. A false positive is near
// impossible: the head is the OLDEST part of the file, and a build that records
// the field at session start does not stop mid-session. A false negative — the
// field appears only past the window, measured on 6 of 25 long transcripts that
// outlived an upgrade — costs one re-read that happens today anyway. So the
// head is a sample in one direction only, and the failure mode is current
// behaviour.
//
// The substring test is sound because of JSON escaping: a mention of
// promptSource inside message content is stored as \"promptSource\", which
// cannot match the quoted needle. Content can never fake the field.
func recordsPromptSource(f *os.File) bool
```

The first draft justified this with "schema is uniform within a transcript, so
the head is representative." That is **false** and is not the argument. The
argument is the failure mode: answering `false` runs the pass, which is today's
behaviour.

### Honest billing

All four observed instances are ≤ 108 KB, so the saved re-read is ~100 KB —
microseconds. The 88 MB transcript the surrounding comment cites has never been
observed in this state. §3 is **correct-by-schema and cheap insurance**, not a
measured performance win. It should be billed that way in review.

### Success criteria

- A recording transcript with no typed prompt is **not** rescanned — asserted on
  the result (`UserPrompts == 0`), since the pass is unexported.
  **The fixture must contain string-content user entries the rescan would
  otherwise count**, and they must survive §4's filters (no `toolUseResult`, no
  `isMeta`, untagged). Without that the assertion holds whether or not the gate
  exists, and the mutation check below is vacuous.
- A **false-negative** fixture: `promptSource` present only PAST the 64 KiB
  window, zero typed prompts anywhere. The gate answers `false`, the rescan
  runs, and the result must equal today's behaviour. This is the only fixture
  that exercises `recordsPromptSource` returning `false` on a recording file —
  the direction the honest-billing argument rests on.
- A **mixed-schema** fixture — old-schema head, typed prompt only in the tail.
  This pins `readTitle` running fallback 2 on the head while `readDetail`'s first
  pass finds the tail prompt. Note what it does **not** do: with
  `UserPrompts >= 1`, the `&&` short-circuits and `recordsPromptSource` is never
  called, so this fixture says nothing about the gate. It is a behavioural pin
  for the head/whole-file split, and is filed here only because that split is
  what §3 reasons about.
- An old-schema transcript still IS rescanned and still reports its
  shape-detected prompts.
- Mutation check: forcing `recordsPromptSource` to `true` fails the old-schema
  test.
- **Old-schema fixtures must drive the second pass directly.** With the gate on,
  a §4 denylist gap is unreachable through current-schema fixtures — the gate
  would otherwise mask §4's test surface.

---

## §4 — The `promptSource == ""` guard is per-line, documented as per-file

### Problem

Two sites carry the same defect, both introduced in #174:

- `readTitle` fallback 2 — guard at `:594`, comment at `:575`
- `readDetail` second pass — guard at `:455`, comment at `:428`

The comments claim the check *"confines this to schemas that never record it"*
and *"a current-schema transcript can never be reclassified here."* The check is
`tl.PromptSource != ""` on a single line. A recording transcript is full of user
entries that carry no `promptSource` — command machinery and assorted non-prompt
input — and every one passes.

This is exactly the failure the comment says it prevents. It ships in v1.62.5.

### Design: reject what the entry IS, not what schema wrote it

The first rewrite proposed a schema gate on `readTitle`'s fallback 2, mirroring
§3. **That is dropped.** The measurements above supply two structural markers
that identify machinery directly, so the pass no longer needs to guess from
schema — and, unlike the gate, they never blank a transcript that does hold a
real prompt.

Three filters, applied at both sites, in this order:

| # | filter | rejects | entries |
|---|---|---|---|
| 1 | `toolUseResult` present | tool results with bare-string content | `a`, `main-agent`, paths, `ls` / `git status` output |
| 2 | `isMeta: true` | Claude-injected meta text | `<local-command-caveat>`, `<system-reminder>`, and the untagged Stop-hook notice |
| 3 | leading tag in a 6-entry denylist | the remaining tagged machinery | `<task-notification>`, `<command-name>`, `<command-message>`, `<local-command-stdout>`, `<bash-stdout>`, `<bash-input>` |

Filters 1 and 2 are maintenance-free and schema-independent: neither field ever
appears on a typed prompt (0 / 2,698 each). Only filter 3 is a list, and it is
now six tags rather than eleven, because filters 1–2 absorb two of the original
rows and three more were unreachable (see *Evidence*).

`transcriptLine` gains two fields:

```go
	IsMeta        bool            `json:"isMeta"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
```

`ToolUseResult` is decoded as `json.RawMessage` and tested with `len(...) > 0` —
a presence check, not a value check. Its shape varies by tool and none of it is
needed. Both are top-level keys, siblings of `message`, not nested inside it.

### The existing guards SURVIVE — the filters are additive

**This is the single most important sentence in §4.** The three filters are added
to the existing conditions; they replace nothing. Both guards keep every term
they have today, `PromptSource == ""` included:

```go
	// readTitle fallback 2, after the change
	if tl.Type != "user" || tl.IsSidechain || tl.PromptSource != "" ||
		!contentIsString(tl.Message.Content) || isMachinery(tl) {
		continue
	}
```

Deleting `PromptSource != ""` while adding the filters produces a **different and
wrong** feature. It is what excludes the `"sdk"` entries in `47acfe01`,
`b384c502` and `87eb2ea8` and the `"queued"` entry in `332fec5d` — none of the
three filters touches those, so the measured-effect table below only holds while
that term stays.

The filters therefore run **only on the fallback paths**, on entries that have
already passed type, sidechain, `promptSource`-absence and `contentIsString`.
They add nothing to the typed-prompt first pass, whose lines carry neither new
field.

### Matching rule

Filters 1 and 2 are field reads on the already-unmarshalled struct. Filter 3 is a
prefix test on the **decoded** content string — after `json.Unmarshal`, before
`sanitizeTitle`:

```go
	strings.HasPrefix(content, "<"+tag+">") || strings.HasPrefix(content, "<"+tag+" ")
```

Both delimiters are required: `<command-name>` closes immediately, while
`<task-notification …>` carries attributes. **No `TrimSpace`** — every observed
machinery entry starts its tag at byte 0, and a whitespace-prefixed tag is
therefore a miss, which yields today's behaviour. "Begins with", never
"contains": a prompt that merely *mentions* `<command-name>` is prose.

### Measured effect

Every observed defect, before and after:

| transcript | today | after |
|---|---|---|
| `fc8e3889` | *"I want you to buld a prototype…"* | **unchanged** |
| `fd72815d` | *"Read docs/global-question-domain.md…"* | **unchanged** |
| `47acfe01` | `a` | UUID |
| `b384c502` | `main-agent` | UUID |
| `332fec5d` | `E:/Projects/…/.claude/wor…` | UUID |
| `9a540121`, `4afd60df` | `<local-command-caveat>…` | UUID |
| `e57ea64e`, `4b56614d`, `8f8c8498` | `<command-name>/goal…` | UUID |
| `9027ed39` | `<bash-input>git status</bash-input>` | UUID |
| `87eb2ea8` | (none) | UUID |

Both correct titles are preserved; all ten garbage titles are removed. Filters
1–2 alone leave the `<command-name>` cases; filter 3 alone leaves `a` and
`main-agent`; filter 2 alone catches the Stop-hook notice that neither of the
others sees. All three are load-bearing.

### Filter 3 — the denylist



Six tags, derived from entries whose `message.content` is a string — the only
ones the guarded pass can reach:

| tag | entries | |
|---|---|---|
| `task-notification` | 2541 | |
| `command-name` | 375 | |
| `local-command-stdout` | 360 | |
| `command-message` | 31 | |
| `bash-stdout` | 17 | `!`-prefix shell-forward feature |
| `bash-input` | 17 | same |

`local-command-caveat` and `system-reminder` are **not** listed — filter 2
(`isMeta`) covers both, on 218/218 and 39/39 entries respectively. Listing them
as well would be dead weight that hides which mechanism is actually load-bearing.

`tool_use_error`, `persisted-output` and `retrieval_status` appeared in the
previous draft's table and are **removed**: they occur only inside array content,
which `contentIsString` (`:613`) rejects before the denylist runs.
`teammate-message` is plausible but did not occur; excluded to keep the list
strictly data-derived.

**Three boundary rules, each with a counterexample behind it:**

1. **Never match on a bare leading `<`.** 80,630 string-content entries are
   ordinary prose, and legitimate user-pasted markup occurs with leading tags
   `html`, `svg`, `div`, `form`, `script`, `article`, `style`, `details`,
   `section`, `input`, `title`, `li`, `a`, `tr`, `h…`, `testsuite`, `version`.
   A "starts with `<`" rule eats real prompts.
2. **Match the full tag with its closing delimiter** (`<command-name>` or
   `<command-name `). Prefix matching makes `bash` swallow `bash-stdout`, and a
   bare `h` collide with `html`.
3. **`<` followed by a non-tag character is prose.** 184 entries begin `</`,
   `<!`, or `<` plus a digit. Exact-tag matching handles these for free.

The check applies at **both** sites — `readTitle` fallback 2 and `readDetail`'s
second pass — because the same entries inflate `UserPrompts` and become
`FirstPrompt`/`LastPrompt` in the detail panel, not only titles.

Failure mode is benign and strictly better than today: an unrecognised machinery
tag yields the bad title it already yields, and a user whose real prompt opens
with a denylisted tag loses one candidate while the scan takes the next entry.

### Success criteria

- `fc8e3889` and `fd72815d`'s shape — an old-schema head whose first
  string-content entry is ordinary prose — still yields its correct title. This
  is the *preservation* test and it must exist before any filter lands.
- One fixture per filter, each placing the rejected entry **before** a real
  prompt and asserting the real prompt wins, at **both** call sites. Each
  fixture must be reachable ONLY by the filter it targets, or its mutation check
  is shadowed by a neighbour:
  1. a string-content entry carrying `toolUseResult`, **untagged and not
     `isMeta`** — otherwise deleting filter 1 still passes via 2 or 3,
  2. an entry carrying `isMeta: true` and **no** tag (the Stop-hook shape) —
     otherwise deleting filter 2 still passes via 3,
  3. a `<command-name>`-led entry carrying neither new field.
- At the `readDetail` site each fixture asserts **`UserPrompts == 1`** as well as
  the text, not the text alone. The count is incremented unconditionally for a
  rejected entry with empty text, so a text-only assertion can pass while the
  count is wrong — which is the stronger failure this pass can produce.
- Fixtures mirror real entry shape: `isMeta` and `toolUseResult` are top-level
  siblings of `message`. A fixture written to match the struct rather than the
  wire would pass even if the real placement differed.
- A `<html>`-led entry IS taken as the title — the denylist must not eat
  user-pasted markup.
- Mutation checks, each paired with the fixture that makes it bite: deleting
  filter 1 fails (1); deleting filter 2 fails (2); emptying the denylist fails
  (3). The fixture notes above are what keep these from being vacuous.

---

## Rejected alternatives

**Deleting `TranscriptPath` (§1).** The natural public spelling in a package
documented as importable. Delegation removes the drift risk, which was the actual
problem.

**Adding the schema guard to fallback 1 (§2).** Makes the pass dead code on every
current transcript to restore a claim in a merged PR description.

**Justifying §2 by contrast with fallback 2 (§2).** The first draft's argument.
Fallback 2's guard does not deliver the safety the contrast credits it with — §4
is fixing it in the same PR.

**"Schema is uniform within a transcript" (§3).** False: 6 of 25 sampled long
transcripts have an old-schema head and a new-schema tail.

**Tracking a `sawPromptSource` flag during `readDetail`'s first pass (§3).**
Avoids the seek but adds a `bytes.Contains` to every line of the loop whose byte
pre-filter is what keeps an 88 MB transcript affordable. Moves cost from the rare
path to the hot one.

**A schema gate on `readTitle`'s fallback 2 (§4).** The previous draft's design,
mirroring §3. Dropped once `toolUseResult` and `isMeta` were measured. The reason
is **not** that it would have blanked `fc8e3889` / `fd72815d` — measured, both
have old-schema heads (`headHasPS=0`), so the gate answers `false` there and
their titles survive it. The reason is that it rejects at the wrong granularity:
it blanks a whole transcript's title on a schema signal, where the filters reject
the individual entries that are not prompts. Entry-level rejection is strictly
more precise, needs no schema inference, and reaches every case the gate reached.

**A denylist alone (§4).** Leaves `a`, `main-agent` and a directory path as
titles — none is tag-wrapped — and leaves the untagged Stop-hook notice.

**`isMeta` alone (§4).** Covers `local-command-caveat`, `system-reminder` and the
untagged notice, but is absent on `task-notification`, `command-name`,
`command-message` and both `bash-*` forms — 3,341 entries.

**Trusting `contentIsString` to mean "this is a prompt" (§4).** It is what the
current code assumes. Measured false: of user entries with string
`message.content`, tool results are the common case, not the exception. Shape
was never the discriminant.

**Treating any `<`-leading entry as machinery (filter 3).** Eats user-pasted HTML,
which is a legitimate prompt.

---

## Risks

| Risk | Mitigation |
|---|---|
| The denylist is incomplete | Failure mode is today's behaviour — an unrecognised tag yields the bad title it already yields. No regression, only unfixed cases. Two of the three filters need no list at all, so a new tag has to evade `toolUseResult` *and* `isMeta` before the list matters. |
| A future machinery form evades all three filters | Bounded to today's behaviour. Note also that the vulnerable corpus is not growing: current builds emit `<command-name>` as `"type":"system","subtype":"local_command"` and most `task-notification` traffic as `"type":"queue-operation"` — neither shape is a `type:"user"` entry, so neither reaches the guarded passes. |
| §4 suppresses a title a user wanted | The three affected `sdk` sessions are agent-driven, where `main-agent` describes plumbing rather than content; a stable-looking label that names nothing invites a misclick, where a UUID is honestly anonymous. The one real loss is `332fec5d`'s `queued` prompt — see *Non-goals*. |
| §3's head sample misclassifies | Costs one re-read that happens today anyway; the per-line filters keep the answer correct either way. |
| **§3 and §4 can disagree about the same entry** | Deliberate and eyes-open: §3 gates `readDetail`'s counts on schema, while `readTitle`'s fallback 2 stays ungated. Machinery that evades all three filters on a recording transcript can still become a *title* while the detail panel reports zero prompts. Bounded to today's behaviour. Do not "fix" the asymmetry by gating `readTitle` — that is the rejected alternative above, and the gate rejects at the wrong granularity. |
| §1's delegation changes `TranscriptPath` when the env is unset | A test asserts the two spellings agree; both resolve through `ConfigDir()`. |
| **All measurements are one machine, one user, 193 transcripts** | Every mechanism argument stands independently of frequency. Rates are cited as sample-local throughout and no design decision rests on a rate alone. |
| A future reader re-adds the §2 guard by analogy with §4 | §2's comment states the asymmetry and a named test fails if the guard returns. |

## Delivery

One PR on `fix/claudesessions-fallback-gaps`, four commits — one per section, so
any section can be dropped by dropping a commit **before merge**. The repo
squash-merges, so this granularity does not survive onto master; it is a
review-time affordance, not a revert-time one.

**Commit order is §1, §2, §4, §3 — not section order.** §4 must land with or
before §3: §3's gate masks §4's test surface on current-schema fixtures, and
§3's safety argument assumes the filters already exist behind it. Numbering the
sections by subject and committing them by dependency is deliberate.

**§4 must land with or before §3.** §3's safety story assumes the denylist exists
behind it, and §3's gate masks §4's test surface on current-schema fixtures (see
§3's success criteria).

**PR title:** `fix(claude): stop non-prompts becoming session titles`

Release-critical — it is the squash subject the release pipeline classifies.
`fix` is right: §4 repairs user-visible behaviour shipped in v1.62.5.

**Changelog:** the PR touches `internal/`, so CI requires a fragment. §4 is the
user-visible item. One `changelog.d/fixed-session-title-fallbacks.md` with the
mandatory three-line `headline:` block (≤64 bytes, no `"` or `\`).

## Verification

- `./scripts/dev.sh test internal/claudesessions` for the inner loop.
- `./scripts/dev.sh test` and `./scripts/dev.sh vet` before the PR.
- `./scripts/dev.sh test-race` — this is the CI command; `dev.sh test` is not.
- Every new guard mutation-checked: removing it must fail a named test.
- No dev-mode runtime check needed — all four changes are in a pure, stdlib-only
  package with no daemon or PTY involvement.
- **Re-run the population sweep after implementing** and confirm all ten rows of
  §4's measured-effect table: the four untagged cases (`47acfe01`, `b384c502`,
  `332fec5d`, `87eb2ea8`) and the six tagged ones (`e57ea64e`, `4b56614d`,
  `8f8c8498` — `command-name`; `9a540121`, `4afd60df` — `local-command-caveat`;
  `9027ed39` — `bash-input`) no longer produce their recorded titles, and that
  `fc8e3889` and `fd72815d` still produce theirs.
