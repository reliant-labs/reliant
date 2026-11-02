#!/usr/bin/env bash
# Keeps ONE rolling `ci:main-red` issue for main's CI verdict; gate-health.yml's
# main-health job runs it with the audit's outputs.
#
#   VERDICT=red|green RUN_URL=<run url> SHA=<sha> REPORT=<report.md> main-red-issue.sh
#
# Red: the report goes on the newest `ci:main-red` issue in ANY state — reopened
# if it is closed — and on a new one only when none exists. Green: that issue is
# closed. The label is the key, never the title. Opening a fresh issue per red
# episode turned a main that flapped red/green/red into 35 issues in the 14
# days to 2026-11-01, each a new notification with the history split across
# threads; reopening keeps every episode in one.
set -euo pipefail

label="ci:main-red"
title="main is red: CI"

gh label create "${label}" --force --color B60205 \
  --description "CI is red on main; opened and closed by gate-health.yml" >/dev/null
# `gh issue list` is newest first.
latest="$(gh issue list --label "${label}" --state all --limit 1 --json number,state --jq '.[] | "\(.number) \(.state)"')"
number="${latest%% *}"
state="${latest#* }"

if [ "${VERDICT}" = "green" ]; then
  if [ -n "${number}" ] && [ "${state}" = "OPEN" ]; then
    gh issue close "${number}" --comment "main is green again at \`${SHA}\`: ${RUN_URL}"
  fi
  exit 0
fi

if [ -z "${number}" ]; then
  gh issue create --title "${title}" --label "${label}" --body-file "${REPORT}"
  exit 0
fi

# The weekly backstop re-audits a run already reported: say it once. Read into
# a variable so an early grep exit cannot SIGPIPE the pipeline.
seen="$(gh issue view "${number}" --json body,comments --jq '[.body, .comments[].body] | join("\n")')"
if [ "${state}" != "OPEN" ]; then
  gh issue reopen "${number}"
  # Older episodes were titled "main is red: PR CI".
  gh issue edit "${number}" --title "${title}"
fi
if ! grep -qF "${RUN_URL}" <<<"${seen}"; then
  gh issue comment "${number}" --body-file "${REPORT}"
elif [ "${state}" != "OPEN" ]; then
  gh issue comment "${number}" --body "main is still red at \`${SHA}\`: ${RUN_URL}"
fi
