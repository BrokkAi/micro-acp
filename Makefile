.PHONY: build test vet check demo clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/micro-acp .

test:
	go test -race ./...

vet:
	go vet ./...

check: test vet

demo: build
	./bin/micro-acp --demo

clean:
	rm -rf bin
