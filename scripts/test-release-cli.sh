#!/usr/bin/env bash
# Hermetic self-test of scripts/release-cli.sh, the decisions behind release.yml's
# standalone CLI assets and daemon-channels.json: the six archives hold the
# build-backend binaries under the CLI's name and are reproducible; verify refuses a
# missing, extra, tampered or binary-less archive; the manifest points one channel at
# this release, keeps the other, and never moves a channel backwards; reading the
# published manifest tells "none yet" from "could not read"; publishing uploads the
# mutable manifest last and a dry run uploads nothing; verify-published reads what a
# client reads. A fake aws and a fake curl; no network.
set -euo pipefail
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-cli.sh"
fx="$(mktemp -d)"
trap 'rm -rf "$fx"' EXIT
mkdir -p "$fx/bin" "$fx/r2" "$fx/gh"
# The fake aws is R2: $FX/r2/<key>. head-object fails with a 403 when
# AWS_HEAD=error; uploads are recorded in $AWS_CALLS and stored.
cat > "$fx/bin/aws" << 'AWS'
#!/usr/bin/env bash
case "$1 $2" in
  "s3api head-object")
    [ "${AWS_HEAD:-}" = error ] && { echo "An error occurred (403) when calling the HeadObject operation: Forbidden" >&2; exit 254; }
    [ -f "$FX/r2/$6" ] && exit 0
    echo "An error occurred (404) when calling the HeadObject operation: Not Found" >&2; exit 254 ;;
  "s3 cp")
    if [[ "$3" == s3://* ]]; then cp "$FX/r2/${3#s3://downloads/}" "$4" 2> /dev/null; exit; fi
    printf '%s\n' "$*" >> "$AWS_CALLS"
    k="${4#s3://downloads/}"; mkdir -p "$(dirname "$FX/r2/$k")"; cp "$3" "$FX/r2/$k" ;;
  *) echo "unexpected aws: $*" >&2; exit 90 ;;
esac
AWS
# The fake gh is the GitHub Release of each tag: $FX/gh/<tag>/. An upload fails,
# storing nothing, when GH_UPLOAD_FAIL=1.
cat > "$fx/bin/gh" << 'GH'
#!/usr/bin/env bash
d="$FX/gh/$3"
case "$1 $2" in
  "release view") [ -d "$d" ] || exit 1
    (cd "$d" && find . -type f | sed 's|^\./||') | jq -Rn '{assets: [inputs | {name: .}]}' ;;
  "release download") shift 3; out=""; pats=()
    while [ $# -gt 0 ]; do case "$1" in --dir) out="$2"; shift 2 ;; --pattern) pats+=("$2"); shift 2 ;; *) shift ;; esac; done
    mkdir -p "$out"; for p in "${pats[@]}"; do [ -f "$d/$p" ] && cp "$d/$p" "$out/"; done; exit 0 ;;
  "release upload") [ "${GH_UPLOAD_FAIL:-}" = 1 ] && { echo "HTTP 502" >&2; exit 1; }
    shift 3; clobber=""; [ "$1" = --clobber ] && { clobber=1; shift; }
    for f in "$@"; do [ -e "$d/$(basename "$f")" ] && [ -z "$clobber" ] && { echo "asset exists" >&2; exit 1; }; done
    for f in "$@"; do printf 'upload %s\n' "$(basename "$f")" >> "$GH_CALLS"; cp "$f" "$d/"; done ;;
  *) echo "unexpected gh: $*" >&2; exit 90 ;;
esac
GH
# The fake curl: https://dl.test/ is the CDN in front of R2, https://gh.test/ is
# github.com's release downloads. With -w it prints the status.
cat > "$fx/bin/curl" << 'CURL'
#!/usr/bin/env bash
url="${*: -1}" out="" prev=""
case "$url" in
  https://dl.test/*) f="$FX/r2/${url#https://dl.test/}" ;;
  https://gh.test/acme/app/releases/download/*) f="${url#https://gh.test/acme/app/releases/download/}"; f="$FX/gh/$f" ;;
  *) exit 6 ;;
