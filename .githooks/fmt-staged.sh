#!/usr/bin/env bash
#
# Formats the files staged for this commit, and only those, by language.
#
# The pre-commit hook used to run `task fmt` (`go fmt ./...`), which formats
# the whole module on every commit: over a minute on a TypeScript-only commit.
# This formats what the commit carries and nothing else, so its cost follows
# the size of the commit, not the repo. `task fmt` stays the whole-repo
# command.
#
#   *.go                    gofmt -w (what `go fmt` runs)
#   web files of a package  that package's own prettier (node_modules/.bin,
#   under WEB_ROOTS         the version its lockfile pins) — only when its
#                           package.json declares prettier. None does today
#                           (their style is eslint's, `task fmt:web`), so
#                           today this is a no-op for them.
#   anything else           nothing
#
# A formatted file is re-staged when it was fully staged. A partially staged
# file (unstaged edits beside the staged ones) is formatted in the work tree
# but not re-staged, since `git add` would sweep the unstaged hunks into the
# commit; if its STAGED content is unformatted, the commit is refused with the
# command to fix it.
#
# GOFMT overrides the gofmt binary (the hook's test points it at a recorder).

set -euo pipefail

GOFMT="${GOFMT:-gofmt}"
WEB_ROOTS=(frontends/web electron)

YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

cd "$(git rev-parse --show-toplevel)"

# Added, copied, modified or renamed: a deletion has nothing to format.
staged=()
while IFS= read -r -d '' f; do
    [ -f "$f" ] && staged+=("$f") # staged, then deleted from the work tree: nothing to format
done < <(git diff --cached --name-only --diff-filter=ACMR -z)

# Files with unstaged edits too (bash 3.2 has no associative arrays, so this
# is a newline-joined list matched with a case pattern).
partial=$'\n'
while IFS= read -r -d '' f; do partial+="$f"$'\n'; done < <(git diff --name-only -z)
is_partial() { case "$partial" in *$'\n'"$1"$'\n'*) return 0 ;; esac; return 1; }

refused=0

# restage FILE... re-stages each fully staged file the formatter changed.
restage() {
    local f
    for f in "$@"; do
        is_partial "$f" && continue
        git diff --quiet -- "$f" && continue
        echo "  Re-staging formatted: $f"
        git add -- "$f"
    done
}

# ── Go ───────────────────────────────────────────────────────────────────
go_files=()
for f in "${staged[@]+"${staged[@]}"}"; do
    case "$f" in *.go) go_files+=("$f") ;; esac
done
if [ ${#go_files[@]} -gt 0 ]; then
    echo -e "${YELLOW}gofmt: ${#go_files[@]} staged Go file(s)${NC}"
    "$GOFMT" -w -- "${go_files[@]}"
    restage "${go_files[@]}"
    for f in "${go_files[@]}"; do
        is_partial "$f" || continue
        if [ -n "$(git show ":$f" | "$GOFMT" -l)" ]; then
            echo -e "${RED}  $f is partially staged and its staged content is not gofmt-ed.${NC}"
            echo "  The work tree copy is formatted now; stage it: git add -p -- $f"
            refused=1
        fi
    done
fi

# ── Web ──────────────────────────────────────────────────────────────────
for root in "${WEB_ROOTS[@]}"; do
    web_files=()
    for f in "${staged[@]+"${staged[@]}"}"; do
        case "$f" in
        "$root"/node_modules/* | "$root"/dist/*) ;;
        "$root"/*.ts | "$root"/*.tsx | "$root"/*.js | "$root"/*.jsx | "$root"/*.mjs | \
            "$root"/*.cjs | "$root"/*.css | "$root"/*.json | "$root"/*.html)
            web_files+=("$f") ;;
        esac
    done
    [ ${#web_files[@]} -gt 0 ] || continue
    # Only a package that pins prettier gets it; no pin means no formatter.
    grep -q '"prettier"' "$root/package.json" 2>/dev/null || continue
    prettier="$root/node_modules/.bin/prettier"
    if [ ! -x "$prettier" ]; then
        echo -e "${RED}$root pins prettier but it is not installed: run npm ci in $root${NC}"
        exit 1
    fi
    echo -e "${YELLOW}prettier: ${#web_files[@]} staged file(s) in $root${NC}"
    "$prettier" --write --ignore-unknown -- "${web_files[@]}" >/dev/null
    restage "${web_files[@]}"
    for f in "${web_files[@]}"; do
        is_partial "$f" || continue
        if ! git show ":$f" | "$prettier" --check --stdin-filepath "$f" >/dev/null 2>&1; then
            echo -e "${RED}  $f is partially staged and its staged content is not prettier-formatted.${NC}"
            echo "  The work tree copy is formatted now; stage it: git add -p -- $f"
            refused=1
        fi
    done
done

exit "$refused"
