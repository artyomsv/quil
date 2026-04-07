.PHONY: build test test-race vet cross clean

VERSION := $(shell cat VERSION)
LDFLAGS := -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o quil ./cmd/quil
	go build -ldflags "$(LDFLAGS)" -o quild ./cmd/quild

test:
	go test ./...

test-race:
	CGO_ENABLED=1 go test -race ./...

vet:
	go vet ./...

cross:
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/quil-linux-amd64      ./cmd/quil
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/quild-linux-amd64     ./cmd/quild
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/quil-linux-arm64      ./cmd/quil
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/quild-linux-arm64     ./cmd/quild
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/quil-darwin-amd64     ./cmd/quil
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/quild-darwin-amd64    ./cmd/quild
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/quil-darwin-arm64     ./cmd/quil
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/quild-darwin-arm64    ./cmd/quild
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/quil-windows-amd64.exe  ./cmd/quil
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/quild-windows-amd64.exe ./cmd/quild

clean:
	rm -f quil quild quil.exe quild.exe
	rm -rf dist/
