package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// generateDXFiles writes developer-experience and operations scaffolding:
// VSCode settings, devcontainer + bootstrap, SECURITY.md, pre-commit
// config, example migrations/seeds, an ADR example, and benchmarks. Each
// helper is kept small and idempotent — the generator re-runs on `forge
// generate` and should not clobber user edits for files users typically
// own. We keep these files' contents here (rather than in
// internal/templates/) because they're static scaffolds that don't need
// template-engine features; embedding them as Go string constants keeps
// the surface area explicit and reviewable.
func (g *ProjectGenerator) generateDXFiles() error {
	if err := g.generateVSCodeConfig(); err != nil {
		return fmt.Errorf("write .vscode scaffolding: %w", err)
	}
	if err := g.generateDevcontainer(); err != nil {
		return fmt.Errorf("write devcontainer: %w", err)
	}
	if err := g.generateBootstrapScript(); err != nil {
		return fmt.Errorf("write scripts/bootstrap.sh: %w", err)
	}
	if err := g.generateSecurityPolicy(); err != nil {
		return fmt.Errorf("write SECURITY.md: %w", err)
	}
	if err := g.generatePreCommitConfig(); err != nil {
		return fmt.Errorf("write .pre-commit-config.yaml: %w", err)
	}
	if err := g.generateGitHooks(); err != nil {
		return fmt.Errorf("write .githooks: %w", err)
	}
	if g.Features.CIEnabled() {
		if err := g.generatePreCommitWorkflow(); err != nil {
			return fmt.Errorf("write pre-commit workflow: %w", err)
		}
	}
	if g.isService() && g.Features.MigrationsEnabled() {
		if err := g.generateExampleMigration(); err != nil {
			return fmt.Errorf("write example migration: %w", err)
		}
	}
	if g.isService() && g.Features.MigrationsEnabled() {
		if err := g.generateSeeds(); err != nil {
			return fmt.Errorf("write db/seeds: %w", err)
		}
	}
	// benchmarks/k6/ is a server load-test harness; skip for non-service
	// kinds. Library projects can still benefit from a Go testing.B
	// harness, but it's not load-testing — defer until requested.
	if g.isService() {
		if err := g.generateBenchmarks(); err != nil {
			return fmt.Errorf("write benchmarks: %w", err)
		}
	}
	if g.isService() && g.Features.ObservabilityEnabled() {
		if err := g.generateRunbook(); err != nil {
			return fmt.Errorf("write docs/runbook.md: %w", err)
		}
	}
	if g.isService() && g.Features.ORMEnabled() {
		if err := g.generateDBReadme(); err != nil {
			return fmt.Errorf("write db/README.md: %w", err)
		}
	}
	return nil
}

