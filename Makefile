BINARY := bin/pggo
VERSION ?= dev

.PHONY: run build test test-integration fmt vet docker

run:
	go run . -addr 127.0.0.1:8080

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) .

test:
	go test ./...

# 本地集成测试: make test-integration PGTEST_DSN='postgres://user:pass@127.0.0.1:5432/postgres'
test-integration:
	PGTEST_DSN="$(PGTEST_DSN)" go test -run TestIntegrationSession -v ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t pggo:$(VERSION) .
