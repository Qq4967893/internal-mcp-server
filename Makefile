.PHONY: build test vet fmt run

build:
	go build -o bin/internal-mcp-server ./cmd/server

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

run:
	go run ./cmd/server -config config/config.local.json
