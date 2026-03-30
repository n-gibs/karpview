.PHONY: build test lint vet govulncheck clean bench bench-update bench-compare size

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

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

## size: build stripped binary and report size vs 30 MB budget
size:
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o /tmp/karpview-size-check .
	@SIZE=$$(stat -f%z /tmp/karpview-size-check 2>/dev/null || stat -c%s /tmp/karpview-size-check); \
	 SIZE_MB=$$(echo "scale=2; $$SIZE / 1048576" | bc); \
	 echo "Binary size: $${SIZE_MB} MB (budget: 30 MB)"; \
	 test $$(echo "$$SIZE_MB > 30" | bc) -eq 0 || (echo "FAIL: exceeds 30 MB budget"; exit 1)
	@rm -f /tmp/karpview-size-check
