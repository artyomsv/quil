# Quil shell integration — OSC 7 + OSC 133 for PowerShell
# Source user's profile (-NoProfile prevents auto-loading)
if (Test-Path $PROFILE.CurrentUserCurrentHost) { . $PROFILE.CurrentUserCurrentHost }

# Override prompt to emit OSC 7 + OSC 133 command markers
# Use [char]0x1b for ESC — compatible with both PowerShell 7+ and Windows PowerShell 5.1
$__quil_esc = [char]0x1b
$__quil_original_prompt = $function:prompt
function prompt {
    # OSC 133;D — report previous command exit code
    $ec = $LASTEXITCODE; if ($null -eq $ec) { $ec = 0 }
    $host.UI.Write("$__quil_esc]133;D;$ec$__quil_esc\")
    # OSC 133;A — prompt start
    $host.UI.Write("$__quil_esc]133;A$__quil_esc\")
    # OSC 7 — current working directory
    $cwd = (Get-Location).Path -replace '\\', '/'
    if ($cwd -match '^[A-Z]:') { $cwd = "/$cwd" }
    $host.UI.Write("$__quil_esc]7;file://$([System.Net.Dns]::GetHostName())$cwd$__quil_esc\")
    & $__quil_original_prompt
}
