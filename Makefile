.PHONY: build test test-integration lint vet fmt mod-tidy docker clean

BINARY := seaweedfs-provisioner

build:
	go build -o $(BINARY) ./cmd/seaweedfs-provisioner

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration -v ./...

lint:
	go tool golangci-lint run

vet:
	go vet ./...

fmt:
	go fmt ./...

mod-tidy:
	go mod tidy

docker:
	docker build -t $(BINARY) .

clean:
	rm -f $(BINARY)
