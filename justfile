# schlaflos task runner. Requires `just` (https://github.com/casey/just).

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X main.version=" + version

# List available recipes.
default:
    @just --list

# Build the binary into ./bin/schlaflos.
build:
    mkdir -p bin
    go build -trimpath -ldflags "{{ldflags}}" -o bin/schlaflos ./cmd/schlaflos

# Run gofmt (check only), go vet, and the unit tests.
check: fmt-check vet test

# Run the unit tests. Nothing in this suite touches the machine's power settings.
test:
    go test ./...

# Run the unit tests plus the read-only pmset queries against this Mac.
test-readonly:
    SCHLAFLOS_READONLY_INTEGRATION=1 go test ./...

vet:
    go vet ./...

fmt:
    gofmt -w cmd internal packaging

fmt-check:
    @out="$(gofmt -l cmd internal packaging)"; if [ -n "$out" ]; then echo "gofmt needed:"; echo "$out"; exit 1; fi

# Validate a configuration file with the freshly built binary.
config-check path="examples/schlaflos.toml": build
    ./bin/schlaflos config check {{path}}

# Cross-compile release binaries into ./dist with SHA-256 checksums.
dist:
    rm -rf dist && mkdir -p dist
    GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "{{ldflags}}" -o dist/schlaflos-darwin-arm64 ./cmd/schlaflos
    GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "{{ldflags}}" -o dist/schlaflos-darwin-amd64 ./cmd/schlaflos
    cd dist && shasum -a 256 schlaflos-darwin-* > SHA256SUMS

clean:
    rm -rf bin dist
