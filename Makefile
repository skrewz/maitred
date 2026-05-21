.PHONY: build test test-coverage test-race lint check-format clean image test-ui

MODULE  := maitred
GO      := go
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.Version=$(VERSION)

all: build

build: ## Build the maitred binary
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/maitred ./cmd/maitred

test: lint test-coverage test-race test-ui ## Run all tests (after linting)
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

image: ## Build the maitred container image
	podman build \
		--format docker \
		--build-arg LDFLAGS="$(LDFLAGS)" \
		-t maitred:latest \
		.

clean: ## Remove build artifacts
	rm -rf bin/ coverage.out coverage.html
	podman rmi maitred:latest 2>/dev/null || true

TEST_UI_PORT ?= 18090

.PHONY: test-ui

# Run UI tests against a running maitred instance
# Usage: make test-ui
# Or:     MAITRED_WEB_URL=http://other:9090 make test-ui
test-ui: build
	@echo "Starting maitred for UI tests on port $(TEST_UI_PORT)..."
	@rm -rf /tmp/maitred-ui-test-data && mkdir -p /tmp/maitred-ui-test-data
	@MAITRED_DATA_DIR=/tmp/maitred-ui-test-data MAITRED_WEB_PORT=$(TEST_UI_PORT) $(CURDIR)/bin/maitred -trigger-dir $(CURDIR)/config/triggers.d > /tmp/maitred-ui-test-server.log 2>&1 &
	@echo $$! > /tmp/maitred-ui-test.pid
	@sleep 2
	@node pkg/web/ui_test.mjs --base-url http://localhost:$(TEST_UI_PORT)
	@RET=$$? ; \
	kill $$(cat /tmp/maitred-ui-test.pid) 2>/dev/null || true ; \
	rm -f /tmp/maitred-ui-test.pid /tmp/maitred-ui-test-server.log ; \
	rm -rf /tmp/maitred-ui-test-data ; \
	exit $$RET
