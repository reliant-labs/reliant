#!/usr/bin/env bash
#
# Fails when a dependency carries a license that is not on the ALLOWLIST.
#
# Allowlist, not blocklist. A blocklist only catches the licenses someone
# thought to name; the license that hurts is the one nobody anticipated, in a
# transitive dep added by a routine `go get`. An unrecognized license is
# therefore a FAILURE requiring an explicit decision, never a silent pass.
#
# What this is actually protecting against: a copyleft (AGPL/GPL/SSPL) or
# source-available (BSL/Elastic) library entering the IMPORT GRAPH of a
# proprietary service. Linking is the real exposure — running unmodified
# copyleft software as a service is permitted by AGPL §2, but importing an
# AGPL library into a proprietary binary reaches that binary's source. The
# accident is silent and only surfaces during diligence.
#
# Scope is the IMPORT graph (`go list -deps`), not the module graph
# (`go list -m all`). The module graph includes every dep of every dep of
# every tool, most of which never reach the binary; gating on it produces
# false positives that train people to add exceptions. Measured on
# control-plane: 108 imported modules vs 520 in the module graph.
#
# Detection reads the dependency's own LICENSE file out of the module cache
# and matches its text. It does not trust pkg.go.dev, a vendor manifest, or a
# hand-maintained list, all of which drift from what is actually linked.
#
# Escape hatch, for a dep whose license is fine but undetected (an unusual
# BSD variant, a dual-licensed package, a vendor-specific permissive license):
# add `module/path  # reason` to .license-allowlist. Per-module, never
# per-license — an exception should name one dependency and say why.
#
# Usage:
#   scripts/check-licenses.sh            # this module
#   scripts/check-licenses.sh --json     # machine-readable, for CI annotation
#   scripts/check-licenses.sh --list     # print every dep + detected license
#
set -uo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || repo_root="$(pwd)"
allowlist_file="${repo_root}/.license-allowlist"

mode="check"
case "${1:-}" in
--json) mode="json" ;;
--list) mode="list" ;;
"") ;;
*)
  echo "usage: $0 [--json|--list]" >&2
  exit 2
  ;;
esac

# ALLOWED_LICENSES — permissive only.
#
# The bar: no source-disclosure obligation, no "provide as a service"
# restriction, no patent-retaliation clause that reaches our code. These four
# families cover the overwhelming majority of the Go and npm ecosystems.
#
# Deliberately EXCLUDED, with reasons:
#   AGPL-3.0  linking reaches our source; fine to RUN unmodified, never to link
#   GPL-2.0 / GPL-3.0  same, without the network clause
#   LGPL      dynamic-linking carve-out does not survive Go static linking
#   SSPL      Mongo's; requires open-sourcing the whole service stack
#   BSL-1.1   source-available, time-delayed; "no offering as a service"
#   Elastic-2.0  same family
#   CC-BY-SA  copyleft on the data/asset side
#   MPL-2.0   file-level copyleft. Modifying an MPL FILE obliges publishing
#             that file. Tolerable for a standalone SERVICE (OpenBao), which
#             is why it is not on this list — that is a hosting decision, not
#             a linking one. If a library genuinely needs it, allowlist the
#             module explicitly so the decision is recorded.
ALLOWED_LICENSES=(
  "MIT"
  "Apache-2.0"
  "BSD-2-Clause"
  "BSD-3-Clause"
  "ISC"
  "0BSD"
  "Unlicense"
  "Zlib"
  "PostgreSQL"
  "Python-2.0" # the stdlib-ish license on a few Go ports; permissive
  "CC0-1.0"    # public-domain dedication, NOT copyleft CC-BY-SA
)

is_allowed_license() {
  local want="$1" have
  for have in "${ALLOWED_LICENSES[@]}"; do
    [ "${want}" = "${have}" ] && return 0
  done
  return 1
}

is_allowlisted_module() {
  [ -f "${allowlist_file}" ] || return 1
  # Strip comments and blank lines; match the module path exactly.
  grep -v '^[[:space:]]*#' "${allowlist_file}" 2>/dev/null |
    sed 's/#.*//' |
    tr -d '[:blank:]' |
    grep -qxF -- "$1"
}

