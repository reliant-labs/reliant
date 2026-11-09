#!/usr/bin/env bash
# Hermetic test of scripts/release.sh (phase 1: propose the version) over a
# throwaway checkout whose origin is a local bare repository. gh, task and yq are
# stubs; git, node and npm are real. "Published" means a release-* branch reached
# origin or gh was asked to open a PR; a refusal must do neither.
set -euo pipefail
SRC="$(cd "$(dirname "$0")" && pwd)"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail=0

mkdir "$T/bin"
cat >"$T/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >>"$GH_LOG"
EOF
cat >"$T/bin/task" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
# yq -r <expr> <file>: the changelog fixture is flat `key: value` lines.
cat >"$T/bin/yq" <<'EOF'
#!/usr/bin/env bash
case "$2" in
  *.version*) sed -n 's/^version: //p' "$3" ;;
  *.title*) sed -n 's/^title: //p' "$3" ;;
  *.summary*) sed -n 's/^summary: //p' "$3" ;;
  *) echo 1 ;;
esac
EOF
chmod +x "$T/bin/"*
export PATH="$T/bin:$PATH" GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 npm_config_update_notifier=false

# fixture <version> <commit subject>...: a fresh checkout at <version>, tagged
# v<version> (unless it is an RC), with the subjects merged on origin/main since.
fixture() {
  local version="$1"; shift
  R="$T/case$((++n))"; O="$R.origin"; export GH_LOG="$R.gh"; : >"$GH_LOG"
  git init -q --bare -b main "$O"
  git init -q -b main "$R"
  mkdir -p "$R/scripts" "$R/electron" "$R/docs/data/releases"
  cp "$SRC/release.sh" "$SRC/semver-next.sh" "$R/scripts/"
  printf '{\n  "name": "fixture",\n  "version": "%s"\n}\n' "$version" >"$R/electron/package.json"
  printf '{\n  "name": "fixture",\n  "version": "%s",\n  "lockfileVersion": 3,\n  "requires": true,\n  "packages": {\n    "": {\n      "name": "fixture",\n      "version": "%s"\n    }\n  }\n}\n' "$version" "$version" >"$R/electron/package-lock.json"
  echo "changelog" >"$R/docs/changelog.mdx"
  git -C "$R" add -A; git -C "$R" commit -q -m "chore: release v1.4.2"
  git -C "$R" tag v1.4.2
  local s; for s in "$@"; do git -C "$R" commit -q --allow-empty -m "$s"; done
  git -C "$R" remote add origin "$O"
  git -C "$R" push -q origin main --tags
}

# changelog <version>: the changelog source a final release requires.
changelog() {
  printf 'version: v%s\ntitle: t\nsummary: s\n' "$1" >"$R/docs/data/releases/v$1.yaml"
  git -C "$R" add docs; git -C "$R" commit -q -m "docs: changelog v$1"
  git -C "$R" push -q origin main
}

# expect_ok <want version> <release.sh args...>
expect_ok() {
  local want="$1" rc=0 got; shift
  (cd "$R" && bash scripts/release.sh "$@") >"$R.out" 2>&1 || rc=$?
  got="$(git -C "$O" for-each-ref --format='%(refname:short)' 'refs/heads/release-*')"
  if [ "$rc" != 0 ] || [ "$got" != "release-$want" ] || ! grep -q "chore: release v$want" "$GH_LOG" \
     || [ "$(node -p "require('$R/electron/package.json').version")" != "$want" ]; then
    echo "FAIL: release.sh $* -> exit $rc, pushed '$got' (want release-$want)"; cat "$R.out"; fail=1
  elif ! handoff_ok "$want"; then
    cat "$R.out"; fail=1
  else echo "ok: release.sh $* -> v$want"; fi
}

