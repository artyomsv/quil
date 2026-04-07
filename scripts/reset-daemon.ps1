# Kill the quil daemon and reset all persisted state
$proc = Get-Process -Name quild -ErrorAction SilentlyContinue
if ($proc) {
    Stop-Process -Name quild -Force
    Write-Host "Daemon killed"
} else {
    Write-Host "Daemon not running"
}

$quilDir = Join-Path $env:USERPROFILE ".quil"
Remove-Item -Path (Join-Path $quilDir "workspace.json") -Force -ErrorAction SilentlyContinue
Remove-Item -Path (Join-Path $quilDir "workspace.json.bak") -Force -ErrorAction SilentlyContinue
Remove-Item -Path (Join-Path $quilDir "buffers") -Recurse -Force -ErrorAction SilentlyContinue
Remove-Item -Path (Join-Path $quilDir "quild.pid") -Force -ErrorAction SilentlyContinue
Write-Host "State cleaned"
