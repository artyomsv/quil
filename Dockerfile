# ---- Build stage ----
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build both binaries — static, no CGO by default
ARG CGO_ENABLED=0
ARG GOOS=linux
ARG GOARCH=amd64

RUN go build -o /out/quil ./cmd/quil \
 && go build -o /out/quild ./cmd/quild

# ---- Release stage ----
FROM scratch AS release

COPY --from=builder /out/quil /quil
COPY --from=builder /out/quild /quild

ENTRYPOINT ["/quil"]
