#!/usr/bin/env bash
# Hermetic self-test of scripts/release-sourcemaps.sh, release.yml's filing of the
# desktop renderer's source maps: a release's maps are pushed for frontend `desktop`,
# build id = the version the renderer reports, env `desktop`, project control-plane;
# a non-release version or a missing credential files nothing and does not fail;
# a renderer reporting another version, a failed push, or maps filed outside
# control-plane's subtree fail. A fake forge; no network.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-sourcemaps.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# The fake forge records its arguments and answers like `forge frontend sourcemaps
# push`, filing under FAKE_PROJECT (the project a forge actually used).
cat > "$fx/forge" << 'FORGE'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FORGE_CALLS"
[ "${FAKE_FAIL:-}" = 1 ] && { echo "Error: push refused"; exit 1; }
id=""; prev=""
for a in "$@"; do [ "$prev" = --build-id ] && id="$a"; prev="$a"; done
org=""; prev=""
for a in "$@"; do [ "$prev" = --org ] && org="$a"; prev="$a"; done
echo "  [sourcemaps] desktop: 3 source map(s) filed as registry.reliantapi.com/${FAKE_ORG:-$org}/${FAKE_PROJECT:-control-plane}/desktop-sourcemaps:$id (sha256:abc)"
FORGE
chmod +x "$fx/forge"
mkdir -p "$fx/dist"
echo '{"version":"1.2.3"}' > "$fx/dist/version.json"
export FORGE="$fx/forge" FORGE_CALLS="$fx/calls" RELIANT_DESKTOP_SOURCEMAPS_ORG=org-1

run() { : > "$fx/calls"; bash "$src" "$@" > "$fx/out" 2>&1; }

# 1. A release with the credential: one push, with exactly these arguments.
RELIANT_DESKTOP_SOURCEMAPS_TOKEN=rlat_test run v1.2.3 "$fx/dist" || fail "a release push failed: $(cat "$fx/out")"
want="frontend sourcemaps push $fx/dist --frontend desktop --build-id 1.2.3 --env desktop --project control-plane --org org-1"
[ "$(cat "$fx/calls")" = "$want" ] || fail "pushed with '$(cat "$fx/calls")', want '$want'"
echo "ok: a release files its maps as desktop:<version> under control-plane"

# 2. A prerelease tag files under its own version.
echo '{"version":"1.2.3-rc1"}' > "$fx/dist/version.json"
RELIANT_DESKTOP_SOURCEMAPS_TOKEN=rlat_test run v1.2.3-rc1 "$fx/dist" || fail "a prerelease push failed: $(cat "$fx/out")"
grep -q -- "--build-id 1.2.3-rc1 " "$fx/calls" || fail "prerelease pushed with $(cat "$fx/calls")"
echo "ok: a prerelease files under its own version"
echo '{"version":"1.2.3"}' > "$fx/dist/version.json"

# 3. Not a release: nothing filed, not a failure.
RELIANT_DESKTOP_SOURCEMAPS_TOKEN=rlat_test run dev-abc1234 "$fx/dist" || fail "a dev build failed: $(cat "$fx/out")"
[ ! -s "$fx/calls" ] || fail "a dev build pushed: $(cat "$fx/calls")"
echo "ok: a non-release version files nothing"

# 4. No credential: a warning, nothing filed, not a failure (the release still ships).
RELIANT_DESKTOP_SOURCEMAPS_TOKEN="" run v1.2.3 "$fx/dist" || fail "a missing credential failed the step: $(cat "$fx/out")"
[ ! -s "$fx/calls" ] || fail "pushed without a credential"
grep -q '::warning::RELIANT_DESKTOP_SOURCEMAPS_TOKEN is not set' "$fx/out" || fail "no warning without a credential: $(cat "$fx/out")"
echo "ok: a missing credential warns and files nothing"

# 5. The renderer must report the version, or the maps are filed where nothing looks.
! RELIANT_DESKTOP_SOURCEMAPS_TOKEN=rlat_test run v1.2.4 "$fx/dist" || fail "filed maps for a renderer reporting another version"
[ ! -s "$fx/calls" ] || fail "pushed before checking the renderer's version"
grep -q "reports version '1.2.3', not 1.2.4" "$fx/out" || fail "unexplained refusal: $(cat "$fx/out")"
echo "ok: a renderer reporting another version is refused before any push"

# 6. A failed push fails, with forge's output shown.
! FAKE_FAIL=1 RELIANT_DESKTOP_SOURCEMAPS_TOKEN=rlat_test run v1.2.3 "$fx/dist" || fail "a failed push passed"
grep -q 'push refused' "$fx/out" || fail "forge's error was not shown: $(cat "$fx/out")"
echo "ok: a failed push fails and shows why"

# 7. Maps filed outside control-plane's subtree (a forge ignoring --project) fail.
! FAKE_PROJECT=reliant RELIANT_DESKTOP_SOURCEMAPS_TOKEN=rlat_test run v1.2.3 "$fx/dist" || fail "maps filed under reliant/ passed"
grep -q 'not filed under org-1/control-plane/desktop-sourcemaps' "$fx/out" || fail "unexplained failure: $(cat "$fx/out")"
! FAKE_ORG=org-2 RELIANT_DESKTOP_SOURCEMAPS_TOKEN=rlat_test run v1.2.3 "$fx/dist" || fail "maps filed under another org passed"
echo "ok: maps filed where the issues worker does not look fail"

# 7b. No org: a registry-only token cannot look it up, so nothing is filed, with a warning.
RELIANT_DESKTOP_SOURCEMAPS_ORG="" RELIANT_DESKTOP_SOURCEMAPS_TOKEN=rlat_test run v1.2.3 "$fx/dist" || fail "a missing org failed the step: $(cat "$fx/out")"
[ ! -s "$fx/calls" ] || fail "pushed without an org"
grep -q '::warning::RELIANT_DESKTOP_SOURCEMAPS_ORG is not set' "$fx/out" || fail "no warning without an org: $(cat "$fx/out")"
echo "ok: a missing org warns and files nothing"

# 8. Without FORGE, the forge forge.yaml pins (never one from PATH).
mkdir -p "$fx/repo"
echo 'forge_version: v9.9.9-0.20261110000000-abcdefabcdef' > "$fx/repo/forge.yaml"
cat > "$fx/go" << 'GO'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FORGE_CALLS"
echo "  [sourcemaps] desktop: 1 source map(s) filed as r/org-1/control-plane/desktop-sourcemaps:1.2.3 (sha256:x)"
GO
chmod +x "$fx/go"
: > "$fx/calls"
(cd "$fx/repo" && PATH="$fx:$PATH" FORGE="" RELIANT_DESKTOP_SOURCEMAPS_TOKEN=rlat_test bash "$src" v1.2.3 "$fx/dist" > "$fx/out" 2>&1) ||
  fail "the pinned-forge default failed: $(cat "$fx/out")"
grep -q '^run github.com/reliant-labs/forge/cmd/forge@v9.9.9-0.20261110000000-abcdefabcdef frontend sourcemaps push ' "$fx/calls" ||
  fail "did not run the pinned forge: $(cat "$fx/calls")"
echo "ok: the default forge is forge.yaml's pin"

echo "PASS: release-sourcemaps.sh"
