# Kill daemon, reset state, and rebuild executables
param(
    [switch]$SkipReset  # Use -SkipReset to rebuild without wiping state
)

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectDir = Split-Path -Parent $ScriptDir
$GoImage = "golang:1.25-alpine"

# 1. Kill processes
foreach ($name in @("aetheld", "aethel")) {
    $proc = Get-Process -Name $name -ErrorAction SilentlyContinue
    if ($proc) {
        Stop-Process -Name $name -Force
        Write-Host "$name killed"
    }
}

## 2. Reset state (unless -SkipReset)
#if (-not $SkipReset) {
#    $aethelDir = Join-Path $env:USERPROFILE ".aethel"
#    Remove-Item -Path (Join-Path $aethelDir "workspace.json") -Force -ErrorAction SilentlyContinue
#    Remove-Item -Path (Join-Path $aethelDir "workspace.json.bak") -Force -ErrorAction SilentlyContinue
#    Remove-Item -Path (Join-Path $aethelDir "buffers") -Recurse -Force -ErrorAction SilentlyContinue
#    Remove-Item -Path (Join-Path $aethelDir "aetheld.pid") -Force -ErrorAction SilentlyContinue
#    Write-Host "State reset"
#}

# 3. Delete old executables
Remove-Item -Path (Join-Path $ProjectDir "aethel.exe") -Force -ErrorAction SilentlyContinue
Remove-Item -Path (Join-Path $ProjectDir "aetheld.exe") -Force -ErrorAction SilentlyContinue

# 4. Rebuild via Docker
Write-Host "Building..."
$mountPath = ($ProjectDir -replace '\\', '/')
$ver = Get-Content (Join-Path $ProjectDir "VERSION") -Raw
$ver = $ver.Trim()

docker run --rm `
    -v "${mountPath}:/src" `
    -v "aethel-gomod:/go/pkg/mod" `
    -w /src `
    $GoImage `
    sh -c "GOOS=windows GOARCH=amd64 go build -ldflags ""-X main.version=$ver"" -o aethel.exe ./cmd/aethel && GOOS=windows GOARCH=amd64 go build -ldflags ""-X main.version=$ver"" -o aetheld.exe ./cmd/aetheld"

if ($LASTEXITCODE -eq 0) {
    Write-Host "Done" -ForegroundColor Green
} else {
    Write-Host "Build failed" -ForegroundColor Red
    exit 1
}
