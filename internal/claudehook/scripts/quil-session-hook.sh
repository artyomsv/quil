#!/bin/sh
# Quil hook handler for Claude Code (multi-event v1).
#
# Quil registers this script under multiple Claude hook events via
# --settings. Claude invokes it once per fired event with a JSON payload on
# stdin including a `hook_event_name` discriminator and event-specific
# fields. We branch on hook_event_name and route to one of two outputs:
#
#   - SessionStart → atomically write the session id to
#     $QUIL_HOME/sessions/$QUIL_PANE_ID.id so Quil's restore path can
#     dispatch --resume vs --continue. (Unchanged from the original
#     SessionStart-only hook.)
#
#   - Every other forwarded event → append one JSONL line to
#     $QUIL_HOME/events/$QUIL_PANE_ID.jsonl carrying the Quil hookevents
#     wire schema (v=1, ts_ms, seq, pane_id, src=claude, hook_event, title,
#     sev, data). The daemon's hookEventsWatcher picks the line up within
#     200 ms, runs it through rate-limit + coalesce, and emits a PaneEvent.
#
# QUIL_PANE_ID and QUIL_HOME are set by Quil at pane spawn. When either is
# unset the hook is a no-op (Claude invoked outside Quil).
#
# Always exit 0 so Claude is never blocked by our bookkeeping. Failure
# breadcrumbs land in $QUIL_HOME/claudehook/hook.log.

set -u

pane_id="${QUIL_PANE_ID-}"
[ -z "$pane_id" ] && exit 0

quil_home="${QUIL_HOME-}"
if [ -z "$quil_home" ]; then
    quil_home="${HOME-}/.quil"
fi

log_dir="$quil_home/claudehook"
log_file="$log_dir/hook.log"
log_err() {
    mkdir -p "$log_dir" 2>/dev/null || return 0
    printf '%s pane=%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$pane_id" "$1" \
        >>"$log_file" 2>/dev/null || true
}

# QUIL_HOOK_MODE gates the spool emission tier. The daemon passes it at pane
# spawn from `[notification.hooks] claude` in config.toml. SessionStart
# always writes the session id file regardless of mode (it is the
# infrastructure for resume, not a notification).
mode="${QUIL_HOOK_MODE:-default}"

# Stdin is consumed exactly once; everything else reads from $payload.
payload="$(cat)"

# Extract a JSON string field by key. Tries jq first, falls back to a
# best-effort sed for environments without jq. The sed regex deliberately
# tolerates whitespace + escaped quotes only by giving up — falling back to
# empty is safe because callers treat empty as absent.
jget() {
    key="$1"
    if command -v jq >/dev/null 2>&1; then
        printf '%s' "$payload" | jq -r ".$key // empty" 2>/dev/null
        return
    fi
    printf '%s' "$payload" | \
        sed -n 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

hook_event="$(jget hook_event_name)"
session_id="$(jget session_id)"

# json_escape escapes a string for embedding inside a JSON string literal.
# Handles backslash, double-quote, tab, and converts any C0 control byte
# (including \n and \r) to its \uXXXX form so the resulting JSON line is
# valid even when Claude payloads contain raw control characters (ESC from
# ANSI-colored tool output, embedded newlines in multi-line prompts, etc).
#
# We use `printf | tr` to first replace each control byte with a printable
# marker, then sed to substitute the JSON escape — this sidesteps the
# GNU-vs-BSD sed slurp-into-pattern-space portability question. Less
# elegant than a single sed, but portable to busybox / dash / macOS sed.
#
# Note on byte truncation: callers feed json_escape strings that have
# already been bounded by `head -c N`. head -c counts BYTES not characters,
# so non-ASCII content can land mid-codepoint at the cut. The daemon's
# strict UTF-8 json.Unmarshal then rejects the line and the event is
# silently dropped. Acceptable v1 limitation for Claude (commands and
# tool names are ASCII); user prompts containing non-ASCII may produce
# events with truncated previews replaced by … in the daemon side cap.
json_escape() {
    # Replace newlines/CR/tab with a single marker each, then escape via
    # sed. Use `printf` to avoid `echo -e` portability differences.
    printf '%s' "$1" | \
        tr '\n' '\1' | tr '\r' '\2' | tr '\t' '\3' | \
        sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' \
            -e 's/\x01/\\n/g' -e 's/\x02/\\r/g' -e 's/\x03/\\t/g'
}

# Append a JSONL event line to the spool. Single write(2) keeps it atomic
# under PIPE_BUF on Unix (the schema cap of 2 KiB stays well under the
# typical 4 KiB PIPE_BUF). Args: 1=hook_event, 2=title, 3=severity,
# 4=data_json (already a {"k":"v",...} string, may be empty for none).
#
# Off-mode short-circuit: when QUIL_HOOK_MODE=off, drop everything. When
# the mode is "default" (the standard tier — see forwardedHookEvents in
# claudehook.go for what claude registers under) or "verbose" (a superset
# claude would have to register additional hooks to populate), the spool
# write proceeds. The router below dispatches the default tier only;
# extending to verbose requires extending forwardedHookEvents.
spool() {
    [ "$mode" = "off" ] && return 0
    he="$1"
    ti="$2"
    sv="$3"
    da="$4"
    events_dir="$quil_home/events"
    if ! mkdir -p "$events_dir" 2>/dev/null; then
        log_err "mkdir events dir failed: $events_dir"
        return 0
    fi
    spool_file="$events_dir/$pane_id.jsonl"

    ts_ms="$(date +%s%3N 2>/dev/null || echo 0)"
    # %3N is GNU date; macOS BSD date lacks it. Fall back to seconds × 1000.
    case "$ts_ms" in
        ''|*[!0-9]*) ts_ms="$(( $(date +%s) * 1000 ))" ;;
    esac

    he_e="$(json_escape "$he")"
    ti_e="$(json_escape "$ti")"
    sid_e="$(json_escape "$session_id")"

    line="{\"v\":1,\"ts_ms\":$ts_ms,\"seq\":0,\"pane_id\":\"$(json_escape "$pane_id")\",\"src\":\"claude\",\"hook_event\":\"$he_e\",\"session_id\":\"$sid_e\",\"title\":\"$ti_e\",\"sev\":\"$sv\""
    if [ -n "$da" ]; then
        line="$line,\"data\":$da"
    fi
    line="$line}"

    printf '%s\n' "$line" >>"$spool_file" 2>/dev/null || \
        log_err "write spool failed: $spool_file"
}

