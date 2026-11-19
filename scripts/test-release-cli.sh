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
mkdir -p "$fx/bin" "$fx/r2" "$fx/www"
# The fake aws: head-object answers from $AWS_HEAD (found|missing|error); copying
# the manifest down reads $FX/r2; every upload is recorded in $AWS_CALLS.
cat > "$fx/bin/aws" << 'AWS'
#!/usr/bin/env bash
case "$1 $2" in
  "s3api head-object")
    case "$AWS_HEAD" in
      found) exit 0 ;;
      missing) echo "An error occurred (404) when calling the HeadObject operation: Not Found" >&2; exit 254 ;;
      *) echo "An error occurred (403) when calling the HeadObject operation: Forbidden" >&2; exit 254 ;;
    esac ;;
  "s3 cp")
    if [[ "$3" == s3://* ]]; then cp "$FX/r2/${3#s3://downloads/}" "$4"; exit; fi
    printf '%s\n' "$*" >> "$AWS_CALLS" ;;
  *) echo "unexpected aws: $*" >&2; exit 90 ;;
esac
AWS
# The fake curl serves $FX/www for https://dl.test/: with -w it prints the status.
cat > "$fx/bin/curl" << 'CURL'
#!/usr/bin/env bash
url="${*: -1}" out="" prev=""
f="$FX/www/${url#https://dl.test/}"
for a in "$@"; do [ "$prev" = -o ] && out="$a"; prev="$a"; done
case " $* " in
  *" -w "*) if [ -f "$f" ]; then printf 200; else printf 404; fi ;;
  *) [ -f "$f" ] || exit 22; if [ -n "$out" ]; then cp "$f" "$out"; else cat "$f"; fi ;;
esac
CURL
chmod +x "$fx/bin/aws" "$fx/bin/curl"
export PATH="$fx/bin:$PATH" FX="$fx" AWS_CALLS="$fx/aws-calls" BASE_URL=https://dl.test
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
if tar --version 2> /dev/null | grep -q 'GNU tar'; then
  sleep 1
  touch "$fx/backend/backend-linux-amd64/reliant-backend"
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
AWS_HEAD=missing bash "$src" fetch-manifest "$fx/cur.json" 2> /dev/null || fail "fetch-manifest failed on a bucket with no manifest"
[ ! -e "$fx/cur.json" ] || fail "fetch-manifest wrote a manifest that does not exist"
! AWS_HEAD=error bash "$src" fetch-manifest "$fx/cur.json" 2> "$fx/err" || fail "fetch-manifest treated a 403 as no manifest"
grep -q 'cannot tell' "$fx/err" || fail "the read refusal does not say why: $(cat "$fx/err")"
cp "$fx/m1.json" "$fx/r2/daemon-channels.json"
AWS_HEAD=found bash "$src" fetch-manifest "$fx/cur.json" 2> /dev/null || fail "fetch-manifest"
cmp -s "$fx/cur.json" "$fx/m1.json" || fail "fetch-manifest did not copy the published manifest"
echo "ok: no published manifest yet is not an error; an unreadable one is"

