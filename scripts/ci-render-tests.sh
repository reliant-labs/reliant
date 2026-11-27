#!/usr/bin/env bash
# The full-mode KCL render tests, when a change touches what they render: the
# scaffolded KCL templates or the composition-root (config_gen.k) emitters.
# ci.yml's test-short job runs it in the merge tier, after the -short tier,
# and scripts/ci-premerge.sh (`task ci`) runs it locally.
#
#   scripts/ci-render-tests.sh [--plan] [BASE]
#       BASE defaults to $CI_BASE, else the merge base of HEAD and origin/main.
#       --plan prints what changed and which tests would run, and runs nothing.
#
# WHY. forge main went red three times on 2026-11-21/22 for one class of
# change. #2683 added fields to internal/templates/project/config-roots.k.tmpl
# and to codegen's rootsKCLData; a test helper rendered that template with an
# input of its own that lacked them, and every scaffold → KCL render test in
# internal/templates failed. Every one of them is full mode only
# (testkit.Slow, or a testing.Short() skip: each scaffolds a project, which
# execs `go env GOVERSION`), and the merge tier ran only -short, so the queue
# proved nothing and main found it. The renders themselves are in-process
# (internal/kclrender with the embedded forge module): no network, no kcl
# CLI.
#
# WHAT IT COSTS. Nothing for a change that touches no trigger path (about 2%
# of merges touched one, 2026-10-22..11-22). Otherwise the tests below run
# once in full mode, without -race (main's `test` job runs them under it):
# 1.5-2.5 minutes on a loaded 48-core dev machine (2026-11-22), nearly all of
# it run time: the -short tier just compiled the same packages with the same
# flags. The merge tier's critical path is windows-changed (9-13
# minutes), which runs beside test-short.
#
# TRIGGERS are git pathspecs; TESTS are <package dir>.<TestName>, run with an
# anchored -run per package. A test listed here that the package no longer
# declares fails the script (and internal/ciguard's -short guard), so a rename
# cannot turn this step into a vacuous pass.
set -euo pipefail

triggers=(
  # The project templates rendered as KCL (config-roots.k.tmpl).
  ':(glob)internal/templates/project/**/*.k.tmpl'
  # The env main.k, ingress, identity and workload templates.
  ':(glob)internal/templates/deploy/kcl/**'
  # config_gen.k's writers: the composition roots and the config projection.
  ':(glob)internal/codegen/composition_roots*'
  ':(glob)internal/codegen/config_projection*'
  'internal/codegen/config_native_emit.go'
  # The render tests' own fixtures and files: the flake gate runs edited tests
  # under -short, where these skip.
  'internal/templates/roots_stub_test.go'
  'internal/templates/scaffold_hosted_envs_test.go'
  'internal/templates/scaffold_identity_test.go'
  'internal/templates/scaffold_kcl_eval_test.go'
  'internal/templates/scaffold_object_storage_test.go'
  'internal/templates/release_scaffold_kcl_render_test.go'
)

tests=(
  # A scaffolded project's envs, rendered (internal/templates).
  internal/templates.TestFreshScaffoldRendersEveryEnvHosted
  internal/templates.TestHostedFrontendReachesEveryService
  internal/templates.TestFreshOperatorRendersUntilOnlyTheClusterIsMissing
  internal/templates.TestScaffoldedClusterBinderRefusesUntilDeclared
  internal/templates.TestDevSignInReachesTheAPI
  internal/templates.TestDevObservabilityIsOnByDefaultAndDeliversTelemetry
  internal/templates.TestDevHostProcessesGetTheirOwnPprofPort
  internal/templates.TestReleaseBuildScaffoldResolvesAndRenders
  internal/templates.TestScaffoldedIngressEvaluates
  internal/templates.TestScaffoldedDevObjectStorage
  internal/templates.TestScaffoldDeclaresManagedIdentityForEveryEnv
  internal/templates.TestScaffoldWithoutFrontendDeclaresNoIdentity
  internal/templates.TestScaffoldAuthNoneDeclaresNoIdentity
  # config_gen.k's roots, rendered through the native runtime (internal/codegen).
  # The kcl-CLI tests beside them skip without the CLI, so they are not here.
  internal/codegen.TestPerBinaryRoots_AppConfigBesideACommandConfigRenders
  internal/codegen.TestPerBinaryRoots_UnavailableFailsTheRenderNamingTheFix
  internal/codegen.TestPerBinaryKCL_UnsetOptionalConfigRendersAsItsDefault
  internal/codegen.TestRootsKCL_WorkloadEnvOverridesAProjectedString
)

plan=false
if [[ "${1:-}" == "--plan" ]]; then
  plan=true
  shift
fi
[[ $# -le 1 ]] || { echo "usage: $0 [--plan] [BASE]" >&2; exit 2; }

cd "$(git rev-parse --show-toplevel)"
if [[ -n "${1:-}" ]]; then
  base=$1
elif [[ -n "${CI_BASE:-}" ]]; then
  base=$CI_BASE
else
  base=$(git merge-base origin/main HEAD) || { echo "ci-render-tests: no merge base with origin/main; pass BASE" >&2; exit 2; }
fi
git rev-parse -q --verify "$base^{commit}" >/dev/null || { echo "ci-render-tests: cannot resolve base '$base' (fetch it)" >&2; exit 2; }

changed=$(git diff --name-only --no-renames "$base" HEAD -- "${triggers[@]}") ||
  { echo "ci-render-tests: git diff failed; cannot tell whether a KCL template changed" >&2; exit 2; }
if [[ -z "$changed" ]]; then
  echo "ci-render-tests: no KCL template or composition-root change since $(git rev-parse --short "$base"): nothing to run"
  exit 0
fi

# One anchored -run regex per package, in the order the packages first appear.
declare -A names=()
pkgs=()
for t in "${tests[@]}"; do
  pkg=${t%.*}
  if [[ -z "${names[$pkg]:-}" ]]; then
    pkgs+=("$pkg")
    names[$pkg]=${t##*.}
  else
    names[$pkg]+="|${t##*.}"
  fi
done

echo "ci-render-tests: the change touches what the full-mode KCL render tests render:"
while read -r f; do echo "  $f"; done <<<"$changed"
for pkg in "${pkgs[@]}"; do
  echo "./$pkg: ^(${names[$pkg]})\$"
done
if $plan; then
  exit 0
fi

status=0
for pkg in "${pkgs[@]}"; do
  regex="^(${names[$pkg]})\$"
  # Every listed test must still exist: -run over a renamed test selects
  # nothing and passes. -list builds the same binary the run below uses.
  listed=$(go test -list "$regex" "./$pkg") || { echo "ci-render-tests: go test -list ./$pkg failed" >&2; exit 1; }
  IFS='|' read -ra want <<<"${names[$pkg]}"
  for name in "${want[@]}"; do
    if ! grep -qx "$name" <<<"$listed"; then
      echo "ci-render-tests: ./$pkg declares no $name: update the tests list in $0" >&2
      status=1
    fi
  done
done
((status == 0)) || exit "$status"

# Full mode: no -short, no -race (main's `test` runs these under it). No
# -count=1 either: what these tests read — the templates, the forge KCL
# module, codegen — is compiled or go:embed'ded into the test binary, so a
# trigger change is a new binary and a real run, and an unchanged one may
# replay its result (`task ci` run twice). go test takes one -run, so each
# package is its own invocation, through the wrapper the -short tier uses.
for pkg in "${pkgs[@]}"; do
  scripts/ci-go-test.sh -timeout 10m -run "^(${names[$pkg]})\$" "./$pkg" || status=1
done
exit "$status"
