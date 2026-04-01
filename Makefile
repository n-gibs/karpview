.PHONY: build test lint vet govulncheck clean bench bench-update bench-compare size release

FILE_VERSION := $(shell cat VERSION 2>/dev/null | tr -d '[:space:]')
VERSION := $(if $(FILE_VERSION),v$(FILE_VERSION),$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))

build:
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o karpview .

test:
	go test -race -cover ./...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

govulncheck:
	govulncheck ./...

clean:
	rm -f karpview

## bench: run benchmarks with benchmem (5 iterations for statistical stability)
bench:
	go test -bench=BenchmarkAnalyze -benchmem -count=5 ./internal/analyzer/

## bench-update: re-record the committed golden baseline (run on main only after a verified improvement)
bench-update:
	go test -bench=BenchmarkAnalyze -benchmem -count=5 ./internal/analyzer/ \
	  | tee internal/analyzer/testdata/bench-baseline.txt
	@echo "Baseline updated. Review and commit internal/analyzer/testdata/bench-baseline.txt."

## bench-compare: compare current run against committed baseline using benchstat
bench-compare:
	@which benchstat 2>/dev/null || go install golang.org/x/perf/cmd/benchstat@latest
	go test -bench=BenchmarkAnalyze -benchmem -count=5 ./internal/analyzer/ \
	  > /tmp/bench-local.txt
	benchstat internal/analyzer/testdata/bench-baseline.txt /tmp/bench-local.txt

## release: build darwin binary, upload GitHub release, print SHA256 for cask update
## Usage: make release VERSION=v0.1.0 [ARCH=arm64|amd64]
ARCH ?= arm64
RELEASE_TARBALL = karpview-$(VERSION)-darwin-$(ARCH).tar.gz
RELEASE_BINARY  = karpview-darwin-$(ARCH)

release:
	@echo "==> Building karpview $(VERSION) darwin/$(ARCH)"
	GOOS=darwin GOARCH=$(ARCH) go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(RELEASE_BINARY) .
	tar czf $(RELEASE_TARBALL) $(RELEASE_BINARY)
	@echo "==> SHA256 (paste into tap/Casks/karpview.rb):"
	@shasum -a 256 $(RELEASE_TARBALL) | awk '{print $$1}'
	@echo "==> Creating GitHub release $(VERSION)"
	gh release create $(VERSION) $(RELEASE_TARBALL) \
	  --title "$(VERSION)" \
	  --notes "karpview $(VERSION)"
	@rm -f $(RELEASE_BINARY) $(RELEASE_TARBALL)
	@echo "==> Done. Update tap/Casks/karpview.rb with the SHA256 above, then push your tap repo."

## size: build stripped binary and report size vs 50 MB budget
size:
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o /tmp/karpview-size-check .
	@SIZE=$$(stat -f%z /tmp/karpview-size-check 2>/dev/null || stat -c%s /tmp/karpview-size-check); \
	 SIZE_MB=$$(echo "scale=2; $$SIZE / 1048576" | bc); \
	 echo "Binary size: $${SIZE_MB} MB (budget: 30 MB)"; \
	 test $$(echo "$$SIZE_MB > 50" | bc) -eq 0 || (echo "FAIL: exceeds 50 MB budget"; exit 1)
	@rm -f /tmp/karpview-size-check