esac
for a in "$@"; do [ "$prev" = -o ] && out="$a"; prev="$a"; done
case " $* " in
  *" -w "*) if [ -f "$f" ]; then printf 200; else printf 404; fi ;;
  *) [ -f "$f" ] || exit 22; if [ -n "$out" ]; then cp "$f" "$out"; else cat "$f"; fi ;;
esac
CURL
chmod +x "$fx/bin/aws" "$fx/bin/gh" "$fx/bin/curl"
export PATH="$fx/bin:$PATH" FX="$fx" AWS_CALLS="$fx/aws-calls" GH_CALLS="$fx/gh-calls" BASE_URL=https://dl.test
fail() { echo "FAIL: $*" >&2; exit 1; }
sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
sha2=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
targets="linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 windows-arm64"

# 1. Packaging: build-backend's six binaries become the six CLI archives.
for t in $targets; do
  mkdir -p "$fx/backend/backend-$t"
  b=reliant-backend
  [[ "$t" == windows-* ]] && b=reliant-backend.exe
  printf 'binary %s\n' "$t" > "$fx/backend/backend-$t/$b"
done
SOURCE_DATE_EPOCH=1700000000 bash "$src" package "$fx/backend" "$fx/dist" 2> "$fx/err" || fail "package: $(cat "$fx/err")"
[ "$(wc -l < "$fx/dist/checksums.txt")" = 6 ] || fail "checksums.txt: $(cat "$fx/dist/checksums.txt")"
bash "$src" verify "$fx/dist" || fail "verify refused what package wrote"
[ "$(tar -xzOf "$fx/dist/reliant_windows_arm64.tar.gz" reliant.exe)" = "binary windows-arm64" ] || fail "the windows/arm64 archive does not hold its backend as reliant.exe"
[ "$(tar -tzf "$fx/dist/reliant_darwin_amd64.tar.gz" | sort | tr '\n' ' ')" = "LICENSE LICENSES.txt reliant " ] ||
  fail "darwin/amd64 holds $(tar -tzf "$fx/dist/reliant_darwin_amd64.tar.gz" | tr '\n' ' ')"
echo "ok: each archive holds its target's backend as reliant, with the licenses"
if tar --version 2> /dev/null | grep 'GNU tar' > /dev/null; then
  # A different mtime on an input must not change the archive.
  touch -d @1600000000 "$fx/backend/backend-linux-amd64/reliant-backend"
  SOURCE_DATE_EPOCH=1700000000 bash "$src" package "$fx/backend" "$fx/dist2" 2> /dev/null
  cmp -s "$fx/dist/checksums.txt" "$fx/dist2/checksums.txt" || fail "packaging the same binaries twice differs"
  echo "ok: packaging is reproducible"
fi
mv "$fx/backend/backend-darwin-arm64" "$fx/backend-darwin-arm64.away"
! bash "$src" package "$fx/backend" "$fx/dist3" 2> "$fx/err" || fail "package wrote archives without darwin/arm64"
grep -q 'backend-darwin-arm64' "$fx/err" || fail "the missing-target refusal does not name it: $(cat "$fx/err")"
[ ! -e "$fx/dist3/checksums.txt" ] || fail "a failed package left a checksums.txt"
mv "$fx/backend-darwin-arm64.away" "$fx/backend/backend-darwin-arm64"
echo "ok: a missing target fails packaging and writes no checksums.txt"

