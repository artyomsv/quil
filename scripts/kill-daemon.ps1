# Kill the quil daemon
$proc = Get-Process -Name quild -ErrorAction SilentlyContinue
if ($proc) {
    Stop-Process -Name quild -Force
    Write-Host "Daemon killed"
} else {
    Write-Host "Daemon not running"
}
