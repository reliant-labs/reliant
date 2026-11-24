#!/usr/bin/env bash
# shellcheck disable=SC2016 # the backticks in the reports are markdown, not command substitution
# Keeps ONE rolling `visual:stale` issue for the visual baseline on main;
# visual-nightly.yml runs it after scripts/visual-gate.sh.
#
#   VERDICT=mismatch|pass RUN_URL=<run url> SHA=<sha> REPORT=<report.md> visual-stale-issue.sh
#
# Mismatch: the report (the surfaces that differ and what differed; the diffs
# are the run's artifact) goes on the newest `visual:stale` issue in ANY
# state, reopened if it is closed, and on a new one only when none exists.
# Pass: that issue is closed. The label is the key, never the title; one issue
# keeps every episode in one thread, as main-red-issue.sh does for CI.
set -euo pipefail

label="visual:stale"
title="visual baselines are stale on main"

case "${VERDICT:-}" in
  mismatch|pass) ;;
  *) echo "visual-stale-issue: VERDICT must be mismatch or pass, got '${VERDICT:-}'" >&2; exit 2 ;;
esac

gh label create "${label}" --force --color FBCA04 \
  --description "The visual baseline differs on main; opened and closed by visual-nightly.yml" >/dev/null
# `gh issue list` is newest first.
latest="$(gh issue list --label "${label}" --state all --limit 1 --json number,state --jq '.[] | "\(.number) \(.state)"')"
number="${latest%% *}"
state="${latest#* }"

if [ "${VERDICT}" = "pass" ]; then
  if [ -n "${number}" ] && [ "${state}" = "OPEN" ]; then
    gh issue close "${number}" --comment "Every visual capture matches its baseline on main again at \`${SHA}\`: ${RUN_URL}"
  fi
  exit 0
fi

body="$(mktemp)"
trap 'rm -f "${body}"' EXIT
{
  cat "${REPORT}"
  printf '\nRun: %s (the diffs are its `visual-results` artifact)\n' "${RUN_URL}"
} >"${body}"

if [ -z "${number}" ]; then
  gh issue create --title "${title}" --label "${label}" --body-file "${body}"
  exit 0
fi

# A re-run of a reported run says it once. Read into a variable so an early
# grep exit cannot SIGPIPE the pipeline.
seen="$(gh issue view "${number}" --json body,comments --jq '[.body, .comments[].body] | join("\n")')"
if [ "${state}" != "OPEN" ]; then
  gh issue reopen "${number}"
fi
if ! grep -qF "${RUN_URL}" <<<"${seen}"; then
  gh issue comment "${number}" --body-file "${body}"
fi
