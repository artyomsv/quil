# Remote Daemon Phase 4 — mTLS Transport

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Registry:** RD-030 … RD-034 — see `docs/roadmap/remote-daemon.md` § Work registry.

**Prerequisites:** Phases 2 and 3 complete. This phase changes only the transport; if the UI is still reading the wrong filesystem, replacing ssh with TLS makes a broken experience differently broken.

> ### This plan is deliberately shallow, and that is the finding
>
> Phases 1.5, 2 and 3 are engineering with known answers. This one is not: its
> hard part is a **product and security decision** (RD-034), and the code that
> follows is small and conventional by comparison. Writing 800 lines of TDD
> steps for `crypto/tls` wiring before that decision is made would produce
> confident-looking detail resting on an unmade choice — which is worse than an
> honest outline, because it reads as settled.
>
> So: Task 1 is the decision, with the analysis needed to make it. Tasks 2–5 are
> scoped outlines that become full plans once Task 1 lands. **Do not start Task 2
> until Task 1 has a written answer in the registry.**

**Goal:** Let a user reach a Quil daemon without ssh, for deployments where Quil should be its own service.

**Architecture:** The `ipc.DialFunc` seam already exists and Phase 1 shaped it for exactly this — a TLS backend joins `Local` and `SSH` in `internal/transport` without the protocol layer knowing. The daemon grows an optional TLS listener alongside its Unix socket. Client certificates are the authentication.

**Tech Stack:** Go 1.25, `crypto/tls` and `crypto/x509` (stdlib). **No new dependencies** — if the design appears to need an ACME client or a PKI library, that is a signal Task 1 was answered wrong.

## Global Constraints

- Module path `github.com/artyomsv/quil`; Go 1.25.
- Docker-only build/test: `./scripts/dev.sh build|test|vet|test-race`.
- Production isolation: never touch `~/.quil/`.
- Commit subjects imperative, ≤72 chars, cite the RD id. No AI/model/vendor attribution.
- **TLS is off by default and must stay off by default.** Enabling it opens a pre-authentication network port in front of a protocol that spawns arbitrary processes. Every default, every example config, and every doc snippet ships disabled.
- **TLS 1.3 minimum.** No cipher-suite configuration knob — Go's TLS 1.3 suites are not configurable and that is the correct outcome.
- The daemon's Unix socket keeps working unchanged when TLS is enabled. TLS is additive, never a replacement.

---

## Task 1 (RD-034): Decide whether Quil issues certificates

**This is the whole phase's gate.** Everything downstream changes shape depending on the answer.

### Option A — consume only (bring your own PKI)

Quil reads a CA bundle, a server certificate/key, and a client certificate/key from paths in `config.toml`. It never generates, signs, renews or revokes anything.

- **Cost:** small. A `tls.Config` on each side and documentation.
- **Burden on the user:** they need a CA. In practice that means `step-ca`, `cfssl`, an internal PKI, or Tailscale's certs — real work, and a barrier for a solo user with two machines.
- **Security posture:** Quil holds no signing key. Revocation is the user's existing process. There is no new key-management surface.

### Option B — issue certificates

Quil generates a CA on first use, issues a server certificate for the daemon and client certificates on request, and provides some enrolment flow.

- **Cost:** large, and the cost is *ongoing*. Issuance, expiry, renewal-before-expiry, storage of a signing key on disk, revocation (a CRL or OCSP responder or a short-lived-cert refresh loop), plus recovery when the CA key is lost or leaked.
- **Burden on the user:** low at first use, then unbounded — a leaked CA key means every daemon that trusts it is reachable, and Quil would own the story for detecting and fixing that.
- **Security posture:** Quil becomes a CA. A signing key sits on a developer laptop.

### Recommendation: Option A

Three reasons, in order of weight:

1. **The enrolment problem is unsolved either way.** Option B still has to get the first client certificate onto the client machine over some already-trusted channel. That channel is, realistically, ssh — which Quil already supports and which already authenticates. Option B does not remove the bootstrap dependency; it relocates it.
2. **The blast radius is a signing key rather than a session.** With Option A, a compromised certificate reaches one daemon. With Option B, a compromised CA key reaches every daemon that trusts it, and Quil owns detection and revocation for a problem it created.
3. **The users who want this already have a PKI.** The stated motivation is deployments where Quil is its own service — a cluster, a shared machine, something behind an ingress. Those environments have certificate infrastructure. The solo user with two machines is better served by ssh, which already works and is already documented.

A middle path worth considering if Option A proves too high a barrier: ship a **`quil cert` helper** that generates a self-signed CA and a certificate pair for users who have no PKI, with the key material clearly the user's responsibility and no renewal, revocation or enrolment machinery. That is a convenience wrapper over `crypto/x509`, not a CA — the distinction is that Quil never signs anything after the initial call, and the daemon has no code path that issues anything.

- [ ] **Step 1: Write the decision down** in `docs/roadmap/remote-daemon.md`, replacing open question 5 with the answer and its reasoning. A decision that lives only in a merged pull request is a decision that gets relitigated.
- [ ] **Step 2: Set RD-034 to `done` and record which option was chosen.**
- [ ] **Step 3: If Option B was chosen, stop and rewrite Tasks 2–5.** They are written for Option A.

---

## Task 2 (RD-030): TLS dialer backend

**Files:**
- Create: `internal/transport/tls.go`, `internal/transport/tls_test.go`
- Modify: `internal/config/config.go` (a `[remote.tls]` section)

**Interfaces:**
- Produces:
  ```go
  type TLSOptions struct {
      CAFile     string // PEM bundle the client trusts
      CertFile   string // client certificate
      KeyFile    string // client key
      ServerName string // SNI + verification name; required
  }
  func TLS(addr string, opts TLSOptions) func(context.Context) (net.Conn, error)
  ```
- Satisfies `ipc.DialFunc`, and honours the RD-001 contract: **ctx bounds the dial only.** The returned conn owns the TLS connection and closes it in `Close`.

**Scope outline** (expand to full TDD steps after Task 1):

- [ ] `tls.Dialer` with `Config{MinVersion: tls.VersionTLS13, RootCAs: <bundle>, Certificates: <client pair>, ServerName: opts.ServerName}`.
- [ ] **Never** expose `InsecureSkipVerify` as a config field. Not as a flag, not as an env var, not "for testing" — a knob that disables verification will be found and used. Tests build a real ephemeral CA with `crypto/x509` instead; it is about fifteen lines.
- [ ] Reject an empty `ServerName` at dial time with a clear error. Go would otherwise fall back to the dialed host, which silently weakens verification when the address is an IP.
- [ ] `LinkStatus` equivalents: the ssh backend needed `LinkErr`/`Established`/`ExitCode` because `exec.Cmd.Start` succeeds before the network is touched. TLS does **not** have that problem — a failed handshake fails the dial. So the version gate's remote-link diagnosis must branch on transport rather than assume ssh semantics. Check `version_gate.go`'s `remoteLinkError()` path before assuming it works here.

---

## Task 3 (RD-031): Daemon TLS listener

**Files:**
- Create: `internal/daemon/tlslisten.go`, `internal/daemon/tlslisten_test.go`
- Modify: `internal/ipc/server.go`, `cmd/quild/main.go`, `internal/config/config.go`

**Scope outline:**

- [ ] `[daemon.tls] enabled = false` (default), `listen = "127.0.0.1:7654"`, `ca_file`, `cert_file`, `key_file`.
- [ ] Default `listen` binds **loopback**, not `0.0.0.0`. A user who wants it exposed must say so explicitly, in a file, on one line that is easy to find in a review.
- [ ] `tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: <bundle>, MinVersion: tls.VersionTLS13}`. `RequireAndVerifyClientCert` is the whole security model — anything weaker is an open port to an RCE-equivalent protocol.
- [ ] Refuse to start with TLS enabled and `ClientCAs` empty. Failing loudly at startup beats listening without client verification.
- [ ] Accepted TLS connections join the same `ipc.Server` connection handling as Unix-socket clients — the dual-queue `critCh`/`outCh` machinery, the overflow close, the write deadline. Do not reimplement.
- [ ] The single-instance guard (`daemonAlreadyHealthy`) reasons about the Unix socket. Decide what a second daemon with only TLS configured means before writing the listener.