# 2. Verify refuses anything but exactly the six, matching, binary-holding archives.
refuse() { # refuse <what> <dir>
  ! bash "$src" verify "$2" 2> "$fx/err" || fail "verify accepted $1"
  echo "ok: verify refuses $1"
}
cp -r "$fx/dist" "$fx/v1" && printf 'x' >> "$fx/v1/reliant_linux_amd64.tar.gz"
refuse "a tampered archive" "$fx/v1"
cp -r "$fx/dist" "$fx/v2" && rm "$fx/v2/reliant_darwin_arm64.tar.gz"
refuse "a missing archive" "$fx/v2"
cp -r "$fx/dist" "$fx/v3" && cp "$fx/v3/reliant_linux_amd64.tar.gz" "$fx/v3/reliant_linux_386.tar.gz"
refuse "an extra archive" "$fx/v3"
cp -r "$fx/dist" "$fx/v4" && sed -i.bak '/windows_amd64/d' "$fx/v4/checksums.txt" && rm "$fx/v4/checksums.txt.bak"
refuse "checksums.txt missing an archive" "$fx/v4"
cp -r "$fx/dist" "$fx/v5" && mkdir "$fx/v5x" && echo x > "$fx/v5x/other" &&
  tar -C "$fx/v5x" -czf "$fx/v5/reliant_linux_arm64.tar.gz" other &&
  sed -i.bak "s/^[0-9a-f]*  reliant_linux_arm64.tar.gz\$/$(sha256sum "$fx/v5/reliant_linux_arm64.tar.gz" | cut -d' ' -f1)  reliant_linux_arm64.tar.gz/" "$fx/v5/checksums.txt"
refuse "an archive without its binary" "$fx/v5"

# 3. The attestation predicate names the repository, the release and the commit.
[ "$(bash "$src" predicate acme/app v1.2.3 "$sha")" = "{\"repository\":\"acme/app\",\"release\":\"v1.2.3\",\"commit\":\"$sha\"}" ] ||
  fail "predicate: $(bash "$src" predicate acme/app v1.2.3 "$sha")"
! bash "$src" predicate acme/app v1.2.3 abc 2> /dev/null || fail "predicate accepted a short commit"
! bash "$src" predicate acme/app 1.2.3 "$sha" 2> /dev/null || fail "predicate accepted a tag without v"
echo "ok: the predicate is {repository, release, commit}"

# 4. The manifest.
export GITHUB_REPOSITORY=acme/app
bash "$src" manifest "$fx/none.json" latest v1.2.3 "$sha" 1700000000 "$fx/dist" > "$fx/m1.json" 2> "$fx/err" || fail "manifest: $(cat "$fx/err")"
jq -e --arg sha "$sha" '
  .schema == 1 and .repository == "acme/app"
  and .written_by == {tag: "v1.2.3", commit: $sha, bundle: {url: "https://dl.test/cli/v1.2.3/reliant-release.sigstore.json"}}
  and (.channels | keys) == ["latest"]
  and .channels.latest.version == "1.2.3" and .channels.latest.tag == "v1.2.3"
  and .channels.latest.commit == $sha and .channels.latest.commit_time_unix == 1700000000
  and .channels.latest.checksums.url == "https://dl.test/cli/v1.2.3/checksums.txt"
  and .channels.latest.bundle.url == "https://dl.test/cli/v1.2.3/reliant-release.sigstore.json"
  and (.channels.latest.platforms | keys) == ["darwin/amd64","darwin/arm64","linux/amd64","linux/arm64","windows/amd64","windows/arm64"]
  and .channels.latest.platforms["windows/arm64"].url == "https://dl.test/cli/v1.2.3/reliant_windows_arm64.tar.gz"' "$fx/m1.json" > /dev/null ||
  fail "the first manifest: $(cat "$fx/m1.json")"
[ "$(jq -r '.channels.latest.checksums.sha256' "$fx/m1.json")" = "$(sha256sum "$fx/dist/checksums.txt" | cut -d' ' -f1)" ] || fail "checksums.txt's sha256"
while read -r sum name; do
  os_arch="$(sed -E 's/^reliant_([a-z]+)_([a-z0-9]+)\.tar\.gz$/\1\/\2/' <<< "$name")"
  [ "$(jq -r --arg k "$os_arch" '.channels.latest.platforms[$k].sha256' "$fx/m1.json")" = "$sum" ] || fail "$os_arch's sha256"
