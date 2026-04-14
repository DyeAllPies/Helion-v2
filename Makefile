PROTO_DIR   := proto
PROTO_FILES := coordinator.proto node.proto runtime.proto

# Go workspace mode is disabled for every target because:
# 1. This repo is a single module — `go.work` adds no value.
# 2. An untracked `go.work` plus accumulated `go.work.sum` entries
#    pull in pre-split genproto, which conflicts with the post-split
#    override in go.mod and produces an "ambiguous import" error.
# 3. CI does not have a go.work (it's untracked), so disabling it
#    locally matches CI's behaviour and prevents drift.
#
# Individual targets prepend this to their go invocations; leaving it
# unset here means running `go build` manually in a shell won't be
# affected (developers who want workspace mode keep that option).
GO_ENV := GOWORK=off

.PHONY: proto build build-rust build-all test test-short lint clean \
        test-dashboard lint-dashboard coverage-go test-all lint-all \
        test-e2e test-e2e-headed test-e2e-ui bench check verify-repo \
        docker-smoke

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
	$(GO_ENV) go build ./...

test:
	$(GO_ENV) CGO_ENABLED=0 go test ./internal/... ./tests/integration/...

test-short:
	$(GO_ENV) CGO_ENABLED=0 go test -short ./internal/...

lint:
	$(GO_ENV) go vet ./...
	$(GO_ENV) $$(go env GOPATH)/bin/golangci-lint run --timeout=5m

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
# Runs Go lint + tests + coverage, then Angular lint + tests + coverage,
# plus repo-hygiene guards that catch CI-only failures (missing go.sum
# entries that the Docker build catches, shell scripts without the exec
# bit, stale module hashes).
#
# Calls ng directly to avoid sub-make path issues on Windows.
#
# Angular coverage thresholds are enforced by scripts/check-dashboard-coverage.sh,
# which parses the generated HTML. The Angular test builder
# (@angular-devkit/build-angular:karma) ignores karma.conf.js `check:` blocks,
# so external enforcement is required.
check: lint test coverage-go verify-repo
	cd dashboard && npx ng lint
	cd dashboard && npx ng test --watch=false --browsers=ChromeHeadless --code-coverage
	./scripts/check-dashboard-coverage.sh
	@echo ""
	@echo "==> All local checks passed (Go lint + test + coverage, Angular lint + test + coverage, repo hygiene)."

# verify-repo: `go mod verify` + exec-bit checks on tracked shell
# scripts. Cheap, part of the default `make check`.
verify-repo:
	./scripts/verify-repo-hygiene.sh

# docker-smoke: build the coordinator container image. This catches
# CI-only failures such as missing go.sum entries that a pristine
# Docker module cache would hit (Dockerfile runs `go mod download`
# against go.sum only, then `go build` — a transitive dep not in
# go.sum fails the build). Run before pushing a dep change.
#
# Uses Docker's default layer cache so the build matches CI's behaviour
# (CI uses `cache-from: type=gha`). For a fully pristine repro, add
# `--no-cache --pull` to the docker build command manually.
docker-smoke:
	@echo "==> Building Dockerfile.coordinator..."
	docker build -f Dockerfile.coordinator -t helion-coordinator:smoke .
	@echo "==> Smoke build passed."

build-all: build build-rust

# Go coverage: generates coverage.out + coverage.html, enforced in CI at 25 %
coverage-go:
	$(GO_ENV) CGO_ENABLED=0 go test -coverprofile=coverage.out -covermode=atomic \
	    ./internal/... ./tests/integration/...
	$(GO_ENV) go tool cover -func=coverage.out | tail -10
	$(GO_ENV) go tool cover -html=coverage.out -o coverage.html
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
