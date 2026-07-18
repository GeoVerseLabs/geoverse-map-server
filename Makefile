VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)

.PHONY: build test vet run docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/geoverse ./cmd/geoverse

test:
	go test ./...

vet:
	go vet ./...

run: build
	./bin/geoverse -config config.example.yaml

docker:
	docker build -t geoverse-map-server:$(VERSION) .

clean:
	rm -rf bin