# SessionStart keeps the original behaviour: validate + atomically write
# the session id file. The session_id rotates over the lifetime of the
# pane (after /clear, /resume, compaction) and the restore path needs the
# LATEST id, not just the first one.
write_session_file() {
    sessions_dir="$quil_home/sessions"
    if ! mkdir -p "$sessions_dir" 2>/dev/null; then
        log_err "mkdir sessions dir failed: $sessions_dir"
        return 0
    fi
    if [ -z "$session_id" ]; then
        log_err "no session_id extracted from stdin"
        return 0
    fi
    if ! printf '%s' "$session_id" | grep -Eq '^[0-9a-fA-F-]{32,64}$'; then
        log_err "session_id rejected as non-uuid: $session_id"
        return 0
    fi
    out="$sessions_dir/$pane_id.id"
    tmp="$(mktemp "$out.XXXXXX" 2>/dev/null)"
    if [ -z "$tmp" ]; then
        log_err "mktemp failed for $out"
        return 0
    fi
    if ! printf '%s\n' "$session_id" >"$tmp" 2>/dev/null; then
        log_err "write tmp failed: $tmp"
        rm -f "$tmp" 2>/dev/null
        return 0
    fi
    if ! mv "$tmp" "$out" 2>/dev/null; then
        log_err "rename failed: $tmp -> $out"
        rm -f "$tmp" 2>/dev/null
        return 0
    fi
}

# Route by hook_event_name. Every branch returns to the final exit 0 at the
# bottom of the script.
case "$hook_event" in
    SessionStart)
        # Session id rotation tracking (original behaviour).
        write_session_file
        ;;
    SessionEnd)
        spool "SessionEnd" "Session ended" "info" ""
        ;;
    UserPromptSubmit)
        prompt="$(jget prompt)"
        preview="$(printf '%s' "$prompt" | head -c 60)"
        prev_e="$(json_escape "$preview")"
        title="Working on: $preview"
        # Cap title to 200 chars defensively (preview already truncated).
        title="$(printf '%s' "$title" | head -c 200)"
        title_e="$(json_escape "$title")"
        spool "UserPromptSubmit" "$title" "info" "{\"prompt_preview\":\"$prev_e\"}"
        ;;
    Notification)
        # Claude's own notification text — pass through as title.
        message="$(jget message)"
        msg_t="$(printf '%s' "$message" | head -c 200)"
        spool "Notification" "$msg_t" "warning" ""
        ;;
    PermissionRequest)
        tool="$(jget tool_name)"
        tool_e="$(json_escape "$tool")"
        title="Needs approval: $tool"
        title="$(printf '%s' "$title" | head -c 200)"
        spool "PermissionRequest" "$title" "warning" "{\"tool\":\"$tool_e\"}"
        ;;
    Stop)
        spool "Stop" "Reply ready" "warning" ""
        ;;
    PreCompact)
        reason="$(jget reason)"
        reason_e="$(json_escape "$reason")"
        title="Compacting context"
        if [ -n "$reason" ]; then
            title="$title ($reason)"
            title="$(printf '%s' "$title" | head -c 200)"
        fi
        spool "PreCompact" "$title" "info" "{\"reason\":\"$reason_e\"}"
        ;;
    PostCompact)
        spool "PostCompact" "Compaction complete" "info" ""
        ;;
    SubagentStart)
        agent="$(jget agent_type)"
        agent_e="$(json_escape "$agent")"
        title="Spawned: $agent"
        title="$(printf '%s' "$title" | head -c 200)"
        spool "SubagentStart" "$title" "info" "{\"agent_type\":\"$agent_e\"}"
        ;;
    SubagentStop)
        agent="$(jget agent_type)"
        agent_e="$(json_escape "$agent")"
        title="$agent done"
        title="$(printf '%s' "$title" | head -c 200)"
        spool "SubagentStop" "$title" "info" "{\"agent_type\":\"$agent_e\"}"
        ;;
    TaskCreated)
        content="$(jget content)"
        content_t="$(printf '%s' "$content" | head -c 180)"
        content_e="$(json_escape "$content_t")"
        title="Task: $content_t"
        title="$(printf '%s' "$title" | head -c 200)"
        spool "TaskCreated" "$title" "info" "{\"content\":\"$content_e\"}"
        ;;
    TaskCompleted)
        content="$(jget content)"
        content_t="$(printf '%s' "$content" | head -c 180)"
        content_e="$(json_escape "$content_t")"
        title="✓ $content_t"
        title="$(printf '%s' "$title" | head -c 200)"
        spool "TaskCompleted" "$title" "info" "{\"content\":\"$content_e\"}"
        ;;
    *)
        # Unknown / unhandled event — log a breadcrumb and drop. Not an
        # error because Claude can add new events at any time and we want
        # graceful forward-compat.
        log_err "unhandled hook_event: $hook_event"
        ;;
esac

exit 0
