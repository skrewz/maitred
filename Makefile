.PHONY: build test test-coverage test-race lint check-format clean

MODULE  := maitred
GO      := go
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.Version=$(VERSION)

all: build

build: ## Build the maitred binary
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/maitred ./cmd/maitred

test: lint test-coverage test-race ## Run all tests (after linting)
	@echo "All test targets passed"

test-coverage: ## Run tests with coverage report
	$(GO) test -race -count=1 -coverprofile=coverage.out -coverpkg=./pkg/... ./...
	@echo ""
	@$(GO) tool cover -func=coverage.out | grep -E 'total'
	@$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "HTML report: coverage.html"
	@echo "Coverage passed"

test-race: ## Run tests with race detector
	$(GO) test -race -count=1 ./...
	@echo "Race detector passed"

lint: ## Run go vet and check formatting with gofumpt
	$(GO) vet ./...
	$(MAKE) check-format

check-format: ## Check Go code formatting with gofumpt (fails if not formatted)
	@if [ -n "$$($(GO) env GOPATH)" ]; then \
		GOFUMPT=$$($(GO) env GOPATH)/bin/gofumpt; \
	else \
		GOFUMPT=gofumpt; \
	fi; \
	files=$$($$GOFUMPT -l .); \
	if [ -n "$$files" ]; then \
		echo "Files not formatted with gofumpt:"; \
		echo "$$files"; \
		exit 1; \
	fi

install: ## Install binary to $GOPATH/bin
	$(GO) install -trimpath -ldflags "$(LDFLAGS)" ./cmd/maitred

clean: ## Remove build artifacts
	rm -rf bin/ coverage.out coverage.html
