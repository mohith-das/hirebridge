.PHONY: build run test vet clean dev

BUILD_TAGS = sqlite_fts5 sqlite_load_extension
BINARY    ?= hirebridge

build:
	CGO_ENABLED=1 go build -tags "$(BUILD_TAGS)" -ldflags="-s -w" -o $(BINARY) ./cmd/hirebridge

run: build
	./$(BINARY)

dev:
	HB_DB_PATH=data/hirebridge.db \
	HB_LISTEN=:8080 \
	CGO_ENABLED=1 go run -tags "$(BUILD_TAGS)" ./cmd/hirebridge

test:
	CGO_ENABLED=1 go test -tags "$(BUILD_TAGS)" ./...

vet:
	go vet -tags "$(BUILD_TAGS)" ./...

clean:
	rm -f $(BINARY)
	rm -rf data/
