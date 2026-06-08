# Quil hook handler for Claude Code (multi-event v1, Windows).
#
# PowerShell variant matching scripts/quil-session-hook.sh. See the .sh
# header for the full design; this file mirrors the same routing table:
#
#   - SessionStart → atomically write the session id to
#     $QUIL_HOME/sessions/$QUIL_PANE_ID.id (used by Quil's restore path).
#
#   - Every other forwarded event → append one JSONL line to
#     $QUIL_HOME/events/$QUIL_PANE_ID.jsonl carrying the hookevents wire
#     schema. The daemon's watcher polls this file every 200 ms.
#
# QUIL_PANE_ID and QUIL_HOME are set by Quil at pane spawn. Unset → no-op.
# Always exits 0 so Claude is never blocked.

$ErrorActionPreference = 'SilentlyContinue'

$paneId = $env:QUIL_PANE_ID
if (-not $paneId) { exit 0 }

$quilHome = $env:QUIL_HOME
if (-not $quilHome) { $quilHome = Join-Path $env:USERPROFILE '.quil' }

$logDir = Join-Path $quilHome 'claudehook'
$logFile = Join-Path $logDir 'hook.log'
function Write-HookErr([string]$msg) {
    try {
        New-Item -ItemType Directory -Path $logDir -Force | Out-Null
        $stamp = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
        Add-Content -Path $logFile -Value ("{0} pane={1} {2}" -f $stamp, $paneId, $msg)
    } catch {}
}

$mode = $env:QUIL_HOOK_MODE
if (-not $mode) { $mode = 'default' }

$payload = [Console]::In.ReadToEnd()

# Try the structured ConvertFrom-Json first; fall back to regex extraction
# if the payload is malformed. We need hook_event_name plus the per-event
# specific fields.
$obj = $null
try { $obj = $payload | ConvertFrom-Json } catch { }

function Json-Field([string]$key) {
    if ($obj -and ($obj.PSObject.Properties.Name -contains $key)) {
        $v = $obj.$key
        if ($null -ne $v) { return [string]$v }
    }
    if ($payload -match ('"' + [regex]::Escape($key) + '"\s*:\s*"([^"]*)"')) {
        return $Matches[1]
    }
    return ''
}

$hookEvent = Json-Field 'hook_event_name'
$sessionId = Json-Field 'session_id'

# Truncate a string defensively to N chars, appending an ellipsis if cut.
function Truncate([string]$s, [int]$n) {
    if ($null -eq $s) { return '' }
    if ($s.Length -le $n) { return $s }
    return $s.Substring(0, [Math]::Max(0, $n - 1)) + "…"
}

# Escape a string for embedding in a JSON string literal.
#
# PowerShell -replace is a .NET Regex.Replace under the hood. In the
# replacement string, `$` is the substitution sigil but backslash is
# literal. So to emit JSON's `\\` (two chars) the replacement value must
# be the LITERAL two-character string `\\`, expressed in PowerShell as
# the single-quoted `'\\'` — which is exactly two characters.
#
# Order matters: backslash is escaped FIRST so subsequent replacements'
# inserted `\` characters are not re-escaped.
#
# C0 control characters (0x00–0x1F) other than `\n`/`\r`/`\t` are
# escaped as `\uXXXX` so the resulting JSON string literal is valid;
# without this, a Claude payload containing e.g. `\x1b` (ESC, common in
# ANSI-colored tool output) would produce a line the daemon's strict
# json.Unmarshal rejects, silently dropping the event.
function JsonEscape([string]$s) {
    if ($null -eq $s) { return '' }
    $s = $s -replace '\\', '\\'
    $s = $s -replace '"', '\"'
    $s = $s -replace "`n", '\n'
    $s = $s -replace "`r", '\r'
    $s = $s -replace "`t", '\t'
    # Catch-all for remaining C0 control bytes [\x00-\x1F]. Without this a
    # Claude payload containing e.g. ESC (\x1B from ANSI-colored tool
    # output) would produce a line the daemon's strict json.Unmarshal
    # rejects, silently dropping the event. Match evaluator formats the
    # matched character as JSON's \u00XX hex escape.
    $s = [regex]::Replace($s, '[\x00-\x1f]', {
        param($m)
        '\u{0:x4}' -f [int][char]$m.Value
    })
    return $s
}