done < "$fx/dist/checksums.txt"
echo "ok: the first manifest points latest at this release's archives, with their checksums"
bash "$src" manifest "$fx/m1.json" alpha v1.2.4-rc.1 "$sha2" 1700000100 "$fx/dist" > "$fx/m2.json" 2> /dev/null || fail "an alpha release after latest"
jq -e --slurpfile m1 "$fx/m1.json" '.channels.latest == $m1[0].channels.latest and .channels.alpha.tag == "v1.2.4-rc.1" and .written_by.tag == "v1.2.4-rc.1"' "$fx/m2.json" > /dev/null ||
  fail "an alpha release changed latest: $(cat "$fx/m2.json")"
echo "ok: a release changes its own channel only"
! bash "$src" manifest "$fx/m2.json" alpha v1.2.3-rc.9 "$sha" 1700000050 "$fx/dist" > /dev/null 2> "$fx/err" || fail "the manifest moved alpha to an older commit"
grep -q 'backwards' "$fx/err" || fail "the backwards refusal does not say why: $(cat "$fx/err")"
bash "$src" manifest "$fx/m2.json" alpha v1.2.4-rc.1 "$sha2" 1700000100 "$fx/dist" > /dev/null 2>&1 || fail "re-running the same release was refused"
echo "ok: a channel never moves to an older commit, and a re-run of the same release is allowed"
jq '.schema = 2' "$fx/m1.json" > "$fx/s2.json"
! bash "$src" manifest "$fx/s2.json" latest v1.2.5 "$sha" 1700000200 "$fx/dist" > /dev/null 2>&1 || fail "the manifest overwrote another schema"
! bash "$src" manifest "" latest v1.2.5 "$sha" 0 "$fx/dist" > /dev/null 2>&1 || fail "the manifest accepted an unordered commit time"
! bash "$src" manifest "" beta v1.2.5 "$sha" 1700000200 "$fx/dist" > /dev/null 2>&1 || fail "the manifest accepted channel beta"
! bash "$src" manifest "" latest v1.2.5 "$sha" 1700000200 "$fx/v1" > /dev/null 2>&1 || fail "the manifest described a tampered archive"
echo "ok: another schema, an unordered commit, an unknown channel and a tampered archive are refused"

# 5. Reading the published manifest: none yet is not an error, an unreadable one is.
export R2_ENDPOINT=https://r2.invalid
bash "$src" fetch-manifest "$fx/cur.json" 2> /dev/null || fail "fetch-manifest failed on a bucket with no manifest"
[ ! -e "$fx/cur.json" ] || fail "fetch-manifest wrote a manifest that does not exist"
! AWS_HEAD=error bash "$src" fetch-manifest "$fx/cur.json" 2> "$fx/err" || fail "fetch-manifest treated a 403 as no manifest"
grep -q 'cannot tell' "$fx/err" || fail "the read refusal does not say why: $(cat "$fx/err")"
cp "$fx/m1.json" "$fx/r2/daemon-channels.json"
bash "$src" fetch-manifest "$fx/cur.json" 2> /dev/null || fail "fetch-manifest"
cmp -s "$fx/cur.json" "$fx/m1.json" || fail "fetch-manifest did not copy the published manifest"
echo "ok: no published manifest yet is not an error; an unreadable one is"