# 6. Publishing: versioned files first, the root manifest last; never backwards.
cp -r "$fx/dist" "$fx/pub"
cp "$fx/m2.json" "$fx/pub/daemon-channels.json"
echo '{}' > "$fx/pub/reliant-release.sigstore.json"
: > "$AWS_CALLS"
DRY_RUN=true R2_ENDPOINT='' bash "$src" publish v1.2.4-rc.1 "$fx/pub" > "$fx/out" 2> "$fx/err" || fail "dry-run publish: $(cat "$fx/err")"
[ ! -s "$AWS_CALLS" ] || fail "a dry run uploaded: $(cat "$AWS_CALLS")"
[ "$(grep -c '^publish ' "$fx/out")" = 10 ] || fail "the dry run's plan: $(cat "$fx/out")"
[ "$(tail -1 "$fx/out")" = "publish daemon-channels.json" ] || fail "the dry run does not publish the manifest last: $(cat "$fx/out")"
echo "ok: a dry run prints the plan and uploads nothing"
AWS_HEAD=found bash "$src" publish v1.2.4-rc.1 "$fx/pub" > /dev/null 2> "$fx/err" || fail "publish: $(cat "$fx/err")"
[ "$(wc -l < "$AWS_CALLS")" = 10 ] || fail "want 10 uploads: $(cat "$AWS_CALLS")"
grep -q "^s3 cp $fx/pub/reliant_linux_amd64.tar.gz s3://downloads/cli/v1.2.4-rc.1/reliant_linux_amd64.tar.gz " "$AWS_CALLS" || fail "archives are not versioned: $(cat "$AWS_CALLS")"
tail -1 "$AWS_CALLS" | grep -q "^s3 cp $fx/pub/daemon-channels.json s3://downloads/daemon-channels.json .*--cache-control" || fail "the root manifest is not uploaded last: $(cat "$AWS_CALLS")"
echo "ok: the versioned files upload first and the mutable manifest last"
: > "$AWS_CALLS"
cp "$fx/m2.json" "$fx/r2/daemon-channels.json"
jq '.written_by.tag = "v1.2.3"' "$fx/m1.json" > "$fx/pub/daemon-channels.json"
! AWS_HEAD=found bash "$src" publish v1.2.3 "$fx/pub" > /dev/null 2> "$fx/err" || fail "publish replaced a manifest that has alpha with one that has not"
grep -q 'backwards' "$fx/err" || fail "the backwards refusal does not say why: $(cat "$fx/err")"
[ ! -s "$AWS_CALLS" ] || fail "a refused publish uploaded: $(cat "$AWS_CALLS")"
cp "$fx/m2.json" "$fx/pub/daemon-channels.json"
! bash "$src" publish v1.2.3 "$fx/pub" > /dev/null 2>&1 || fail "publish took a manifest written by another release"
rm "$fx/pub/reliant-release.sigstore.json"
! bash "$src" publish v1.2.4-rc.1 "$fx/pub" > /dev/null 2>&1 || fail "publish went ahead without the signature"
echo "ok: publish refuses an older manifest, another release's manifest, and an unsigned set"

# 7. verify-published reads the manifest and the files as a client does.
mkdir -p "$fx/www/cli/v1.2.4-rc.1"
cp "$fx/dist"/reliant_*.tar.gz "$fx/dist/checksums.txt" "$fx/www/cli/v1.2.4-rc.1/"
echo '{}' > "$fx/www/cli/v1.2.4-rc.1/reliant-release.sigstore.json"
bash "$src" manifest "$fx/m1.json" alpha v1.2.4-rc.1 "$sha2" 1700000100 "$fx/dist" > "$fx/www/daemon-channels.json" 2> /dev/null
bash "$src" verify-published v1.2.4-rc.1 alpha --base https://dl.test --wait 0 > "$fx/out" 2>&1 || fail "verify-published: $(cat "$fx/out")"
! bash "$src" verify-published v1.2.5 alpha --base https://dl.test --wait 0 > "$fx/out" 2>&1 || fail "verify-published accepted a channel naming another release"
rm "$fx/www/cli/v1.2.4-rc.1/reliant_darwin_arm64.tar.gz"
! bash "$src" verify-published v1.2.4-rc.1 alpha --base https://dl.test --wait 0 > "$fx/out" 2>&1 || fail "verify-published accepted a missing archive"
grep -q '^FAIL  https://dl.test/cli/v1.2.4-rc.1/reliant_darwin_arm64.tar.gz' "$fx/out" || fail "verify-published does not name the missing archive: $(cat "$fx/out")"
cp "$fx/dist/reliant_darwin_arm64.tar.gz" "$fx/www/cli/v1.2.4-rc.1/"
sed -i.bak 's/^[0-9a-f]\{4\}/0000/' "$fx/www/cli/v1.2.4-rc.1/checksums.txt"
! bash "$src" verify-published v1.2.4-rc.1 alpha --base https://dl.test --wait 0 > "$fx/out" 2>&1 || fail "verify-published accepted sha256s that differ from checksums.txt"
grep -q 'sha256 of checksums.txt differs' "$fx/out" || fail "verify-published does not name checksums.txt's sha256: $(cat "$fx/out")"
grep -q 'sha256 of reliant_.* differs' "$fx/out" || fail "verify-published does not name an archive's sha256: $(cat "$fx/out")"
echo "ok: verify-published requires the channel, every file, and matching checksums"
echo "PASS: release-cli.sh"
