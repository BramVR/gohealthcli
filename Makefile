.PHONY: build test vet check

# Local test gate: vet then the behavior-focused command contract tests.
check: vet test

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...
