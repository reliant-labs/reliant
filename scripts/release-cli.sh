#!/usr/bin/env bash
# release-cli.sh — the standalone `reliant` CLI release assets and the public
# daemon channel manifest (forge docs/proposals/self-upgrade.md §6, phase R0).
#
# release.yml's build-backend matrix builds `reliant-backend` once per target for
# the Electron installers. These are the same bytes, repackaged as the CLI a
# self-run daemon installs and later upgrades itself from. Nothing is built here.
# Every decision release.yml's cli-archives and cli-publish jobs make lives in this
# file, so scripts/test-release-cli.sh can prove it offline. Design, schema and
# how to verify: dev-docs/cli-release-artifacts.md.
#
#   release-cli.sh package <backend-dir> <out-dir>
#       Write reliant_<os>_<arch>.tar.gz for the six targets, and checksums.txt
#       (`<sha256>  <file>`, sha256sum format), from build-backend's artifacts as
#       download-artifact unpacks them: <backend-dir>/backend-<os>-<arch>/
#       reliant-backend[.exe]. Each archive holds `reliant` (`reliant.exe`),
#       LICENSE and LICENSES.txt at its root. With GNU tar the archives are
#       reproducible: sorted, owner 0, mtime $SOURCE_DATE_EPOCH (default 0).
#
#   release-cli.sh verify <dir>
#       Refuse unless <dir> holds exactly the six archives, checksums.txt lists
#       exactly them and each matches, and each archive holds its binary.
#
#   release-cli.sh predicate <owner/repo> <tag> <commit>
#       Print the attestation predicate, {repository, release, commit}.
#
#   release-cli.sh manifest <current-file> <channel> <tag> <commit> <commit-time-unix> <dir>
#       Print daemon-channels.json with <channel> (alpha|latest) pointing at this
#       release's archives in <dir>; every other channel is kept as it is in
#       <current-file> (absent or empty: the first manifest). Refuses a current
#       manifest of another schema, and moving a channel to an older commit.
#
#   release-cli.sh fetch-manifest <out-file>
#       Copy the published daemon-channels.json from R2 to <out-file>. No manifest
#       yet leaves <out-file> absent; any other failure is fatal (never overwrite a
#       manifest that could not be read). Needs R2_ENDPOINT (and aws creds).
#
#   The signed set of a tag is the six archives, checksums.txt, the tag's
#   daemon-channels.json and reliant-release.sigstore.json. It is signed ONCE and
#   then only copied: a store that holds the signature holds the whole set (the
#   signature is stored last), and is never overwritten.
#
#   release-cli.sh recover <tag> <dir>
#       If the GitHub Release or s3://downloads/cli/<tag>/ already holds a signed
#       set, replace <dir> with it and print `github` or `r2`; else print `none`
#       (only then may the caller sign). Needs gh and R2_ENDPOINT.
#
#   release-cli.sh store-github <tag> <dir>
#   release-cli.sh store-r2 <tag> <dir>
#       Make the GitHub Release / cli/<tag>/ on R2 hold <dir>'s signed set,
#       signature last. Where a signed set is already stored it must be
#       byte-identical (a no-op), or this fails and changes nothing.
#
#   release-cli.sh publish-root <tag> <dir>
#       Upload <dir>'s daemon-channels.json to the bucket root, the one mutable
#       object, only once both stores hold this exact set, and never when it would
#       move a published channel backwards.
#
#   release-cli.sh verify-published <tag> <channel> [--base URL] [--github URL] [--wait SECONDS]
#       Read the manifest as a client does, through the CDN, until <channel> names
#       <tag>; then download every archive and checksums.txt from R2 and from the
#       GitHub Release and require each copy's bytes to hash to the manifest's
#       digest (itself checksums.txt's), and the signature to be the same on both.
set -euo pipefail

