# Quil SessionStart hook for Claude Code.
#
# Windows / pwsh variant. Receives Claude's hook JSON on stdin, extracts
# session_id, writes it atomically to $QUIL_HOME/sessions/$QUIL_PANE_ID.id.
# QUIL_PANE_ID is set by Quil at pane spawn; when unset the hook is a no-op.
# Always exits 0 so Claude is never blocked by our bookkeeping.
#
# Failure breadcrumbs land in $QUIL_HOME/claudehook/hook.log so a silently
# broken hook is detectable from the logs.

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

$sessionsDir = Join-Path $quilHome 'sessions'
try {
    New-Item -ItemType Directory -Path $sessionsDir -Force | Out-Null
} catch {
    Write-HookErr "mkdir sessions dir failed: $sessionsDir"
    exit 0
}

$payload = [Console]::In.ReadToEnd()
$sessionId = $null
try {
    $obj = $payload | ConvertFrom-Json
    $sessionId = $obj.session_id
} catch {
    if ($payload -match '"session_id"\s*:\s*"([^"]+)"') { $sessionId = $Matches[1] }
}
if (-not $sessionId) {
    Write-HookErr "no session_id extracted from stdin"
    exit 0
}

# Defence-in-depth: only accept uuid-shaped values.
if ($sessionId -notmatch '^[0-9a-fA-F-]{32,64}$') {
    Write-HookErr ("session_id rejected as non-uuid: {0}" -f $sessionId)
    exit 0
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
exit 0
