// Copyright (c) 2025 Reliant Labs
package practions

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// CheckScript is the fixer's scoped check as a POSIX shell script: what the
// `pr` workflow's check node runs in the fixer's checkout, after the fixer
// and outside its session (redesign §6.1, the owner's fix-loop shape). It
// is built from the repository's config as read from its base branch, so a
// fix cannot change what checks it: the PR head's own pipeline.yaml is
// never read.
//
// It runs the repository's `check` command when pipeline.yaml declares one
// (the repo's own fast tier, scoped by the repo). Otherwise it runs the
// gate's stages scoped to the diff from the base branch: lint (the gate's
// lint command), the affected tests (`go test -short` over the changed Go
// packages; off for anything else) and the build (`go build ./...` in a Go
// module, else the gate's build command). A stage after a failed one does
// not run. A stage with nothing to run is off, never passed.
//
// Its last line of stdout is the report pr.checked reads
// (`{"stages":[{"name","verdict","failures":[{"check","detail"}]}]}`), each
// failure's detail the tail of that stage's output. It exits 1 when a stage
// failed. Each stage's whole output goes to stderr, for the run's log.
func CheckScript(cfg config.Resolved, baseRef string) string {
	if baseRef == "" {
		baseRef = "main"
	}
	var b strings.Builder
	b.WriteString(checkPrelude)
	fmt.Fprintf(&b, "base=$(git merge-base HEAD %s 2>/dev/null || git merge-base HEAD %s 2>/dev/null || git rev-parse HEAD)\n",
		shellQuote("origin/"+baseRef), shellQuote(baseRef))
	b.WriteString(`{ git diff --name-only "$base" --; git ls-files --others --exclude-standard; } >"$t/changed" 2>/dev/null` + "\n")
	if cfg.Check != nil && strings.TrimSpace(cfg.Check.Run) != "" {
		stage(&b, "check", shellQuote(cfg.Check.Run))
	} else {
		stage(&b, "lint", shellQuote(stageCommand(cfg.Gate.Stage(config.StageLint))))
		b.WriteString(goPackages)
		b.WriteString(`if [ -f go.mod ] && [ -s "$t/packages" ]; then test_cmd=run_go_tests; else test_cmd=; fi` + "\n")
		stage(&b, "test", `"$test_cmd"`)
		fmt.Fprintf(&b, "if [ -f go.mod ]; then build_cmd='go build ./...'; else build_cmd=%s; fi\n",
			shellQuote(stageCommand(cfg.Gate.Stage(config.StageBuild))))
		stage(&b, "build", `"$build_cmd"`)
	}
	b.WriteString("printf '%s\\n' \"{\\\"stages\\\":[$out]}\"\nexit \"$failed\"\n")
	return b.String()
}

// stageCommand is an on stage's command, or "" when it has none to run
// here (off, unavailable, or the built-in scan).
func stageCommand(s config.ResolvedStage) string {
	if s.State != config.StageOn || s.Kind != config.StageCommand {
		return ""
	}
	return strings.TrimSpace(s.Command)
}

// stage appends one stage. arg is the command as one shell word: a quoted
// literal from the config, or a variable the script set ("$test_cmd"). An
// empty command is off.
func stage(b *strings.Builder, name, arg string) {
	fmt.Fprintf(b, "run_stage %s %s\n", name, arg)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// checkPrelude defines the report helpers. esc keeps a log's last 3000
// bytes as a JSON string body: control characters dropped, tabs as
// spaces, backslashes and quotes escaped, newlines as \n.
const checkPrelude = `# The fixer's scoped check (reliant/pr.checked@1 reads the last line).
set -u
t=$(mktemp -d) || exit 2
trap 'rm -rf "$t"' EXIT
out=''
failed=0
esc() { tail -c 3000 "$1" | LC_ALL=C tr -d '\000-\010\013-\037' | tr '\t' ' ' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' | awk 'BEGIN{ORS="\\n"}{print}'; }
add() { [ -n "$out" ] && out="$out,"; out="$out$1"; }
run_stage() {
  name=$1; cmd=$2
  if [ "$failed" != 0 ] || [ -z "$cmd" ]; then add "{\"name\":\"$name\",\"verdict\":\"off\"}"; return; fi
  echo "== $name: $cmd" >&2
  if (eval "$cmd") >"$t/$name.log" 2>&1; then
    cat "$t/$name.log" >&2; add "{\"name\":\"$name\",\"verdict\":\"pass\"}"
  else
    cat "$t/$name.log" >&2; failed=1
    add "{\"name\":\"$name\",\"verdict\":\"fail\",\"failures\":[{\"check\":\"$name\",\"detail\":\"$(esc "$t/$name.log")\"}]}"
  fi
}
`

// goPackages records existing changed Go directories, then passes them as
// literal arguments. Paths must never become shell source for run_stage's eval.
const goPackages = `grep '\.go$' "$t/changed" | while IFS= read -r f; do d=$(dirname "$f"); ls "$d"/*.go >/dev/null 2>&1 && printf './%s\n' "$d"; done | sort -u >"$t/packages"
run_go_tests() {
  set --
  while IFS= read -r pkg; do set -- "$@" "$pkg"; done <"$t/packages"
  go test -short "$@"
}
`