function Spool-Event([string]$he, [string]$title, [string]$sev, [string]$dataJson) {
    # Off-mode short-circuit (see .sh for design notes).
    if ($mode -eq 'off') { return }
    $eventsDir = Join-Path $quilHome 'events'
    try { New-Item -ItemType Directory -Path $eventsDir -Force | Out-Null } catch {
        Write-HookErr ("mkdir events dir failed: {0}" -f $eventsDir)
        return
    }
    $spoolFile = Join-Path $eventsDir ($paneId + '.jsonl')

    $tsMs = [int64]([Math]::Floor((Get-Date -UFormat %s)) * 1000)

    $heE  = JsonEscape $he
    $tiE  = JsonEscape (Truncate $title 200)
    $sidE = JsonEscape $sessionId
    $pidE = JsonEscape $paneId

    $line = '{"v":1,"ts_ms":' + $tsMs + ',"seq":0,"pane_id":"' + $pidE + '","src":"claude","hook_event":"' + $heE + '","session_id":"' + $sidE + '","title":"' + $tiE + '","sev":"' + $sev + '"'
    if ($dataJson) {
        $line = $line + ',"data":' + $dataJson
    }
    $line = $line + '}'

    try {
        Add-Content -Path $spoolFile -Value $line -Encoding UTF8
    } catch {
        Write-HookErr ("write spool failed: {0}" -f $_.Exception.Message)
    }
}

function Write-SessionFile {
    if (-not $sessionId) {
        Write-HookErr "no session_id extracted from stdin"
        return
    }
    if ($sessionId -notmatch '^[0-9a-fA-F-]{32,64}$') {
        Write-HookErr ("session_id rejected as non-uuid: {0}" -f $sessionId)
        return
    }

    $sessionsDir = Join-Path $quilHome 'sessions'
    try { New-Item -ItemType Directory -Path $sessionsDir -Force | Out-Null } catch {
        Write-HookErr ("mkdir sessions dir failed: {0}" -f $sessionsDir)
        return
    }
    $out = Join-Path $sessionsDir ($paneId + '.id')
    $tmp = "$out.{0}" -f ([guid]::NewGuid().ToString('N'))
    try {
        Set-Content -Path $tmp -Value $sessionId -Encoding ASCII
        Move-Item -Path $tmp -Destination $out -Force
    } catch {
        Write-HookErr "write/rename failed: $($_.Exception.Message)"
        Remove-Item -Path $tmp -ErrorAction SilentlyContinue
    }
}

switch ($hookEvent) {
    'SessionStart'      { Write-SessionFile }
    'SessionEnd'        { Spool-Event 'SessionEnd' 'Session ended' 'info' '' }
    'UserPromptSubmit'  {
        $prompt = Json-Field 'prompt'
        $preview = Truncate $prompt 60
        Spool-Event 'UserPromptSubmit' ("Working on: " + $preview) 'info' ('{"prompt_preview":"' + (JsonEscape $preview) + '"}')
    }
    'Notification'      {
        $message = Json-Field 'message'
        Spool-Event 'Notification' (Truncate $message 200) 'warning' ''
    }
    'PermissionRequest' {
        $tool = Json-Field 'tool_name'
        Spool-Event 'PermissionRequest' ("Needs approval: " + $tool) 'warning' ('{"tool":"' + (JsonEscape $tool) + '"}')
    }
    'Stop'              { Spool-Event 'Stop' 'Reply ready' 'warning' '' }
    'PreCompact'        {
        $reason = Json-Field 'reason'
        $title  = 'Compacting context'
        if ($reason) { $title = $title + ' (' + $reason + ')' }
        Spool-Event 'PreCompact' $title 'info' ('{"reason":"' + (JsonEscape $reason) + '"}')
    }
    'PostCompact'       { Spool-Event 'PostCompact' 'Compaction complete' 'info' '' }
    'SubagentStart'     {
        $agent = Json-Field 'agent_type'
        Spool-Event 'SubagentStart' ("Spawned: " + $agent) 'info' ('{"agent_type":"' + (JsonEscape $agent) + '"}')
    }
    'SubagentStop'      {
        $agent = Json-Field 'agent_type'
        Spool-Event 'SubagentStop' ($agent + ' done') 'info' ('{"agent_type":"' + (JsonEscape $agent) + '"}')
    }
    'TaskCreated'       {
        $content = Json-Field 'content'
        $contentT = Truncate $content 180
        Spool-Event 'TaskCreated' ("Task: " + $contentT) 'info' ('{"content":"' + (JsonEscape $contentT) + '"}')
    }
    'TaskCompleted'     {
        $content = Json-Field 'content'
        $contentT = Truncate $content 180
        Spool-Event 'TaskCompleted' ("✓ " + $contentT) 'info' ('{"content":"' + (JsonEscape $contentT) + '"}')
    }
    default             { Write-HookErr ("unhandled hook_event: {0}" -f $hookEvent) }
}

exit 0