die() { echo "::error::$*" >&2; exit 1; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
BUCKET=downloads
MANIFEST=daemon-channels.json
SIGNATURE=reliant-release.sigstore.json
SCHEMA=1
BASE_URL="${BASE_URL:-https://downloads.reliantlabs.io}"
BASE_URL="${BASE_URL%/}"
TAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

sha256_of() {
  if command -v sha256sum > /dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

words() { tr '\n' ' ' <<< "$1" | sed 's/ *$//'; }

bin_for() { if [ "$1" = windows ]; then echo reliant.exe; else echo reliant; fi; }

archives() {
  local t
  for t in $TARGETS; do echo "reliant_${t%/*}_${t#*/}.tar.gz"; done
}

check_tag() { [[ "$1" =~ $TAG_RE ]] || die "release tag must look like vX.Y.Z, got '$1'"; }
check_channel() { case "$1" in alpha | latest) ;; *) die "channel must be alpha or latest, got '$1'" ;; esac; }

cmd_package() {
  [ $# -eq 2 ] || die "usage: package <backend-dir> <out-dir>"
  local src="$1" out="$2" epoch="${SOURCE_DATE_EPOCH:-0}" t os arch bin from dir name stage
  [ -d "$src" ] || die "no backend directory $src"
  [[ "$epoch" =~ ^[0-9]+$ ]] || die "SOURCE_DATE_EPOCH must be unix seconds, got '$epoch'"
  mkdir -p "$out"
  rm -f "$out"/reliant_*.tar.gz "$out/checksums.txt"
  stage="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand now: stage is local
  trap "rm -rf '$stage'" RETURN
  : > "$stage/checksums.txt"
  for t in $TARGETS; do
    os="${t%/*}" arch="${t#*/}"
    bin="$(bin_for "$os")"
    from="$src/backend-$os-$arch/reliant-backend"
    [ "$os" = windows ] && from="$from.exe"
    [ -f "$from" ] || die "no $os/$arch backend at $from (build-backend's artifact backend-$os-$arch)"
    dir="$stage/$os-$arch"
    mkdir -p "$dir"
    cp "$from" "$dir/$bin"
    cp "$ROOT/LICENSE" "$ROOT/LICENSES.txt" "$dir/"
    chmod 0755 "$dir/$bin"
    chmod 0644 "$dir/LICENSE" "$dir/LICENSES.txt"
    name="reliant_${os}_${arch}.tar.gz"
    if tar --version 2> /dev/null | grep 'GNU tar' > /dev/null; then
      tar -C "$dir" --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
        -cf - "$bin" LICENSE LICENSES.txt | gzip -n -9 > "$out/$name"
    else
      COPYFILE_DISABLE=1 tar -C "$dir" -cf - "$bin" LICENSE LICENSES.txt | gzip -n -9 > "$out/$name"
    fi
    printf '%s  %s\n' "$(sha256_of "$out/$name")" "$name" >> "$stage/checksums.txt"
    echo "packaged $name" >&2
  done
  # checksums.txt appears only once every archive exists.
  mv "$stage/checksums.txt" "$out/checksums.txt"
}

cmd_verify() {
  [ $# -eq 1 ] || die "usage: verify <dir>"
  local dir="$1" want listed present line sum name t bin
  [ -f "$dir/checksums.txt" ] || die "$dir has no checksums.txt"
  want="$(archives | sort)"
  present="$(cd "$dir" && find . -maxdepth 1 -name 'reliant_*.tar.gz' | sed 's|^\./||' | sort)"
  [ "$present" = "$want" ] || die "$dir holds [$(words "$present")], want exactly [$(words "$want")]"
  listed="$(awk '{ sub(/^\*/, "", $2); print $2 }' "$dir/checksums.txt" | sort)"
  [ "$listed" = "$want" ] || die "checksums.txt lists [$(words "$listed")], want exactly [$(words "$want")]"
  while read -r line; do
    sum="${line%% *}" name="${line##* }"
    name="${name#\*}"
    [ "$(sha256_of "$dir/$name")" = "$sum" ] || die "$name does not match checksums.txt"
  done < "$dir/checksums.txt"
  for t in $TARGETS; do
    bin="$(bin_for "${t%/*}")"
    # Not grep -q: an early exit SIGPIPEs tar, and pipefail fails the check.
    tar -tzf "$dir/reliant_${t%/*}_${t#*/}.tar.gz" | grep -x "$bin" > /dev/null ||
      die "reliant_${t%/*}_${t#*/}.tar.gz does not hold $bin at its root"
  done
}

cmd_predicate() {
  [ $# -eq 3 ] || die "usage: predicate <owner/repo> <tag> <commit>"
  local repo="$1" tag="$2" commit="$3"
  [[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "repository must be owner/name, got '$repo'"
  check_tag "$tag"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "commit must be a full sha, got '$commit'"
  jq -cn --arg repo "$repo" --arg release "$tag" --arg commit "$commit" \
    '{repository: $repo, release: $release, commit: $commit}'
}

# assert_not_backwards <current-manifest> <new-manifest>: no channel the current
# manifest publishes may name an older commit in the new one, unless it is the
# same release (a re-run).
assert_not_backwards() {
  local back
  [ -s "$1" ] || return 0
  back="$(jq -r --slurpfile new "$2" '
    .channels // {} | to_entries[]
    | select(($new[0].channels[.key] // null) == null
        or (($new[0].channels[.key].tag != .value.tag)
            and ($new[0].channels[.key].commit_time_unix < .value.commit_time_unix)))
    | "\(.key) (\(.value.tag) -> \($new[0].channels[.key].tag // "nothing"))"' "$1")" ||
    die "cannot compare $1 with $2"
  [ -z "$back" ] || die "the manifest would move a channel backwards: $(words "$back"); a release never replaces a newer one"
}

cmd_manifest() {
  [ $# -eq 6 ] || die "usage: manifest <current-file> <channel> <tag> <commit> <commit-time-unix> <dir>"
  local current="$1" channel="$2" tag="$3" commit="$4" ctime="$5" dir="$6"
  local repo="${GITHUB_REPOSITORY:-reliant-labs/reliant}" prefix platforms entry base new
  check_channel "$channel"
  check_tag "$tag"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "commit must be a full sha, got '$commit'"
  # release-identity.sh --commit-time prints 0 when it cannot read the commit:
  # an unordered build must never become a channel's target.
  [[ "$ctime" =~ ^[1-9][0-9]*$ ]] || die "commit time must be positive unix seconds, got '$ctime'"
  cmd_verify "$dir"
  base='{}'
  if [ -s "$current" ]; then
    base="$(jq -c . "$current")" || die "$current is not JSON"
    [ "$(jq -r '.schema' <<< "$base")" = "$SCHEMA" ] ||
      die "$current is schema $(jq -r '.schema' <<< "$base"), this script writes $SCHEMA: update it before publishing"
  fi
  prefix="$BASE_URL/cli/$tag"
  platforms="$(awk '{ sub(/^\*/, "", $2); print $1, $2 }' "$dir/checksums.txt" | jq -Rn --arg p "$prefix" '
    [inputs | split(" ") | {
      key: (.[1] | capture("^reliant_(?<os>[a-z]+)_(?<arch>[a-z0-9]+)\\.tar\\.gz$") | "\(.os)/\(.arch)"),
      value: {url: "\($p)/\(.[1])", sha256: .[0]}}]
    | from_entries')"
  entry="$(jq -cn --arg tag "$tag" --arg commit "$commit" --argjson ctime "$ctime" --arg p "$prefix" \
    --arg sums "$(sha256_of "$dir/checksums.txt")" --arg sig "$SIGNATURE" --argjson platforms "$platforms" '
    {version: ($tag | ltrimstr("v")), tag: $tag, commit: $commit, commit_time_unix: $ctime,
     checksums: {url: "\($p)/checksums.txt", sha256: $sums},
     bundle: {url: "\($p)/\($sig)"},
     platforms: $platforms}')"
  new="$(jq -S --argjson schema "$SCHEMA" --arg repo "$repo" --arg ch "$channel" --argjson e "$entry" '
    .schema = $schema | .repository = $repo
    | .written_by = {tag: $e.tag, commit: $e.commit, bundle: $e.bundle}
    | .channels = ((.channels // {}) + {($ch): $e})' <<< "$base")"
  local tmp
  tmp="$(mktemp)"
  printf '%s\n' "$base" > "$tmp.cur"
  printf '%s\n' "$new" > "$tmp"
  assert_not_backwards "$tmp.cur" "$tmp"
  rm -f "$tmp" "$tmp.cur"
  printf '%s\n' "$new"
}

# r2_has <key>: 0 when the object exists, 1 when it does not; any other answer is
# fatal (never treat "could not tell" as "absent").
r2_has() {
  local err
  [ -n "${R2_ENDPOINT:-}" ] || die "R2_ENDPOINT is not set"
  if err="$(aws s3api head-object --bucket "$BUCKET" --key "$1" --endpoint-url "$R2_ENDPOINT" 2>&1 > /dev/null)"; then
    return 0
  elif grep -q '(404)\|Not Found' <<< "$err"; then
    return 1
  fi
  die "cannot tell whether s3://$BUCKET/$1 exists: $err"
}

r2_get() { # r2_get <key> <file>
  aws s3 cp "s3://$BUCKET/$1" "$2" --endpoint-url "$R2_ENDPOINT" --only-show-errors || die "cannot read s3://$BUCKET/$1"
}

cmd_fetch_manifest() {
  [ $# -eq 1 ] || die "usage: fetch-manifest <out-file>"
  local out="$1"
  rm -f "$out"
  if r2_has "$MANIFEST"; then
    r2_get "$MANIFEST" "$out"
    jq -e . "$out" > /dev/null || die "s3://$BUCKET/$MANIFEST is not JSON"
    echo "read the published $MANIFEST" >&2
  else
    echo "no $MANIFEST is published yet: this release writes the first" >&2
  fi
}

# The signed set, in the order it is stored: the signature LAST, so a store that
# holds the signature holds the whole set.
set_files() {
  archives
  echo checksums.txt
  echo "$MANIFEST"
  echo "$SIGNATURE"
}

content_type() {
  case "$1" in
    *.json) echo application/json ;;
    *.txt) echo text/plain ;;
    *) echo application/gzip ;;
  esac
}

# check_set <tag> <dir>: <dir> holds a complete set for <tag>.
check_set() {
  local tag="$1" dir="$2" f
  check_tag "$tag"
  cmd_verify "$dir"
  for f in "$MANIFEST" "$SIGNATURE"; do
    [ -s "$dir/$f" ] || die "$dir has no $f"
  done
  [ "$(jq -r '.written_by.tag' "$dir/$MANIFEST")" = "$tag" ] ||
    die "$dir/$MANIFEST was written by $(jq -r '.written_by.tag' "$dir/$MANIFEST"), not $tag"
}

gh_signed() { # gh_signed <tag>: 0 when the GitHub Release carries the signature
  local assets
  assets="$(gh release view "$1" --json assets)" || die "cannot read the GitHub Release $1"
  jq -e --arg a "$SIGNATURE" 'any(.assets[]; .name == $a)' <<< "$assets" > /dev/null
}

gh_get_set() { # gh_get_set <tag> <dir>
  local -a pats=()
  local f
  while read -r f; do pats+=(--pattern "$f"); done < <(set_files)
  mkdir -p "$2"
  gh release download "$1" --dir "$2" "${pats[@]}" || die "cannot download the signed set of the GitHub Release $1"
}

r2_get_set() { # r2_get_set <tag> <dir>
  local f
  mkdir -p "$2"
  while read -r f; do r2_get "cli/$1/$f" "$2/$f"; done < <(set_files)
}

# same_set <what> <stored-dir> <dir>: the stored set is byte-identical to <dir>'s.
same_set() {
  local f
  while read -r f; do
    cmp -s "$2/$f" "$3/$f" || die "$1 already holds a different signed $f: a published signed set is never replaced"
  done < <(set_files)
  echo "$1 already holds this signed set, byte for byte" >&2
}

cmd_recover() {
  [ $# -eq 2 ] || die "usage: recover <tag> <dir>"
  local tag="$1" dir="$2"
  check_tag "$tag"
  if gh_signed "$tag"; then
    rm -rf "$dir"
    gh_get_set "$tag" "$dir"
    check_set "$tag" "$dir"
    echo "recovered the signed set of $tag from its GitHub Release" >&2
    echo github
  elif r2_has "cli/$tag/$SIGNATURE"; then
    rm -rf "$dir"
    r2_get_set "$tag" "$dir"
    check_set "$tag" "$dir"
    echo "recovered the signed set of $tag from s3://$BUCKET/cli/$tag/" >&2
    echo r2
  else
    echo "$tag has no signed set yet: sign one" >&2
    echo none
  fi
}

cmd_store_github() {
  [ $# -eq 2 ] || die "usage: store-github <tag> <dir>"
  local tag="$1" dir="$2" f stored
  local -a files=()
  check_set "$tag" "$dir"
  if gh_signed "$tag"; then
    stored="$(mktemp -d)"
    gh_get_set "$tag" "$stored"
    same_set "the GitHub Release $tag" "$stored" "$dir"
    rm -rf "$stored"
    return 0
  fi
  # Unsigned assets are a partial upload: replaceable. The signature goes last,
  # never with --clobber.
  while read -r f; do [ "$f" = "$SIGNATURE" ] || files+=("$dir/$f"); done < <(set_files)
  gh release upload "$tag" --clobber "${files[@]}" || die "cannot attach the set to the GitHub Release $tag"
  gh release upload "$tag" "$dir/$SIGNATURE" || die "cannot attach $SIGNATURE to the GitHub Release $tag"
}

cmd_store_r2() {
  [ $# -eq 2 ] || die "usage: store-r2 <tag> <dir>"
  local tag="$1" dir="$2" f stored
  check_set "$tag" "$dir"
  if r2_has "cli/$tag/$SIGNATURE"; then
    stored="$(mktemp -d)"
    r2_get_set "$tag" "$stored"
    same_set "s3://$BUCKET/cli/$tag/" "$stored" "$dir"
    rm -rf "$stored"
    return 0
  fi
  while read -r f; do
    echo "publish cli/$tag/$f"
    aws s3 cp "$dir/$f" "s3://$BUCKET/cli/$tag/$f" --endpoint-url "$R2_ENDPOINT" --acl public-read \
      --only-show-errors --content-type "$(content_type "$f")" || die "cannot upload cli/$tag/$f"
  done < <(set_files)
}

cmd_publish_root() {
  [ $# -eq 2 ] || die "usage: publish-root <tag> <dir>"
  local tag="$1" dir="$2" stored current
  check_set "$tag" "$dir"
  # Only once the set is durable in both places, and is this set.
  gh_signed "$tag" || die "the GitHub Release $tag has no $SIGNATURE: store the set first"
  r2_has "cli/$tag/$SIGNATURE" || die "s3://$BUCKET/cli/$tag/ has no $SIGNATURE: store the set first"
  stored="$(mktemp -d)"
  r2_get "cli/$tag/$MANIFEST" "$stored/$MANIFEST"
  r2_get "cli/$tag/$SIGNATURE" "$stored/$SIGNATURE"
  if ! { cmp -s "$stored/$MANIFEST" "$dir/$MANIFEST" && cmp -s "$stored/$SIGNATURE" "$dir/$SIGNATURE"; }; then
    die "s3://$BUCKET/cli/$tag/ holds another signed set than $dir"
  fi
  current="$stored/current.json"
  cmd_fetch_manifest "$current"
  assert_not_backwards "$current" "$dir/$MANIFEST"
  rm -rf "$stored"
  echo "publish $MANIFEST"
  aws s3 cp "$dir/$MANIFEST" "s3://$BUCKET/$MANIFEST" --endpoint-url "$R2_ENDPOINT" --acl public-read \
    --only-show-errors --content-type application/json --cache-control "public, max-age=60" ||
    die "cannot upload $MANIFEST"
}

# fetch_sha <url>: the sha256 of the bytes <url> serves (redirects followed), or
# nothing when it cannot be read.
fetch_sha() {
  local tmp sum=""
  tmp="$(mktemp)"
  if curl -fsSL --max-time 300 -o "$tmp" "$1" 2> /dev/null; then sum="$(sha256_of "$tmp")"; fi
  rm -f "$tmp"
  echo "$sum"
}

cmd_verify_published() {
  [ $# -ge 2 ] || die "usage: verify-published <tag> <channel> [--base URL] [--github URL] [--wait SECONDS]"
  local tag="$1" channel="$2" wait=600 base="$BASE_URL" github="https://github.com" body="" got deadline fail=0
  local repo name want sums_want sig_want url copy sum
  shift 2
  while [ $# -gt 0 ]; do
    case "$1" in
      --base) base="${2:?--base needs a URL}"; shift 2 ;;
      --github) github="${2:?--github needs a URL}"; shift 2 ;;
      --wait) wait="${2:?--wait needs seconds}"; shift 2 ;;
      *) die "verify-published: unknown argument $1" ;;
    esac
  done
  base="${base%/}" github="${github%/}"
  check_tag "$tag"
  check_channel "$channel"
  deadline=$(($(date +%s) + wait))
  while :; do
    body="$(curl -fsS --max-time 30 "$base/$MANIFEST" 2> /dev/null || true)"
    got="$(jq -r --arg ch "$channel" '.channels[$ch].tag // empty' <<< "$body" 2> /dev/null || true)"
    [ "$got" = "$tag" ] && break
    if [ "$(date +%s)" -ge "$deadline" ]; then
      die "$base/$MANIFEST names ${got:-nothing} for $channel, not $tag"
    fi
    sleep 20
  done
  echo "ok    $base/$MANIFEST names $tag for $channel"
  [ "$(jq -r '.schema' <<< "$body")" = "$SCHEMA" ] || die "$base/$MANIFEST is not schema $SCHEMA"
  repo="$(jq -r '.repository' <<< "$body")"
  ent() { jq -r --arg ch "$channel" ".channels[\$ch]$1" <<< "$body"; }
  # Every copy, on R2 (through the CDN) and on the GitHub Release, is hashed:
  # a 200 proves nothing about the bytes.
  check_copies() { # check_copies <file> <want-sha256> <r2-url>
    local copy sum
    for copy in "$3" "$github/$repo/releases/download/$tag/$1"; do
      sum="$(fetch_sha "$copy")"
      if [ "$sum" = "$2" ]; then echo "ok    sha256 $copy"; else echo "FAIL  sha256 of $copy is '${sum:-unreadable}', want $2"; fail=1; fi
    done
  }
  sums_want="$(ent .checksums.sha256)"
  check_copies checksums.txt "$sums_want" "$(ent .checksums.url)"
  sums="$(curl -fsS --max-time 30 "$(ent .checksums.url)" 2> /dev/null)" || die "cannot read the channel's checksums.txt"
  while read -r name want; do
    # The manifest's digest must be the one checksums.txt records, and every
    # copy's bytes must hash to it.
    grep -Fqx "$want  $name" <<< "$sums" || { echo "FAIL  the manifest's sha256 of $name is not checksums.txt's"; fail=1; }
    check_copies "$name" "$want" "$(ent ".platforms[] | select(.url | endswith(\"/$name\")) | .url")"
  done < <(ent '.platforms[] | "\(.url | split("/") | last) \(.sha256)"')
  # The signature: the same bytes in both places.
  sig_want="$(fetch_sha "$(ent .bundle.url)")"
  [ -n "$sig_want" ] || { echo "FAIL  cannot read $(ent .bundle.url)"; fail=1; }
  url="$github/$repo/releases/download/$tag/$SIGNATURE"
  sum="$(fetch_sha "$url")"
  if [ -n "$sig_want" ] && [ "$sum" = "$sig_want" ]; then echo "ok    $SIGNATURE identical on both"; else echo "FAIL  $url differs from $(ent .bundle.url)"; fail=1; fi
  [ "$fail" = 0 ] || die "the $channel channel's CLI assets are not all published intact"
}

sub="${1:-}"
[ $# -gt 0 ] && shift
case "$sub" in
  package) cmd_package "$@" ;;
  verify) cmd_verify "$@" ;;
  predicate) cmd_predicate "$@" ;;
  manifest) cmd_manifest "$@" ;;
  fetch-manifest) cmd_fetch_manifest "$@" ;;
  recover) cmd_recover "$@" ;;
  store-github) cmd_store_github "$@" ;;
  store-r2) cmd_store_r2 "$@" ;;
  publish-root) cmd_publish_root "$@" ;;
  verify-published) cmd_verify_published "$@" ;;
  *) die "usage: release-cli.sh package|verify|predicate|manifest|fetch-manifest|recover|store-github|store-r2|publish-root|verify-published ... (see the header)" ;;
esac
