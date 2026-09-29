APP := openpassd
VERSION ?= 0.1.1
GO ?= go
CGO_ENABLED ?= 0

.PHONY: all build test linux-amd64 linux-386 package packages checksums clean

all: build

build:
	$(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(APP) ./cmd/openpass

test:
	$(GO) test ./...

linux-amd64:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/$(APP)-linux-amd64 ./cmd/openpass

linux-386:
	mkdir -p dist
	GOOS=linux GOARCH=386 CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/$(APP)-linux-386 ./cmd/openpass

package:
	VERSION=$(VERSION) ARCH=amd64 sh ./scripts/package.sh

packages:
	VERSION=$(VERSION) ARCH=amd64 sh ./scripts/package.sh
	VERSION=$(VERSION) ARCH=386 sh ./scripts/package.sh
	$(MAKE) checksums

checksums:
	cd dist && sha256sum openpass-linux-amd64.tar.gz openpass-linux-386.tar.gz > SHA256SUMS

clean:
	rm -f $(APP)
	rm -rf dist
