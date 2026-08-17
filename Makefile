# FPM Makefile

BINARY_NAME ?= fpm
BUILD_DIR ?= bin
MAIN_PATH=cmd/fpm/main.go

# The registry service. Built separately from the CLI: operators deploy this,
# publishers install the CLI, and shipping one as a side effect of the other
# would put a server binary on every developer's machine.
REGISTRY_BINARY_NAME ?= fpm-registry
REGISTRY_MAIN_PATH=cmd/fpm-registry/main.go

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
BINARY_OUT = $(BUILD_DIR)/$(BINARY_NAME)

# Version reported by `fpm --version`, taken from the git tag so a released binary
# identifies itself. Falls back to "dev" outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS = -X 'fpm/cmd.version=$(VERSION)' -X 'fpm/cmd.commit=$(COMMIT)'

# Add .exe extension for Windows if not already present
ifeq ($(GOOS),windows)
    ifneq ($(suffix $(BINARY_NAME)),.exe)
        BINARY_OUT = $(BUILD_DIR)/$(BINARY_NAME).exe
    endif
endif

.PHONY: all build build-registry test test-integration clean help fmt

all: fmt test build

fmt:
	@echo "Formatting and simplifying code..."
	@gofmt -s -w .

build:
	@echo "Building FPM binary for $(GOOS)/$(GOARCH)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -v -ldflags "$(LDFLAGS)" -o $(BINARY_OUT) $(MAIN_PATH)

build-registry:
	@echo "Building the FPM registry service for $(GOOS)/$(GOARCH)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -v -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(REGISTRY_BINARY_NAME) $(REGISTRY_MAIN_PATH)

test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...

# Boots real nginx and the registry service. Needs a container runtime, so it
# is kept out of the default `test` target.
test-integration:
	@echo "Running integration tests..."
	go test -tags integration -count=1 -v ./test/...

clean:
	@echo "Cleaning up..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

help:
	@echo "Available targets:"
	@echo "  build          : Build the FPM CLI binary"
	@echo "  build-registry : Build the registry service binary"
	@echo "  test           : Run unit tests with coverage"
	@echo "  test-integration: Run container-backed acceptance tests"
	@echo "  clean          : Remove build artifacts"
	@echo "  all            : Run tests and build"
	@echo ""
	@echo "Variables: BINARY_NAME, BUILD_DIR, GOOS, GOARCH, VERSION, COMMIT"
