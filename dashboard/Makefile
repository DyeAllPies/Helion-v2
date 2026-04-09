# dashboard/Makefile
#
# Mirrors the project-level Makefile conventions so `make lint`, `make test`,
# `make build` etc. work the same way as the Go side.
#
# Prerequisites: Node 20+, Angular CLI 18 (installed via npm ci)
#
# Quick reference:
#   make install    — npm ci (first-time setup)
#   make lint       — ESLint across all TS + HTML
#   make test       — unit tests, headless Chrome, single run
#   make test-watch — unit tests in watch mode (dev)
#   make build      — production build → dist/
#   make docker     — build Docker image tagged helion-dashboard:dev
#   make clean      — remove dist/, .angular/, coverage/

# ── Config ────────────────────────────────────────────────────────────────────
IMAGE_NAME  := helion-dashboard
IMAGE_TAG   := dev
NG          := npx ng
NODE_BIN    := node_modules/.bin

.PHONY: install lint test test-watch test-short build docker clean help

# ── Default target ─────────────────────────────────────────────────────────────
help:
	@echo ""
	@echo "  Helion Dashboard — make targets"
	@echo ""
	@echo "  install      npm ci (install dependencies)"
	@echo "  lint         ESLint (TS + HTML)"
	@echo "  test         unit tests, headless, single run + coverage"
	@echo "  test-watch   unit tests in watch mode"
	@echo "  test-short   unit tests, headless, no coverage (fast)"
	@echo "  build        ng build --configuration production"
	@echo "  docker       docker build -t $(IMAGE_NAME):$(IMAGE_TAG)"
	@echo "  clean        remove dist/ .angular/ coverage/"
	@echo ""

# ── Dependencies ──────────────────────────────────────────────────────────────
install:
	npm ci

# ── Lint ──────────────────────────────────────────────────────────────────────
# Matches `make lint` on the Go side (go vet ./...).
# Runs ESLint via the Angular CLI lint builder (respects .eslintrc.json).
lint: node_modules
	$(NG) lint

# ── Tests ─────────────────────────────────────────────────────────────────────
# `make test` — full run with coverage, mirrors `go test ./internal/...`
test: node_modules
	$(NG) test \
		--watch=false \
		--browsers=ChromeHeadless \
		--code-coverage

# `make test-watch` — dev mode, re-runs on file change
test-watch: node_modules
	$(NG) test --browsers=Chrome

# `make test-short` — fast, no coverage, mirrors `go test -short`
test-short: node_modules
	$(NG) test \
		--watch=false \
		--browsers=ChromeHeadless \
		--no-code-coverage

# ── Build ─────────────────────────────────────────────────────────────────────
build: node_modules
	$(NG) build --configuration production

# ── Docker ────────────────────────────────────────────────────────────────────
docker:
	docker build \
		--tag $(IMAGE_NAME):$(IMAGE_TAG) \
		--tag $(IMAGE_NAME):$(shell git rev-parse --short HEAD 2>/dev/null || echo local) \
		.

# ── Helpers ───────────────────────────────────────────────────────────────────
# Ensure node_modules exist before any target that needs them.
# Avoids cryptic "ng: not found" errors.
node_modules:
	@if [ ! -d node_modules ]; then \
		echo "node_modules not found — run: make install"; \
		exit 1; \
	fi

# ── Clean ─────────────────────────────────────────────────────────────────────
clean:
	rm -rf dist/ .angular/ coverage/