# 6. Storing a signed set: both places, signature last, root manifest after both;
# recovered, never re-signed; never overwritten.
# signed <dir> <channel> <tag> <commit-time> <signature>: what cli-publish signs,
# the manifest written against the root manifest R2 serves now.
signed() {
  rm -rf "$1"; cp -r "$fx/dist" "$1"
  bash "$src" fetch-manifest "$fx/cur.json" 2> /dev/null
  bash "$src" manifest "$fx/cur.json" "$2" "$3" "$sha2" "$4" "$fx/dist" > "$1/daemon-channels.json" 2> /dev/null || fail "manifest for $3"
  printf '{"sig":"%s"}\n' "$5" > "$1/reliant-release.sigstore.json"
}
publish() { # publish <tag> <dir>: cli-publish's order
  bash "$src" store-github "$1" "$2" && bash "$src" store-r2 "$1" "$2" && bash "$src" publish-root "$1" "$2"
}
same() { # same <tag> <dir>: both stores hold exactly <dir>'s set
  local f
  for f in $(cd "$2" && ls); do
    cmp -s "$2/$f" "$fx/r2/cli/$1/$f" || fail "R2 cli/$1/$f is not the signed set's"
    cmp -s "$2/$f" "$fx/gh/$1/$f" || fail "the GitHub Release $1's $f is not the signed set's"
  done
}
mkdir -p "$fx/gh/v1.2.4-rc.1" "$fx/gh/v1.2.5-rc.1" "$fx/gh/v1.2.6"
[ "$(bash "$src" recover v1.2.4-rc.1 "$fx/rec" 2> /dev/null)" = none ] || fail "recover found a set nobody stored"
signed "$fx/A" alpha v1.2.4-rc.1 1700000100 A
! bash "$src" publish-root v1.2.4-rc.1 "$fx/A" > /dev/null 2>&1 || fail "the root manifest went out before the set was stored"
cmp -s "$fx/r2/daemon-channels.json" "$fx/m1.json" || fail "a refused publish-root changed the root manifest"
: > "$AWS_CALLS"; : > "$GH_CALLS"
publish v1.2.4-rc.1 "$fx/A" > /dev/null 2> "$fx/err" || fail "publish: $(cat "$fx/err")"
same v1.2.4-rc.1 "$fx/A"
[ "$(tail -1 "$GH_CALLS")" = "upload reliant-release.sigstore.json" ] || fail "the GitHub signature is not attached last: $(cat "$GH_CALLS")"
grep -n 'cli/v1.2.4-rc.1/reliant-release.sigstore.json' "$AWS_CALLS" | grep '^9:' > /dev/null || fail "the R2 signature is not stored last of the set: $(cat "$AWS_CALLS")"
tail -1 "$AWS_CALLS" | grep -q "s3://downloads/daemon-channels.json .*--cache-control" || fail "the root manifest is not uploaded last: $(cat "$AWS_CALLS")"
cmp -s "$fx/r2/daemon-channels.json" "$fx/A/daemon-channels.json" || fail "the root manifest is not the signed one"
echo "ok: the set goes to the GitHub Release and R2, signature last in each, then the root manifest"
: > "$AWS_CALLS"; : > "$GH_CALLS"
[ "$(bash "$src" recover v1.2.4-rc.1 "$fx/rec" 2> /dev/null)" = github ] || fail "recover did not find the stored set"
diff -r "$fx/rec" "$fx/A" > /dev/null || fail "recover did not return the stored set"
publish v1.2.4-rc.1 "$fx/rec" > /dev/null 2> "$fx/err" || fail "re-publishing the same set: $(cat "$fx/err")"
[ ! -s "$GH_CALLS" ] || fail "an identical set was attached again: $(cat "$GH_CALLS")"
[ "$(wc -l < "$AWS_CALLS")" = 1 ] || fail "an identical set was uploaded again: $(cat "$AWS_CALLS")"
echo "ok: a re-run recovers the stored set and stores nothing again"
signed "$fx/A2" alpha v1.2.4-rc.1 1700000100 A-resigned
! bash "$src" store-r2 v1.2.4-rc.1 "$fx/A2" > /dev/null 2> "$fx/err" || fail "store-r2 replaced a signed set"
grep -q 'never replaced' "$fx/err" || fail "the R2 refusal does not say why: $(cat "$fx/err")"
! bash "$src" store-github v1.2.4-rc.1 "$fx/A2" > /dev/null 2>&1 || fail "store-github replaced a signed set"
same v1.2.4-rc.1 "$fx/A"
echo "ok: a different signed set for a published tag is refused in both places"

