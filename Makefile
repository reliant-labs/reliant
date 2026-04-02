
# Build metadata
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build -buildvcs=false
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test -buildvcs=false
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=$(GOCMD) fmt
GOVET=$(GOCMD) vet

# Build parameters
BINARY_NAME=reliant
BINARY_PATH=dist/$(BINARY_NAME)
BUILD_DIR=dist
MAIN_PACKAGE=./cmd/reliant/
PKG=github.com/reliant-labs/reliant/internal/version

# Go build flags
LDFLAGS=-s -w \
	-X $(PKG).Version=$(VERSION) \
	-X $(PKG).Commit=$(COMMIT) \
	-X $(PKG).Date=$(DATE) \
	-X $(PKG).Branch=$(BRANCH)

# CGO settings
CGO_ENABLED ?= 0

# Test parameters
TEST_TIMEOUT=300s
BENCH_TIMEOUT=60s

# Colors for output
GREEN := \033[0;32m
YELLOW := \033[0;33m
RED := \033[0;31m
BLUE := \033[0;34m
NC := \033[0m # No Color

MINTLIFY_DOCS_DIR := docs
MINTLIFY_PORT ?= 3000

.PHONY: all build build-all clean test test-race test-coverage test-ci deps fmt vet lint security help generate generate-cli generate-tools-ref generate-shortcuts generate-nodes generate-types generate-presets generate-workflow-builder-preset generate-mintlify-reference generate-changelog docs docs-build mint changelog changelog-draft postgres-up postgres-down db-driver-audit verify-yaml-bindings build-api-server build-temporal-worker build-tools-daemon build-services docker-build

# Default target
all: deps fmt vet test build

## build: Build reliant with version info
build: generate-all
	@echo "$(YELLOW)Building $(BINARY_NAME)...$(NC)"
	@echo "Version: $(VERSION)"
	@echo "Commit:  $(COMMIT)"
	@echo "Date:    $(DATE)"
	@echo "Branch:  $(BRANCH)"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BINARY_PATH) $(MAIN_PACKAGE)
	@echo "$(GREEN)✅ Build complete: $(BINARY_PATH)$(NC)"


## build-all: Build for multiple platforms
build-all: generate-all
	@echo "$(YELLOW)Building for multiple platforms...$(NC)"
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GOBUILD) -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-linux-amd64 $(MAIN_PACKAGE)
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 $(GOBUILD) -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-darwin-amd64 $(MAIN_PACKAGE)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GOBUILD) -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-darwin-arm64 $(MAIN_PACKAGE)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GOBUILD) -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PACKAGE)
	@echo "$(GREEN)✅ Multi-platform build complete$(NC)"

# ========================================
# Microservice Builds
# ========================================

## build-api-server: Build the standalone API server (unified binary)
build-api-server:
	@echo "$(YELLOW)Building reliant (api-server)...$(NC)"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/reliant ./cmd/reliant/
	@echo "$(GREEN)✅ reliant built: $(BUILD_DIR)/reliant$(NC)"

## build-temporal-worker: Build the standalone Temporal worker (unified binary)
build-temporal-worker:
	@echo "$(YELLOW)Building reliant (temporal-worker)...$(NC)"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/reliant ./cmd/reliant/
	@echo "$(GREEN)✅ reliant built: $(BUILD_DIR)/reliant$(NC)"

## build-tools-daemon: Build the standalone tools daemon (unified binary)
build-tools-daemon:
	@echo "$(YELLOW)Building reliant (tools-daemon)...$(NC)"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/reliant ./cmd/reliant/
	@echo "$(GREEN)✅ reliant built: $(BUILD_DIR)/reliant$(NC)"

## build-services: Build all microservice binaries
build-services: build-api-server build-temporal-worker build-tools-daemon
	@echo "$(GREEN)✅ All service binaries built$(NC)"

# ========================================
# Docker
# ========================================

DOCKER_REGISTRY ?= reliant
DOCKER_TAG ?= latest

## docker-build: Build the Reliant Docker image locally (for dev/testing)
docker-build:
	@echo "$(YELLOW)Building Docker image...$(NC)"
	docker build -t $(DOCKER_REGISTRY)/reliant:$(DOCKER_TAG) .
	@echo "$(GREEN)✅ Docker image built: $(DOCKER_REGISTRY)/reliant:$(DOCKER_TAG)$(NC)"
	@echo "$(YELLOW)Run with: docker run $(DOCKER_REGISTRY)/reliant:$(DOCKER_TAG) <command>$(NC)"
	@echo "$(YELLOW)  e.g. docker run $(DOCKER_REGISTRY)/reliant:$(DOCKER_TAG) server api$(NC)"

