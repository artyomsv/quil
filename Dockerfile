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

RUN go build -o /out/aethel ./cmd/aethel \
 && go build -o /out/aetheld ./cmd/aetheld

# ---- Release stage ----
FROM scratch AS release

COPY --from=builder /out/aethel /aethel
COPY --from=builder /out/aetheld /aetheld

ENTRYPOINT ["/aethel"]
