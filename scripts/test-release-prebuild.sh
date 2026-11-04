#!/usr/bin/env bash
# Hermetic self-test of scripts/release-prebuild.sh, the decisions behind release.yml's
# Electron prebuild reuse: the marker carries the renderer's identity and refuses a
# renderer that does not report the version; a tag reuses only a successful prebuild of
# exactly its version on exactly its commit, with all three platforms' artifacts, and
# builds otherwise; publishing refuses a prebuild missing any update feed its clients
# poll (Windows' <channel>.yml, both Linux architectures), uploads feeds last, and a dry
# run uploads nothing. A fake gh and a fake aws; no network.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-prebuild.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
mkdir -p "$fx/bin" "$fx/runs"
# The fake gh answers from $fx/runs: runs/<id>/artifacts lists the run's artifact
# names, runs/<id>/prebuild.json is its marker; RUNS lists the successful runs.
cat > "$fx/bin/gh" << 'GH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$GH_CALLS"
case "$*" in
  "run list -R acme/app --workflow release.yml --event workflow_dispatch --commit $SHA "*) printf '%s\n' $RUNS ;;
  "run list "*) ;; # another commit: no runs
  "api repos/acme/app/actions/runs/"*"/artifacts"*)
    id="${2#repos/acme/app/actions/runs/}"; id="${id%%/*}"
    cat "$FX/runs/$id/artifacts" ;;
  "run download "*)
    id="$3"; dir=""; prev=""
    for a in "$@"; do [ "$prev" = -D ] && dir="$a"; prev="$a"; done
    mkdir -p "$dir" && cp "$FX/runs/$id/prebuild.json" "$dir/" ;;
  *) echo "unexpected gh: $*" >&2; exit 90 ;;
esac
GH
cat > "$fx/bin/aws" << 'AWS'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$AWS_CALLS"
AWS
chmod +x "$fx/bin/gh" "$fx/bin/aws"
sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export PATH="$fx/bin:$PATH" FX="$fx" GH_CALLS="$fx/gh-calls" AWS_CALLS="$fx/aws-calls" SHA="$sha"
fail() { echo "FAIL: $*" >&2; exit 1; }
all='prebuild-v1.2.3
macos-builds
linux-builds
windows-builds'
# run <id> <artifact names> <marker json>
run() { mkdir -p "$fx/runs/$1"; printf '%s\n' "$2" > "$fx/runs/$1/artifacts"; printf '%s\n' "$3" > "$fx/runs/$1/prebuild.json"; }
good="{\"version\":\"v1.2.3\",\"sha\":\"$sha\",\"renderer\":\"1.2.3\"}"
# find <want> <name> <runs> [tag] [sha]
find_is() {
  local got
  got="$(RUNS="$3" bash "$src" find acme/app "${4:-v1.2.3}" "${5:-$sha}" 2> "$fx/err")" || fail "$2: find failed: $(cat "$fx/err")"
  [ "$got" = "$1" ] || fail "$2: find printed '$got', want '$1' ($(cat "$fx/err"))"
  echo "ok: $2"
}

# 1. The marker records the renderer identity, and refuses one that is not the version.
mkdir -p "$fx/dist"
echo '{"version":"1.2.3","commit":"x"}' > "$fx/dist/version.json"
bash "$src" marker v1.2.3 "$sha" "$fx/dist" "$fx/m/prebuild.json"
[ "$(jq -c . "$fx/m/prebuild.json")" = "$good" ] || fail "marker: $(cat "$fx/m/prebuild.json")"
echo "ok: the marker names the version, the commit and the renderer identity"
echo '{"version":"1.2.3-dev+aaaaaaaa"}' > "$fx/dist/version.json"
! bash "$src" marker v1.2.3 "$sha" "$fx/dist" "$fx/m2.json" 2> /dev/null || fail "marker accepted a dev renderer"
rm "$fx/dist/version.json"
! bash "$src" marker v1.2.3 "$sha" "$fx/dist" "$fx/m2.json" 2> /dev/null || fail "marker accepted a renderer with no version.json"
! bash "$src" marker v1.2.3-rc.1 "$sha" "$fx/dist" "$fx/m2.json" 2> /dev/null || fail "marker accepted a prerelease version"
echo "ok: the marker refuses a renderer that does not report the version"

