# Docker-based development commands — no local Go required.

param(
    [Parameter(Position = 0)]
    [string]$Command = "help"
)

$ErrorActionPreference = "Stop"

$GoImage = "golang:1.24-alpine"
$ProjectDir = $PSScriptRoot

function Invoke-DockerGo {
    $dockerArgs = @(
        "run", "--rm",
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
        Invoke-DockerGo sh -c "GOOS=windows GOARCH=amd64 go build -o aethel.exe ./cmd/aethel && GOOS=windows GOARCH=amd64 go build -o aetheld.exe ./cmd/aetheld"
    }

    "test" {
        Invoke-DockerGo go test ./...
    }

    "test-race" {
        Invoke-DockerGo sh -c "apk add --no-cache gcc musl-dev && CGO_ENABLED=1 go test -race ./..."
    }

    "vet" {
        Invoke-DockerGo go vet ./...
    }

    "cross" {
        Invoke-DockerGo sh -c "mkdir -p dist && GOOS=linux GOARCH=amd64 go build -o dist/aethel-linux-amd64 ./cmd/aethel && GOOS=linux GOARCH=amd64 go build -o dist/aetheld-linux-amd64 ./cmd/aetheld && GOOS=linux GOARCH=arm64 go build -o dist/aethel-linux-arm64 ./cmd/aethel && GOOS=linux GOARCH=arm64 go build -o dist/aetheld-linux-arm64 ./cmd/aetheld && GOOS=darwin GOARCH=amd64 go build -o dist/aethel-darwin-amd64 ./cmd/aethel && GOOS=darwin GOARCH=amd64 go build -o dist/aetheld-darwin-amd64 ./cmd/aetheld && GOOS=darwin GOARCH=arm64 go build -o dist/aethel-darwin-arm64 ./cmd/aethel && GOOS=darwin GOARCH=arm64 go build -o dist/aetheld-darwin-arm64 ./cmd/aetheld && GOOS=windows GOARCH=amd64 go build -o dist/aethel-windows-amd64.exe ./cmd/aethel && GOOS=windows GOARCH=amd64 go build -o dist/aetheld-windows-amd64.exe ./cmd/aetheld"
    }

    "image" {
        & docker build -t aethel:latest $ProjectDir
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }

    "clean" {
        Remove-Item -Force -ErrorAction SilentlyContinue "$ProjectDir/aethel", "$ProjectDir/aetheld",
            "$ProjectDir/aethel.exe", "$ProjectDir/aetheld.exe"
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue "$ProjectDir/dist"
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
