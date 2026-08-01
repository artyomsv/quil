# Remote Stale Binary Record — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Registry:** RD-045, RD-046 — see `docs/roadmap/remote-daemon.md` § Work registry.

**Goal:** A user attaching to a host whose quil binary has disappeared gets offered an install, instead of being told their CPU architecture is wrong and left to hand-edit `config.toml`.

**Architecture:** Replace the config-based install loop guard with a probe-based one, and heal the recorded binary path from what the probe reports. No new machinery — `remoteinstall.Probe.ExistingPath` already carries the fact, and `runRemoteSetup` already calls `RunProbe`; the guard just decides before it.

**Tech Stack:** Go 1.25, stdlib only. No new dependencies.

## Global Constraints

- Module path `github.com/artyomsv/quil`; Go 1.25.
- Docker-only build/test: `./scripts/dev.sh build|test|vet|test-race`. Go is not installed locally.
- Production isolation: never touch `~/.quil/`.
- Commit subjects imperative, ≤72 chars, cite the RD id. No AI/model/vendor attribution.
- **Positive evidence only.** The config record may be mutated when the probe ANSWERED. Never on a probe error, a timeout, or unparsed output.
- The probe must run **once** per `offerRemoteInstall` call. It is an ssh round trip; the existing `runRemoteSetup` probe is reused by passing the result through.
- Local (non-remote) behaviour is untouched. Every change is behind `remoteMode()`-reached code paths.

---

## The truth table this implements

`offerRemoteInstall` currently branches on `alreadyProvisionedFn(dest)` — "config holds a path". It should branch on what the host reports:

| recorded in config | `probe.ExistingPath` | meaning | action |
|---|---|---|---|
| anything | `""` | not installed — removed since, or never | clear record if set; **offer install** |
| `X` | `X` (equal) | present at the path we ran, still failed | wrong-arch message; **no install** (loop guard) |
| `X` or `""` | `Y` ≠ `X` | we ran the wrong path | record `Y`; **re-dial** |

Row 2 is what preserves the loop guard: "offer forever" requires a path that exists, which only becomes true after a successful write.

Row 3 subsumes two separate recoveries — an admin moving the binary, and a hand-installed host that was never recorded. `offerRemoteInstall`'s existing comment already names the second as a known wart.

---

## Task 1 (RD-046): `Config.ClearRemoteBinary`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `func (c *Config) ClearRemoteBinary(dest string)` — removes the entry for `dest`. Safe on a nil map and on an absent key.

- [ ] **Step 1: Write the failing test**

