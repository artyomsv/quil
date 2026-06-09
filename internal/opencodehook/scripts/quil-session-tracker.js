// Quil OpenCode session tracker + hook events forwarder.
//
// Loaded by opencode via OPENCODE_CONFIG_CONTENT when Quil spawns an opencode
// pane. Two responsibilities:
//
//   1. Session-id rotation tracking (original behaviour). Writes the current
//      session id to $QUIL_HOME/sessions/opencode-<paneID>.id; consulted by
//      Quil's restore path to decide between --session <id> and --continue.
//
//   2. Hook events forwarding (Phase C). Appends one JSONL line to
//      $QUIL_HOME/events/<paneID>.jsonl per filtered bus event so the
//      daemon's hookEventsWatcher can translate them into PaneEvents.
//      Filtered to the "default" tier from the hook events plan:
//      session.idle/error/compacted, session.status retry-only,
//      file.edited (batched 1 s), permission.ask (typed hook).
//
// When QUIL_PANE_ID is absent (opencode invoked outside Quil) the plugin is
// a no-op. Safe to delete — Quil recreates it on next daemon start.

import { writeFile, rename, mkdir, unlink, appendFile } from "node:fs/promises";
import { join } from "node:path";
import { randomUUID } from "node:crypto";

const PANE_ID_RE = /^[A-Za-z0-9_-]{1,64}$/;
const SESSION_ID_RE = /^[0-9a-zA-Z_-]{1,128}$/;

// Wire-format caps mirroring internal/hookevents/types.go. The hook side is
// responsible for truncating before write so the daemon's validator never
// rejects an oversized line.
const MAX_TITLE_BYTES = 200;
const MAX_DATA_VALUE_BYTES = 128;
const MAX_TOTAL_BYTES = 2 * 1024;

// Per-pane rate budget. 20 sustained events / second with 50 burst. Larger
// than any real-world hook stream but small enough to detect a runaway
// pattern (e.g. a hook plugin in a loop) before the daemon's own limiter
// trips.
const RATE_BUDGET_PER_SEC = 20;
const RATE_BURST = 50;

// file.edited batching window: opencode emits one file.edited per write;
// batch them in this window and emit a single "Edited N files" event.
const FILE_EDITED_BATCH_MS = 1000;

