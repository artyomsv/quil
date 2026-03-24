# Docker-based development commands — no local Go required.

param(
    [Parameter(Position = 0)]
    [string]$Command = "help"
)

$ErrorActionPreference = "Stop"

$GoImage = "golang:1.25-alpine"
$ProjectDir = $PSScriptRoot

function Invoke-DockerGo {
    $dockerArgs = @(
        "run", "--rm", "-t",
        "-v", "${ProjectDir}:/src",
        "-v", "aethel-gomod:/go/pkg/mod",
        "-w", "/src",
        $GoImage
    ) + $args
    & docker $dockerArgs
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

switch ($Command) {
    "build" {
        Write-Host "[aethel] Building Windows binaries..." -ForegroundColor Cyan
        Invoke-DockerGo sh -c 'VER=$(cat VERSION) && LDFLAGS="-X main.version=$VER" && GOOS=windows GOARCH=amd64 go build -v -ldflags "$LDFLAGS" -o aethel.exe ./cmd/aethel && GOOS=windows GOARCH=amd64 go build -v -ldflags "$LDFLAGS" -o aetheld.exe ./cmd/aetheld'
        Write-Host "[aethel] Built: aethel.exe, aetheld.exe" -ForegroundColor Green
    }

    "test" {
        Write-Host "[aethel] Running tests..." -ForegroundColor Cyan
        Invoke-DockerGo go test -v ./...
        Write-Host "[aethel] Tests passed" -ForegroundColor Green
    }

    "test-race" {
        Write-Host "[aethel] Running tests with race detector..." -ForegroundColor Cyan
        Invoke-DockerGo sh -c "apk add --no-cache gcc musl-dev && CGO_ENABLED=1 go test -race -v ./..."
        Write-Host "[aethel] Tests passed (race)" -ForegroundColor Green
    }

    "vet" {
        Write-Host "[aethel] Running go vet..." -ForegroundColor Cyan
        Invoke-DockerGo go vet ./...
        Write-Host "[aethel] Vet passed" -ForegroundColor Green
    }

    "cross" {
        Write-Host "[aethel] Cross-compiling for all platforms..." -ForegroundColor Cyan
        Invoke-DockerGo sh -c 'VER=$(cat VERSION) && LDFLAGS="-X main.version=$VER" && mkdir -p dist && GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/aethel-linux-amd64 ./cmd/aethel && GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/aetheld-linux-amd64 ./cmd/aetheld && GOOS=linux GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/aethel-linux-arm64 ./cmd/aethel && GOOS=linux GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/aetheld-linux-arm64 ./cmd/aetheld && GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/aethel-darwin-amd64 ./cmd/aethel && GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/aetheld-darwin-amd64 ./cmd/aetheld && GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/aethel-darwin-arm64 ./cmd/aethel && GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/aetheld-darwin-arm64 ./cmd/aetheld && GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/aethel-windows-amd64.exe ./cmd/aethel && GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/aetheld-windows-amd64.exe ./cmd/aetheld'
        Write-Host "[aethel] Cross-compilation complete. See dist/" -ForegroundColor Green
    }

    "image" {
        Write-Host "[aethel] Building Docker image..." -ForegroundColor Cyan
        & docker build -t aethel:latest $ProjectDir
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        Write-Host "[aethel] Image built: aethel:latest" -ForegroundColor Green
    }

    "clean" {
        Write-Host "[aethel] Cleaning..." -ForegroundColor Cyan
        Remove-Item -Force -ErrorAction SilentlyContinue "$ProjectDir/aethel", "$ProjectDir/aetheld",
            "$ProjectDir/aethel.exe", "$ProjectDir/aetheld.exe"
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue "$ProjectDir/dist"
        Write-Host "[aethel] Clean" -ForegroundColor Green
    }

    default {
        Write-Host "Usage: .\dev.ps1 <command>"
        Write-Host ""
        Write-Host "Commands:"
        Write-Host "  build       Build Windows binaries (aethel.exe + aetheld.exe)"
        Write-Host "  test        Run all tests"
        Write-Host "  test-race   Run tests with race detector"
        Write-Host "  vet         Run go vet"
        Write-Host "  cross       Cross-compile for all platforms"
        Write-Host "  image       Build Docker image (scratch-based)"
        Write-Host "  clean       Remove built binaries"
    }
}
