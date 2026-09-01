# corvid-go — developer entry points.
#
# The engine artifacts are NOT vendored: `make deps` fetches the pinned
# release (fetch.sh / fetch.ps1), sha256-verifies it against the release's
# checksums.txt, and normalizes corvid.h + the cdylib into deps/current,
# which the package's #cgo flags point at. After `make deps`,
# `go build ./...` and `go test ./...` work offline.

GO ?= go

.PHONY: deps build test vet lint clean

deps:            ## fetch + verify the pinned engine artifacts into deps/
	./fetch.sh

build:           ## compile the package (requires deps)
	$(GO) build ./...

test:            ## run the golden suite (requires deps)
	$(GO) test ./...

vet:             ## go vet (requires deps)
	$(GO) vet ./...

lint:            ## golangci-lint when installed, else go vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || $(GO) vet ./...

clean:           ## drop fetched artifacts and test caches
	rm -rf deps
	$(GO) clean -testcache