# The interruption: v1.2.5-rc.1 reaches R2, its GitHub attachment fails; then
# v1.2.6 (latest) publishes; then v1.2.5-rc.1 is re-run.
signed "$fx/B" alpha v1.2.5-rc.1 1700000200 B
cp -r "$fx/B" "$fx/B.orig"
bash "$src" store-r2 v1.2.5-rc.1 "$fx/B" > /dev/null 2>&1 || fail "store-r2 of v1.2.5-rc.1"
! GH_UPLOAD_FAIL=1 bash "$src" store-github v1.2.5-rc.1 "$fx/B" > /dev/null 2>&1 || fail "the failing GitHub attachment succeeded"
signed "$fx/C" latest v1.2.6 1700000300 C
publish v1.2.6 "$fx/C" > /dev/null 2> "$fx/err" || fail "the intervening release: $(cat "$fx/err")"
cp "$fx/r2/daemon-channels.json" "$fx/root-after-C.json"
[ "$(bash "$src" recover v1.2.5-rc.1 "$fx/rec5" 2> /dev/null)" = r2 ] || fail "the re-run did not recover v1.2.5-rc.1's set from R2"
diff -r "$fx/rec5" "$fx/B.orig" > /dev/null || fail "the re-run's set is not the original signed set"
bash "$src" store-github v1.2.5-rc.1 "$fx/rec5" > /dev/null 2>&1 || fail "the re-run could not attach the original set"
bash "$src" store-r2 v1.2.5-rc.1 "$fx/rec5" > /dev/null 2>&1 || fail "the re-run's R2 store of the same set"
same v1.2.5-rc.1 "$fx/B.orig"
! bash "$src" publish-root v1.2.5-rc.1 "$fx/rec5" > /dev/null 2> "$fx/err" || fail "the re-run's manifest replaced the newer root manifest"
grep -q 'backwards' "$fx/err" || fail "the root refusal does not say why: $(cat "$fx/err")"
cmp -s "$fx/r2/daemon-channels.json" "$fx/root-after-C.json" || fail "the root manifest changed"
echo "ok: an interrupted publish is recovered as signed, stored unchanged, and never rolls the root manifest back"

# 7. An SBOM per archive, from the binary inside it, attached beside the signed set.
# The fake forge (FORGE_BIN, which scripts/forge.sh runs as given) records its
# argv and writes a CycloneDX document naming the binary it read; it fails for
# the windows/arm64 binary when FORGE_SBOM_FAIL=1.
cat > "$fx/bin/forge" << 'FORGE'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FORGE_CALLS"
[ "$1 $2 $3" = "vuln sbom --file" ] || { echo "unexpected forge: $*" >&2; exit 90; }
[ "${FORGE_SBOM_FAIL:-}" = 1 ] && [ "$(cat "$4")" = "binary windows-arm64" ] && { echo "syft: cannot read it" >&2; exit 1; }
jq -n --arg read "$(cat "$4")" --arg subject "$6" --arg version "$8" \
  '{bomFormat: "CycloneDX", specVersion: "1.6", metadata: {component: {name: $subject, version: $version}}, read: $read}'
FORGE
chmod +x "$fx/bin/forge"
export FORGE_BIN="$fx/bin/forge" FORGE_CALLS="$fx/forge-calls"
bash "$src" sbom v1.2.6 "$fx/dist" "$fx/sbom" 2> "$fx/err" || fail "sbom: $(cat "$fx/err")"
for t in $targets; do
  a="reliant_${t%-*}_${t#*-}.tar.gz"
  jq -e --arg a "$a" --arg t "$t" '.metadata.component == {name: $a, version: "v1.2.6"} and .read == "binary \($t)"' "$fx/sbom/$a.cdx.json" > /dev/null ||
    fail "the SBOM of $a: $(cat "$fx/sbom/$a.cdx.json" 2> /dev/null)"
