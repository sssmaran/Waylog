SHELL := /bin/sh

.PHONY: help build build-examples ingest ingest-mcp waylog waylog-live checkout test test-race test-sdk lint ci fmt vet vet-sdk clean kafka-up kafka-down demo demo-stop micro-demo micro-demo-stop docker-build docker-up docker-down docker-reset docker-dev docker-prod

help:
	@echo "Targets:"
	@echo "  build    - build core binaries (SDK tooling)"
	@echo "  build-examples - build example/demo binaries"
	@echo "  ingest   - run ingest server"
	@echo "  ingest-mcp - run ingest server with MCP stdio enabled"
	@echo "  waylog   - run CLI"
	@echo "  checkout - run checkout server"
	@echo "  test     - run tests"
	@echo "  fmt      - gofmt code"
	@echo "  vet      - go vet"
	@echo "  clean    - remove build outputs"
	@echo "  kafka-up - start local Kafka via docker compose"
	@echo "  kafka-down - stop local Kafka via docker compose"
	@echo "  demo     - start Kafka + demo flow (single terminal)"
	@echo "  demo-stop - stop Kafka + demo processes"
	@echo "  micro-demo - start 4-service micro-demo (gateway+checkout+db+payment)"
	@echo "  micro-demo-stop - stop micro-demo processes"
	@echo "  waylog-live - run TUI dashboard (connects to ingest server)"
	@echo "  docker-build - build all Docker images"
	@echo "  docker-up   - start full stack via docker compose"
	@echo "  docker-down - stop stack (preserve volumes)"
	@echo "  docker-reset - stop stack and delete volumes"
	@echo "  docker-dev  - start stack with dev profile (100% sampling)"
	@echo "  docker-prod - start stack with prod profile (5% sampling)"

build:
	go build ./cmd/ingest
	go build ./cmd/checkout
	go build ./cmd/waylog
	go build ./cmd/bridge
	go build ./cmd/waylog-live

build-examples:
	go build ./examples/cmd/api-gateway
	go build ./examples/cmd/checkout-demo
	go build ./examples/cmd/db-demo
	go build ./examples/cmd/payment-demo

ingest:
	go run ./cmd/ingest

ingest-mcp:
	MCP_STDIO=1 go run ./cmd/ingest

waylog:
	go run ./cmd/waylog

waylog-live:
	go run ./cmd/waylog-live

checkout:
	go run ./cmd/checkout

test:
	go test ./...

fmt:
	gofmt -w ./cmd ./internal ./pkg ./examples

vet:
	go vet ./...

test-race:
	go test -race ./...

lint:
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed, skipping"

test-sdk: ## Test SDK modules
	cd pkg && go test -race ./...
	cd pkg/transport/kafka && go build ./...

vet-sdk: ## Vet SDK modules
	cd pkg && go vet ./...
	cd pkg/transport/kafka && go vet ./...

ci: fmt vet vet-sdk test-race test-sdk check-doc-links
	@echo "CI checks passed"

.PHONY: check-doc-links
check-doc-links:
	@bash scripts/check-doc-links.sh

clean:
	rm -f ingest checkout waylog bridge api-gateway checkout-demo db-demo payment-demo waylog-live

kafka-up:
	docker compose -f docker-compose.kafka.yml up -d

kafka-down:
	docker compose -f docker-compose.kafka.yml down -v

demo:
	START_KAFKA=1 ./scripts/demo.sh

demo-stop:
	./scripts/demo-stop.sh

micro-demo:
	START_KAFKA=1 ./scripts/micro-demo.sh

micro-demo-stop:
	./scripts/micro-demo-stop.sh

docker-build:
	docker compose build

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

docker-reset:
	docker compose down -v

docker-dev:
	ENV_FILE=deploy/dev.env docker compose up -d --build

docker-prod:
	ENV_FILE=deploy/prod.env docker compose up -d --build