## test: Run all tests
test: schema-generate
	@echo "$(YELLOW)Running tests...$(NC)"
	@$(MAKE) stop 2>/dev/null || true
	$(GOTEST) -v -timeout=$(TEST_TIMEOUT) ./...
	@$(MAKE) stop 2>/dev/null || true
	@echo "$(GREEN)✅ Tests complete$(NC)"

## test-race: Run tests with race detection
test-race: schema-generate
	@echo "$(YELLOW)Running tests with race detection...$(NC)"
	$(GOTEST) -v -race -timeout=$(TEST_TIMEOUT) ./...
	@echo "$(GREEN)✅ Race tests complete$(NC)"

## test-coverage: Run tests with coverage
test-coverage:
	@echo "$(YELLOW)Running tests with coverage...$(NC)"
	$(GOTEST) -v -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)✅ Coverage report generated: coverage.html$(NC)"


## fmt: Format Go code
fmt:
	@echo "$(YELLOW)Formatting code...$(NC)"
	$(GOFMT) ./...
	@echo "$(GREEN)✅ Formatting complete$(NC)"

## vet: Run go vet
vet:
	@echo "$(YELLOW)Running go vet...$(NC)"
	$(GOVET) ./...
	@echo "$(GREEN)✅ Vet complete$(NC)"

## lint: Run golangci-lint
lint:
	@echo "$(YELLOW)Running golangci-lint...$(NC)"
	@if command -v golangci-lint >/dev/null 2>&1; then \
		GOFLAGS="-buildvcs=false" golangci-lint run; \
	else \
		echo "$(RED)golangci-lint not installed. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest$(NC)"; \
		exit 1; \
	fi
	@echo "$(GREEN)✅ Linting complete$(NC)"

## security: Run security checks with gosec and govulncheck
security:
	@echo "$(YELLOW)Running security checks...$(NC)"
	@if command -v gosec >/dev/null 2>&1; then \
		gosec ./...; \
	else \
		echo "Installing gosec..."; \
		go install github.com/securego/gosec/v2/cmd/gosec@latest; \
		gosec ./...; \
	fi
	@echo "Running vulnerability check..."
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./...; \
	else \
		echo "Installing govulncheck..."; \
		go install golang.org/x/vuln/cmd/govulncheck@latest; \
		govulncheck ./...; \
	fi
	@echo "$(GREEN)✅ Security scan complete$(NC)"

## deps: Download and tidy dependencies
deps:
	@echo "$(YELLOW)Downloading dependencies...$(NC)"
	$(GOMOD) download
	$(GOMOD) tidy
	@echo "$(GREEN)✅ Dependencies updated$(NC)"

## proto-generate: Generate Go and TypeScript code from protobuf definitions
proto-generate:
	@echo "$(YELLOW)Generating code from protobuf definitions...$(NC)"
	@PATH="$(shell pwd)/web/node_modules/.bin:$(PATH)" buf generate
	@echo "$(GREEN)✅ Protobuf code generated$(NC)"

## proto-generate-go: Generate only Go code from protobuf definitions
proto-generate-go:
	@echo "$(YELLOW)Generating Go code from protobuf definitions...$(NC)"
	@buf generate --template buf.gen-go-only.yaml
	@echo "$(GREEN)✅ Go protobuf code generated$(NC)"

## proto-lint: Lint protobuf files
proto-lint:
	@echo "$(YELLOW)Linting protobuf files...$(NC)"
	@buf lint
	@echo "$(GREEN)✅ Protobuf lint complete$(NC)"

## proto-format: Format protobuf files
proto-format:
	@echo "$(YELLOW)Formatting protobuf files...$(NC)"
	@buf format -w
	@echo "$(GREEN)✅ Protobuf formatting complete$(NC)"

## schema-generate: Generate schema.sql from migrations
schema-generate:
	@echo "$(YELLOW)Generating schema.sql from migrations...$(NC)"
	@bash ./scripts/generate-schema.sh
	@echo "$(GREEN)✅ Schema generated$(NC)"

## schema-validate: Validate schema.sql is in sync with migrations
schema-validate:
	@echo "$(YELLOW)Validating schema.sql...$(NC)"
	@./scripts/validate-schema.sh
	@echo "$(GREEN)✅ Schema is in sync$(NC)"

## db-driver-audit: Static dual-driver audit (SQLite/Postgres parity and bindQuery checks)
db-driver-audit:
	@echo "$(YELLOW)Running DB driver audit...$(NC)"
	@./scripts/db-driver-audit.sh

