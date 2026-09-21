# lazyadb task runner. Run `just` with no arguments to see this list.

set shell := ["bash", "-euo", "pipefail", "-c"]

bin := justfile_directory() / "lazyadb"

# List available recipes.
default:
    @just --list

# Build the lazyadb binary into the repo root.
build:
    go build -o {{bin}} ./cmd/lazyadb

# Build (if needed) and launch the TUI.
run: build
    {{bin}}

# Run the unit tests.
test:
    go test ./...

# Vet, format-check, and lint (golangci-lint if present, go vet otherwise).
lint:
    go vet ./...
    test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
    command -v golangci-lint >/dev/null && golangci-lint run || true

# Everything CI runs.
check: lint test
