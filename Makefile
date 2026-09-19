BINARY := cody

.PHONY: build test vet fmt check install bench-smoke snapshot clean

build:
	go build -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

check: fmt vet test

install:
	go install .

bench-smoke:
	./$(BINARY) bench --task read

# Local GoReleaser dry run; requires goreleaser v2.
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -f $(BINARY)
