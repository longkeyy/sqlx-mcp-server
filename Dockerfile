# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /build
ENV GOPROXY=https://goproxy.cn,direct
ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
    -trimpath \
    -ldflags="-s -w" \
    -o sql-mcp ./cmd/sql-mcp

FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /build/sql-mcp .

RUN mkdir -p /app/data

EXPOSE 8088

# No DSN or memo is baked into the image: the container refuses to start
# until the operator supplies a database through DB_URL, DB_{ID}_URL, or --dsn.
ENV DB_NAME=sql-mcp
ENV LISTEN=:8088

ENTRYPOINT ["/app/sql-mcp"]
