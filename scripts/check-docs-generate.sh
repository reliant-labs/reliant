#!/usr/bin/env bash
# Fail when regenerating the docs changes the tree.
#
# ── Why this exists ──────────────────────────────────────────────────────
#
# The reference docs (generated/docs-source/**, docs/reference/*.mdx,
# docs/changelog.mdx), the workflow-builder SKILL.md, the keyboard-shortcut
# tables and internal/workflow/runtime/schema/field_descriptions.go are
# written by tools/docgen and scripts/generate-mintlify-reference.py, and all
# of it is committed. A hand edit to one of those outputs is reverted by the
# next regeneration (reliant #1228 edited SKILL.md and #1230 had to move the
# text into tools/docgen/assembler), and an input changed without
# regenerating leaves main with docs that no longer match their source.
#
# ── What it judges ───────────────────────────────────────────────────────
#
# The CHANGE regeneration makes, not the cleanliness of the tree: every dirty
# or untracked file is hashed before and after, so it runs as well on a
# working tree with your uncommitted edits (`task test:affected`) as on a
# clean checkout (CI). Any file the generators create, rewrite or delete is
# named and fails it. What they regenerated is left in the tree.
#
# It regenerates with `task generate:docs`, the one list of docs generators
# (Taskfile.yml). Each generate:docs:<name> task pins GOTOOLCHAIN and
# GOWORK=off itself, so this regenerates exactly as `task generate` does.
# TASK=<command> runs another task binary.
#
# Usage:
#   scripts/check-docs-generate.sh [--github]   (or: task generate:docs:check)
#     --github   also ::error annotations and the diff, for CI
set -euo pipefail

github=0
if [ "${1:-}" = "--github" ]; then github=1; shift; fi
if [ "$#" != 0 ]; then
  echo "usage: $0 [--github] (run it as: task generate:docs:check)" >&2
  exit 2
fi

cd "$(git rev-parse --show-toplevel)"

# One line per dirty or untracked path: "<path>\t<blob hash, or 'deleted'>".
# The same snapshot as scripts/check-forge-generate.sh.
snapshot() {
  git ls-files -z --modified --others --exclude-standard | sort -zu |
    while IFS= read -r -d '' f; do
      if [ -e "${f}" ] || [ -L "${f}" ]; then
        printf '%s\t%s\n' "${f}" "$(git hash-object --no-filters -- "${f}")"
      else
        printf '%s\tdeleted\n' "${f}"
      fi
    done
}

before="$(snapshot)"
echo "==> regenerating the docs: task generate:docs"
read -r -a task <<<"${TASK:-task}"
"${task[@]}" generate:docs
after="$(snapshot)"

changed=()
# An empty snapshot is one blank line, which diff reports too: skip it.
while IFS= read -r f; do if [ -n "${f}" ]; then changed+=("${f}"); fi; done < <(
  diff <(printf '%s\n' "${before}") <(printf '%s\n' "${after}") |
    awk -F'\t' '/^[<>] / { print substr($1, 3) }' | sort -u
)
if [ "${#changed[@]}" = 0 ]; then
  echo "The docs generators are a no-op: the generated docs match their inputs."
  exit 0
fi

echo "The docs generators changed ${#changed[@]} file(s):" >&2
printf '  %s\n' "${changed[@]}" >&2
if [ "${github}" = 1 ]; then
  for f in "${changed[@]}"; do echo "::error file=${f},title=Stale generated docs::regenerating the docs changes ${f}"; done
  echo "--- diff (tracked files) ---"
  git --no-pager diff -- "${changed[@]}" || true
fi
msg="Commit what they wrote (now in your tree), in the change that caused it. If a file above is one you edited by hand, regeneration reverted your edit: put the text in its generator's source (tools/docgen/..., or the input it reads) and run task generate:docs:check again."
if [ "${github}" = 1 ]; then echo "::error title=Generated docs are out of date::${msg}"; fi
echo "error: generated docs are out of date" >&2
echo "${msg}" >&2
exit 1