## sqlc: Generate database code with sqlc
sqlc:
	@echo "$(YELLOW)Generating database code with sqlc...$(NC)"
	@if command -v sqlc >/dev/null 2>&1; then \
		sqlc generate; \
	elif [ -f "$(HOME)/go/bin/sqlc" ]; then \
		$(HOME)/go/bin/sqlc generate; \
	else \
		echo "$(RED)sqlc not found. Installing...$(NC)"; \
		$(GOCMD) install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0; \
		$(HOME)/go/bin/sqlc generate; \
	fi
	@echo "$(GREEN)✅ Database code generated$(NC)"

## db-regenerate: Regenerate schema.sql and sqlc code (run after migration changes)
db-regenerate: schema-generate sqlc
	@echo "$(GREEN)✅ Database schema and code regenerated$(NC)"
	@echo "$(BLUE)You can now write Go code using the new schema types$(NC)"

## generate-all: Run all code generators (protobuf, sqlc, docs/presets)
generate-all: proto-generate generate
	@echo "$(GREEN)✅ All code generation complete$(NC)"

## generate-go: Run Go code generators only (protobuf Go + sqlc + Go reference files)
generate-go: proto-generate-go schema-generate sqlc generate-schema generate-refcheck generate-cel-reference generate-nodes
	@echo "$(GREEN)✅ Go code generation complete$(NC)"

## verify-yaml-bindings: Ensure descriptor-generated YAML bindings are up to date
verify-yaml-bindings: proto-generate-go
	@echo "$(YELLOW)Verifying YAML bindings are up to date...$(NC)"
	@$(GOCMD) generate ./internal/workflow/yaml
	@git diff --exit-code -- internal/workflow/yaml/bindings_generated.go
	@echo "$(GREEN)✅ YAML bindings verified$(NC)"

