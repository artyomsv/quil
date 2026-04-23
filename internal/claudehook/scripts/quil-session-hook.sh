#!/bin/sh
# Quil SessionStart hook for Claude Code.
#
# Receives Claude's hook JSON on stdin, extracts session_id, and writes it
# atomically to $QUIL_HOME/sessions/$QUIL_PANE_ID.id. QUIL_PANE_ID is set by
# Quil at pane spawn; when unset the hook is a no-op (Claude invoked outside
# Quil). Always exits 0 so Claude is never blocked by our bookkeeping.
#
# Failure breadcrumbs are appended to $QUIL_HOME/claudehook/hook.log so a
# silently-broken hook is detectable from the logs (otherwise the rotation
# tracking would silently regress to the preassigned session id).

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

sessions_dir="$quil_home/sessions"
if ! mkdir -p "$sessions_dir" 2>/dev/null; then
    log_err "mkdir sessions dir failed: $sessions_dir"
    exit 0
fi

payload="$(cat)"

session_id=""
if command -v jq >/dev/null 2>&1; then
    session_id="$(printf '%s' "$payload" | jq -r '.session_id // empty' 2>/dev/null || true)"
fi
if [ -z "$session_id" ]; then
    session_id="$(printf '%s' "$payload" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
fi
if [ -z "$session_id" ]; then
    log_err "no session_id extracted from stdin"
    exit 0
fi

# Defence-in-depth: only accept uuid-shaped values so a malformed payload can't
# poison the persisted id with arbitrary text.
if ! printf '%s' "$session_id" | grep -Eq '^[0-9a-fA-F-]{32,64}$'; then
    log_err "session_id rejected as non-uuid: $session_id"
    exit 0
fi

out="$sessions_dir/$pane_id.id"
tmp="$(mktemp "$out.XXXXXX" 2>/dev/null)"
if [ -z "$tmp" ]; then
    log_err "mktemp failed for $out"
    exit 0
fi
if ! printf '%s\n' "$session_id" >"$tmp" 2>/dev/null; then
    log_err "write tmp failed: $tmp"
    rm -f "$tmp" 2>/dev/null
    exit 0
fi
if ! mv "$tmp" "$out" 2>/dev/null; then
    log_err "rename failed: $tmp -> $out"
    rm -f "$tmp" 2>/dev/null
    exit 0
fi
exit 0
