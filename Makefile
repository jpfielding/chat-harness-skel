SHELL := /bin/bash
SCRIPTS_DIR := $(CURDIR)/scripts

MODULE_NAME = chat-harness
REPO_PATH   = $(shell git rev-parse --show-toplevel || pwd)
REPO_NAME   = $(shell basename $$REPO_PATH)
GIT_SHA     = $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
BUILD_DATE  = $(shell date +%Y-%m-%d)
BUILD_TIME  = $(shell date +%H:%M:%S)

all: test build

help: ## Prints help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST) | sort

nuke: ## Reset repo to clean state.
	git clean -ffdqx

clean: ## Remove build/test outputs.
	rm -rf bin tmp *.test
	go clean

update-deps: ## Tidy/vendor go modules.
	go get -u ./... && go mod tidy

### TEST ###
test: ## Short tests.
	go test -short -v ./...

test-report: ## JUnit test output.
	mkdir -p tmp && gotestsum --junitfile tmp/report.xml --format testname ./...

.PHONY: integration-test
integration-test: ## Full tests.
	go test -v ./...

live-smoke: ## Live provider smoke tests (NOT in CI).
	go run ./scripts/smoke --live --provider=all

lint: ## Static analysis.
	golangci-lint run ./...

lint-report:
	mkdir -p tmp && golangci-lint run --issues-exit-code 0 --out-format code-climate:tmp/gl-code-quality-report.json,line-number

vet:
	go vet ./...

vulnerability: install-govulncheck
	govulncheck ./...

vulnerability-report:
	mkdir -p tmp && govulncheck -json ./... > tmp/go-vuln-report.json

validate-config: build ## Validate config.example.toml via CLI.
	./bin/chat-harness --validate-config --config config.example.toml

record-transcript: ## Record a live transcript for replay tests.
	go run ./scripts/record-transcript $(ARGS)

### TOOLS ###
install-tools: install-claude install-cicd-tools

.PHONY: install-claude
install-claude:
	claude update || \
	(curl -fsSL https://claude.ai/install.sh | bash 2>/dev/null) && \
	claude doctor

.PHONY: install-codex
install-codex:
	ARCH="$(shell arch | sed 's/arm64/aarch64/' | sed 's/amd64/x86_64/')" && \
	OS=$(shell uname | sed 's/Darwin/apple-darwin/' | sed 's/Linux/unknown-linux-musl/') && \
	curl -fsSL -o /tmp/codex.tar.gz "https://github.com/openai/codex/releases/latest/download/codex-$${ARCH}-$${OS}.tar.gz" && \
	tar -xzf /tmp/codex.tar.gz -C /tmp && \
	mv /tmp/codex-$${ARCH}-$${OS} ${HOME}/bin/codex && \
	chmod +x ${HOME}/bin/codex

install-cicd-tools: install-gotestsum install-govulncheck install-golint

install-gotestsum:
	go install gotest.tools/gotestsum@latest
install-govulncheck:
	go install golang.org/x/vuln/cmd/govulncheck@latest
install-golint:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

### BUILD ###
build: ## Build the chat-harness binary.
	mkdir -p bin
	CGO_ENABLED=0 go build \
		-trimpath \
		-ldflags "-X 'main.GitSHA=$(GIT_SHA)' -X 'main.BuildDate=$(BUILD_DATE)'" \
		-o bin/chat-harness \
		./cmd/chat-harness

run: build ## Build and run locally with example config.
	./bin/chat-harness --config config.example.toml