done
[ "$(ls "$fx/sbom" | wc -l)" = 6 ] || fail "sbom wrote $(ls "$fx/sbom")"
grep -q -- '--artifact reliant_darwin_arm64.tar.gz --release v1.2.6$' "$FORGE_CALLS" || fail "forge was not asked for the archive as subject: $(cat "$FORGE_CALLS")"
echo "ok: each archive gets an SBOM of its own binary, named for the archive and the release"
! bash "$src" sbom v1.2.6 "$fx/v1" "$fx/sbom-bad" 2> /dev/null || fail "sbom described a tampered set"
! FORGE_SBOM_FAIL=1 bash "$src" sbom v1.2.6 "$fx/dist" "$fx/sbom-part" 2> "$fx/err" || fail "sbom passed with an archive forge could not describe"
grep -q '::warning::no SBOM for reliant_windows_arm64.tar.gz: syft: cannot read it' "$fx/err" || fail "the missing SBOM is not named: $(cat "$fx/err")"
[ "$(ls "$fx/sbom-part" | wc -l)" = 5 ] || fail "a failed archive stopped the others' SBOMs: $(ls "$fx/sbom-part")"
echo "ok: a tampered set is refused; an archive forge cannot describe is named, and the others are still written"
: > "$GH_CALLS"
mkdir -p "$fx/gh/v1.2.9"
! bash "$src" store-sbom-github v1.2.9 "$fx/sbom" 2> /dev/null || fail "SBOMs were attached to a Release with no signed set"
bash "$src" store-sbom-github v1.2.6 "$fx/sbom" 2> "$fx/err" || fail "store-sbom-github: $(cat "$fx/err")"
bash "$src" store-sbom-github v1.2.6 "$fx/sbom-part" 2> "$fx/err" || fail "a re-run's SBOMs did not replace the stored ones: $(cat "$fx/err")"
[ "$(grep -c '\.cdx\.json$' "$GH_CALLS")" = 11 ] || fail "store-sbom-github uploaded: $(cat "$GH_CALLS")"
[ ! -e "$fx/r2/cli/v1.2.6/reliant_linux_amd64.tar.gz.cdx.json" ] || fail "an SBOM reached R2's signed set"
echo "ok: SBOMs go to a Release that holds the signed set, replaced by a re-run, never into the signed set"

# 8. verify-published hashes every copy, on R2 and on the GitHub Release.
vp() { bash "$src" verify-published "$@" --base https://dl.test --github https://gh.test --wait 0 > "$fx/out" 2>&1; }
vp v1.2.6 latest || fail "verify-published: $(cat "$fx/out")"
! vp v1.2.7 latest || fail "verify-published accepted a channel naming another release"
cp "$fx/r2/cli/v1.2.6/reliant_linux_amd64.tar.gz" "$fx/keep"
printf 'x' >> "$fx/r2/cli/v1.2.6/reliant_linux_amd64.tar.gz"
! vp v1.2.6 latest || fail "verify-published accepted corrupted archive bytes on R2"
grep -q '^FAIL  sha256 of https://dl.test/cli/v1.2.6/reliant_linux_amd64.tar.gz' "$fx/out" || fail "the R2 corruption is not named: $(cat "$fx/out")"
cp "$fx/keep" "$fx/r2/cli/v1.2.6/reliant_linux_amd64.tar.gz"
cp "$fx/dist/reliant_linux_amd64.tar.gz" "$fx/gh/v1.2.6/reliant_darwin_arm64.tar.gz"
! vp v1.2.6 latest || fail "verify-published accepted a replaced archive on the GitHub Release"
grep -q '^FAIL  sha256 of https://gh.test/acme/app/releases/download/v1.2.6/reliant_darwin_arm64.tar.gz' "$fx/out" || fail "the GitHub replacement is not named: $(cat "$fx/out")"
cp "$fx/dist/reliant_darwin_arm64.tar.gz" "$fx/gh/v1.2.6/"
rm "$fx/r2/cli/v1.2.6/reliant_windows_arm64.tar.gz"
! vp v1.2.6 latest || fail "verify-published accepted a missing archive"
cp "$fx/dist/reliant_windows_arm64.tar.gz" "$fx/r2/cli/v1.2.6/"
echo '{"sig":"other"}' > "$fx/gh/v1.2.6/reliant-release.sigstore.json"
! vp v1.2.6 latest || fail "verify-published accepted different signatures on R2 and GitHub"
echo "ok: verify-published hashes every copy and compares the signatures"
echo "PASS: release-cli.sh"
