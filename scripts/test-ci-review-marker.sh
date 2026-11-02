#!/usr/bin/env bash
# Hermetic self-test of scripts/ci-review-marker.sh (the `Reliant review` required
# check): an exact-head pass marker by a pipeline reviewer passes a branch of this
# repository, and nothing passes a fork's head (control-plane finding CPI-1). A
# fake gh answers from fixed JSON; no network.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-review-marker.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
mkdir -p "$fx/bin"
cat > "$fx/bin/gh" << 'GH'
#!/usr/bin/env bash
jqexpr=""; prev=""
for a in "$@"; do [ "$prev" = --jq ] && jqexpr="$a"; prev="$a"; done
case "$*" in
  *"/reviews"*) printf '%s\n' "$REVIEWS" ;;
  "api repos/acme/app/pulls/7"*) printf '%s\n' "$PR" | jq -r "$jqexpr" ;;
  *) echo "unexpected gh: $*" >&2; exit 90 ;;
esac
GH
chmod +x "$fx/bin/gh"
export PATH="$fx/bin:$PATH" GITHUB_REPOSITORY=acme/app
fail() { echo "FAIL: $*" >&2; exit 1; }
head=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
pass="[{\"user\":{\"id\":186876276},\"commit_id\":\"$head\",\"body\":\"<!-- review-passed sha=$head -->\"}]"
# check <want exit> <name> <head repo json> <reviews json>
check() {
  local got=0
  PR="{\"head\":{\"sha\":\"$head\",\"repo\":$3}}" REVIEWS="$4" bash "$src" pr 7 > "$fx/out" 2>&1 || got=$?
  [ "$got" = "$1" ] || fail "$2: exit $got, want $1: $(cat "$fx/out")"
  echo "ok: $2"
}
check 0 "a branch of this repository with the reviewer's marker passes" '{"full_name":"acme/app"}' "$pass"
check 1 "a branch of this repository without a marker fails" '{"full_name":"acme/app"}' '[]'
check 1 "a fork's head fails even with the reviewer's marker" '{"full_name":"stranger/app"}' "$pass"
grep -q 'a fork' "$fx/out" || fail "the fork refusal does not say why: $(cat "$fx/out")"
check 1 "a deleted fork's head (null repo) fails even with the reviewer's marker" 'null' "$pass"
