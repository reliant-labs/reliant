#!/usr/bin/env bash
# integration-packages.sh — the packages `task test:integration` runs when it
# is given none: every package of the module with an integration-tagged test
# file, i.e. whose test files differ under -tags=integration. Prints one
# import path per line, and nothing when no package has one.
#
# WHY NOT ./...: -tags=integration only ADDS files, so it compiles every
# untagged test as well. Over ./... the integration lane re-ran the whole unit
# lane under -race (minus the testkit.Slow tests) right after `task test` had
# run it: ~14 of the release gate's ~70 minutes, with not one integration test
# among them (#1934). A package with no integration test has nothing for this
# lane to add.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export LC_ALL=C # one collation for sort and comm

fmt='{{.ImportPath}} {{join .TestGoFiles ","}} {{join .XTestGoFiles ","}}'
default="$(go list -e -f "${fmt}" ./... | sort)"
tagged="$(go list -e -tags=integration -f "${fmt}" ./... | sort)"
comm -13 <(printf '%s\n' "${default}") <(printf '%s\n' "${tagged}") | cut -d' ' -f1
