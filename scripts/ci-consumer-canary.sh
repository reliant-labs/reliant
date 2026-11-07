#!/usr/bin/env bash
# ci-consumer-canary.sh — run the consumer canary over the consumer checkouts
# the workflow made, and say plainly which consumers it could NOT check.
#
#   scripts/ci-consumer-canary.sh [BASE] [-- extra consumercanary flags]
#
# BASE defaults to HEAD^1: on a pull request actions/checkout checks out the
# merge commit, whose first parent is the base branch's tip, and on a push to
# main it is the previous commit, so `BASE..HEAD` is exactly the change.
#
# The consumers are expected in $CANARY_DIR (default _consumers):
#   _consumers/reliant         public; always checked out
#   _consumers/control-plane   private; checked out only when the deploy-key
#                              secret CONTROL_PLANE_DEPLOY_KEY exists
# A consumer whose directory is missing is reported as NOT CHECKED — in the
# job log, as a ::warning:: annotation and in the step summary — and never as
# a pass. Without the secret that is control-plane on every run (and on fork
# pull requests, which get no secrets); with it, both run. If neither is
# present the script fails: a canary that checked nothing is not green.
#
# The logic lives here and not in the workflow YAML so that it runs the same
# on a laptop: `task canary` is the local entry point, this script is the CI
# wrapper around it.
set -euo pipefail

base=HEAD^1
if [[ $# -gt 0 && $1 != -- ]]; then
  base=$1
  shift
fi
[[ ${1:-} == -- ]] && shift

dir=${CANARY_DIR:-_consumers}
summary=${GITHUB_STEP_SUMMARY:-/dev/null}

args=(-base "$base")
checked=()
missing=()
for name in reliant control-plane; do
  if [[ -d $dir/$name ]]; then
    # @HEAD: the commit the workflow checked out (main), by whatever branch name.
    args+=(-consumer "$name=$dir/$name@HEAD")
    checked+=("$name")
  else
    missing+=("$name")
  fi
done

if [[ ${#checked[@]} -eq 0 ]]; then
  echo "error: no consumer checkout under $dir/ (expected reliant and/or control-plane)" >&2
  exit 2
fi

{
  echo "### Consumer canary scope"
  echo
  echo "- checked: ${checked[*]}"
} >>"$summary"
for name in "${missing[@]}"; do
  msg="$name was NOT CHECKED: no checkout under $dir/$name"
  if [[ $name == control-plane ]]; then
    msg="$msg (the CONTROL_PLANE_DEPLOY_KEY secret is not available to this run; see dev-docs/consumer-canary.md)"
  fi
  echo "::warning title=Consumer canary::$msg"
  echo "- **NOT CHECKED:** $name — $msg" >>"$summary"
done

exec go run ./internal/consumercanary "${args[@]}" "$@"
