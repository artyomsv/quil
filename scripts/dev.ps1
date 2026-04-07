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
        "-v", "quil-gomod:/go/pkg/mod",
        "-w", "/src",
        $GoImage
    ) + $args
    & docker $dockerArgs
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

switch ($Command) {
    "build" {
        Write-Host "[quil] Building Windows binaries..." -ForegroundColor Cyan
        Invoke-DockerGo sh -c 'VER=$(cat VERSION) && LDFLAGS="-X main.version=$VER" && GOOS=windows GOARCH=amd64 go build -v -ldflags "$LDFLAGS" -o quil.exe ./cmd/quil && GOOS=windows GOARCH=amd64 go build -v -ldflags "$LDFLAGS" -o quild.exe ./cmd/quild'
        Write-Host "[quil] Built: quil.exe, quild.exe" -ForegroundColor Green
    }

    "test" {
        Write-Host "[quil] Running tests..." -ForegroundColor Cyan
        Invoke-DockerGo go test -v ./...
        Write-Host "[quil] Tests passed" -ForegroundColor Green
    }

    "test-race" {
        Write-Host "[quil] Running tests with race detector..." -ForegroundColor Cyan
        Invoke-DockerGo sh -c "apk add --no-cache gcc musl-dev && CGO_ENABLED=1 go test -race -v ./..."
        Write-Host "[quil] Tests passed (race)" -ForegroundColor Green
    }

    "vet" {
        Write-Host "[quil] Running go vet..." -ForegroundColor Cyan
        Invoke-DockerGo go vet ./...
        Write-Host "[quil] Vet passed" -ForegroundColor Green
    }

    "cross" {
        Write-Host "[quil] Cross-compiling for all platforms..." -ForegroundColor Cyan
        Invoke-DockerGo sh -c 'VER=$(cat VERSION) && LDFLAGS="-X main.version=$VER" && mkdir -p dist && GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/quil-linux-amd64 ./cmd/quil && GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/quild-linux-amd64 ./cmd/quild && GOOS=linux GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/quil-linux-arm64 ./cmd/quil && GOOS=linux GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/quild-linux-arm64 ./cmd/quild && GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/quil-darwin-amd64 ./cmd/quil && GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/quild-darwin-amd64 ./cmd/quild && GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/quil-darwin-arm64 ./cmd/quil && GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/quild-darwin-arm64 ./cmd/quild && GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/quil-windows-amd64.exe ./cmd/quil && GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/quild-windows-amd64.exe ./cmd/quild'
        Write-Host "[quil] Cross-compilation complete. See dist/" -ForegroundColor Green
    }

    "image" {
        Write-Host "[quil] Building Docker image..." -ForegroundColor Cyan
        & docker build -t quil:latest $ProjectDir
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        Write-Host "[quil] Image built: quil:latest" -ForegroundColor Green
    }

    "clean" {
        Write-Host "[quil] Cleaning..." -ForegroundColor Cyan
        Remove-Item -Force -ErrorAction SilentlyContinue "$ProjectDir/quil", "$ProjectDir/quild",
            "$ProjectDir/quil.exe", "$ProjectDir/quild.exe"
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue "$ProjectDir/dist"
        Write-Host "[quil] Clean" -ForegroundColor Green
    }

    default {
        Write-Host "Usage: .\dev.ps1 <command>"
        Write-Host ""
        Write-Host "Commands:"
        Write-Host "  build       Build Windows binaries (quil.exe + quild.exe)"
        Write-Host "  test        Run all tests"
        Write-Host "  test-race   Run tests with race detector"
        Write-Host "  vet         Run go vet"
        Write-Host "  cross       Cross-compile for all platforms"
        Write-Host "  image       Build Docker image (scratch-based)"
        Write-Host "  clean       Remove built binaries"
    }
}
