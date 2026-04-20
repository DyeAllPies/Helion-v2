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

.PHONY: proto build build-rust build-all test test-short test-race lint clean \
        test-dashboard lint-dashboard coverage-go test-all lint-all \
        test-e2e test-e2e-headed test-e2e-ui bench check check-full verify-repo \
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

# test-race: run the Go test suite with the race detector (-race). The
# race detector requires CGO / a C compiler, which is often not
# installed on Windows developer machines. This target wraps
# `go test -race` in the same golang:1.26 Docker image CI uses, so
# every developer (regardless of platform) can catch data races before
# pushing. Mirrors the CI step `go test -race -count=1 ./...`.
test-race:
	./scripts/test-race.sh ./internal/... ./tests/integration/...

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

# check: local pre-push validation.
#
# Runs, in order:
#   - Go lint (vet + golangci-lint)
#   - Go tests (regular)
#   - Go tests with -race (inside Docker so Windows devs don't need a C
#     compiler). This is critical: data races that pass regular tests
#     will break CI, and that's exactly what happened once before.
#   - Go coverage gate (internal/ ≥ 85%, cmd/ ≥ 25%)
#   - Angular lint + tests + coverage gate
#   - Repo hygiene (go.sum verify, shell-script exec bits)
#
# Calls ng directly to avoid sub-make path issues on Windows.
#
# Angular coverage thresholds are enforced by scripts/check-dashboard-coverage.sh
# because @angular-devkit/build-angular:karma ignores karma.conf.js
# `check:` blocks.
check: lint test test-race coverage-go verify-repo
	cd dashboard && npx ng lint
	cd dashboard && npx ng test --watch=false --browsers=ChromeHeadless --code-coverage
	./scripts/check-dashboard-coverage.sh
	@echo ""
	@echo "==> All local checks passed (Go lint + test + race + coverage, Angular lint + test + coverage, repo hygiene)."
	@echo ""
	@echo "NOTE: E2E tests are NOT part of \`make check\` (they take ~3 min and need Docker)."
	@echo "If this change touches infrastructure (docker-compose*, Dockerfile*, .github/workflows,"
	@echo "scripts/run-e2e.sh, or anything the cluster startup depends on), run \`make check-full\`"
	@echo "before pushing — that adds the Playwright E2E suite."

# check-full: check + the full Playwright E2E suite. Use this before pushing
# changes that affect how the cluster boots or how the dashboard integrates
# with the coordinator (compose files, Dockerfiles, CI workflow, cluster env
# vars). The CI e2e job has historically been the first place such drift
# surfaces; running check-full locally eliminates the round-trip.
check-full: check test-e2e
	@echo ""
	@echo "==> All local checks passed, including Playwright E2E."

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

# Go coverage.
#
# Generates three coverage artifacts and enforces the same two-tier
# threshold CI applies:
#   - coverage.out / coverage.html — internal/ + tests/integration/ (≥ 85%)
#   - coverage-cmd.out             — cmd/ binary entry points (≥ 25%)
#
# Two tiers because cmd/ is dominated by main() I/O wiring that is
# covered end-to-end rather than per-unit. Keeping the same thresholds
# locally means a regression never reaches CI.
coverage-go:
	$(GO_ENV) CGO_ENABLED=0 go test -coverprofile=coverage.out -covermode=atomic \
	    ./internal/... ./tests/integration/...
	$(GO_ENV) go tool cover -func=coverage.out | tail -10
	$(GO_ENV) go tool cover -html=coverage.out -o coverage.html
	@echo "HTML report → coverage.html"
	@echo "==> Internal coverage threshold check (≥ 85 %)"
	@pct=$$($(GO_ENV) go tool cover -func=coverage.out | awk '/^total:/ { gsub(/%/,"",$$3); print $$3 }'); \
	  awk -v p="$$pct" 'BEGIN { if (p+0 < 85) { print "FAIL: internal/ coverage " p "% < 85%"; exit 1 } print "PASS: internal/ coverage " p "%" }'
	@echo "==> cmd/ coverage threshold check (≥ 25 %)"
	$(GO_ENV) CGO_ENABLED=0 go test -coverprofile=coverage-cmd.out -covermode=atomic ./cmd/... > /dev/null
	@pct=$$($(GO_ENV) go tool cover -func=coverage-cmd.out | awk '/^total:/ { gsub(/%/,"",$$3); print $$3 }'); \
	  awk -v p="$$pct" 'BEGIN { if (p+0 < 25) { print "FAIL: cmd/ coverage " p "% < 25%"; exit 1 } print "PASS: cmd/ coverage " p "%" }'

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
