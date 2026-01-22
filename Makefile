# FPM Makefile

BINARY_NAME=fpm
BUILD_DIR=bin
MAIN_PATH=cmd/fpm/main.go

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
BINARY_OUT = $(BUILD_DIR)/$(BINARY_NAME)

# Add .exe extension for Windows if not already present
ifeq ($(GOOS),windows)
    ifneq ($(suffix $(BINARY_NAME)),.exe)
        BINARY_OUT = $(BUILD_DIR)/$(BINARY_NAME).exe
    endif
endif

.PHONY: all build test clean help

all: test build

build:
	@echo "Building FPM binary for $(GOOS)/$(GOARCH)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -v -o $(BINARY_OUT) $(MAIN_PATH)

test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...

clean:
	@echo "Cleaning up..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

help:
	@echo "Available targets:"
	@echo "  build   : Build the FPM binary"
	@echo "  test    : Run all tests with coverage"
	@echo "  clean   : Remove build artifacts"
	@echo "  all     : Run tests and build"
