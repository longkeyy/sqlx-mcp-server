FROM golang:1.26-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o sql-mcp .

FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /build/sql-mcp .

COPY data/ /app/data/

RUN mkdir -p /app/data

EXPOSE 8088

ENV SERVER_MODE=http
ENV SERVER_PORT=8088

ENTRYPOINT ["/app/sql-mcp"]
CMD ["--dsn", "sqlite:///app/data/tcm_unified.db", "--name", "tcm-db", "--mode", "http", "--port", "8088"]
