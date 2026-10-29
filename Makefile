BINARY := cody
# Release-grade local builds: trimmed paths, no symbol/debug tables.
GO_BUILD := go build -trimpath -ldflags="-s -w"

.PHONY: build test test-browser vet fmt check install bench-smoke macapp snapshot clean

build:
	$(GO_BUILD) -o $(BINARY) .

test:
	go test ./...

test-browser:
	cd tests/browser && npm ci && npx playwright install chromium && npm test

vet:
	go vet ./...

fmt:
	gofmt -w .

check: fmt vet test

install:
	go install .

bench-smoke:
	./$(BINARY) bench --task read

# Build dist/Cody.app (requires Xcode command line tools).
macapp:
	./packaging/macos/build.sh

# Local GoReleaser dry run; requires goreleaser v2.
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -f $(BINARY)
