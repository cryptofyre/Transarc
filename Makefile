.PHONY: test build

test:
	go test ./...

build:
	go build -trimpath -o bin/transarc ./cmd/transarc
