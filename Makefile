# Makefile — one-line `make → task` forwarders, for one merge.
#
# Taskfile.yml is where reliant's build, test, generate, lint, pin and release
# commands live now (`task --list`). Every Make target that still did
# something forwards to its task here, so muscle memory, scripts and CI keep
# working while they move to `task`; the targets that called files which no
# longer exist (the env-loader / run-environment stack, docker-compose.test.yml,
# the changelog helpers) and the duplicates (build-api-server & co, test-race,
# vet, the proto-generate-* splits) are gone. P8 lane L5 deletes this file
# (research/forge-migration/12-p8-devtooling-plan.md §2, D-P8-8).
#
# Make variables map onto the task's arguments: `make test-short PKGS=./x/...`
# is `task test:short -- ./x/...`, `make test-affected LANES=go` is
# `task test:affected LANES=go`; TEST_BASE, TEST_FULL, DATABASE_URL and FORGE
# pass through the environment, as before.
#
# TASK is the task on PATH, else go-task built from source at a fixed version
# (CI runners have no `task` until its workflows install one). It is exported
# for the scripts a task runs that run task again (check-docs-generate.sh).
TASK ?= $(shell command -v task 2>/dev/null || echo 'env GOWORK=off GOFLAGS=-trimpath go run github.com/go-task/task/v3/cmd/task@v3.52.0')
export TASK

.DEFAULT_GOAL := default
.PHONY: default build build-dev-cli build-all install deps clean \
	test test-short test-affected test-affected-plan test-coverage test-e2e \
	replay-fixtures freeze-replay-fixtures test-web test-web-coverage test-web-watch \
	lint fmt lint-web lint-electron fmt-web fmt-electron proto-lint proto-format \
	generate generate-all proto-generate proto-generate-controlplane \
	generate-yaml-bindings generate-tool-catalog generate-manifest-schema generate-integrations \
	generate-schema generate-scenario-schema generate-refcheck generate-cel-reference \
	generate-cli generate-tools-ref generate-shortcuts generate-nodes generate-types \
	generate-models generate-presets generate-workflow-builder-skill generate-changelog \
	generate-mintlify-reference forge-generate-check docs-generate-check migration \
	postgres-up postgres-down pin-forge pin-drift pin-ancestry \
	release-rc release-patch release-minor release-major release-tag release-tag-dry-run \
	check-release-tags docs mint build-web

default: ; @$(TASK) default

# Build
build: ; @$(TASK) build
build-dev-cli: ; @$(TASK) build:dev-cli
build-all: ; @$(TASK) build:cross
install: ; @$(TASK) install
deps: ; @$(TASK) deps
clean: ; @$(TASK) clean
build-web: ; @$(TASK) build:web

# Tests
test: ; @$(TASK) test
test-short: ; @$(TASK) test:short -- $(PKGS)
test-affected: ; @$(TASK) test:affected $(if $(LANES),LANES=$(LANES))
test-affected-plan: ; @$(TASK) test:affected:plan $(if $(LANES),LANES=$(LANES))
test-coverage: ; @$(TASK) coverage
test-e2e: ; @$(TASK) test:e2e
replay-fixtures: ; @$(TASK) replay-fixtures
freeze-replay-fixtures: ; @$(TASK) replay-fixtures:freeze $(if $(NAME),NAME=$(NAME))
test-web: ; @$(TASK) test:web
test-web-coverage: ; @$(TASK) test:web:coverage
test-web-watch: ; @$(TASK) test:web:watch
postgres-up: ; @$(TASK) postgres:up
postgres-down: ; @$(TASK) postgres:down

# Lint and format
lint: ; @$(TASK) lint
fmt: ; @$(TASK) fmt
lint-web: ; @$(TASK) lint:web
lint-electron: ; @$(TASK) lint:electron
fmt-web: ; @$(TASK) fmt:web
fmt-electron: ; @$(TASK) fmt:electron
proto-lint: ; @$(TASK) proto:lint
proto-format: ; @$(TASK) proto:fmt

# Code generation. The proto targets and the four Go generators are all one
# `forge generate` now (buf over buf.gen.yaml, then forge.yaml's generate.hooks).
generate: ; @$(TASK) generate
generate-all: ; @$(TASK) generate
proto-generate: ; @$(TASK) generate:forge
proto-generate-controlplane: ; @$(TASK) generate:controlplane
generate-yaml-bindings: ; @$(TASK) generate:forge
generate-tool-catalog: ; @$(TASK) generate:forge
generate-manifest-schema: ; @$(TASK) generate:forge
generate-integrations: ; @$(TASK) generate:forge
forge-generate-check: ; @$(TASK) generate:check -- $(FORGE_CHECK_FLAGS)
docs-generate-check: ; @$(TASK) generate:docs:check -- $(DOCS_CHECK_FLAGS)
generate-schema: ; @$(TASK) generate:docs:schema
generate-scenario-schema: ; @$(TASK) generate:docs:scenario-schema
generate-refcheck: ; @$(TASK) generate:docs:refcheck
generate-cel-reference: ; @$(TASK) generate:docs:cel-reference
generate-cli: ; @$(TASK) generate:docs:cli
generate-tools-ref: ; @$(TASK) generate:docs:tools-ref
generate-shortcuts: ; @$(TASK) generate:docs:shortcuts
generate-nodes: ; @$(TASK) generate:docs:nodes
generate-types: ; @$(TASK) generate:docs:types
generate-models: ; @$(TASK) generate:docs:models
generate-presets: ; @$(TASK) generate:docs:presets
generate-workflow-builder-skill: ; @$(TASK) generate:docs:workflow-builder-skill
generate-changelog: ; @$(TASK) generate:docs:changelog
generate-mintlify-reference: ; @$(TASK) generate:docs:mintlify-reference
migration: ; @$(TASK) db:migration $(if $(NAME),NAME=$(NAME))

# Pins, releases, docs site
pin-forge: ; @$(TASK) pin:forge -- $(REF)
pin-drift: ; @$(TASK) pin:drift
pin-ancestry: ; @$(TASK) pin:ancestry
release-rc: ; @$(TASK) release:rc
release-patch: ; @$(TASK) release:patch
release-minor: ; @$(TASK) release:minor
release-major: ; @$(TASK) release:major
release-tag: ; @$(TASK) release:tag
release-tag-dry-run: ; @$(TASK) release:tag:dry-run
check-release-tags: ; @$(TASK) release:check-tags
docs: ; @$(TASK) docs:serve $(if $(MINTLIFY_PORT),MINTLIFY_PORT=$(MINTLIFY_PORT))
mint: ; @$(TASK) docs:serve $(if $(MINTLIFY_PORT),MINTLIFY_PORT=$(MINTLIFY_PORT))
