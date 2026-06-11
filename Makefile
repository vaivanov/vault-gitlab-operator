BINARY  := vault-gitlab-operator
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test cover lint fmt docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test -race ./...

cover:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run

fmt:
	gofmt -w .

docker:
	docker build -t $(BINARY):$(VERSION) .

clean:
	rm -rf bin coverage.out
