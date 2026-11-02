PLATFORMS ?= linux/amd64 darwin/arm64 darwin/amd64 windows/amd64
DIST_DIR ?= dist
.PHONY: all build frontend release test tidy clean help
all: build
frontend:
	cd web && npm ci --no-audit --no-fund && npm run build
build: frontend
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 go build -trimpath -o $(DIST_DIR)/sql-mcp ./cmd/sql-mcp
release: frontend
	mkdir -p $(DIST_DIR)
	@set -e; for platform in $(PLATFORMS); do \
	  os=$${platform%/*}; arch=$${platform#*/}; suffix=; \
	  if [ "$$os" = windows ]; then suffix=.exe; fi; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags='-s -w' \
	    -o $(DIST_DIR)/sql-mcp-$$os-$$arch$$suffix ./cmd/sql-mcp; \
	done
test: frontend
	go test ./...
tidy:
	go mod tidy
clean:
	rm -rf $(DIST_DIR)
help:
	@echo 'make build | release | test | clean (Go + Node required for building only)'