# handoff_ok <version>: the PR body (in the gh log) and the terminal output hand
# phase 2 to control-plane's rc-train, not to the person who ran release.sh.
handoff_ok() {
  local v="$1" pat
  for pat in "rc-train" "release-tag.sh v$v --commit <rc sha> --rc" "release:asap" \
             "nobody runs anything after it merges"; do
    grep -qF -- "$pat" "$GH_LOG" || { echo "FAIL: PR body lacks '$pat'"; return 1; }
  done
  for pat in "rc-train" "release:asap" "Nothing to run by hand"; do
    grep -qF -- "$pat" "$R.out" || { echo "FAIL: terminal guidance lacks '$pat'"; return 1; }
  done
  # The retired manual handoff: "After it merges, run: ./scripts/release-tag.sh"
  # in the body, and "2. ./scripts/release-tag.sh" as a next step.
  if grep -qF "After it merges, run:" "$GH_LOG"; then
    echo "FAIL: PR body tells the author to tag by hand"; return 1
  fi
  if grep -qE '[0-9]\. \./scripts/release-tag\.sh' "$R.out"; then
    echo "FAIL: terminal next steps tell the author to tag by hand"; return 1
  fi
}

# expect_refused <stderr pattern> <release.sh args...>
expect_refused() {
  local pat="$1" rc=0 got; shift
  (cd "$R" && bash scripts/release.sh "$@") >"$R.out" 2>&1 || rc=$?
  got="$(git -C "$O" for-each-ref --format='%(refname:short)' 'refs/heads/release-*')"
  if [ "$rc" = 0 ] || [ -n "$got" ] || [ -s "$GH_LOG" ] || ! grep -q -- "$pat" "$R.out" \
     || ! git -C "$R" diff --quiet; then
    echo "FAIL: release.sh $* -> exit $rc, pushed '$got', want a refusal matching '$pat'"; cat "$R.out"; fail=1
  else echo "ok: release.sh $* refused ($pat)"; fi
}

n=0
# Ordinary releases: a fix-only auto patch, a feature auto minor, a matching explicit bump.
fixture 1.4.2 "fix(api): a fix (#1)"; changelog 1.4.3
expect_ok 1.4.3
fixture 1.4.2 "fix: a fix (#1)" "feat(cli): a feature (#2)"; changelog 1.5.0
expect_ok 1.5.0 auto
fixture 1.4.2 "feat(cli): a feature (#2)"; changelog 1.5.0
expect_ok 1.5.0 minor

# An explicit bump the titles do not compute is the owner's alone.
fixture 1.4.2 "feat(cli): a feature (#2)"; changelog 1.4.3
expect_refused "compute v1.5.0" patch
fixture 1.4.2 "feat(cli): a feature (#2)"; changelog 1.4.3
expect_ok 1.4.3 patch --owner-override

# A breaking change refuses before anything is published, in every mode.
fixture 1.4.2 "refactor(core)!: drop the old flag (#5)"; changelog 1.5.0
expect_refused "(#5)"
fixture 1.4.2 "refactor(core)!: drop the old flag (#5)"
expect_refused "(#5)" prerelease
fixture 1.4.2 "fix: x (#6)

BREAKING CHANGE: the y field is gone"; changelog 2.0.0
expect_refused "(#6)" major
fixture 1.4.2 "refactor(core)!: drop the old flag (#5)"; changelog 2.0.0
expect_ok 2.0.0 major --owner-override

# Release candidates take their core version from the computed one.
fixture 1.4.2 "fix: a fix (#1)"
expect_ok 1.4.3-rc1 prerelease
fixture 1.4.2 "feat(cli): a feature (#2)"
expect_ok 1.5.0-rc1 prerelease
fixture 1.5.0-rc1 "feat(cli): a feature (#2)"
expect_ok 1.5.0-rc2 prerelease
# A stable current version equal to the computed one (its tag still pending) is
# the RC's core, not a reason to bump the patch again.
fixture 1.4.3 "fix: a fix (#1)"
expect_ok 1.4.3-rc1 prerelease
fixture 1.4.3-rc1 "fix: a fix (#1)" "feat(cli): a feature since the RC (#3)"
expect_ok 1.5.0-rc1 prerelease
# A break an earlier release candidate (rc/ tag) carried does not refuse again.
fixture 1.5.0-rc1 "refactor(core)!: drop the old flag (#5)"
git -C "$R" tag rc/v1.5.0-rc1; git -C "$R" push -q origin --tags
expect_ok 1.5.0-rc2 prerelease

exit "$fail"
