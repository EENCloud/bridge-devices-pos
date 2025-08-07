.PHONY: help test coverage coverage-html clean build fmt vet lint luacheck check ci-local

help:
	@echo "Available targets:"
	@echo "  build         - Build the application"
	@echo "  test          - Run all tests"
	@echo "  coverage      - Run tests with coverage report (entire codebase)"
	@echo "  coverage-html - Generate HTML coverage report (entire codebase)"
	@echo "  fmt           - Format Go code"
	@echo "  vet           - Run go vet"
	@echo "  lint          - Run golangci-lint"
	@echo "  luacheck      - Run luacheck on Lua scripts"
	@echo "  check         - Run fmt, vet, lint, luacheck, and test"
	@echo "  ci-local      - Run CI workflow locally with act"
	@echo "  clean         - Clean generated files"

build:
	go build -v ./cmd/bridge-devices-pos

test:
	go test -v ./...

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

coverage-html:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

fmt:
	go fmt ./...

vet:
	go vet ./...

lint:
	golangci-lint run --config .golangci.yml ./internal/... ./cmd/bridge-devices-pos/...

luacheck:
	luacheck --config internal/bridge/.luacheckrc internal/bridge/point_of_sale.lua

check: fmt vet lint luacheck test
	@echo "All checks passed!"

ci-local:
	@echo "Running GitHub CI workflow locally with act..."
	act --pull=false

clean:
	rm -f coverage.out coverage.html bridge-devices-pos 