# classify_license reads a license file and returns an SPDX-ish identifier.
#
# Ordering matters. AGPL text contains the string "GNU General Public
# License", and GPL text contains "Lesser" only in the LGPL variant, so the
# most restrictive / most specific patterns MUST be tested first or an AGPL
# file classifies as GPL. Every early return below is load-bearing.
classify_license() {
  local file="$1"
  [ -r "${file}" ] || {
    echo "UNREADABLE"
    return
  }

  # Read a bounded prefix. License texts put their identity in the first few
  # hundred lines; reading whole files makes this loop slow across ~800 deps.
  local head_text
  head_text="$(head -c 40000 "${file}" 2>/dev/null)"

  # MPL and CC0 MUST be tested before the GNU patterns.
  #
  # MPL-2.0 §1.12 defines "Secondary License" by NAMING the GNU GPL, LGPL and
  # AGPL. Every HashiCorp dependency is MPL-2.0 and contains those strings, so
  # testing GPL first misclassifies all of them as GPL — a false positive that
  # would fail this gate on four modules that are perfectly fine to link.
  # Ordering here is load-bearing; do not "tidy" these into alphabetical order.
  case "${head_text}" in
  *"Mozilla Public License"* | *"MOZILLA PUBLIC LICENSE"*)
    echo "MPL-2.0"
    return
    ;;
  # CC0 is a public-domain dedication, not a copyleft Creative Commons
  # license. It must be distinguished from CC-BY-SA before the generic
  # "Creative Commons" catch-all below.
  *"CC0 1.0 Universal"*)
    echo "CC0-1.0"
    return
    ;;
  esac

  # --- copyleft / source-available (most specific wins) ---
  #
  # AGPL is identified by its TITLE LINE, not by any mention of the name.
  # GPL-3.0's §13 is headed "Use with the GNU Affero General Public License",
  # so a substring match on the name classifies plain GPL as AGPL. Both are
  # blocked either way, but reporting the wrong one sends the reader chasing
  # the wrong obligation. The title appears in the first ~3 lines of the file.
  local title_block
  title_block="$(printf '%s' "${head_text}" | head -5)"
  case "${title_block}" in
  *"AFFERO GENERAL PUBLIC LICENSE"* | *"Affero General Public License"*)
    echo "AGPL-3.0"
    return
    ;;
  esac

  case "${head_text}" in
  *"Server Side Public License"* | *"SERVER SIDE PUBLIC LICENSE"*)
    echo "SSPL"
    return
    ;;
  *"Business Source License"* | *"BUSINESS SOURCE LICENSE"*)
    echo "BSL-1.1"
    return
    ;;
  *"Elastic License"* | *"ELASTIC LICENSE"*)
    echo "Elastic-2.0"
    return
    ;;
  *"GNU LESSER GENERAL PUBLIC LICENSE"* | *"GNU Lesser General Public License"*)
    echo "LGPL"
    return
    ;;
  *"GNU GENERAL PUBLIC LICENSE"* | *"GNU General Public License"*)
    echo "GPL"
    return
    ;;
  *"Creative Commons"*)
    echo "CC-*"
    return
    ;;
  esac

  # --- permissive ---
  case "${head_text}" in
  *"Apache License"*)
    echo "Apache-2.0"
    return
    ;;
  esac

  # BSD variants differ only by clause count. Test 3-clause's distinguishing
  # "Neither the name of" endorsement clause before falling back to 2-clause.
  case "${head_text}" in
  *"Redistribution and use in source and binary forms"*)
    case "${head_text}" in
    *"Neither the name of"* | *"Neither the names of"*)
      echo "BSD-3-Clause"
      return
      ;;
    *)
      echo "BSD-2-Clause"
      return
      ;;
    esac
    ;;
  esac

  case "${head_text}" in
  # ISC and MIT share "Permission to use/copy"; ISC's distinguishing marker is
  # the "ISC" name or its characteristic single-paragraph grant.
  *"ISC License"* | *"ISC LICENSE"*)
    echo "ISC"
    return
    ;;
  *"Permission is hereby granted, free of charge"*)
    echo "MIT"
    return
    ;;
  *"Permission to use, copy, modify, and distribute this software"*)
    echo "ISC"
    return
    ;;
  *"BSD Zero Clause"* | *"Zero-Clause BSD"*)
    echo "0BSD"
    return
    ;;
  *"This is free and unencumbered software released into the public domain"*)
    echo "Unlicense"
    return
    ;;
  *"zlib License"* | *"altered source versions must be plainly marked"*)
    echo "Zlib"
    return
    ;;
  *"PostgreSQL License"*)
    echo "PostgreSQL"
    return
    ;;
  *"PYTHON SOFTWARE FOUNDATION LICENSE"*)
    echo "Python-2.0"
    return
    ;;
  esac

  echo "UNKNOWN"
}