## migration: Create a new migration file (Usage: make migration NAME=my_changes)
migration:
	@if [ -z "$(NAME)" ]; then \
		echo "$(RED)Error: NAME is required. Usage: make migration NAME=my_changes$(NC)"; \
		exit 1; \
	fi
	@echo "$(YELLOW)Creating migration: $(NAME)$(NC)"
	@TIMESTAMP=$$(date +%Y%m%d%H%M%S); \
	LATEST=$$(ls -1 internal/db/migrations/sqlite/*.sql 2>/dev/null | xargs -I{} basename {} | grep -oE '^[0-9]+' | sort -rn | head -1 || echo "0"); \
	if [ "$$TIMESTAMP" -le "$$LATEST" ]; then \
		TIMESTAMP=$$((LATEST + 10000)); \
		echo "$(YELLOW)⚠️  Timestamp collision detected, bumped to $$TIMESTAMP$(NC)"; \
	fi; \
	FILE="internal/db/migrations/sqlite/$${TIMESTAMP}_$(NAME).sql"; \
	printf -- '-- +goose Up\n\n-- +goose Down\n' > "$$FILE"; \
	echo "$(GREEN)✅ Migration created: $$FILE$(NC)"
	@echo "$(YELLOW)Next steps:$(NC)"
	@echo "  1. Edit the migration file"
	@echo "  2. If schema changes affect Postgres, add matching migration in internal/db/migrations/postgres/"
	@echo "  3. Run: make db-regenerate"
	@echo "  4. Write your Go code using the new types"

## postgres-up: Start local Postgres container for DATABASE_DRIVER=postgres dev
postgres-up:
	@echo "$(YELLOW)Starting local Postgres via docker-compose.yml...$(NC)"
	docker compose up -d postgres
	@echo "$(GREEN)✅ Postgres started on localhost:5433 (use per-worktree DB names in dev)$(NC)"

## postgres-down: Stop local Postgres container
postgres-down:
	@echo "$(YELLOW)Stopping local Postgres...$(NC)"
	docker compose stop postgres
	@echo "$(GREEN)✅ Postgres stopped$(NC)"

# ========================================
# Documentation Generation
# ========================================

DOCS_DIR=generated/docs-source
REFERENCE_DIR=$(DOCS_DIR)/reference
WORKFLOWS_DIR=$(DOCS_DIR)/workflows
SETTINGS_DIR=$(DOCS_DIR)/settings
TOOLS_DIR=internal/llm/tools
PRESETS_DIR=internal/workflow/builtin/presets
CONFIG_DIR=config
WEB_SRC_DIR=web/src
CHANGELOG_DIR=$(MINTLIFY_DOCS_DIR)/data/releases

## generate: Generate all docs and presets (run during build)
generate: verify-yaml-bindings schema-generate sqlc generate-schema generate-scenario-schema generate-refcheck generate-cel-reference generate-cli generate-tools-ref generate-shortcuts generate-nodes generate-types generate-models generate-presets generate-workflow-builder-preset generate-mintlify-reference generate-changelog
	@echo "$(GREEN)✅ All generated files up to date$(NC)"

## generate-schema: Generate workflow schema reference from proto types
generate-schema:
	@echo "$(YELLOW)Generating workflow schema reference...$(NC)"
	@$(GOCMD) run ./tools/docgen/schema/... $(REFERENCE_DIR)/workflow-schema.md
	@echo "$(GREEN)✅ Workflow schema reference generated$(NC)"

## generate-scenario-schema: Generate scenario schema reference from simulator types
generate-scenario-schema:
	@echo "$(YELLOW)Generating scenario schema reference...$(NC)"
	@$(GOCMD) run ./tools/docgen/scenarios/... internal/workflow/runtime/simulator $(REFERENCE_DIR)/scenario-schema.md
	@echo "$(GREEN)✅ Scenario schema reference generated$(NC)"

## generate-refcheck: Validate reference data from proto descriptors
generate-refcheck:
	@echo "$(YELLOW)Validating reference data...$(NC)"
	@$(GOCMD) run ./tools/docgen/refcheck/...
	@echo "$(GREEN)✅ Reference data validated$(NC)"

## generate-cel-reference: Generate CEL reference for discovery tools
generate-cel-reference:
	@echo "$(YELLOW)Generating CEL reference...$(NC)"
	@$(GOCMD) run ./tools/docgen/celref/...
	@echo "$(GREEN)✅ CEL reference generated$(NC)"


## generate-tools-ref: Generate tool reference documentation (uses registry directly)
generate-tools-ref:
	@echo "$(YELLOW)Generating tool reference...$(NC)"
	@$(GOCMD) run ./tools/docgen/tools/... $(REFERENCE_DIR)/tools.md
	@echo "$(GREEN)✅ Tool reference generated$(NC)"

## generate-shortcuts: Generate keyboard shortcuts from YAML source of truth
generate-shortcuts:
	@echo "$(YELLOW)Generating keyboard shortcuts...$(NC)"
	@$(GOCMD) run ./tools/docgen/shortcuts/... \
		$(CONFIG_DIR)/shortcuts.yaml \
		$(WEB_SRC_DIR)/store/shortcutsData.generated.ts \
		$(SETTINGS_DIR)/keyboard-shortcuts.generated.md
	@echo "$(GREEN)✅ Keyboard shortcuts generated$(NC)"

## generate-nodes: Generate node type I/O reference from node type registry
generate-nodes:
	@echo "$(YELLOW)Generating node types reference...$(NC)"
	@$(GOCMD) run ./tools/docgen/nodes/... $(REFERENCE_DIR)/nodes.md
	@echo "$(GREEN)✅ Node types reference generated$(NC)"

## generate-types: Generate types reference documentation
generate-types:
	@echo "$(YELLOW)Generating types reference...$(NC)"
	@$(GOCMD) run ./tools/docgen/types/... $(REFERENCE_DIR)/types.md
	@echo "$(GREEN)✅ Types reference generated$(NC)"

## generate-models: Generate models reference from registry
generate-models:
	@echo "$(YELLOW)Generating models reference...$(NC)"
	@$(GOCMD) run ./tools/docgen/models/... $(REFERENCE_DIR)/models.md
	@echo "$(GREEN)✅ Models reference generated$(NC)"

## generate-cli: Generate CLI reference from Cobra command tree
generate-cli:
	@echo "$(YELLOW)Generating CLI reference...$(NC)"
	@$(GOCMD) run ./tools/docgen/cli/... $(REFERENCE_DIR)/cli.md
	@echo "$(GREEN)✅ CLI reference generated$(NC)"

## generate-mintlify-reference: Generate Mintlify-safe reference docs from generated reference markdown
generate-mintlify-reference: verify-yaml-bindings schema-generate sqlc generate-schema generate-scenario-schema generate-cli generate-tools-ref generate-nodes generate-types generate-models
	@echo "$(YELLOW)Generating Mintlify reference docs...$(NC)"
	@python3 ./scripts/generate-mintlify-reference.py
	@echo "$(GREEN)✅ Mintlify reference docs generated$(NC)"

## generate-presets: Generate preset reference from YAML files
generate-presets:
	@echo "$(YELLOW)Generating presets reference...$(NC)"
	@$(GOCMD) run ./tools/docgen/presets/... $(WORKFLOWS_DIR)/presets-reference.generated.md
	@echo "$(GREEN)✅ Presets reference generated$(NC)"

## generate-changelog: Generate Mintlify changelog page from YAML release files
generate-changelog:
	@echo "$(YELLOW)Generating Mintlify changelog...$(NC)"
	@$(GOCMD) run ./tools/docgen/changelog/... $(CHANGELOG_DIR) $(MINTLIFY_DOCS_DIR)/changelog.mdx
	@echo "$(GREEN)✅ Mintlify changelog generated$(NC)"

## generate-workflow-builder-preset: Generate workflow_builder.yaml with embedded docs
generate-workflow-builder-preset: generate-schema generate-scenario-schema
	@echo "$(YELLOW)Generating workflow builder preset...$(NC)"
	@$(GOCMD) run ./tools/docgen/assembler/... $(DOCS_DIR) $(PRESETS_DIR)/workflow_builder.yaml
	@echo "$(GREEN)✅ Workflow builder preset generated$(NC)"

## clean: Clean build artifacts
clean:
	@echo "$(YELLOW)Cleaning...$(NC)"
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)/ dist/ reports/ artifacts/ *.log coverage.out coverage.html
	@echo "$(GREEN)✅ Clean complete$(NC)"

## stop-legacy: Stop all running test servers and processes (legacy)
stop-legacy:
	@echo "$(YELLOW)Stopping test servers...$(NC)"
	@pkill -f "reliant api" 2>/dev/null || true
	@pkill -f "api-server" 2>/dev/null || true
	@pkill -f "go run.*cmd/api" 2>/dev/null || true
	@pkill -f "vite" 2>/dev/null || true
	@pkill -f "npm run dev" 2>/dev/null || true
	@lsof -ti:8080 | xargs kill -9 2>/dev/null || true
	@lsof -ti:5173 | xargs kill -9 2>/dev/null || true
	@lsof -ti:5176 | xargs kill -9 2>/dev/null || true
	@lsof -ti:4173 | xargs kill -9 2>/dev/null || true
	@echo "$(GREEN)✅ All test servers stopped$(NC)"

## install: Install reliant
install:
	@echo "$(YELLOW)Installing $(BINARY_NAME)...$(NC)"
	$(GOCMD) install -ldflags="$(LDFLAGS)" $(MAIN_PACKAGE)
	@echo "$(GREEN)✅ $(BINARY_NAME) installed$(NC)"

## dev-build: Development workflow (format, vet, test, build)
dev-build: generate-all fmt vet test build
	@echo "$(GREEN)✅ Development build complete$(NC)"

## ci: Continuous integration workflow
ci: deps generate-all fmt vet schema-validate db-driver-audit test-race security
	@echo "$(GREEN)✅ CI checks complete$(NC)"

## check: Run all checks (format, vet, lint, test)
check: fmt vet lint test
	@echo "$(GREEN)✅ All checks passed$(NC)"

## version: Show version information
version:
	@echo "Version: $(VERSION)"
	@echo "Commit:  $(COMMIT)"
	@echo "Date:    $(DATE)"
	@echo "Branch:  $(BRANCH)"

## release-rc: Release new release candidate (0.0.1-rc38 → 0.0.1-rc39)
release-rc:
	@echo "$(YELLOW)Creating new release candidate...$(NC)"
	@./scripts/release.sh prerelease

## release-patch: Release patch version (0.0.1-rc38 → 0.0.1 or 0.0.1 → 0.0.2)
release-patch:
	@echo "$(YELLOW)Creating patch release...$(NC)"
	@./scripts/release.sh patch

## release-minor: Release minor version (0.0.1 → 0.1.0)
release-minor:
	@echo "$(YELLOW)Creating minor release...$(NC)"
	@./scripts/release.sh minor

## release-major: Release major version (0.1.0 → 1.0.0)
release-major:
	@echo "$(YELLOW)Creating major release...$(NC)"
	@./scripts/release.sh major

## info: Show build information
info:
	@echo "$(BLUE)Build Information:$(NC)"
	@echo "  Binary:     $(BINARY_NAME)"
	@echo "  Package:    $(MAIN_PACKAGE)"
	@echo "  Build Dir:  $(BUILD_DIR)"
	@echo "  Version:    $(VERSION)"
	@echo "  Commit:     $(COMMIT)"
	@echo "  Date:       $(DATE)"
	@echo "  Branch:     $(BRANCH)"
	@echo "  LDFLAGS:    $(LDFLAGS)"
	@echo "  CGO:        $(CGO_ENABLED)"

## tools: Install development tools
tools:
	@echo "$(YELLOW)Installing development tools...$(NC)"
	$(GOCMD) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(GOCMD) install github.com/securego/gosec/v2/cmd/gosec@latest
	$(GOCMD) install golang.org/x/vuln/cmd/govulncheck@latest
	@echo "$(GREEN)✅ Tools installed$(NC)"

## help: Show this help message
help:
	@echo ""
	@echo "$(BLUE)Reliant$(NC)"
	@echo "Available targets:"
	@echo ""
	@echo "$(YELLOW)📦 Release Commands:$(NC)"
	@echo "  release-rc            - Create release candidate (0.0.1-rc38 → 0.0.1-rc39)"
	@echo "  release-patch         - Create patch release (0.0.1-rc38 → 0.0.1 or 0.0.1 → 0.0.2)"
	@echo "  release-minor         - Create minor release (0.0.1 → 0.1.0)"
	@echo "  release-major         - Create major release (0.1.0 → 1.0.0)"
	@echo ""
	@echo "$(YELLOW)🔧 Development:$(NC)"
	@grep -E '^## [a-zA-Z_-]+:' $(MAKEFILE_LIST) | grep -v release | sed 's/## /  /' | sed 's/: / - /'
	@echo ""

# Show help by default
.DEFAULT_GOAL := help

# Web-specific targets
install-web: ## Install web dependencies
	cd web && npm ci

test-web: ## Run web unit tests
	cd web && npm test -- --run

test-web-watch: ## Run web unit tests in watch mode
	cd web && npm test

test-web-coverage: ## Run web unit tests with coverage
	cd web && npm test -- --run --coverage

lint-web: ## Run web linter
	cd web && npm run lint

fmt-web: ## Format web code
	cd web && npm run lint -- --fix

lint-electron: ## Run electron linter
	cd electron && npx eslint src/

fmt-electron: ## Format electron code
	cd electron && npx eslint src/ --fix

lint-frontend: ## Run linters for web and electron
	@$(MAKE) lint-web
	@$(MAKE) lint-electron

fmt-frontend: ## Format code for web and electron
	@$(MAKE) fmt-web
	@$(MAKE) fmt-electron

build-web: proto-generate ## Build web application
	cd web && npm run build

dev-web: ## Start web dev server
	cd web && npm run dev

# Documentation

## docs: Start Mintlify docs locally
docs: mint

## docs-build: Regenerate Mintlify docs content

docs-build:
	@echo "$(YELLOW)Regenerating Mintlify docs content...$(NC)"
	@$(MAKE) generate
	@echo "$(GREEN)✅ Mintlify docs content regenerated$(NC)"

## mint: Start Mintlify docs locally from docs/
mint:
	@echo "$(YELLOW)Starting Mintlify pilot docs...$(NC)"
	@echo "$(BLUE)Docs root: $(MINTLIFY_DOCS_DIR)$(NC)"
	@echo "$(BLUE)Site available at: http://localhost:$(MINTLIFY_PORT)/$(NC)"
	@if command -v mint >/dev/null 2>&1; then \
		cd $(MINTLIFY_DOCS_DIR) && mint dev --port $(MINTLIFY_PORT); \
	elif command -v mintlify >/dev/null 2>&1; then \
		cd $(MINTLIFY_DOCS_DIR) && mintlify dev --port $(MINTLIFY_PORT); \
	else \
		echo "$(RED)Neither 'mint' nor 'mintlify' CLI is installed.$(NC)"; \
		echo "Install one of them:"; \
		echo "  npm i -g mint"; \
		echo "  # or"; \
		echo "  npm i -g mintlify"; \
		exit 1; \
	fi

## changelog: Show PRs since last release for changelog
changelog:
	@./scripts/changelog-helper.sh

## changelog-draft: Generate a draft changelog YAML for Mintlify (requires VERSION=v1.2.0, optional SINCE_TAG=v1.1.0)
changelog-draft:
	@./scripts/changelog-draft.sh $(VERSION) $(SINCE_TAG)

# Docker-based testing
docker-build: ## Build Docker images for testing
	docker compose -f docker-compose.test.yml build

docker-test: docker-build ## Run all tests in Docker
	docker compose -f docker-compose.test.yml up --abort-on-container-exit --exit-code-from test-runner

docker-e2e: docker-build ## Run e2e tests in Docker
	docker compose -f docker-compose.test.yml up -d api web
	@echo "Waiting for services to be ready..."
	@sleep 10
	docker compose -f docker-compose.test.yml run --rm test-runner sh -c "cd web && npx playwright test"
	docker compose -f docker-compose.test.yml down

docker-clean: ## Clean up Docker containers and volumes
	docker compose -f docker-compose.test.yml down -v --remove-orphans

# Combined testing
test-all: ## Run all tests (Go and Web)
	@$(MAKE) stop 2>/dev/null || true
	@echo "Running Go tests..."
	go test -v ./...
	@echo "Running Web tests..."
	$(MAKE) test-web
	@$(MAKE) stop 2>/dev/null || true

test-full-ci: ## Run tests as they would run in CI
	@echo "Running CI test suite..."
	@echo "1. Go lint..."
	golangci-lint run
	@echo "2. Go tests..."
	go test -v -race -coverprofile=coverage.out ./...
	@echo "3. Web lint..."
	$(MAKE) lint-web
	@echo "4. Web tests..."
	$(MAKE) test-web-coverage
	@echo "All CI tests passed!"

clean-all: docker-clean clean ## Clean all test artifacts
	rm -rf coverage.out
	rm -rf web/coverage
	rm -rf web/test-results
	rm -rf test-results

clean-full: clean-all ## Clean all artifacts

# Environment-specific targets
ENV ?= dev

## env-setup: Setup and validate environment configuration
env-setup:
	@echo "$(YELLOW)Setting up $(ENV) environment...$(NC)"
	@./.reliant/scripts/env-loader.sh $(ENV) setup

## env-cleanup: Cleanup environment (removes test data, stops processes)
env-cleanup:
	@echo "$(YELLOW)Cleaning up $(ENV) environment...$(NC)"
	@./.reliant/scripts/env-loader.sh $(ENV) cleanup

## env-stop: Stop all services for environment
env-stop:
	@echo "$(YELLOW)Stopping $(ENV) environment services...$(NC)"
	@./.reliant/scripts/env-loader.sh $(ENV) stop

# API Server targets per environment
## run-api-test: Run API server in test environment
run-api-test:
	@echo "$(YELLOW)Starting API server in test environment...$(NC)"
	@./.reliant/scripts/env-loader.sh test setup
	@eval "$$(. ./.reliant/scripts/env-loader.sh test env)" && go run cmd/reliant/main.go api --port=$$BACKEND_PORT --debug=false

## run-api-dev: Run API server in development environment
run-api-dev:
	@echo "$(YELLOW)Starting API server in development environment...$(NC)"
	@./.reliant/scripts/env-loader.sh dev setup
	@eval "$$(. ./.reliant/scripts/env-loader.sh dev env)" && go run cmd/reliant/main.go api --port=$$BACKEND_PORT --debug=true

# Web Frontend targets per environment
## run-web-test: Run web frontend in test environment
run-web-test:
	@echo "$(YELLOW)Starting web frontend in test environment...$(NC)"
	@./.reliant/scripts/env-loader.sh test setup
	@cd web && eval "$$(../.reliant/scripts/env-loader.sh test env)" && npm run dev -- --port=$$FRONTEND_PORT

## run-web-dev: Run web frontend in development environment
run-web-dev:
	@echo "$(YELLOW)Starting web frontend in development environment...$(NC)"
	@./.reliant/scripts/env-loader.sh dev setup
	@cd web && eval "$$(../.reliant/scripts/env-loader.sh dev env)" && npm run dev -- --port=$$FRONTEND_PORT

# CLI targets per environment
## run-cli-test: Run CLI in test environment
run-cli-test:
	@echo "$(YELLOW)Running CLI in test environment...$(NC)"
	@./.reliant/scripts/env-loader.sh test setup
	@eval "$$(. ./.reliant/scripts/env-loader.sh test env)" && go run cmd/reliant/main.go

## run-cli-dev: Run CLI in development environment
run-cli-dev:
	@echo "$(YELLOW)Running CLI in development environment...$(NC)"
	@./.reliant/scripts/env-loader.sh dev setup
	@eval "$$(. ./.reliant/scripts/env-loader.sh dev env)" && go run cmd/reliant/main.go

# Electron targets per environment
## run-electron-dev: Run Electron app in development environment
run-electron-dev:
	@echo "$(YELLOW)Starting Electron app in development environment...$(NC)"
	@./.reliant/scripts/env-loader.sh dev setup
	@cd electron && eval "$$(../.reliant/scripts/env-loader.sh dev env)" && npm run dev

# Unified environment targets
## run-all-test: Start all services in test environment
run-all-test: env-setup
	@echo "$(YELLOW)Starting all services in test environment...$(NC)"
	@eval "$$(. ./.reliant/scripts/env-loader.sh test env)" && \
		concurrently --kill-others --prefix-colors "blue,green" \
		"make run-api-test" \
		"make run-web-test"

## run-all-dev: Start all services in development environment
run-all-dev: env-setup
	@echo "$(YELLOW)Starting all services in development environment...$(NC)"
	@eval "$$(. ./.reliant/scripts/env-loader.sh dev env)" && \
		concurrently --kill-others --prefix-colors "blue,green,magenta" \
		"make run-api-dev" \
		"make run-web-dev" \
		"make run-electron-dev"

# Test targets with environment isolation
## test-env: Run tests in isolated test environment
test-env:
	@echo "$(YELLOW)Running tests in isolated test environment...$(NC)"
	@$(MAKE) env-cleanup ENV=test 2>/dev/null || true
	@$(MAKE) env-setup ENV=test
	@eval "$$(. ./.reliant/scripts/env-loader.sh test env)" && $(GOTEST) -v -timeout=$(TEST_TIMEOUT) ./...
	@$(MAKE) env-cleanup ENV=test

## test-integration: Run integration tests with test environment
test-integration:
	@echo "$(YELLOW)Running integration tests in test environment...$(NC)"
	@$(MAKE) env-cleanup ENV=test 2>/dev/null || true
	@$(MAKE) env-setup ENV=test
	@eval "$$(. ./.reliant/scripts/env-loader.sh test env)" && \
		timeout 30 sh -c 'until curl -s http://localhost:$$BACKEND_PORT/health; do sleep 1; done' && \
		$(GOTEST) -v -tags=integration ./test/integration/...
	@$(MAKE) env-cleanup ENV=test

## test-env-integration: Test environment setup integration
test-env-integration:
	@echo "$(YELLOW)Running environment integration tests...$(NC)"
	@./.reliant/test/env-integration-test.sh

# Environment info and validation
## env-info: Show current environment information
env-info:
	@echo "$(BLUE)Environment Information:$(NC)"
	@echo "  Environment: $(ENV)"
	@echo "  Config File: .reliant/envs/$(ENV).yaml"
	@if [ -f ".reliant/envs/$(ENV).yaml" ]; then \
		echo "  $(GREEN)✅ Config exists$(NC)"; \
		echo "  API Port: $$(grep 'api_port:' .reliant/envs/$(ENV).yaml | sed 's/.*: *//')"; \
		echo "  Web Port: $$(grep 'web_port:' .reliant/envs/$(ENV).yaml | sed 's/.*: *//')"; \
	else \
		echo "  $(RED)❌ Config missing$(NC)"; \
	fi

## env-validate: Validate environment configuration
env-validate:
	@echo "$(YELLOW)Validating $(ENV) environment...$(NC)"
	@if [ ! -f ".reliant/envs/$(ENV).yaml" ]; then \
		echo "$(RED)❌ Environment config not found: .reliant/envs/$(ENV).yaml$(NC)"; \
		exit 1; \
	fi
	@echo "$(GREEN)✅ Environment config exists$(NC)"
	@./.reliant/scripts/env-loader.sh $(ENV) setup > /dev/null
	@echo "$(GREEN)✅ Environment $(ENV) is valid$(NC)"

# Add environment help
## env-help: Show environment-specific help
env-help:
	@echo ""
	@echo "$(BLUE)Environment Management Commands:$(NC)"
	@echo ""
	@echo "  $(YELLOW)Quick Start:$(NC)"
	@echo "    make dev                  # Start development environment"
	@echo "    make test-env-start       # Start test environment"
	@echo "    make stop                 # Stop current environment"
	@echo ""
	@echo "  $(YELLOW)Basic Usage:$(NC)"
	@echo "    make run-all-dev          # Start all services in dev environment"
	@echo "    make run-all-test         # Start all services in test environment"
	@echo "    make ENV=test run-api     # Run API in specific environment"
	@echo ""
	@echo "  $(YELLOW)Individual Services:$(NC)"
	@echo "    make run-api-dev          # API server (development)"
	@echo "    make run-api-test         # API server (test)"
	@echo "    make run-web-dev          # Web frontend (development)"
	@echo "    make run-web-test         # Web frontend (test)"
	@echo "    make run-cli-dev          # CLI (development)"
	@echo "    make run-cli-test         # CLI (test)"
	@echo ""
	@echo "  $(YELLOW)Environment Management:$(NC)"
	@echo "    make env-setup ENV=dev    # Setup environment"
	@echo "    make env-cleanup ENV=test # Cleanup environment"
	@echo "    make env-stop ENV=dev     # Stop environment services"
	@echo "    make env-info ENV=dev     # Show environment info"
	@echo "    make env-validate ENV=dev # Validate environment"
	@echo ""
	@echo "  $(YELLOW)Testing:$(NC)"
	@echo "    make test-env             # Run tests in isolated environment"
	@echo "    make test-integration     # Run integration tests"
	@echo ""

# Quick start aliases
## dev: Quick start development environment
dev:
	@./.reliant/scripts/run-environment.sh dev start

## test-env-start: Quick start test environment
test-env-start:
	@./.reliant/scripts/run-environment.sh test start

## stop: Stop the currently running environment
stop:
	@echo "$(YELLOW)Stopping all environments...$(NC)"
	@./.reliant/scripts/run-environment.sh dev stop 2>/dev/null || true
	@./.reliant/scripts/run-environment.sh test stop 2>/dev/null || true

## status: Show status of all environments
status:
	@echo "$(BLUE)Environment Status:$(NC)"
	@echo ""
	@echo "$(YELLOW)Development Environment:$(NC)"
	@./.reliant/scripts/run-environment.sh dev status
	@echo ""
	@echo "$(YELLOW)Test Environment:$(NC)"
	@./.reliant/scripts/run-environment.sh test status

## restart: Restart development environment
restart:
	@./.reliant/scripts/run-environment.sh dev restart