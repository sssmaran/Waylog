SHELL := /bin/sh

.PHONY: help build ingest ingest-mcp waylog checkout test fmt vet clean

help:
	@echo "Targets:"
	@echo "  build    - build all binaries"
	@echo "  ingest   - run ingest server"
	@echo "  ingest-mcp - run ingest server with MCP stdio enabled"
	@echo "  waylog   - run CLI"
	@echo "  checkout - run checkout server"
	@echo "  test     - run tests"
	@echo "  fmt      - gofmt code"
	@echo "  vet      - go vet"
	@echo "  clean    - remove build outputs"

build:
	go build ./cmd/ingest
	go build ./cmd/checkout
	go build ./cmd/waylog

ingest:
	go run ./cmd/ingest

ingest-mcp:
	MCP_STDIO=1 go run ./cmd/ingest

waylog:
	go run ./cmd/waylog

checkout:
	go run ./cmd/checkout

test:
	go test ./...

fmt:
	gofmt -w ./cmd ./internal ./pkg

vet:
	go vet ./...

clean:
	rm -f ingest checkout waylog
