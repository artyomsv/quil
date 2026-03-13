# Kill the aethel daemon and reset all persisted state
$proc = Get-Process -Name aetheld -ErrorAction SilentlyContinue
if ($proc) {
    Stop-Process -Name aetheld -Force
    Write-Host "Daemon killed"
} else {
    Write-Host "Daemon not running"
}

$aethelDir = Join-Path $env:USERPROFILE ".aethel"
Remove-Item -Path (Join-Path $aethelDir "workspace.json") -Force -ErrorAction SilentlyContinue
Remove-Item -Path (Join-Path $aethelDir "workspace.json.bak") -Force -ErrorAction SilentlyContinue
Remove-Item -Path (Join-Path $aethelDir "buffers") -Recurse -Force -ErrorAction SilentlyContinue
Remove-Item -Path (Join-Path $aethelDir "aetheld.pid") -Force -ErrorAction SilentlyContinue
Write-Host "State cleaned"