// generateVSCodeConfig writes .vscode/settings.json and .vscode/extensions.json.
// The existing .vscode/launch.json (written elsewhere) is untouched.
// Settings are intentionally minimal: format-on-save wired to the correct
// formatter per language, plus gopls analyses users already expect. We
// avoid opinionated lint/style choices that conflict with golangci-lint.
func (g *ProjectGenerator) generateVSCodeConfig() error {
	settings := `{
  "editor.formatOnSave": true,
  "[go]": {
    "editor.defaultFormatter": "golang.go",
    "editor.codeActionsOnSave": {
      "source.organizeImports": "explicit"
    }
  },
  "go.useLanguageServer": true,
  "gopls": {
    "build.buildFlags": [],
    "ui.diagnostic.analyses": {
      "unusedparams": true,
      "shadow": true,
      "nilness": true,
      "unusedwrite": true
    },
    "formatting.gofumpt": false
  },
  "[typescript]": {
    "editor.defaultFormatter": "esbenp.prettier-vscode"
  },
  "[typescriptreact]": {
    "editor.defaultFormatter": "esbenp.prettier-vscode"
  },
  "[javascript]": {
    "editor.defaultFormatter": "esbenp.prettier-vscode"
  },
  "[json]": {
    "editor.defaultFormatter": "esbenp.prettier-vscode"
  },
  "[yaml]": {
    "editor.defaultFormatter": "esbenp.prettier-vscode"
  },
  "tailwindCSS.experimental.classRegex": [
    ["cva\\(([^)]*)\\)", "[\"'` + "`" + `]([^\"'` + "`" + `]*).*?[\"'` + "`" + `]"],
    ["cn\\(([^)]*)\\)", "[\"'` + "`" + `]([^\"'` + "`" + `]*).*?[\"'` + "`" + `]"]
  ],
  "files.associations": {
    "*.kcl": "kcl",
    "*.k": "kcl",
    "Taskfile.yml": "yaml",
    "Taskfile.yaml": "yaml"
  },
  "buf.binaryPath": "buf",
  "protoc": {
    "path": "protoc"
  }
}
`
	extensions := `{
  "recommendations": [
    "golang.go",
    "bufbuild.vscode-buf",
    "esbenp.prettier-vscode",
    "dbaeumer.vscode-eslint",
    "bradlc.vscode-tailwindcss",
    "ms-azuretools.vscode-docker",
    "task.vscode-task",
    "kcl.vscode-kcl",
    "zxh404.vscode-proto3"
  ]
}
`
	vscodeDir := filepath.Join(g.Path, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0o755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(vscodeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(vscodeDir, "extensions.json"), []byte(extensions), 0o644)
}

// generateDevcontainer writes .devcontainer/devcontainer.json. The base
// image pins a recent stable Go; Node + Docker-in-Docker are provided via
// devcontainer features. postCreateCommand runs the scaffolded
// scripts/bootstrap.sh so new contributors get a fully tooled shell on
// first open.
func (g *ProjectGenerator) generateDevcontainer() error {
	// Keep the devcontainer's Go image aligned with the project's declared
	// minor version — but constrain it to a version we know is actually
	// published (same rationale as the Dockerfile builder base). The
	// devcontainers/go images lag upstream Go releases; pinning to a known
	// tag avoids `manifest unknown` on first `devcontainer up`.
	goMinor := dockerBuilderGoVersion(g.resolveGoVersion())

	// Forward both the server and frontend ports; infra ports (postgres,
	// jaeger) match the defaults used by docker-compose.yml.tmpl.
	// CLI/library kinds have no long-running server, so forward nothing.
	var forward []string
	if g.isService() {
		forward = append(forward, fmt.Sprintf("%d", g.ServicePort))
		// FrontendPort 0 = ephemeral (allocated at `forge env up` time), so there
		// is no stable port to pre-declare here; skip it rather than forward
		// port "0". An explicit frontend port (>0) is still forwarded.
		if g.FrontendName != "" && g.FrontendPort > 0 {
			forward = append(forward, fmt.Sprintf("%d", g.FrontendPort))
		}
		forward = append(forward, "5432", "16686")
	}

	var forwardJSON strings.Builder
	forwardJSON.WriteString("[")
	for i, p := range forward {
		if i > 0 {
			forwardJSON.WriteString(", ")
		}
		forwardJSON.WriteString(p)
	}
	forwardJSON.WriteString("]")

	portsAttributes := ""
	if g.isService() {
		portsAttributes = fmt.Sprintf(`,
  "portsAttributes": {
    "%d": { "label": "app" },
    "5432": { "label": "postgres" },
    "16686": { "label": "jaeger-ui" }
  }`, g.ServicePort)
	}

	content := fmt.Sprintf(`{
  "name": "%s",
  "image": "mcr.microsoft.com/devcontainers/go:%s-bookworm",
  "features": {
    "ghcr.io/devcontainers/features/node:1": {
      "version": "lts"
    },
    "ghcr.io/devcontainers/features/docker-in-docker:2": {
      "version": "latest"
    },
    "ghcr.io/devcontainers/features/common-utils:2": {
      "installZsh": true,
      "configureZshAsDefaultShell": false
    }
  },
  "postCreateCommand": "bash scripts/bootstrap.sh",
  "forwardPorts": %s%s,
  "remoteUser": "vscode",
  "customizations": {
    "vscode": {
      "extensions": [
        "golang.go",
        "bufbuild.vscode-buf",
        "esbenp.prettier-vscode",
        "dbaeumer.vscode-eslint",
        "bradlc.vscode-tailwindcss",
        "ms-azuretools.vscode-docker",
        "task.vscode-task",
        "kcl.vscode-kcl",
        "zxh404.vscode-proto3"
      ]
    }
  }
}
`, g.Name, goMinor, forwardJSON.String(), portsAttributes)

	dir := filepath.Join(g.Path, ".devcontainer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "devcontainer.json"), []byte(content), 0o644)
}

// generateBootstrapScript writes scripts/bootstrap.sh, a one-shot
// idempotent installer for the tools the repo expects on PATH. Each check
// is guarded by `command -v`, so rerunning it is cheap. Docs inline the
// install methods the script doesn't cover (Task, Buf, KCL) because
// those vary by platform.
func (g *ProjectGenerator) generateBootstrapScript() error {
	content := `#!/usr/bin/env bash
# scripts/bootstrap.sh — install the tools this repo expects.
#
# Safe to re-run: every step checks whether the tool is already on PATH.
# Intended for devcontainer postCreateCommand and new-contributor setup.
set -euo pipefail

log() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

need_go() {
  if ! have go; then
    echo "go is required but not found on PATH." >&2
    echo "Install Go (matching the go.mod directive): https://go.dev/dl/" >&2
    exit 1
  fi
}

install_go_tool() {
  # $1: binary name, $2: go install path
  local bin="$1" pkg="$2"
  if have "$bin"; then
    log "$bin already installed — skipping"
    return
  fi
  log "installing $bin via go install"
  go install "$pkg"
}

install_task() {
  if have task; then
    log "task already installed — skipping"
    return
  fi
  log "installing Task (https://taskfile.dev)"
  # Official install script; respects GOBIN / $HOME/.local/bin.
  sh -c "$(curl --location https://taskfile.dev/install.sh)" -- -d -b "${GOBIN:-$HOME/go/bin}"
}

install_buf() {
  if have buf; then
    log "buf already installed — skipping"
    return
  fi
  log "installing buf (https://buf.build)"
  install_go_tool buf github.com/bufbuild/buf/cmd/buf@latest
}

# No KCL CLI: forge renders every environment through the KCL runtime it
# embeds, and injects its own KCL module at render time. A bare ` + "`kcl run`" + `
# cannot resolve ` + "`import forge`" + `, so installing one only invites a render
# that bypasses forge — use ` + "`forge env render <env>`" + ` instead.

main() {
  need_go

  install_task
  install_buf

  install_go_tool protoc-gen-go            google.golang.org/protobuf/cmd/protoc-gen-go@latest
  install_go_tool protoc-gen-connect-go    connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
  # v2 module path, pinned to the version this project's CI lint job uses.
  install_go_tool golangci-lint            github.com/golangci/golangci-lint/v2/cmd/golangci-lint@` + templates.GolangciLintVersion + `
  install_go_tool goimports                golang.org/x/tools/cmd/goimports@latest
  install_go_tool govulncheck              golang.org/x/vuln/cmd/govulncheck@latest

  log "bootstrap complete"
}

main "$@"
`
	dir := filepath.Join(g.Path, "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Mode 0755: executable so contributors can run it directly; `bash
	// scripts/bootstrap.sh` in devcontainer.json still works regardless.
	return atomicfile.WriteFile(filepath.Join(dir, "bootstrap.sh"), []byte(content), 0o755)
}

// generateSecurityPolicy writes SECURITY.md at the repo root. We derive
// the contact email from the GitHub owner (module path) when we can;
// otherwise we fall back to a generic placeholder and let the user fill
// it in. The 90-day disclosure window matches industry convention
// (Project Zero / CVD norms).
func (g *ProjectGenerator) generateSecurityPolicy() error {
	owner := githubOwnerFromModulePath(g.ModulePath)
	contact := "security@example.com"
	if owner != "" {
		contact = fmt.Sprintf("security@%s.example", owner)
	}

	content := fmt.Sprintf(`# Security Policy

Thanks for taking the time to help keep %[1]s secure. This document
describes how to report vulnerabilities and what you can expect in
return.

## Supported versions

We patch security issues in the following releases:

| Version       | Supported          |
| ------------- | ------------------ |
| `+"`main`"+`        | :white_check_mark: |
| latest tagged | :white_check_mark: |
| older         | :x:                |

If you need a fix backported to an older release, include that in the
report and we'll discuss feasibility.

## Reporting a vulnerability

**Do not open a public GitHub issue for suspected vulnerabilities.**
Instead, email us privately:

- **Contact:** %[2]s
- **PGP key:** _(optional — add a public-key fingerprint here if your
  project publishes one)_

Please include:

1. A description of the issue and the affected component.
2. A minimal reproduction (command, proto, or request payload).
3. Your assessment of impact (confidentiality / integrity / availability).
4. Any mitigations or workarounds you've identified.

## What to expect

- **Acknowledgement** within 3 business days of the initial report.
- **Triage** (severity + affected versions) within 10 business days.
- **Fix or mitigation** within 90 days of acknowledgement, per the
  industry-standard coordinated-disclosure window. We may request a
  brief extension for complex issues; if so we'll communicate a new
  target date.
- **Public disclosure** after a fix ships, with credit to the reporter
  unless you ask to remain anonymous.

## Scope

This policy applies to the code in this repository. Issues in
third-party dependencies should be reported upstream; we're happy to
coordinate if the patched version needs to propagate here.

## Non-security issues

For bug reports, feature requests, or general contribution questions,
see [CONTRIBUTING.md](CONTRIBUTING.md).
`, g.Name, contact)

	return atomicfile.WriteFile(filepath.Join(g.Path, "SECURITY.md"), []byte(content), 0o644)
}

// forgeGeneratedPathPattern matches every path `forge generate` writes (and
// rewrites) in a project, as a pre-commit `exclude` regex. Mutating hooks must
// skip these: forge emits them already formatted, and `forge ci
// verify-generated` demands they regenerate byte-identically, so a formatter
// rewriting one makes the two gates fight on every commit.
//
// TestForgeGeneratedPathPattern matches it against every forge-owned file in
// a real scaffold, so a new generated path that escapes it fails there.
const forgeGeneratedPathPattern = `(^|/)gen/|_gen(_test)?(\.test)?\.[A-Za-z0-9]+$|^\.forge-kcl/|(^|/)public/config\.js$|(^|/)src/hooks/index\.ts$`

// generatePreCommitConfig writes .pre-commit-config.yaml. Hooks are
// chosen to match the existing CI surface (gofmt/govet/goimports via
// dnephin/pre-commit-golang, frontend prettier, buf format via a local
// hook because the Buf project does not publish an official pre-commit
// mirror). gitleaks covers secrets scanning without colliding with any
// separate correctness-cluster setup: it runs only on the pre-commit
// boundary + the dedicated workflow we install in
// .github/workflows/pre-commit.yml.
func (g *ProjectGenerator) generatePreCommitConfig() error {
	content := `# See https://pre-commit.com for full docs. Run locally with:
#     pip install pre-commit && pre-commit install
# CI runs the same set via .github/workflows/pre-commit.yml.
#
# FORGE-GENERATED FILES ARE EXCLUDED FROM EVERY MUTATING HOOK. forge writes
# them already formatted, and ` + "`forge ci verify-generated`" + ` regenerates them and
# demands a byte-identical tree — so a hook that rewrites one (a whitespace
# fixer, goimports regrouping protoc-gen-go's imports, prettier re-wrapping a
# generated hook file) makes the two gates fight on every commit. The
# exclusion is one pattern, repeated on each mutating hook:
#   (^|/)gen/                     generated Go + TS stubs
#   _gen(_test)?(\.test)?\.<ext>   *_gen.go, *_gen_test.go, *_gen.ts, *_gen.test.ts, *_gen.k
#   ^\.forge-kcl/                 the KCL module forge materializes locally
#   (^|/)public/config\.js$       a frontend's rendered runtime config
#   (^|/)src/hooks/index\.ts$     the generated hooks barrel
# A file you take over with ` + "`forge project disown`" + ` is yours: drop it from
# the pattern if you want the formatters to own it too.
repos:
  - repo: https://github.com/pre-commit/pre-commit-hooks
    rev: v4.6.0
    hooks:
      # testdata/ is excluded from the two whitespace fixers. Golden
      # snapshots assert on EXACT bytes — trailing blank lines included —
      # so "fixing" one silently rewrites the expectation the test exists
      # to check, and the suite goes red for a reason the diff makes look
      # cosmetic. Forge-generated files are excluded too (see the top).
      - id: trailing-whitespace
        exclude: (^|/)testdata/|` + forgeGeneratedPathPattern + `
      - id: end-of-file-fixer
        exclude: (^|/)testdata/|` + forgeGeneratedPathPattern + `
      - id: check-merge-conflict
      - id: check-added-large-files
        args: ["--maxkb=1024"]
      - id: check-yaml
      - id: check-json

  - repo: https://github.com/gitleaks/gitleaks
    rev: v8.18.4
    hooks:
      - id: gitleaks

  - repo: https://github.com/dnephin/pre-commit-golang
    rev: v0.5.1
    hooks:
      # testdata/ is excluded from both: these trees are deliberately
      # malformed fixtures (off-module imports, phantom packages) that the
      # linter tests assert on, and goimports "fixes" them into passing —
      # silently deleting the very import a test expects it to flag.
      # Generated Go is excluded too (see the top): goimports regroups
      # protoc-gen-go's single import block in gen/.
      - id: go-fmt
        exclude: (^|/)testdata/|(^|/)gen/|_gen(_test)?\.go$
      - id: go-imports
        exclude: (^|/)testdata/|(^|/)gen/|_gen(_test)?\.go$

  # prettier is a LOCAL hook pinned to the exact release your frontends'
  # package.json pins, so the hook and ` + "`npm run format`" + ` never disagree about the
  # same file. Bump both together.
  - repo: local
    hooks:
      - id: prettier
        name: prettier
        entry: prettier --write --ignore-unknown
        language: node
        additional_dependencies: ["prettier@` + templates.PrettierVersion + `"]
        types: [text]
        # Restrict to frontend + docs so prettier does not fight gofmt or
        # golangci-lint on Go files.
        #
        # skills/ is excluded: prettier pads every markdown table cell to the
        # column width, which inflates a skill far past the delivery budget
        # (audit-json went 13843 -> 34328 bytes, against a 24000 hard cap) and
        # rewrites the very table headers TestSkillsAuditCategoryDocsMatchEmittedSet
        # asserts on. These files are size- and structure-checked by tests;
        # a formatter cannot own them.
        #
        # Forge-generated files are excluded (see the top), and so is
        # forge.yaml: forge rewrites it in place when a command updates the
        # manifest, and a prettier pass on top is a diff forge then fights.
        files: \.(ts|tsx|js|jsx|json|md|yml|yaml|css)$
        exclude: ` + forgeGeneratedPathPattern + `|\.pb\.go$|/skills/.*\.md$|^forge\.yaml$

  # go vet as a LOCAL hook rather than dnephin's go-vet. Two reasons:
  # v0.5.1 ships no go-vet-mod (the module-wide id), and its go-vet runs
  # ` + "`" + `go vet` + "`" + ` per changed DIRECTORY — which drags in testdata
  # fixtures that are deliberately unbuildable (phantom imports, build
  # constraints that exclude every file) and fails on them. Vetting the
  # module honors those constraints and matches what CI checks.
  - repo: local
    hooks:
      - id: go-vet-mod
        name: go vet (module)
        entry: go vet ./...
        language: system
        files: \.go$
        pass_filenames: false

  # Buf has no first-party pre-commit mirror; invoke its CLI locally.
  # ` + "`" + `buf format -d` + "`" + ` prints a diff and exits non-zero on any
  # formatting difference — exactly the semantics pre-commit expects.
  - repo: local
    hooks:
      - id: buf-format
        name: buf format
        entry: buf format -d --exit-code
        language: system
        files: \.proto$
        pass_filenames: false

  # Conventional Commits enforcement.
  - repo: https://github.com/alessandrojcm/commitlint-pre-commit-hook
    rev: v9.18.0
    hooks:
      - id: commitlint
        stages: [commit-msg]
        additional_dependencies: ["@commitlint/config-conventional"]
`
	if err := atomicfile.WriteFile(filepath.Join(g.Path, ".pre-commit-config.yaml"), []byte(content), 0o644); err != nil {
		return err
	}

	// commitlint config so the pre-commit hook knows which ruleset to use.
	commitlintRC := `module.exports = { extends: ['@commitlint/config-conventional'] };
`
	return atomicfile.WriteFile(filepath.Join(g.Path, ".commitlintrc.js"), []byte(commitlintRC), 0o644)
}

// generateGitHooks writes the committed, tracked git hooks under
// .githooks/. Unlike .pre-commit-config.yaml (the pre-commit *framework*,
// which the user must `pre-commit install` and which lives in the
// untracked .git/hooks/), these are versioned with the repo.
//
// Gate placement is deliberate — it tracks how often each hook fires
// against how expensive/blocking its body is:
//
//   - pre-push runs the project's OWN gates, `forge lint` + `forge project audit`.
//     A full-project lint is slow (tens of seconds on a real repo) and
//     rejects the push when anything fails — exactly right *once per push*,
//     but hostile *per commit*: a per-commit full lint stalls every
//     checkpoint commit and blocks it on any work-in-progress lint issue
//     (the fast path to everyone reaching for `--no-verify`). You push far
//     less than you commit, so the heavy blocking gate lives here.
//   - pre-commit stays fast: just the pre-commit framework's own hooks
//     (gofmt, gitleaks, prettier, buf) when installed — formatting and
//     secret-scanning that belong on every commit and finish quickly.
//   - commit-msg carries commitlint (Conventional Commits).
//
// Activation is `core.hooksPath=.githooks`, set relative so it resolves
// against each worktree's own checkout: one setting in the shared
// .git/config makes the hooks fire in every linked worktree (reliant/forge
// lean heavily on worktrees). Because git resolves each stage to
// .githooks/<stage> under that one setting, adding a stage file is all it
// takes — no per-stage `pre-commit install`, no Python dependency for the
// baseline gate. `forge project new` sets it at init time; a fresh clone self-heals
// on the next forge command (see ensureGitHooksActivated in internal/cli).
//
// core.hooksPath overrides .git/hooks entirely, so the pre-commit
// framework's own hooks would otherwise stop firing locally. The
// pre-commit and commit-msg hooks chain to them explicitly — a no-op when
// `pre-commit` isn't installed — so nothing regresses for contributors who
// use the framework, while CI enforces it regardless via
// .github/workflows/pre-commit.yml.
//
// The hooks are forge-owned scaffolding: regenerated (overwritten) on
// `forge generate`, like .pre-commit-config.yaml. Project-specific
// checks belong in .pre-commit-config.yaml (which the chain runs), not
// in these files.
//
// Every hook opens with forgeManagedHookHeader on the line after its
// shebang. That line is how IsForgeManagedGitHook tells these hooks from a
// project's own: `project new --in-place` keeps an existing
// .githooks/pre-commit, and forge must never activate a hook it did not
// write.
func (g *ProjectGenerator) generateGitHooks() error {
	dir := filepath.Join(g.Path, ".githooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// pre-commit stays cheap — no `forge lint`/`forge project audit` here (they run
	// on pre-push). Just the pre-commit framework's fast per-commit hooks.
	preCommit := `#!/bin/sh
# forge-managed git pre-commit hook — regenerated by ` + "`forge generate`" + `.
# Kept fast: the heavy gates (forge lint + forge project audit) run on pre-push,
# not here. This stage only runs the pre-commit framework's own quick hooks
# (gofmt, gitleaks, prettier, buf) when installed — a no-op otherwise. CI
# runs them regardless via .github/workflows/pre-commit.yml.
#
# Activated via ` + "`core.hooksPath=.githooks`" + ` (relative, so it works in
# every git worktree). Bypass one commit with ` + "`git commit --no-verify`" + `;
# stop forge activating git hooks in a clone with FORGE_NO_GIT_HOOKS=1 (it does
# not affect the forge.yaml generate.hooks that ` + "`forge generate`" + ` runs).
if command -v pre-commit >/dev/null 2>&1 && [ -f .pre-commit-config.yaml ]; then
	exec pre-commit run --hook-stage pre-commit
fi
`
	if err := atomicfile.WriteFile(filepath.Join(dir, "pre-commit"), []byte(preCommit), 0o755); err != nil {
		return err
	}

	// pre-push carries the project's own gates. Slow + blocking is right
	// here: it fires once per push (not per commit), so a full-project lint
	// and audit stop broken code from reaching the remote without taxing the
	// commit loop. Bypass a push with ` + "`git push --no-verify`" + `.
	prePush := `#!/bin/sh
# forge-managed git pre-push hook — regenerated by ` + "`forge generate`" + `.
# Runs the project's own gates before code leaves your machine. This is the
# heavy, blocking gate — it fires once per push, not per commit, so a full
# lint + audit here doesn't stall the commit loop. Bypass with
# ` + "`git push --no-verify`" + `.
set -e

forge lint
forge project audit
`
	if err := atomicfile.WriteFile(filepath.Join(dir, "pre-push"), []byte(prePush), 0o755); err != nil {
		return err
	}

	// commit-msg carries Conventional-Commits enforcement (commitlint),
	// which lives on the pre-commit framework's commit-msg stage. Same
	// override reason as above: chain to it explicitly, no-op when absent.
	commitMsg := `#!/bin/sh
# forge-managed git commit-msg hook — regenerated by ` + "`forge generate`" + `.
# Runs commitlint (Conventional Commits) via the pre-commit framework when
# installed; no-op otherwise. See .githooks/pre-commit for activation notes.
if command -v pre-commit >/dev/null 2>&1 && [ -f .pre-commit-config.yaml ]; then
	exec pre-commit run --hook-stage commit-msg --commit-msg-filename "$1"
fi
`
	return atomicfile.WriteFile(filepath.Join(dir, "commit-msg"), []byte(commitMsg), 0o755)
}

// forgeManagedHookHeader starts the second line (the one after the shebang)
// of every hook generateGitHooks writes: "# forge-managed git <stage> hook".
const forgeManagedHookHeader = "# forge-managed git "

// IsForgeManagedGitHook reports whether script is a git hook forge wrote
// (generateGitHooks) rather than one the project owns. It reads only the
// line after the shebang, where every forge hook carries
// forgeManagedHookHeader, so a project hook that merely mentions forge is
// still the project's.
//
// Forge activates .githooks (core.hooksPath) only for its own hooks. An
// in-place adopted repository keeps the hooks it already had, and pointing
// git at them is that project's decision, made by its own setup step.
func IsForgeManagedGitHook(script []byte) bool {
	lines := strings.SplitN(string(script), "\n", 3)
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "#!") {
		return false
	}
	return strings.HasPrefix(lines[1], forgeManagedHookHeader)
}

// generatePreCommitWorkflow writes .github/workflows/pre-commit.yml as a
// standalone workflow rather than adding a job to ci.yml. Keeping it
// separate keeps ci.yml focused on build/test and lets the pre-commit
// cache be tuned independently. Matches the existing workflow style
// (explicit permissions, pinned action majors, concurrency group).
func (g *ProjectGenerator) generatePreCommitWorkflow() error {
	dest := filepath.Join(g.Path, filepath.FromSlash(preCommitWorkflowPath))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(dest, []byte(preCommitWorkflow), 0o644)
}

// preCommitWorkflowPath is where generatePreCommitWorkflow writes, and the
// path its scaffold-once advisory row reports on.
const preCommitWorkflowPath = ".github/workflows/pre-commit.yml"

// preCommitWorkflow is the whole pre-commit workflow. It takes no render
// data, so this one string is both what `forge project new` writes and what
// `forge project upgrade` compares an existing copy against.
const preCommitWorkflow = `# Runs the same hooks as the local pre-commit config so CI catches
# contributors who haven't installed pre-commit locally. Hook versions
# are pinned in .pre-commit-config.yaml.
name: pre-commit

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

permissions:
  contents: read

# Cancel a superseded run on a pull request only; a run on main always
# finishes (ci.yml says why).
concurrency:
  group: pre-commit-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

jobs:
  pre-commit:
    name: pre-commit
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: "3.12"
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Install buf
        uses: bufbuild/buf-setup-action@v1
      # The go-imports hook shells out to a goimports BINARY and fails with
      # "goimports not installed or available in the PATH" when it is absent
      # — the runner image does not ship one, and setup-go does not add it.
      - name: Install goimports
        run: go install golang.org/x/tools/cmd/goimports@latest
      - uses: pre-commit/action@v3.0.1
`

// generateExampleMigration writes db/README.md (the migrations-as-truth
// workflow doc) and ensures db/migrations/ exists. It deliberately does
// NOT emit a starter migration: tables are born via `forge scaffold entity`
// (or a hand-written migration), and an empty schema means an empty
// entity universe — no ORM, no CRUD wiring, no frontend pages. The old
// example `items` migration made the pristine scaffold lie about
// having an entity.
func (g *ProjectGenerator) generateExampleMigration() error {
	dir := filepath.Join(g.Path, "db", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Overwrite db/README.md with a more complete version that covers
	// the add-entity workflow, transaction guidance and seeds. The
	// earlier stub written from project.go is superseded here
	// intentionally.
	readme := `# db

SQL migrations managed by [golang-migrate](https://github.com/golang-migrate/migrate),
invoked by the generated ` + "`" + `db migrate` + "`" + ` subcommands.

## Schema is code: migrations are the single source of truth

The applied schema drives code generation: ` + "`forge generate`" + ` applies
every ` + "`*.up.sql`" + ` to an in-memory shadow database, introspects it, and
projects entity structs, the ORM (` + "`internal/db/`" + `), CRUD wiring and
frontend pages from the real tables. There is no schema DSL — the SQL
here IS the declaration.

Start a new entity in the service proto — declare the message under a
leading ` + "`// forge:entity`" + ` marker:

` + "```" + `protobuf
// forge:entity
message Bookmark {
  string url = 2;
  string title = 3;
  repeated string tags = 4;
  bool done = 5;
}
` + "```" + `

then run:

` + "```" + `
forge scaffold
` + "```" + `

which births it: the create-table migration pair (yours from that moment)
plus, once, the CRUD messages and RPCs in the service proto. Evolve it by
writing further migrations — add a column, move data with plain
` + "`UPDATE`" + ` statements, drop a column — and re-run ` + "`forge generate`" + `;
the projections follow the schema.

Conventions read off real columns (no annotations):

| columns                  | behavior                                         |
|--------------------------|--------------------------------------------------|
| ` + "`deleted_at`" + `             | soft delete: DELETE becomes UPDATE, reads filter ` + "`IS NULL`" + ` (` + "`forge:no-soft-delete`" + ` on the column opts out) |
| ` + "`created_at`" + ` + ` + "`updated_at`" + ` | managed timestamps (stamped by the ORM)   |
| text columns             | spanned by the generated list ` + "`search`" + ` filter   |

## Layout

` + "```" + `
db/
  migrations/
    00001_create_bookmarks.up.sql    # forward-only migration (there are no down files)
  seeds/
    0001_examples.sql                # idempotent dev/test seed data
` + "```" + `

Migrations run in lexicographic order. Stick to zero-padded, monotonic
5-digit numeric prefixes (` + "`00001`" + `, ` + "`00002`" + `, ...) so the order is
stable regardless of merge order.

## Writing a new migration

1. Create ` + "`N_name.up.sql`" + ` (` + "`forge db migration new <name>`" + `). Write NO down
   migration: forge rolls forward only, and ` + "`forge lint`" + ` fails on a ` + "`.down.sql`" + `.
2. Wrap **multi-statement DDL in an explicit transaction**
   (` + "`BEGIN; ... COMMIT;`" + `) so a mid-migration failure rolls back cleanly.
   Single-statement migrations can omit the transaction.
2b. Write **plain postgres** — forge is postgres-pinned. Your own tests
   and forge's shadow introspection apply these files VERBATIM to a real
   ephemeral postgres, so anything postgres accepts works: ` + "`::type`" + `
   casts, schema-qualified names (` + "`CREATE TABLE app.foo`" + `), native
   arrays (` + "`TEXT[]`" + `), ` + "`JSONB`" + `, generated/identity columns.
3. Keep migrations **forward-compatible with running code** — expand, then
   contract: add the new shape and backfill, switch the code over in a
   release, and drop the old shape in a LATER release. The previous release
   must keep working against the new schema; that is what makes rolling the
   APP back safe.
4. A bad migration is fixed by a **new forward migration** (a hotfix written
   against the state the database is actually in), never by reversing one.

## CLI

` + "```" + `
go run ./cmd db migrate up      # apply all pending migrations
go run ./cmd db migrate status  # print current version / dirty flag
` + "```" + `

All subcommands read ` + "`DATABASE_URL`" + ` (or ` + "`--database-url`" + `) from the
standard project config.

## Seeds

` + "`db/seeds/`" + ` holds idempotent SQL files intended for development and
test fixtures. They are **not** applied automatically on startup — run
them explicitly:

` + "```" + `
task db-seed
` + "```" + `

The task loads every file in ` + "`db/seeds/`" + ` in lexicographic order via
` + "`psql $DATABASE_URL`" + `. Write each seed with ` + "`ON CONFLICT DO NOTHING`" + ` (or
the equivalent) so re-running is safe.
`
	return atomicfile.WriteFile(filepath.Join(g.Path, "db", "README.md"), []byte(readme), 0o644)
}

// generateSeeds documents the runtime seed model and scaffolds the
// user-owned db/seeds/custom/ overlay ONCE. forge writes no seed .sql files
// into the project: the deterministic dataset materializes at runtime via
// `forge db seed apply` (and `forge env up` first-boot auto-seed). The only
// on-disk artifact is the custom overlay — the sanctioned hook for
// hand-authored / domain-flavored demo data — which is user-owned from
// birth and never regenerated.
func (g *ProjectGenerator) generateSeeds() error {
	readme := `# db/seeds

Development seed data is **not** a file in this project. It materializes at
runtime, deterministically, from the applied schema:

    forge db seed apply     # introspect db/migrations, INSERT ~20 FK-coherent
                            # rows per table (idempotent, ON CONFLICT DO NOTHING)
    forge db seed status    # per-table seeded-row counts
    forge db seed reset     # wipe seeded tables and re-seed (dev only)

` + "`forge env up`" + ` auto-seeds a fresh dev database on first boot (all tables
empty). apply/reset refuse any non-dev environment — seeding never runs
against staging or production, and the applier is not compiled into your
server binary.

## Domain vocabulary — make seeded rows speak your domain

**` + "`db/seeds/vocab.yaml`" + ` is where your domain's words live, and nothing
else supplies them.** Without it the seeder invents values that announce
themselves — ` + "`sample_<column>_<n>`" + ` — because what a column MEANS is not
in the schema and forge will not guess it from the column's name. A database
full of ` + "`sample_*`" + ` is forge telling you nobody has filled this in yet.

The file (yours, scaffolded once) maps ` + "`table.column`" + ` keys to value
pools; matched columns draw from them instead — same determinism, validated
against your schema's constraints. Write it right after authoring migrations,
while the domain is fresh. See the file's header for the schema.

What forge DOES derive on its own, because you declared it: enum and CHECK
vocabularies, ` + "`char_length`" + ` / varchar caps, numeric ranges, regex
` + "`CHECK (col ~ '...')`" + ` patterns, NOT NULL, UNIQUE, and every foreign key.

## Custom overlay — your hand-authored demo data

Put domain-flavored or agent-written demo rows in ` + "`db/seeds/custom/`" + `.
Files there apply **after** the runtime dataset, in lexicographic order.
This directory is yours: forge scaffolds it once and never regenerates it.
`
	dir := filepath.Join(g.Path, "db", "seeds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644); err != nil {
		return err
	}

	customREADME := `# db/seeds/custom — your demo-data overlay

Hand-authored (or agent-authored) demo data. Applied AFTER the runtime
` + "`forge db seed apply`" + ` dataset, in lexicographic order. This directory is
scaffolded once and owned by you — forge never regenerates it.

Rules:
  - Make every row idempotent: a stable key + ` + "`ON CONFLICT ... DO NOTHING`" + `.
  - Overriding a generated row is sanctioned: the runtime PKs are stable
    deterministic UUIDv5 values, so ` + "`ON CONFLICT (id) DO UPDATE SET ...`" + `
    against one lets you re-title/flavor it without forking the dataset.
  - A row that violates a constraint (dropped column, bad enum) fails loudly
    at apply — the fix is yours, exactly like a migration.

See example.sql for the shape.
`
	if _, err := WriteScaffoldIfMissing(g.Path, filepath.Join("db", "seeds", "custom", "README.md"), []byte(customREADME)); err != nil {
		return err
	}

	example := `-- db/seeds/custom/example.sql (yours — edit or delete freely)
--
-- Applied after ` + "`forge db seed apply`" + ` materializes the deterministic
-- dataset. Uncomment and adapt to a real table in your schema.
--
-- INSERT INTO organizations (id, name) VALUES
--     ('dev-org-001', 'Acme (demo)')
-- ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name;
`
	if _, err := WriteScaffoldIfMissing(g.Path, filepath.Join("db", "seeds", "custom", "example.sql"), []byte(example)); err != nil {
		return err
	}

	// The domain-vocabulary overlay: scaffolded fully-commented (a clean
	// no-op until filled in), user-owned from birth. The app author — human
	// or LLM — writes it once, right after authoring migrations while the
	// domain is fresh, and `forge db seed` speaks the project's vocabulary
	// from then on.
	if _, err := WriteScaffoldIfMissing(g.Path, filepath.Join("db", "seeds", "vocab.yaml"), []byte(seedVocabTemplate)); err != nil {
		return err
	}
	return nil
}

// generateBenchmarks writes the benchmarks/ harness:
//
//   - benchmarks/k6/smoke.js: ramping-VU smoke test hitting /healthz plus
//     one Connect RPC (ListItems, which the service template exposes when
//     a service is scaffolded — we fall back to /healthz only when there
//     is no initial service).
//   - benchmarks/README.md: how to run k6 and what to watch.
//   - benchmarks/healthz_bench_test.go: a Go ` + "`" + `Benchmark*` + "`" + ` that benchmarks
//     the healthz handler. Kept in its own ` + "`" + `_bench_test.go` + "`" + ` file so it can
//     coexist with any correctness-cluster test file (e.g. recovery_test.go).
func (g *ProjectGenerator) generateBenchmarks() error {
	dir := filepath.Join(g.Path, "benchmarks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "k6"), 0o755); err != nil {
		return err
	}

	targetHost := fmt.Sprintf("http://localhost:%d", g.ServicePort)

	k6 := fmt.Sprintf(`// smoke.js — k6 smoke test.
// Ramps a handful of VUs against the running service and fails the run
// if p95 latency or error rate blow past the thresholds below. Keep the
// load modest; this is a smoke test, not a capacity test.
//
// Run with:
//     k6 run --env BASE_URL=%[1]s benchmarks/k6/smoke.js

import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '10s', target: 5 },
    { duration: '20s', target: 10 },
    { duration: '10s', target: 0 },
  ],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<300'],
  },
};

const BASE_URL = __ENV.BASE_URL || '%[1]s';

export default function () {
  const healthz = http.get(`+"`"+`${BASE_URL}/healthz`+"`"+`);
  check(healthz, {
    'healthz 200': (r) => r.status === 200,
  });

  sleep(1);
}
`, targetHost)

	readme := `# benchmarks

Two flavours of perf harness:

- ` + "`k6/`" + ` — [k6](https://k6.io) scripts for HTTP-level load tests.
  Good for smoke-testing a running service end-to-end.
- ` + "`*_bench_test.go`" + ` — Go ` + "`testing.B`" + ` microbenchmarks. Good for
  measuring specific handlers or hot paths in isolation.

## Running k6

Install k6: <https://k6.io/docs/get-started/installation/>.

Start the service locally (` + "`task dev`" + ` or ` + "`docker compose up`" + `), then:

` + "```" + `bash
k6 run benchmarks/k6/smoke.js
# Or point at a staging host:
k6 run --env BASE_URL=https://staging.example.com benchmarks/k6/smoke.js
` + "```" + `

### What to watch

- ` + "`http_req_duration`" + ` — p(50), p(95), p(99). Thresholds in the script
  will fail the run if p(95) regresses past the baseline.
- ` + "`http_req_failed`" + ` — should stay below 1%. Any non-2xx response that
  isn't an intentional auth/404 test counts.
- ` + "`vus`" + ` / ` + "`iterations`" + ` — sanity check that ramping actually happened.

## Running Go benchmarks

` + "```" + `bash
task bench
# or, for a specific package:
go test -bench=. -benchmem ./benchmarks/...
` + "```" + `

Follow up with ` + "`benchstat`" + ` when comparing two revisions:

` + "```" + `bash
go install golang.org/x/perf/cmd/benchstat@latest
go test -bench=. -count=10 ./benchmarks/... > old.txt
# ...make change, then...
go test -bench=. -count=10 ./benchmarks/... > new.txt
benchstat old.txt new.txt
` + "```" + `
`

	goBench := `package benchmarks

// Package benchmarks hosts the project's microbenchmarks. Each file
// follows the standard Go convention (` + "`" + `func BenchmarkXxx(b *testing.B)` + "`" + `)
// so they run under ` + "`" + `go test -bench` + "`" + ` / ` + "`" + `task bench` + "`" + ` without special flags.
//
// Benchmarks live in their own package so they can evolve independently
// of unit tests in pkg/middleware — no risk of accidentally sharing
// state or setup across the two.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// healthzHandler mirrors cmd/server.go's liveness endpoint closely
// enough to give a realistic baseline for the request/response path:
// a tiny handler with no dependencies. If the real handler grows
// (metrics, checks), update this to match so the benchmark stays
// representative.
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// BenchmarkHealthz measures the cost of serving a single /healthz
// request via net/http/httptest. Reports ns/op and allocs/op so a CI
// regression gate (e.g. benchstat) can detect unexpected growth.
func BenchmarkHealthz(b *testing.B) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		healthzHandler(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", rr.Code)
		}
	}
}
`
	if err := atomicfile.WriteFile(filepath.Join(dir, "k6", "smoke.js"), []byte(k6), 0o644); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "healthz_bench_test.go"), []byte(goBench), 0o644)
}

// generateRunbook writes docs/runbook.md: where this project's telemetry,
// generated dashboards and generated alerts are, and what each built-in alert
// means. It names only what forge ships (ClickStack, the dashboards under
// deploy/observability/dashboards/, the defaults tuned through forge.Alerts),
// so it is useful from the first generate rather than a form of TODOs.
func (g *ProjectGenerator) generateRunbook() error {
	content, err := templates.ProjectTemplates().Render("runbook.md.tmpl", struct{ Name string }{g.Name})
	if err != nil {
		return fmt.Errorf("render runbook.md.tmpl: %w", err)
	}
	dir := filepath.Join(g.Path, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "runbook.md"), content, 0o644)
}

// generateDBReadme writes db/README.md: where the schema lives, what forge
// projects from it, and where a query the generated store cannot express goes.
// There is deliberately no db/queries/ and no sqlc config — the ORM projected
// from the applied migrations is the one data-access path, and raw SQL lives
// beside it in the owned internal/db/<entity>_repo_ext.go seam.
func (g *ProjectGenerator) generateDBReadme() error {
	if err := os.MkdirAll(filepath.Join(g.Path, "db"), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(g.Path, "db", "README.md"), []byte(dbReadme), 0o644)
}

// dbReadme is the scaffolded db/README.md.
const dbReadme = `# Database

The schema is declared here, in SQL, and nowhere else. ` + "`forge generate`" + ` applies
the migrations to a throwaway postgres and projects what it finds into Go: entity
structs, the ` + "`<Entity>Store`" + ` ORM and the CRUD wiring under ` + "`internal/db/`" + `.

## Layout

` + "```" + `
db/
  migrations/   # <UTC timestamp>_<name>.up.sql — roll forward only
  seeds/        # dev seed vocabulary and hand-authored demo data
` + "```" + `

New migration: ` + "`forge db migration new <name>`" + `, then ` + "`forge generate`" + `.
Dev data: ` + "`forge db seed apply`" + `.

## Queries

The generated store covers reads by key, filtered lists and writes. A query it
cannot express goes in ` + "`internal/db/<entity>_repo_ext.go`" + `, which forge scaffolds
beside each entity and never rewrites: compose ` + "`orm.QueryOption`" + ` filters on the
generated delegates, or drop to SQL with ` + "`db.Bun()`" + ` — bun takes CTEs,
` + "`FOR UPDATE SKIP LOCKED`" + ` and raw statements directly. Cover every such query
with a test against a real migrated database (` + "`testkit`" + `), which is what checks
the SQL against the schema.
`

// seedVocabTemplate is the scaffolded db/seeds/vocab.yaml. It lives at package
// scope because it is a long document, not logic: inline it made
// generateSeeds read as one long literal with four file writes buried in it.
const seedVocabTemplate = `# db/seeds/vocab.yaml — domain vocabulary for ` + "`forge db seed`" + ` (yours; forge
# never regenerates it).
#
# EDITING THIS FILE? RUN ` + "`forge generate`" + ` AFTERWARDS.
#
# It feeds two consumers, and only one of them reads it live. ` + "`forge db seed`" + `
# reads it on every run, so a change shows up in the next seeded database. The
# frontend's mock fixtures (src/mocks/*_gen.ts) CACHE it as TypeScript literals,
# because a browser has no database to derive them from — so those go stale the
# moment you edit this file and stay stale until a regenerate. The fixture
# freshness guard fails ` + "`task test`" + ` when that happens rather than letting mock
# mode serve rows for a vocabulary you have since changed.
#
# THIS FILE IS THE ONLY PLACE YOUR DOMAIN'S WORDS COME FROM.
#
# forge derives everything your schema DECLARES — a column's type, its enum or
# CHECK vocabulary, a regex CHECK, char_length / varchar caps, numeric ranges,
# NOT NULL, UNIQUE, and every foreign key. It does not derive what a column
# MEANS, because that is nowhere in the schema: nothing in
# ` + "`price_cents BIGINT`" + ` says money, and nothing in ` + "`name TEXT`" + ` says whether
# it holds a person or a company. forge will not guess it from the column's
# name either — a guess is right for the vocabulary it was written against and
# silently wrong everywhere else.
#
# So an undescribed column seeds an obviously-invented value:
# ` + "`sample_<column>_<row>`" + `, type-correct and inside every constraint. A
# database full of ` + "`sample_*`" + ` is not a bug; it is forge reporting that
# this file is still empty. Fill it in once — right after authoring
# migrations, while the domain is fresh — and seeded rows read like your
# product.
#
# Every value you supply is validated against the applied schema: an invalid
# one is skipped with a warning, a fully-invalid column falls back to
# synthesis, and primary-key / foreign-key columns are never overridable (the
# seeder owns referential integrity — to pin a specific row, write it yourself
# in db/seeds/custom/). Draws stay deterministic.
#
# Schema (uncomment and adapt):
#
# pools:                       # shared pools, referenced from several columns
#   product_names: [Alpha One, Beta Two, Gamma Three]
# columns:                     # "table.column": inline list, {pool: name},
#                              # {type: name}, {min: n, max: n}, or
#                              # {from: -30d, to: now} for a time column
#   products.name: {pool: product_names}
#   catalog_items.name: {pool: product_names}
#   brands.name: [Northwind, Contoso Labs]
#   orders.currency: [USD]     # a pool of ONE pins every row to that value
#
# NUMERIC columns take a range. Describe every one that means something —
# an undescribed numeric column seeds as the ROW INDEX (1, 2, 3 …), which
# is inside every CHECK and therefore never reported, but it makes money
# render as a fraction of a cent and makes two numeric columns on the same
# table perfectly correlated:
#
#   products.price_cents: {min: 1200, max: 24900, step: 100}
#   products.stock_quantity: {min: 0, max: 250}
#   products.rating: {min: 1.0, max: 5.0, step: 0.5, decimals: 1}
#
# TIMESTAMP and DATE columns take instants RELATIVE TO NOW (the day the seed
# runs), so seeded dates stay current: a range, or a list of offsets
# (m/h/d/w) and absolute ISO dates. Undescribed ones land in the last 4 weeks.
#
#   orders.placed_at: {from: -90d, to: now}
#   deliveries.scheduled_for: {from: -1w, to: +4w, step: 1h}
#   invoices.due_on: [+2w, +4w, +30d]
#
# BOOLEAN columns take true/false; null fills a NULLABLE column. Repeating an
# entry weights the draw, for every kind of value:
#
#   products.active: [true, true, true, false]      # ~75% active
#   orders.notes: [null, null, "Leave at the door"] # ~2/3 empty
#   orders.shipped_at: [null, -2d, -5d]             # null on NOT NULL refuses the seed
#
# TWO TRAPS WORTH THE 30 SECONDS, both of which produce data that satisfies
# the schema and still breaks a page:
#
#   * A 3-char currency column (` + "`CHECK (char_length(currency) = 3)`" + `) synthesizes
#     as "sa5" — a valid length, and a currency code nothing recognises.
#     The generated pages can only print it as a bare amount beside the raw
#     code, not as money. Pin it:
#     ` + "`products.currency: [USD]`" + `.
#   * A column you MEANT to be unique but did not declare UNIQUE will repeat.
#     The seeder draws without replacement only for a real UNIQUE constraint —
#     if duplicates matter, fix the migration, not this file.
`