# find_license_file locates a module's license within its cache directory.
# Names vary (LICENSE, LICENSE.txt, LICENCE, COPYING); Valkey ships COPYING,
# Keycloak ships LICENSE.txt. Search the module root first, then one level
# down, which covers monorepo-style modules.
find_license_file() {
  local dir="$1" candidate
  for candidate in \
    LICENSE LICENSE.txt LICENSE.md LICENCE LICENCE.txt \
    COPYING COPYING.txt LICENSE-APACHE LICENSE-MIT; do
    if [ -f "${dir}/${candidate}" ]; then
      echo "${dir}/${candidate}"
      return 0
    fi
  done
  # One level down, first match wins.
  local found
  found="$(find "${dir}" -maxdepth 2 -type f \
    \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' \) \
    2>/dev/null | head -1)"
  [ -n "${found}" ] && echo "${found}" && return 0
  return 1
}

gomodcache="$(go env GOMODCACHE 2>/dev/null)"
if [ -z "${gomodcache}" ]; then
  echo "check-licenses: 'go env GOMODCACHE' returned nothing — is Go installed?" >&2
  exit 2
fi

# Imported modules only. `go list -deps ./...` walks the actual import graph;
# the `{{if .Module}}` guard drops stdlib packages, which have no module.
# Read into a plain newline-delimited string rather than an array: macOS ships
# bash 3.2, which has neither `mapfile` nor `readarray`, and this script has to
# run on dev laptops as well as CI.
modules="$(
  go list -deps -f '{{if .Module}}{{.Module.Path}}@{{.Module.Version}}{{end}}' ./... 2>/dev/null |
    grep -v '^$' | sort -u
)"

if [ -z "${modules}" ]; then
  echo "check-licenses: no dependencies resolved (does this module build?)" >&2
  exit 2
fi

# bash 3.2 has no associative arrays and empty-array expansion under `set -u`
# is an error, so accumulate into newline-delimited strings instead.
violations=""
unknowns=""
rows=""
violation_count=0
unknown_count=0
allowed_count=0
exempt_count=0

self_module="$(go list -m 2>/dev/null | head -1)"

