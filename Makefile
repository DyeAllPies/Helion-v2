PROTO_DIR   := proto
PROTO_FILES := coordinator.proto node.proto runtime.proto

.PHONY: proto build build-rust build-all test test-short lint clean \
        test-dashboard lint-dashboard coverage-go test-all lint-all \
        test-e2e test-e2e-headed test-e2e-ui bench check

# ── protobuf ──────────────────────────────────────────────────────────────────

proto:
	protoc \
		--go_out=$(PROTO_DIR) \
		--go_opt=paths=source_relative \
		--go-grpc_out=$(PROTO_DIR) \
		--go-grpc_opt=paths=source_relative \
		--proto_path=$(PROTO_DIR) \
		$(PROTO_FILES)

# ── Go ────────────────────────────────────────────────────────────────────────

build:
	go build ./...

test:
	CGO_ENABLED=0 go test ./internal/... ./tests/integration/...

test-short:
	CGO_ENABLED=0 go test -short ./internal/...

lint:
	go vet ./...
	$$(go env GOPATH)/bin/golangci-lint run --timeout=5m

# ── Rust ──────────────────────────────────────────────────────────────────────

build-rust:
	cargo build --release --manifest-path runtime-rust/Cargo.toml

build-rust-debug:
	cargo build --manifest-path runtime-rust/Cargo.toml

test-rust:
	cargo test --manifest-path runtime-rust/Cargo.toml

lint-rust:
	cargo clippy --manifest-path runtime-rust/Cargo.toml -- -D warnings

# ── Dashboard ─────────────────────────────────────────────────────────────────

test-dashboard:
	cd dashboard && $(MAKE) test

lint-dashboard:
	cd dashboard && $(MAKE) lint

test-e2e:
	./scripts/run-e2e.sh

test-e2e-headed:
	./scripts/run-e2e.sh --headed

test-e2e-ui:
	./scripts/run-e2e.sh --ui

bench:
	./scripts/run-bench.sh

# ── combined ──────────────────────────────────────────────────────────────────

# check: local pre-push validation (no Docker, no Rust, no E2E).
# Runs Go lint + tests + coverage, then Angular lint + tests.
# Calls ng directly to avoid sub-make path issues on Windows.
check: lint test coverage-go
	cd dashboard && npx ng lint
	cd dashboard && npx ng test --watch=false --browsers=ChromeHeadless --code-coverage
	@echo ""
	@echo "==> All local checks passed (Go lint + test + coverage, Angular lint + test)."

build-all: build build-rust

# Go coverage: generates coverage.out + coverage.html, enforced in CI at 25 %
coverage-go:
	CGO_ENABLED=0 go test -coverprofile=coverage.out -covermode=atomic \
	    ./internal/... ./tests/integration/...
	go tool cover -func=coverage.out | tail -10
	go tool cover -html=coverage.out -o coverage.html
	@echo "HTML report → coverage.html"

test-all: test test-rust test-dashboard test-e2e
	@echo ""
	@echo "==> All test suites passed (Go + Rust + Angular + E2E)."

lint-all: lint lint-rust lint-dashboard
	@echo ""
	@echo "==> All lint checks passed."

# ── clean ─────────────────────────────────────────────────────────────────────

clean:
	rm -f $(PROTO_DIR)/*.pb.go
	rm -f $(PROTO_DIR)/*_grpc.pb.go
	cargo clean --manifest-path runtime-rust/Cargo.toml 2>/dev/null || true
