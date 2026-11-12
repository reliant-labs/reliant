package templates

// GolangciLintVersion is the one golangci-lint release forge installs and
// ships: forge's own CI and scripts/bootstrap.sh, and — through the CI
// template and the scaffolded scripts/bootstrap.sh — every project forge
// creates. TestGolangciLintVersionIsPinnedEverywhere
// (internal/generator/golangci_pin_test.go) fails when any of those sites
// names a different version, `latest`, the v1 module path, or an action major
// that cannot run v2.
//
// It is a pin, not `latest`, because a linter release adds checks: v2.14.0
// turned seven months-old lines red with no commit in between. Bumping it is
// a deliberate one-line change here plus the four files the test names, made
// in a PR that also fixes whatever the new release reports.
const GolangciLintVersion = "v2.14.0"

// PrettierVersion is the one prettier release that formats a scaffolded
// frontend's sources, and therefore the one every formatter of those sources
// must run: the frontends' package.json (`npm run format`, and forge's
// format-at-birth of a scaffolded page, which uses the installed copy), the
// scaffolded .pre-commit-config.yaml hook, forge's OWN pre-commit hook (which
// formats pkg/components, the library copied verbatim into projects) and
// every test that runs prettier to prove a source is clean.
// TestPrettierVersionIsPinnedEverywhere
// (internal/generator/prettier_pin_test.go) fails when any of those names
// another version or a range.
//
// Exact, never a range, for the reason GolangciLintVersion is: prettier
// minors reformat code. Two pins disagreeing on the same file is a
// formatting round-trip neither side is wrong about — and was forge main's
// red pre-commit: the hook ran 3.1.0 over library files #502 had formatted
// with 3.5.3, the version projects ran. A `^3.5.0` range drifts the same way
// on its own, one `npm install` at a time.
const PrettierVersion = "3.5.3"

// CommitlintVersion is the release of @commitlint/cli and
// @commitlint/config-conventional the scaffolded commit-msg hook installs
// (.pre-commit-config.yaml's local commitlint hook). The two packages are
// released in lockstep, so one pin covers both. Exact, for the reason
// PrettierVersion is: a ruleset that moves on its own rejects a commit
// message the previous install accepted.
const CommitlintVersion = "19.8.1"

// PnpmVersion is the pnpm release a scaffolded frontend pins in package.json
// `packageManager`, and so the one every install of it runs: corepack and
// pnpm itself switch to the pinned version, the frontend Dockerfile installs
// it, and the scaffolded workflows' pnpm/action-setup reads it from the same
// field. TestPnpmVersionIsPinnedEverywhere
// (internal/generator/pnpm_pin_test.go) fails when a template or one of
// forge's own workflows names another.
//
// pnpm, not npm, because its content-addressed store hard-links one copy of a
// package into every checkout that uses it: on a machine with many worktrees
// a frontend's node_modules costs a few MiB of its own instead of ~900 MB
// each. Exact, never a range: pnpm majors change settings' homes and
// defaults (11 stopped reading `overrides` from package.json; 12 fails an
// install on an unapproved build script).
const PnpmVersion = "12.15.0"

// BufVersion is the one buf release forge pins wherever buf's output must be
// reproducible: the scaffolded ci.yml installs it (bufbuild/buf-setup-action),
// and `forge ci gate` holds the buf on PATH to it before verify-generated and
// buf lint run. verify-generated regenerates the tree and demands identical
// bytes, so buf must not move under it. 1.50.0 is buf-setup-action@v1's own
// default. TestGateToolPinsMatchTheScaffoldedWorkflow
// (internal/templates/ci_gate_test.go) fails when the workflow names another.
const BufVersion = "1.50.0"

// KCLVersion is the one KCL release forge renders with and installs: the
// kcl-lang.io/kcl-go, kpm and lib modules in go.mod (the runtime forge
// embeds), the KCL CLI forge's own CI and scripts/bootstrap.sh install for
// the tests that shell out to `kcl`, and the version `forge doctor`'s install
// hint names. KCL releases all five repositories in lockstep, so one tag
// covers them. Scaffolded projects install no KCL at all: forge renders
// through the embedded runtime. TestKCLVersionIsPinnedEverywhere
// (internal/generator/kcl_pin_test.go) fails when any of those sites differs.
const KCLVersion = "v0.13.2"

// GovulncheckVersion is the govulncheck release `forge ci gate` installs
// (into forge's tools cache) when the one on PATH is not it.
//
// It has a FLOOR, not just a ceiling: govulncheck vendors golang.org/x/tools
// to build the SSA it traces calls through, and a copy older than the
// toolchain panics on syntax it cannot parse. v1.1.4 vendored x/tools v0.29.0
// and died with `panic: unexpected expr: *ast.KeyValueExpr` under Go 1.27 — a
// crash, not a finding, so the gate reported nothing at all. Keep this at or
// above the release that supports the newest Go a scaffolded go.mod declares.
const GovulncheckVersion = "v1.7.0"

// GoLicensesVersion is the go-licenses release `forge ci gate` installs when
// the one on PATH is not it.
const GoLicensesVersion = "v1.6.0"