while IFS= read -r entry; do
  [ -n "${entry}" ] || continue
  modpath="${entry%@*}"
  modver="${entry##*@}"

  # Skip this repo's own module and our sibling repos — first-party code is
  # not a third-party license question.
  case "${modpath}" in
  "${self_module}" | "${self_module}"/*) continue ;;
  github.com/reliant-labs/*) continue ;;
  esac

  # Module cache path: the version is case-encoded (!u!p!p!e!r), but the
  # unescaped form is what `go list` prints, so try the direct path first and
  # fall back to a glob for the escaped form.
  # The module cache lower-cases paths and escapes each original capital as
  # `!` + the lowercase letter, so github.com/BurntSushi/toml lands at
  # github.com/!burnt!sushi/toml. Without this the whole capitalized tail of
  # the dependency list reports NOT-IN-CACHE and silently fails the gate.
  escaped_path="$(printf '%s' "${modpath}" | sed 's/\([A-Z]\)/!\1/g' | tr '[:upper:]' '[:lower:]')"

  moddir=""
  for candidate_dir in \
    "${gomodcache}/${escaped_path}@${modver}" \
    "${gomodcache}/${modpath}@${modver}"; do
    if [ -d "${candidate_dir}" ]; then
      moddir="${candidate_dir}"
      break
    fi
  done

  if [ -z "${moddir}" ]; then
    # Version mismatch (cache holds a different build of the same module):
    # fall back to any cached version. The license is a property of the
    # project, not usually of the point release.
    for candidate_dir in "${gomodcache}/${escaped_path}"@*; do
      if [ -d "${candidate_dir}" ]; then
        moddir="${candidate_dir}"
        break
      fi
    done
  fi

  if [ -z "${moddir}" ] || [ ! -d "${moddir}" ]; then
    # Not in cache. Report rather than skip — a dep we cannot inspect is
    # exactly the one that should get attention.
    license="NOT-IN-CACHE"
  else
    license_file="$(find_license_file "${moddir}")" || license_file=""
    if [ -z "${license_file}" ]; then
      license="NO-LICENSE-FILE"
    else
      license="$(classify_license "${license_file}")"
    fi
  fi

  rows="${rows}${modpath}|${modver}|${license}
"

  if is_allowlisted_module "${modpath}"; then
    exempt_count=$((exempt_count + 1))
    continue
  fi

  if is_allowed_license "${license}"; then
    allowed_count=$((allowed_count + 1))
  elif [ "${license}" = "UNKNOWN" ] || [ "${license}" = "NO-LICENSE-FILE" ] ||
    [ "${license}" = "NOT-IN-CACHE" ] || [ "${license}" = "UNREADABLE" ]; then
    unknowns="${unknowns}${modpath} (${license})
"
    unknown_count=$((unknown_count + 1))
  else
    violations="${violations}${modpath} — ${license}
"
    violation_count=$((violation_count + 1))
  fi
done <<EOF
${modules}
EOF

if [ "${mode}" = "list" ]; then
  printf '%-64s %-24s %s\n' "MODULE" "VERSION" "LICENSE"
  printf '%s' "${rows}" | while IFS='|' read -r m v l; do
    [ -n "${m}" ] || continue
    printf '%-64s %-24s %s\n' "${m}" "${v}" "${l}"
  done
  exit 0
fi

if [ "${mode}" = "json" ]; then
  json_array() {
    # Emit a newline-delimited list as a JSON array of strings.
    printf '['
    local first=1 line
    while IFS= read -r line; do
      [ -n "${line}" ] || continue
      [ "${first}" -eq 0 ] && printf ', '
      first=0
      # Escape backslashes and quotes; license/module strings contain neither
      # in practice, but a malformed JSON blob in CI is a miserable debug.
      printf '"%s"' "$(printf '%s' "${line}" | sed 's/\\/\\\\/g; s/"/\\"/g')"
    done
    printf ']'
  }
  printf '{\n  "allowed": %d,\n  "exempt": %d,\n' "${allowed_count}" "${exempt_count}"
  printf '  "violations": '
  printf '%s' "${violations}" | json_array
  printf ',\n  "unknown": '
  printf '%s' "${unknowns}" | json_array
  printf '\n}\n'
  [ "${violation_count}" -eq 0 ] && [ "${unknown_count}" -eq 0 ] && exit 0
  exit 1
fi

status=0

if [ "${violation_count}" -gt 0 ]; then
  echo "❌ Non-permissive licenses in the import graph:"
  echo
  printf '%s' "${violations}" | sed 's/^/    /'
  echo
  echo "   These carry source-disclosure or use restrictions that reach code"
  echo "   linking them. Replace the dependency, or — if the obligation is"
  echo "   genuinely acceptable here — record the decision in"
  echo "   .license-allowlist with a reason."
  echo
  status=1
fi

if [ "${unknown_count}" -gt 0 ]; then
  echo "⚠️  Undetermined licenses (allowlist is deny-by-default, so these fail):"
  echo
  printf '%s' "${unknowns}" | sed 's/^/    /'
  echo
  echo "   Inspect each, then either add it to .license-allowlist with a"
  echo "   reason, or teach classify_license() the pattern if it is a common"
  echo "   license this script simply does not recognize yet."
  echo
  status=1
fi

if [ "${status}" -eq 0 ]; then
  echo "✅ ${allowed_count} dependencies checked, all permissive.$(
    [ "${exempt_count}" -gt 0 ] && printf ' %d allowlisted.' "${exempt_count}"
  )"
fi

exit "${status}"
