.PHONY: build clean install start

# Detect OS for any platform-specific notes
UNAME := $(shell uname)

.DEFAULT_GOAL := help

help: ## Display this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build the multigps binary
	go build -o multigps .

clean: ## Remove build artifacts
	rm -f multigps

install: build ## Install the multigps binary to /usr/local/bin (may require sudo)
	install -m 755 multigps /usr/local/bin/multigps

# Quick smoke-test: build and print help
test: build ## Build the binary and print the help message to verify it works
	./multigps -h

start: ## Start the multigps server with API on port 8443 and status on port 8444, using TLS
	./multigps -api-port 8443 -tls
