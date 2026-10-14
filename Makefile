GO        ?= go
GOBIN     ?= $(shell $(GO) env GOPATH)/bin
PKG       := github.com/XSpring/minipsi
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w -X $(PKG)/internal/version.Version=$(VERSION) \
             -X $(PKG)/internal/version.Commit=$(COMMIT) -X $(PKG)/internal/version.Date=$(DATE)

# Pinned dev tool versions
STATICCHECK_VERSION  := v0.8.1
GOVULNCHECK_VERSION  := v1.8.0
BUF_VERSION          := v1.73.0
PROTOC_GEN_GO        := v1.36.12
PROTOC_GEN_GO_GRPC   := v1.6.2
# staticcheck v0.8.1 cannot read Go 1.27 export data; run it under a 1.26 toolchain until
# a >= 2-week-old release supporting 1.27 exists (v0.8.2, released 2026-10-12).
STATICCHECK_GOTOOLCHAIN ?= go1.26.0

FUZZTIME ?= 10s

.PHONY: all build test race cover lint vuln fmt-check check fuzz-smoke bench proto proto-check tools e2e clean

all: check build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/minipsi ./cmd/minipsi

test:
	$(GO) test ./...

# -short trims the largest sizes (race instrumentation slows field/NTT code ~10x);
# `make test` runs the full sizes.
race:
	$(GO) test -race -short ./...

cover:
	$(GO) test -coverprofile=coverage.out ./... && $(GO) tool cover -func=coverage.out | tail -1

fmt-check:
	@out=$$(gofmt -l cmd internal); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: fmt-check
	$(GO) vet ./...
	GOTOOLCHAIN=$(STATICCHECK_GOTOOLCHAIN) $(GOBIN)/staticcheck ./...

vuln:
	$(GOBIN)/govulncheck ./...

check: lint race vuln

# Runs every Fuzz* target for $(FUZZTIME) each.
fuzz-smoke:
	@for pkg in $$($(GO) list ./...); do \
	  for f in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
	    echo "== $$pkg $$f"; $(GO) test -run '^$$' -fuzz "^$$f$$" -fuzztime $(FUZZTIME) $$pkg || exit 1; \
	  done; \
	done

bench:
	$(GO) test -run '^$$' -bench . -benchmem ./...

proto:
	PATH=$(GOBIN):$$PATH $(GOBIN)/buf lint
	PATH=$(GOBIN):$$PATH $(GOBIN)/buf generate

# Fails if the committed generated code differs from a fresh generation.
proto-check: proto
	git diff --exit-code -- internal/gen

tools:
	$(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO)
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC)

# End-to-end: shell smoke test against bin/minipsi, then the Go process-level suite.
e2e: build
	./scripts/e2e.sh
	$(GO) test -tags e2e -count 1 ./test/e2e

clean:
	rm -rf bin coverage.out
