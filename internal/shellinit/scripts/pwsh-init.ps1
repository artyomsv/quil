# Quil shell integration — OSC 7 + OSC 133 for PowerShell
# Source user's profile (-NoProfile prevents auto-loading)
if (Test-Path $PROFILE.CurrentUserCurrentHost) { . $PROFILE.CurrentUserCurrentHost }

# Override prompt to emit OSC 7 + OSC 133 command markers
# Use [char]0x1b for ESC — compatible with both PowerShell 7+ and Windows PowerShell 5.1
$__quil_esc = [char]0x1b
$__quil_original_prompt = $function:prompt
# Id of the last history entry seen at a prompt. OSC 133;D reports a COMMAND's
# exit, so it is emitted only when a command ran since the previous prompt —
# the history id moved. A prompt drawn with nothing run (the first one after
# startup, or a bare Enter) emits no D: the daemon reads every D as "the
# command you sent has finished", and a task handed to a fresh shell used to
# complete on the startup prompt.
$global:__quil_hist = 0
function prompt {
    $ec = $LASTEXITCODE; if ($null -eq $ec) { $ec = 0 }
    $__quil_last = Get-History -Count 1
    $__quil_id = if ($__quil_last) { $__quil_last.Id } else { 0 }
    if ($__quil_id -ne $global:__quil_hist) {
        # OSC 133;D — report previous command exit code
        $host.UI.Write("$__quil_esc]133;D;$ec$__quil_esc\")
        $global:__quil_hist = $__quil_id
    }
    # OSC 133;A — prompt start
    $host.UI.Write("$__quil_esc]133;A$__quil_esc\")
    # OSC 7 — current working directory
    $cwd = (Get-Location).Path -replace '\\', '/'
    if ($cwd -match '^[A-Z]:') { $cwd = "/$cwd" }
    $host.UI.Write("$__quil_esc]7;file://$([System.Net.Dns]::GetHostName())$cwd$__quil_esc\")
    & $__quil_original_prompt
}

# Hand-started agent interception (issue #221). See bash-init.sh for the
# reasoning shared by all three shells; PowerShell differs in four ways, each
# noted at its guard below.
if ($env:QUIL_INTERCEPT -and $env:QUIL_INTERCEPT_TOKEN) {

    function __quil_esc([string]$s) {
        # `%` first or the encoding is ambiguous.
        $s = $s -replace '%', '%25'
        $s = $s -replace ';', '%3B'
        $s -replace ',', '%2C'
    }

    function __quil_intercept {
        param([string]$Name, [object[]]$Rest, [object]$Pipe)

        # Get-Command returns an ARRAY when both claude.cmd and claude.exe are
        # on PATH, and `&` on an array fails.
        $bin = Get-Command $Name -CommandType Application -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if (-not $bin) {
            Write-Error "quil: $Name not found"
            return
        }

        # Pipeline input is OBJECTS, not process stdin, so IsInputRedirected is
        # false for `"text" | claude`. Every run path forwards $Pipe so that
        # input is not dropped by the interception.
        if ([Console]::IsInputRedirected -or [Console]::IsOutputRedirected) {
            $Pipe | & $bin @Rest; return
        }
        if ($Rest.Count -gt 0 -and
            @('-p', '--print', '-v', '--version', '-h', '--help') -contains $Rest[0]) {
            $Pipe | & $bin @Rest; return
        }

        $args_enc = ($Rest | ForEach-Object { __quil_esc ([string]$_) }) -join ','
        $envnames = (Get-ChildItem Env: |
            Where-Object { $_.Name -match '^(CLAUDE_|ANTHROPIC_|CODEX_|OPENAI_|OPENCODE_)' } |
            ForEach-Object { $_.Name }) -join ','
        $ts = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
        $pwdEnc = __quil_esc ((Get-Location).Path)

        $payload = "cmd;$($env:QUIL_INTERCEPT_TOKEN);$Name;$pwdEnc;$ts;$envnames;$args_enc"
        if ($payload.Length -gt 2048) {
            $payload = "cmd;$($env:QUIL_INTERCEPT_TOKEN);$Name;;;;!"
        }

        # ReadKey with NoEcho is PowerShell's own echo suppression, so there is
        # no stty ordering problem here — the marker may be written first.
        $host.UI.Write("$__quil_esc]7770;$payload$__quil_esc\")

        # Exactly 8 characters, polled against a deadline. No terminator: LF is
        # not Enter under ConPTY, which is the whole reason this is a fixed
        # length rather than a line.
        $reply = ''
        $deadline = [DateTime]::UtcNow.AddSeconds(1)
        while ($reply.Length -lt 8 -and [DateTime]::UtcNow -lt $deadline) {
            if ($host.UI.RawUI.KeyAvailable) {
                $k = $host.UI.RawUI.ReadKey('NoEcho,IncludeKeyDown')
                $reply += $k.Character
            } else {
                Start-Sleep -Milliseconds 10
            }
        }

        if ($reply -eq 'quil:cnv') { return }
        $Pipe | & $bin @Rest
    }

    foreach ($__qn in ($env:QUIL_INTERCEPT -split ',')) {
        if ($__qn -notmatch '^[A-Za-z0-9._-]+$') { continue }
        # A user's own function of that name exists to set environment, and
        # must keep winning over Quil's.
        if (Get-Command $__qn -CommandType Function -ErrorAction SilentlyContinue) { continue }
        $__qbody = @"
function global:$__qn {
    [CmdletBinding()] param([Parameter(ValueFromRemainingArguments=`$true)][object[]]`$Rest)
    begin { `$__pipe = @() }
    process { if (`$null -ne `$_) { `$__pipe += `$_ } }
    end { __quil_intercept -Name '$__qn' -Rest `$Rest -Pipe `$__pipe }
}
"@
        Invoke-Expression $__qbody
    }
}