export default async function quilSessionTracker(_input) {
  const paneId = process.env.QUIL_PANE_ID || "";
  const quilHome = process.env.QUIL_HOME || "";
  // Tier gate: "default" forwards the standard event set, "verbose" would
  // add tier-2 (tool.execute.before/after for read-only tools — currently
  // not wired), "off" disables all spool emission while preserving the
  // session-id rotation tracking (used by Quil's resume path).
  const hookMode = process.env.QUIL_HOOK_MODE || "default";
  if (!paneId || !quilHome) return {};
  if (!PANE_ID_RE.test(paneId)) return {};
  // Defense-in-depth: the daemon controls QUIL_HOME but a leaked/forwarded
  // env from a hostile parent could still contain a NUL terminator that
  // truncates the path silently on some platforms. Bail before constructing
  // any filesystem paths.
  if (quilHome.includes("\0")) return {};

  const sessionsDir = join(quilHome, "sessions");
  const eventsDir = join(quilHome, "events");
  const logDir = join(quilHome, "opencodehook");
  const logFile = join(logDir, "hook.log");
  const target = join(sessionsDir, "opencode-" + paneId + ".id");
  const spoolFile = join(eventsDir, paneId + ".jsonl");
  const LOG_SIZE_CAP_BYTES = 1 * 1024 * 1024;

  const logLine = async (msg) => {
    try {
      await mkdir(logDir, { recursive: true });
      try {
        const { stat } = await import("node:fs/promises");
        const st = await stat(logFile);
        if (st.size > LOG_SIZE_CAP_BYTES) {
          await rename(logFile, logFile + ".1");
        }
      } catch (_) { /* file may not exist yet */ }
      const line = new Date().toISOString() + " pane=" + paneId + " " + msg + "\n";
      await appendFile(logFile, line);
    } catch (_) { /* never throw from logger */ }
  };

  // Token bucket for spool emission. Refills at RATE_BUDGET_PER_SEC, caps
  // at RATE_BURST. Drops with a single warn-log when exhausted; resets on
  // refill so a transient burst recovers visibly.
  let tokens = RATE_BURST;
  let lastRefillMs = Date.now();
  let droppedSinceLog = 0;

  const consumeToken = () => {
    const now = Date.now();
    // Clamp elapsed to ≥ 0. A backward clock jump (NTP correction, manual
    // time change, VM snapshot resume) would otherwise produce negative
    // elapsed and push `tokens` below the floor — every subsequent event
    // would drop until the wall clock caught up to the pre-jump time,
    // potentially days later.
    const elapsed = Math.max(0, (now - lastRefillMs) / 1000);
    if (elapsed > 0) {
      tokens = Math.min(RATE_BURST, tokens + elapsed * RATE_BUDGET_PER_SEC);
      lastRefillMs = now;
    } else if (now < lastRefillMs) {
      // Reset the refill anchor on clock jumps so the next call computes
      // a sane positive elapsed from the new clock baseline.
      lastRefillMs = now;
    }
    if (tokens < 1) {
      droppedSinceLog++;
      if (droppedSinceLog === 1) {
        // Log only the first drop in this exhausted period to avoid spam.
        logLine("rate limit exhausted — dropping events");
      }
      return false;
    }
    tokens -= 1;
    if (droppedSinceLog > 0) {
      logLine("rate limit recovered after " + droppedSinceLog + " drop(s)");
      droppedSinceLog = 0;
    }
    return true;
  };

  // Sequence counter for the wire-schema seq field. Per-pane monotonic so
  // events arriving in the same millisecond can be ordered.
  let seq = 0;

  // Truncate a string with an ellipsis. Bytes counted as UTF-8 — opencode
  // is permitted to put unicode in payloads (file paths, prompt text).
  //
  // Buffer.slice on a UTF-8 byte boundary that lands mid-codepoint produces
  // a string with a U+FFFD replacement char when decoded. JSON.stringify
  // happily serialises U+FFFD (it round-trips as �), so the daemon's
  // strict UTF-8 validator accepts the resulting line — but the user sees
  // a notification card with "…" in the middle of what should be a clean
  // path or prompt. Walk backwards from the byte cap to the previous
  // codepoint boundary so the truncated string is always valid UTF-8.
  const truncate = (s, n) => {
    if (s == null) return "";
    const buf = Buffer.from(String(s), "utf8");
    if (buf.length <= n) return String(s);
    // Target the largest cut at n-3 (room for "…" which is 3 bytes UTF-8).
    let cut = n - 3;
    // Back up over any UTF-8 continuation byte (10xxxxxx); we may need to
    // walk back up to 3 bytes for a 4-byte codepoint.
    while (cut > 0 && (buf[cut] & 0xc0) === 0x80) {
      cut--;
    }
    return buf.slice(0, cut).toString("utf8") + "…";
  };

  const truncateData = (data) => {
    if (!data) return undefined;
    const out = {};
    for (const k of Object.keys(data)) {
      const v = data[k] == null ? "" : String(data[k]);
      out[k] = truncate(v, MAX_DATA_VALUE_BYTES);
    }
    return out;
  };

  // Emit one Payload to the spool file. Best-effort; failures land in the
  // hook log but never block the opencode runtime.
  const spool = async (hookEvent, title, sev, data) => {
    // Off-mode short-circuit: keep session-id tracking alive but drop the
    // notification event surface entirely. The user opted out.
    if (hookMode === "off") return;
    if (!consumeToken()) return;
    const payload = {
      v: 1,
      ts_ms: Date.now(),
      seq: ++seq,
      pane_id: paneId,
      src: "opencode",
      hook_event: hookEvent,
      session_id: lastRecorded || "",
      title: truncate(title, MAX_TITLE_BYTES),
      sev,
    };
    const td = truncateData(data);
    if (td && Object.keys(td).length > 0) payload.data = td;

    let line;
    try {
      line = JSON.stringify(payload);
    } catch (e) {
      await logLine("stringify payload failed: " + (e && e.message ? e.message : String(e)));
      return;
    }
    if (Buffer.byteLength(line, "utf8") > MAX_TOTAL_BYTES) {
      // The data values were each capped, but title + base fields could
      // still push us over. Drop with a log; an upstream payload that
      // routinely produces > 2 KiB output is a misuse worth surfacing.
      await logLine("payload exceeds " + MAX_TOTAL_BYTES + " bytes, dropping " + hookEvent);
      return;
    }

    try {
      await mkdir(eventsDir, { recursive: true });
      await appendFile(spoolFile, line + "\n");
    } catch (e) {
      await logLine("write spool failed: " + (e && e.message ? e.message : String(e)));
    }
  };

  // Session-id tracking ─────────────────────────────────────────────────
  let lastRecorded = null;

  const record = async (sessionId, eventType) => {
    if (typeof sessionId !== "string" || !SESSION_ID_RE.test(sessionId)) {
      const shown = sessionId == null ? "(empty)" : String(sessionId).slice(0, 200);
      await logLine("rejected session_id: " + shown + " (type=" + eventType + ")");
      return;
    }
    if (sessionId === lastRecorded) return;
    try {
      await mkdir(sessionsDir, { recursive: true });
      const tmp = target + "." + process.pid + "." + randomUUID() + ".tmp";
      await writeFile(tmp, sessionId);
      await rename(tmp, target);
      lastRecorded = sessionId;
      await logLine("recorded " + eventType + " session=" + sessionId);
    } catch (e) {
      await logLine("write failed: " + (e && e.message ? e.message : String(e)));
    }
  };

  const clear = async () => {
    try { await unlink(target); } catch (_) { /* may not exist */ }
    lastRecorded = null;
    await logLine("cleared (session.deleted)");
  };

  // file.edited batching ────────────────────────────────────────────────
  let fileBatch = [];
  let fileBatchTimer = null;

  const flushFileBatch = async () => {
    fileBatchTimer = null;
    const files = fileBatch;
    fileBatch = [];
    if (files.length === 0) return;
    const titlePreview = files.length === 1
      ? "Edited " + files[0]
      : "Edited " + files.length + " files";
    await spool("file.edited", titlePreview, "info", {
      count: String(files.length),
      first: files[0] || "",
    });
  };

  const enqueueFileEdited = (file) => {
    if (typeof file !== "string" || file.length === 0) return;
    if (fileBatch.length < 50) {
      fileBatch.push(file);
    }
    if (fileBatchTimer == null) {
      fileBatchTimer = setTimeout(() => { flushFileBatch().catch(() => {}); }, FILE_EDITED_BATCH_MS);
      if (typeof fileBatchTimer.unref === "function") fileBatchTimer.unref();
    }
  };

  // Bus event handler routes session-id tracking + filtered spool emits.
  // Note: opencode types describe `properties.info: Session` on session.*,
  // but the runtime ALSO surfaces `properties.sessionID` directly. We
  // accept both for forward-compat with the bus payload format.
  return {
    event: async ({ event }) => {
      try {
        const t = event && event.type;
        if (!t) return;
        const props = event.properties || {};

        // 1) Session-id rotation tracking (original behaviour).
        if (t.startsWith("session.")) {
          const sid = props.sessionID || (props.info && props.info.id);
          switch (t) {
            case "session.created":
            case "session.updated":
            case "session.idle":
            case "session.compacted":
              await record(sid, t);
              break;
            case "session.deleted":
              await clear();
              break;
          }
        }

        // 2) Spool forwarding (Phase C "default" tier).
        switch (t) {
          case "session.idle":
            await spool("session.idle", "Reply ready", "warning", {});
            break;

          case "session.error": {
            let detail = "Session error";
            if (props.error && props.error.message) {
              detail = "Session error: " + props.error.message;
            } else if (props.error && props.error.type) {
              detail = "Session error: " + props.error.type;
            }
            await spool("session.error", detail, "error", {
              error_type: (props.error && props.error.type) || "",
            });
            break;
          }

          case "session.compacted":
            await spool("session.compacted", "Compaction complete", "info", {});
            break;

          case "session.status":
            if (props.status && props.status.type === "retry") {
              const attempt = props.status.attempt || 0;
              const msg = props.status.message || "Retrying";
              await spool("session.status.retry", "Retrying API (attempt " + attempt + ")", "warning", {
                attempt: String(attempt),
                message: msg,
              });
            }
            break;

          case "file.edited": {
            const file = props.file || "";
            enqueueFileEdited(file);
            break;
          }
        }
      } catch (e) {
        await logLine("event handler error: " + (e && e.message ? e.message : String(e)));
      }
    },

    // chat.message — fires when a user message is submitted to the model.
    // This is opencode's analog of Claude's UserPromptSubmit and marks the
    // START of a turn. Quil's TUI flips the pane to "working" on this event
    // and back to idle on session.idle/session.error. Emitting it also
    // produces a "Working…" notification card, symmetric with Claude.
    "chat.message": async (_input, _output) => {
      try {
        await spool("chat.message", "Working…", "info", {});
      } catch (e) {
        await logLine("chat.message handler error: " + (e && e.message ? e.message : String(e)));
      }
    },

    // Typed permission.ask hook — fires when opencode needs the user to
    // approve a tool use. Surfaces as a "Needs approval: <tool>" event.
    "permission.ask": async (input, _output) => {
      try {
        const tool = (input && input.tool) || (input && input.toolName) || "tool";
        const callID = (input && input.callID) || "";
        await spool("permission.ask", "Needs approval: " + tool, "warning", {
          tool: String(tool),
          call_id: String(callID),
        });
      } catch (e) {
        await logLine("permission.ask handler error: " + (e && e.message ? e.message : String(e)));
      }
    },

    // experimental.session.compacting — emitted before compaction starts.
    // We just want the user-facing "Compacting context" card; opencode
    // also emits session.compacted at end which fires PostCompact above.
    "experimental.session.compacting": async (_input, _output) => {
      try {
        await spool("experimental.session.compacting", "Compacting context", "info", {});
      } catch (e) {
        await logLine("compacting handler error: " + (e && e.message ? e.message : String(e)));
      }
    },
  };
}