# 2. Selection: exact version, exact commit, matching renderer, every platform.
run 11 "$all" "$good"
find_is 11 "a matching prebuild on the tag's commit is reused" 11
find_is "" "nothing on another commit: build" 11 v1.2.3 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
find_is "" "no successful prebuild run: build" ""
run 12 "prebuild-v1.2.2
macos-builds
linux-builds
windows-builds" "$good"
find_is "" "a prebuild of another version is not reused" 12
run 13 "$all" "{\"version\":\"v1.2.3\",\"sha\":\"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"renderer\":\"1.2.3\"}"
find_is "" "a marker naming another commit is not reused" 13
run 14 "$all" "{\"version\":\"v1.2.3\",\"sha\":\"$sha\",\"renderer\":\"1.2.3-dev+aaaaaaaa\"}"
find_is "" "a marker whose renderer is not the version is not reused" 14
run 15 "prebuild-v1.2.3
macos-builds
linux-builds" "$good"
find_is "" "a prebuild missing a platform's artifacts is not reused" 15
find_is 11 "a bad run is skipped for a good one" "15 13 11"
find_is 11 "a dry run (another dispatch) without a marker is skipped" "$(run 16 'macos-builds' '{}'; echo 16 11)"

# 3. Publishing: every feed required, feeds last, dry run uploads nothing.
# dist <platform dir> <file...>: files with a version line for the feeds.
dist() { local d="$fx/$1"; shift; rm -rf "$d"; mkdir -p "$d"; for f in "$@"; do
  case "$f" in *.yml) printf 'version: 1.2.3\nfiles: []\n' > "$d/$f" ;; *) : > "$d/$f" ;; esac; done; }
publish() { : > "$AWS_CALLS"; R2_ENDPOINT=https://r2.invalid bash "$src" publish "$@" > "$fx/out" 2>&1; }
dist win Reliant-1.2.3-win-x64.exe Reliant-1.2.3-win-x64.exe.blockmap latest.yml builder-debug.yml
publish win latest v1.2.3 "$fx/win" || fail "win publish: $(cat "$fx/out")"
[ "$(wc -l < "$AWS_CALLS")" = 3 ] || fail "win: want 3 uploads: $(cat "$AWS_CALLS")"
tail -1 "$AWS_CALLS" | grep -q '^s3 cp latest.yml s3://downloads/latest.yml ' || fail "win: the feed is not uploaded last: $(cat "$AWS_CALLS")"
! grep -q builder-debug "$AWS_CALLS" || fail "win: published builder-debug.yml"
echo "ok: Windows publishes its <channel>.yml feed, last"
dist win Reliant-1.2.3-win-x64.exe latest-win.yml
! publish win latest v1.2.3 "$fx/win" || fail "win published without latest.yml"
[ ! -s "$AWS_CALLS" ] || fail "win uploaded files before refusing: $(cat "$AWS_CALLS")"
echo "ok: Windows refuses a prebuild without latest.yml, uploading nothing"
dist linux Reliant-1.2.3-linux-x86_64.AppImage Reliant-1.2.3-linux-arm64.AppImage Reliant-1.2.3-linux-amd64.deb latest-linux.yml latest-linux-arm64.yml
publish linux latest v1.2.3 "$fx/linux" || fail "linux publish: $(cat "$fx/out")"
tail -2 "$AWS_CALLS" | cut -d' ' -f3 | sort | tr '\n' ' ' | grep -qx 'latest-linux-arm64.yml latest-linux.yml ' || fail "linux: feeds not last: $(cat "$AWS_CALLS")"
echo "ok: Linux publishes both architectures' feeds, last"
rm "$fx/linux/latest-linux-arm64.yml"
! publish linux latest v1.2.3 "$fx/linux" || fail "linux published without the arm64 feed"
[ ! -s "$AWS_CALLS" ] || fail "linux uploaded files before refusing"
echo "ok: Linux refuses a prebuild without the arm64 feed"
dist mac Reliant-1.2.3-mac-arm64.dmg Reliant-1.2.3-mac-arm64.zip alpha-mac.yml
! publish mac latest v1.2.3 "$fx/mac" || fail "mac published another channel's feed"
dist mac Reliant-1.2.3-mac-arm64.dmg Reliant-1.2.3-mac-arm64.zip latest-mac.yml
printf 'version: 1.2.2\n' > "$fx/mac/latest-mac.yml"
! publish mac latest v1.2.3 "$fx/mac" || fail "mac published a feed of another version"
grep -q "names version '1.2.2'" "$fx/out" || fail "the version refusal does not say why: $(cat "$fx/out")"
echo "ok: a feed of another channel or version is refused"
dist mac Reliant-1.2.3-mac-arm64.dmg Reliant-1.2.3-mac-arm64.zip latest-mac.yml
: > "$AWS_CALLS"
DRY_RUN=true bash "$src" publish mac latest v1.2.3 "$fx/mac" > "$fx/out" 2>&1 || fail "mac dry run: $(cat "$fx/out")"
[ ! -s "$AWS_CALLS" ] || fail "a dry run uploaded: $(cat "$AWS_CALLS")"
[ "$(grep -c '^publish ' "$fx/out")" = 3 ] || fail "a dry run does not print its plan: $(cat "$fx/out")"
echo "ok: a dry run prints the plan and uploads nothing"
echo "PASS: release-prebuild.sh"
