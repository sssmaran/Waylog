SHELL := /bin/sh

.PHONY: help build ingest ingest-mcp waylog checkout test fmt vet clean kafka-up kafka-down demo demo-stop micro-demo micro-demo-stop

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
	@echo "  kafka-up - start local Kafka via docker compose"
	@echo "  kafka-down - stop local Kafka via docker compose"
	@echo "  demo     - start Kafka + demo flow (single terminal)"
	@echo "  demo-stop - stop Kafka + demo processes"
	@echo "  micro-demo - start 3-service micro-demo (gateway+checkout+payment)"
	@echo "  micro-demo-stop - stop micro-demo processes"

build:
	go build ./cmd/ingest
	go build ./cmd/checkout
	go build ./cmd/waylog
	go build ./cmd/bridge
	go build ./cmd/api-gateway
	go build ./cmd/checkout-demo
	go build ./cmd/payment-demo

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
	rm -f ingest checkout waylog bridge api-gateway checkout-demo payment-demo

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