---

## Task 4 (RD-032): Certificate configuration and rotation

**Scope outline:**

- [ ] Load certificates at startup and on `SIGHUP` (Unix). Windows has no SIGHUP; use a config-file mtime check on the existing snapshot ticker rather than inventing a signal.
- [ ] `tls.Config.GetCertificate` so a renewed certificate is picked up without dropping live connections.
- [ ] Log the certificate's `NotAfter` at startup and warn within 14 days of expiry. An expired certificate presents as a generic handshake failure, which is the least diagnosable failure this feature can produce.
- [ ] Document that revocation is the user's PKI's job (Option A). Do not implement CRL or OCSP.

---

## Task 5 (RD-033): Threat model update

**This task gates the phase's release, not just its documentation.** Phase 1's security model was "the socket is the auth" — `0600`, same UID, no listener. TLS deletes that sentence.

**Files:**
- Modify: `docs/roadmap/remote-daemon.md` (§ Security model), `docs/architecture.md` (new ADR), `docs/configuration.md`

**Scope outline:**

- [ ] State plainly: enabling TLS opens a **pre-authentication network port** in front of a protocol where `MsgCreatePane` spawns arbitrary processes and `MsgPaneInput` types into live AI sessions. A certificate that can reach Quil is a shell.
- [ ] Compare honestly against ssh: ssh has decades of hardening, a mature key-agent story, `known_hosts`, certificates, and hardware-token support that Quil would not reimplement. **TLS is for deployments where ssh is unavailable or inappropriate — it is not an upgrade, and the docs must not imply that it is.**
- [ ] Document the frame-size limits as a denial-of-service boundary now that unauthenticated bytes can reach the parser. Verify `maxFrameSize` is enforced *before* allocation on the read path.
- [ ] Write the ADR covering why the `DialFunc` seam existed first and why mTLS was deferred until after Phases 2 and 3.
- [ ] Add a CHANGELOG entry that leads with the security posture, not the feature.

---

## Verification (whole phase)

- [ ] `./scripts/dev.sh test`, `vet`, `test-race` green.
- [ ] Tests build an ephemeral CA in-process. No fixture certificates in the repo — a committed key is a committed key even when it is "only for tests", and it will outlive the reason it was added.
- [ ] **Negative tests are the important ones, and they must exist before the positive ones:** no client certificate → rejected; certificate from an untrusted CA → rejected; expired certificate → rejected; wrong `ServerName` → rejected; TLS 1.2 client → rejected.
- [ ] Default config with no TLS section → daemon listens on the Unix socket only. Assert this; it is the property most likely to regress silently.
- [ ] Manual: daemon with TLS enabled on loopback, TUI attaches over it, panes work, Phase 2 reconnect works over TLS.
- [ ] Update `docs/roadmap/remote-daemon.md`: RD-030…RD-034 statuses; move Phase 4 out of "planned".

## Self-review notes

- **Deliberate departure from the plan format.** Tasks 2–5 are outlines, not bite-sized TDD steps. The writing-plans skill forbids placeholders, and this is the honest reading of that rule rather than an exception to it: a placeholder is detail that was skipped, whereas these are steps whose content is genuinely determined by an unmade decision. Writing plausible `crypto/tls` code here would manufacture false precision — the failure mode this session has already hit three times, where a confident comment was wrong in a way no test could catch.
- **Dependency check.** The plan claims stdlib-only and holds to it under Option A. Under Option B it would not, which is itself an argument for A.
- **Cross-phase risk.** Task 2 notes that `LinkStatus` exists because of an ssh-specific behaviour (`exec.Cmd.Start` succeeding before the network is touched). Anyone implementing the TLS backend by analogy with the ssh one will carry that machinery across for no reason, and worse, the version gate may then mis-diagnose TLS failures. Read `version_gate.go` before writing `tls.go`.
