SHELL := /bin/sh

.PHONY: help build build-crux install-local build-examples ingest ingest-mcp waylog checkout test test-race test-sdk lint ci fmt vet vet-sdk clean kafka-up kafka-down demo demo-stop demo-acceptance proof-loop rca-scorecard rollup-comparison otlp-conformance demo-up demo-down micro-demo micro-demo-stop docker-build docker-up docker-down docker-reset docker-dev docker-prod ts-install ts-build ts-test bench-gate

help:
	@echo "Targets:"
	@echo "  build    - build core binaries (SDK tooling)"
	@echo "  build-crux - build Crux interactive shell"
	@echo "  install-local - install crux and waylog to GOPATH/bin"
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
	@echo "  demo     - start dashboard demo locally (detached, no Docker)"
	@echo "  demo-stop - stop demo processes"
	@echo "  demo-acceptance - verify a running local demo end-to-end"
	@echo "  proof-loop - run alert -> incident -> triage -> report -> rollup proof"
	@echo "  rca-scorecard - run deterministic RCA scorecard over the demo scenario"
	@echo "  rollup-comparison - run demo proof for root-cause vs naive rollup counts"
	@echo "  otlp-conformance - run deterministic OTLP HTTP/gRPC fixture checks"
	@echo "  demo-up  - start v2 demo stack in Docker (detached)"
	@echo "  demo-down - stop Docker demo stack"
	@echo "  micro-demo - start 4-service micro-demo in foreground for debugging"
	@echo "  micro-demo-stop - stop micro-demo processes"
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

build-crux:
	go build -o crux ./cmd/crux

install-local: build build-crux
	@mkdir -p "$$(go env GOPATH)/bin"
	cp crux waylog "$$(go env GOPATH)/bin/"
	@echo "installed: crux waylog -> $$(go env GOPATH)/bin/"
	@echo ""
	@echo "Add to PATH if needed:"
	@echo "  export PATH=\"$$(go env GOPATH)/bin:$$PATH\""

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

ci: fmt vet vet-sdk test-race test-sdk ts-test build-crux check-doc-links check-rollup-contract otlp-conformance
	@echo "CI checks passed"

ts-install: ## Install TS SDK deps (skipped if node_modules is already present)
	@cd packages/waylog-ts && ( test -d node_modules || npm install --silent )

ts-build: ts-install ## Type-check + build TS SDK
	cd packages/waylog-ts && npm run build

ts-test: ts-install ## Run TS SDK vitest suite
	cd packages/waylog-ts && npm test

.PHONY: check-doc-links
check-doc-links:
	@bash scripts/check-doc-links.sh

.PHONY: check-rollup-contract
check-rollup-contract:
	@bash scripts/check-rollup-contract.sh

bench-gate: ## Enforce v2 SDK §4.4.1 perf budgets (optional; not in `ci` yet)
	@bash scripts/bench-gate.sh

clean:
	rm -f ingest checkout waylog bridge crux api-gateway checkout-demo db-demo payment-demo

kafka-up:
	docker compose -f docker-compose.kafka.yml up -d

kafka-down:
	docker compose -f docker-compose.kafka.yml down -v

demo:
	./scripts/demo.sh

demo-stop:
	./scripts/demo-stop.sh

demo-acceptance:
	./scripts/demo-acceptance.sh

proof-loop:
	bash ./scripts/proof-loop.sh

rca-scorecard:
	bash ./scripts/rca-scorecard.sh

rollup-comparison:
	./scripts/rollup-comparison.sh

otlp-conformance:
	./scripts/otlp-conformance.sh

demo-up: docker-dev

demo-down: docker-down

micro-demo:
	./scripts/micro-demo.sh

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
	@echo "v2 demo stack running:"
	@echo "  Dashboard: http://localhost:8080/ui/  (key: demo)"
	@echo "  Demo UI:   http://localhost:9081/demo"
	@echo "  Trigger:   curl -s -X POST http://localhost:9081/purchase -H 'Content-Type: application/json' --data '{\"sku\":\"X1\",\"scenario\":\"payment_502\"}'"

docker-prod:
	ENV_FILE=deploy/prod.env docker compose up -d --build
