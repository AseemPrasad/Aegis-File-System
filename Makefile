# ============================================================================
# Project Aegis — developer entrypoint (PROMPT 1.2)
# `make help` lists everything. CI mirrors these targets exactly.
# ============================================================================

SHELL      := powershell.exe
.DEFAULT_GOAL := help

COVERAGE_TARGET := 85
COMPOSE_FILE    := deploy/compose/docker-compose.yml
TF_DIR          := deploy/terraform

# ---------------------------------------------------------------- tool checks
.PHONY: verify-env
verify-env: ## Assert all toolchains exist at required versions
	python scripts/check_env.py

.PHONY: help
help: ## Show this help
	python -c "import re,sys; [print(f'{m:<22}{d}') for m,d in re.findall(r'^([a-zA-Z0-9_-]+):.*?##\s*(.*)$$', open('Makefile',encoding='utf-8').read(), re.M)]"

# ------------------------------------------------------------------- rust
.PHONY: tidy-rust fmt-rust lint-rust test-rust cover-rust bench-rust
tidy-rust: ## cargo fmt + fix trivial lints
	cargo fmt --all

fmt-rust: ## check formatting only
	cargo fmt --all -- --check

lint-rust: ## clippy with zero-warning gate
	cargo clippy --workspace --all-targets -- -D warnings

test-rust: ## unit + property tests
	cargo test --workspace

cover-rust: ## coverage gate >= $(COVERAGE_TARGET)%
	cargo llvm-cov --workspace --html --fail-under-lines $(COVERAGE_TARGET)

bench-rust: ## criterion benchmarks (release)
	cargo bench

# --------------------------------------------------------------------- go
.PHONY: tidy-go fmt-go vet-go lint-go test-go cover-go race-go
tidy-go: ## resolve module graph / regenerate go.sum
	go mod tidy

fmt-go: ## gofmt + goimports-style check via gofmt -l
	gofmt -l -w .

vet-go: ## go vet gate
	go vet ./...

lint-go: ## golangci-lint (config in .golangci.yml)
	golangci-lint run

test-go: ## unit tests
	go test ./...

race-go: ## race-detector pass (contract IC-3 concurrency safety)
	go test -race ./...

cover-go: ## coverage gate >= $(COVERAGE_TARGET)%
	go test -coverprofile=coverage.out ./...
	python scripts/coverage_gate.py coverage.out $(COVERAGE_TARGET)

# ---------------------------------------------------------- integration stack
.PHONY: compose-up compose-down compose-ps integration-test
compose-up: ## start postgres+redis+redpanda+minio+monitoring
	docker compose -f $(COMPOSE_FILE) up -d --wait

compose-down: ## stop and remove stack (-v wipes data volumes)
	docker compose -f $(COMPOSE_FILE) down -v

compose-ps:
	docker compose -f $(COMPOSE_FILE) ps

integration-test: compose-up ## run Go integration suite against real services
	go test -tags=integration -count=1 ./...
	$(MAKE) compose-down

# ---------------------------------------------------------------- terraform
.PHONY: tf-init tf-plan tf-apply
tf-init: TF_ENV?=staging
tf-init:
	terraform -chdir=$(TF_DIR) init -reconfigure

tf-plan: TF_ENV?=staging
tf-plan:
	terraform -chdir=$(TF_DIR) plan -var-file=../environments/$(TF_ENV).tfvars -out=tfplan.binary

tf-apply: TF_ENV?=staging
tf-apply:
	terraform -chdir=$(TF_DIR) apply tfplan.binary

# ------------------------------------------------------------------- security
.PHONY: security-audit
security-audit: ## local best-effort mirror of the CI security stage
	-cargo deny check
	-govulncheck ./...
	-trivy fs --scanners vuln,secret,misconfig --severity CRITICAL,HIGH .
	echo "full enforcement happens in CI (.github/workflows/ci.yml)"

.PHONY: secrets-scan
secrets-scan:
	gitleaks detect --redact 2>nul || echo "install gitleaks or rely on CI"
