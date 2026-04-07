# Kill daemon, reset state, and rebuild executables
param(
    [switch]$SkipReset  # Use -SkipReset to rebuild without wiping state
)

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectDir = Split-Path -Parent $ScriptDir
$GoImage = "golang:1.25-alpine"

# 1. Kill processes
foreach ($name in @("quild", "quil")) {
    $proc = Get-Process -Name $name -ErrorAction SilentlyContinue
    if ($proc) {
        Stop-Process -Name $name -Force
        Write-Host "$name killed"
    }
}

## 2. Reset state (unless -SkipReset)
#if (-not $SkipReset) {
#    $quilDir = Join-Path $env:USERPROFILE ".quil"
#    Remove-Item -Path (Join-Path $quilDir "workspace.json") -Force -ErrorAction SilentlyContinue
#    Remove-Item -Path (Join-Path $quilDir "workspace.json.bak") -Force -ErrorAction SilentlyContinue
#    Remove-Item -Path (Join-Path $quilDir "buffers") -Recurse -Force -ErrorAction SilentlyContinue
#    Remove-Item -Path (Join-Path $quilDir "quild.pid") -Force -ErrorAction SilentlyContinue
#    Write-Host "State reset"
#}

# 3. Delete old executables
Remove-Item -Path (Join-Path $ProjectDir "quil.exe") -Force -ErrorAction SilentlyContinue
Remove-Item -Path (Join-Path $ProjectDir "quild.exe") -Force -ErrorAction SilentlyContinue

# 4. Rebuild via Docker
Write-Host "Building..."
$mountPath = ($ProjectDir -replace '\\', '/')
$ver = Get-Content (Join-Path $ProjectDir "VERSION") -Raw
$ver = $ver.Trim()

docker run --rm `
    -v "${mountPath}:/src" `
    -v "quil-gomod:/go/pkg/mod" `
    -w /src `
    $GoImage `
    sh -c "GOOS=windows GOARCH=amd64 go build -ldflags ""-X main.version=$ver"" -o quil.exe ./cmd/quil && GOOS=windows GOARCH=amd64 go build -ldflags ""-X main.version=$ver"" -o quild.exe ./cmd/quild"

if ($LASTEXITCODE -eq 0) {
    Write-Host "Done" -ForegroundColor Green
} else {
    Write-Host "Build failed" -ForegroundColor Red
    exit 1
}
