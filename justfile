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

# Vet, format-check, and lint. golangci-lint findings fail the recipe when it
# is installed; CI always runs it.
lint:
    go vet ./...
    test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }
    if command -v golangci-lint >/dev/null; then golangci-lint run; else echo "golangci-lint not installed; skipped (CI runs it)"; fi

# Everything CI runs.
check: lint test
