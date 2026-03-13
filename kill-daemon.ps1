# Kill the aethel daemon
$proc = Get-Process -Name aetheld -ErrorAction SilentlyContinue
if ($proc) {
    Stop-Process -Name aetheld -Force
    Write-Host "Daemon killed"
} else {
    Write-Host "Daemon not running"
}