```go
func TestClearRemoteBinary_RemovesEntry(t *testing.T) {
	var c Config
	c.SetRemoteBinary("gpu01", "/home/u/.local/bin/quil")
	c.SetRemoteBinary("other", "/usr/local/bin/quil")

	c.ClearRemoteBinary("gpu01")

	if got := c.RemoteBinary("gpu01"); got != "" {
		t.Errorf("RemoteBinary(gpu01) = %q, want \"\"", got)
	}
	if got := c.RemoteBinary("other"); got != "/usr/local/bin/quil" {
		t.Errorf("clearing one host disturbed another: %q", got)
	}
}

func TestClearRemoteBinary_NilMapAndAbsentKey(t *testing.T) {
	// A config that predates the [remote] section has a nil map. Deleting from
	// it must be a no-op, not a panic — this runs on the failure path, where a
	// panic would replace a diagnosable error with a crash.
	var c Config
	c.ClearRemoteBinary("never-seen")

	c.SetRemoteBinary("gpu01", "/p/quil")
	c.ClearRemoteBinary("never-seen")
	if got := c.RemoteBinary("gpu01"); got != "/p/quil" {
		t.Errorf("clearing an absent key disturbed a present one: %q", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `c.ClearRemoteBinary undefined`

- [ ] **Step 3: Implement**

```go
// ClearRemoteBinary forgets the recorded quil path for dest.
//
// Called when the probe reports the host has no quil at all: the record is then
// known-false, and keeping it means the next launch runs the same missing path
// and fails identically. Deleting from a nil map is a no-op in Go, so a config
// predating the [remote] section needs no special case.
func (c *Config) ClearRemoteBinary(dest string) {
	delete(c.Remote.Hosts, dest)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS

---

## Task 2 (RD-046): Persist a cleared or corrected record

**Files:**
- Modify: `cmd/quil/remote_setup.go`
- Test: `cmd/quil/remote_setup_test.go`

**Interfaces:**
- Consumes: `Config.ClearRemoteBinary` from Task 1.
- Produces:
  ```go
  func clearRemoteBinary(dest string) error
  var recordRemoteBinaryFn = recordRemoteBinary   // seam for Task 3's tests
  var clearRemoteBinaryFn = clearRemoteBinary     // seam for Task 3's tests
  ```

`recordRemoteBinary` already does load → mutate → save with the `fs.ErrNotExist` tolerance. `clearRemoteBinary` is the same shape; factor the shared half into `mutateConfig(func(*config.Config))` so the missing-file tolerance cannot drift between them.

- [ ] **Step 1: Write the failing test**

```go
func TestClearRemoteBinary_RoundTripsThroughDisk(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	if err := recordRemoteBinary("gpu01", "/home/u/.local/bin/quil"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := clearRemoteBinary("gpu01"); err != nil {
		t.Fatalf("clear: %v", err)
	}

	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.RemoteBinary("gpu01"); got != "" {
		t.Errorf("RemoteBinary after clear = %q, want \"\"", got)
	}
}

func TestClearRemoteBinary_MissingConfigIsNotAnError(t *testing.T) {
	// The first-run state. Clearing a record that was never written must
	// succeed silently rather than reporting a failure the user cannot act on.
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := clearRemoteBinary("gpu01"); err != nil {
		t.Errorf("clear on a missing config: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `clearRemoteBinary undefined`

- [ ] **Step 3: Implement**

```go
// mutateConfig applies fn to the config on disk and saves it.
//
// A missing config.toml is the NORMAL first-run state, not a failure —
// config.Load surfaces DecodeFile's fs.ErrNotExist verbatim. Shared by the
// record and clear paths so that tolerance cannot drift between them.
func mutateConfig(fn func(*config.Config)) error {
	path := config.ConfigPath()
	cfg, err := config.Load(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("load config: %w", err)
	}
	fn(&cfg)
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func recordRemoteBinary(dest, binary string) error {
	return mutateConfig(func(c *config.Config) { c.SetRemoteBinary(dest, binary) })
}

// clearRemoteBinary forgets dest's recorded path after the probe proved the
// host has no quil. Positive evidence only — see offerRemoteInstall.
func clearRemoteBinary(dest string) error {
	return mutateConfig(func(c *config.Config) { c.ClearRemoteBinary(dest) })
}
```

Note the existing `config.Load` returns a `Config` value, so `fn(&cfg)` mutates the local copy that `Save` then writes. Verify against the real signature before writing.

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test`
Expected: PASS

---

## Task 3 (RD-045): Probe before the loop guard

**Files:**
- Modify: `cmd/quil/remote_setup.go` (`offerRemoteInstall`, `alreadyProvisionedFn` → removed, new `probeRemoteFn` seam)
- Test: `cmd/quil/remote_setup_test.go`

**Interfaces:**
- Consumes: `clearRemoteBinaryFn` / `recordRemoteBinaryFn` from Task 2.
- Produces:
  ```go
  // probeRemoteFn runs the host probe. Swappable so the guard's truth table is
  // testable without ssh.
  var probeRemoteFn = func(dest string) (remoteinstall.Probe, error) { … }

  // recordedRemoteBinaryFn reads the recorded path. Replaces alreadyProvisionedFn,
  // which answered a bool where the path itself is what the decision needs.
  var recordedRemoteBinaryFn = func(dest string) string { … }
  ```

**Steps:**

- [ ] **Step 1: Write the failing tests — the truth table, one case each**

```go
// Row 1: the host has no quil. The record is known-false and the install must
// be offered. This is the shipped bug: today a recorded path alone suppresses
// the offer and prints an architecture theory instead.
func TestOfferRemoteInstall_ProbeFindsNothing_ClearsRecordAndOffers(t *testing.T)

// Row 2: the recorded path is exactly what the probe found, and it still would
// not run. Genuine wrong-arch — the loop guard must hold.
func TestOfferRemoteInstall_ProbeMatchesRecord_DoesNotOffer(t *testing.T)

// Row 3a: quil is present somewhere other than the recorded path (an admin
// moved it). Correct the record and re-dial rather than reinstalling.
func TestOfferRemoteInstall_ProbeFindsDifferentPath_RecordsAndRetries(t *testing.T)

// Row 3b: the same branch covers a hand-installed host that was never recorded.
func TestOfferRemoteInstall_HandInstalledUnrecorded_RecordsAndRetries(t *testing.T)

// The safety rule, and the one most likely to be lost in a later refactor: a
// probe that FAILED is not evidence the binary is gone. Clearing on it would
// downgrade a working host to bare `quil` on the non-interactive PATH, which is
// invisible on Debian/Ubuntu — the exact failure the record prevents.
func TestOfferRemoteInstall_ProbeError_LeavesRecordUntouched(t *testing.T)

// RemedyUpgrade is exempt: quil ran over there, so the binary is fine and the
// probe result cannot change the decision.
func TestOfferRemoteInstall_UpgradeRemedy_SkipsGuardEntirely(t *testing.T)
```

Each test swaps `probeRemoteFn`, `recordedRemoteBinaryFn`, `clearRemoteBinaryFn`, `recordRemoteBinaryFn` and `runRemoteSetupFn`, and asserts both the return value and which config mutation was called.

- [ ] **Step 2: Run to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — the seams do not exist yet.

- [ ] **Step 3: Implement the new guard**

Replace the `alreadyProvisionedFn` branch with:

```go
	// The loop guard, and it asks the HOST rather than the config. A recorded
	// path proves an install landed once; it says nothing about now, and every
	// way a remote binary can vanish (image rebuilt, home wiped, moved by an
	// admin, OS reinstalled) leaves a record pointing at nothing. Reading it as
	// "installed" produced an architecture theory for a host whose arch was
	// always correct, and no way out but editing config.toml by hand.
	//
	// RemedyUpgrade is exempt: quil ran over there, so the binary is fine.
	if remedy != remoteinstall.RemedyUpgrade {
		if done, retry := healRemoteRecord(dest); done {
			return retry
		}
	}
```

with the decision itself extracted so it is testable as a unit:

```go
// healRemoteRecord reconciles the recorded binary path with what the host
// actually has. Returns done=true when the caller should stop and use retry as
// its result; done=false means "carry on and offer an install".
func healRemoteRecord(dest string) (done, retry bool) { … }
```

- [ ] **Step 4: Run to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS

- [ ] **Step 5: Mutation-verify the safety rule**

Invert the probe-error guard so a failed probe clears the record, and confirm `TestOfferRemoteInstall_ProbeError_LeavesRecordUntouched` fails. Revert. A test never watched fail is not evidence.

---

## Task 4 (RD-045): Reuse the probe, and stop asserting what was not checked

**Files:**
- Modify: `cmd/quil/remote_setup.go` (`setupOptions`, `runRemoteSetup`, the wrong-arch message)

**Steps:**

- [ ] **Step 1: Pass the probe through**

Add `probe *remoteinstall.Probe` to `setupOptions`. `runRemoteSetup` uses it when non-nil instead of calling `RunProbe`, so the attach path costs one ssh round trip rather than two. `quil remote setup` passes nil and probes as it does today.

- [ ] **Step 2: Make the wrong-arch message report a finding**

It currently says the binary "was just written" and that the cause "almost always" is a wrong architecture — a guess printed at the one moment the user cannot check it, and the reason the shipped failure sent a user to run `uname -sm` that came back correct. With the probe in hand it can name the path actually found and the `file` output for it.

- [ ] **Step 3: Verify no double probe**

A test that counts `probeRemoteFn` calls across a full `offerRemoteInstall` → `runRemoteSetup` sequence and asserts exactly one.

---

## Task 5: Docs

**Files:**
- Modify: `docs/roadmap/remote-daemon.md` (RD-045/RD-046 → done), `.claude/rules/remote-transport.md`, `CHANGELOG.md`

- [ ] Registry rows to `done` with what was measured.
- [ ] `remote-transport.md`: the loop guard paragraph currently describes the config-based design as correct. Rewrite it — a rule file that documents the old invariant is worse than one that omits it.
- [ ] CHANGELOG entry leading with the user-visible symptom, not the mechanism.

---

## Verification (whole change)

- [ ] `./scripts/dev.sh test`, `vet`, `test-race` green.
- [ ] Every new bound mutation-verified: the safety rule (Task 3 Step 5) and the loop guard (invert row 2 and watch the wrong-arch test fail).
- [ ] **Manual, against the real VM** — the reproduction that started this:
  1. `ssh <vm> 'rm -f ~/.local/bin/quil ~/.local/bin/quild'` with the config record left in place.
  2. `quil --remote <vm>` → must OFFER an install, not print the architecture message.
  3. Accept → installs → attaches.
  4. Confirm the record now holds the new path.
  5. `ssh <vm> 'mv ~/.local/bin/quil /usr/local/bin/quil'` (if writable) → `quil --remote <vm>` → must correct the record and attach without reinstalling.
- [ ] Local mode unchanged: `quil` with no `--remote` never reaches any of this.

## Self-review notes

- **Why not just delete the record when the dial fails?** The dial cannot tell "binary gone" from "host down" from "ssh rejected the key" — 127 arrives for the first, and the record would be destroyed by a network blip. Only the probe distinguishes, which is why the fix is a probe and not a smarter reading of the exit code.
- **Why keep a loop guard at all?** A wrong-arch binary reports 127 exactly like a missing one, so without a guard the install → 127 → offer cycle never terminates. The guard is not removed, it is re-keyed onto a fact (`ExistingPath == recorded`) instead of a memory.
- **Ordering risk.** `healRemoteRecord` runs before `runRemoteSetup` and both read config. The heal writes; the setup writes afterwards on success. Row 1 clears and then the install records — a redundant write, harmless, and simpler than threading the cleared state through.
