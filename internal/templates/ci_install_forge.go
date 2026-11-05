package templates

import "strings"

// installForgeScript is the ONE shell script every scaffolded workflow runs
// to install forge — ci.yml (every job that runs forge), deploy.yml,
// and e2e.yml's k3d runtime all render it through the
// `installForgeRun` template func, so there is exactly one copy to get right.
//
// The version comes from the PROJECT when the job runs, never from the
// workflow: the workflows are scaffold-once, so a version stamped into one
// froze while go.mod moved on (houndersclub: ci.yml at 7787cb0e, go.mod at
// 7355bcb3, and verify-generated refused the newer committed state).
//
// How the version is chosen, and why each branch fails LOUDLY:
//
//   - WHETHER go.mod requires (or replaces) forge is read from the go.mod
//     FILE — `go mod edit -json`, which touches no network and no module
//     cache. It is never inferred from a resolution failing: the previous
//     script ran `go list -m … 2>/dev/null || <forge.yaml>`, and on a runner
//     where the lookup could not resolve it silently installed forge.yaml's
//     older version — the exact drift this script exists to prevent.
//   - A `replace` of forge fails: CI cannot install a local checkout.
//   - go.mod requires forge → install the version the module graph selects
//     (`go list -m`, the one the code compiles against). If that resolution
//     fails, the step fails with go's own error. There is no fallback.
//   - go.mod does not require forge (a CLI or library that never links it)
//     → forge.yaml's forge_version, the generator it was scaffolded with.
//   - An uninstallable pin (a +dirty build, dev, 0.0.0) fails by name.
const installForgeScript = `set -euo pipefail
forge=github.com/reliant-labs/forge
command -v jq >/dev/null || { echo "::error::jq is required to read go.mod (preinstalled on GitHub-hosted runners)"; exit 1; }
mod=$(GOWORK=off go mod edit -json)
replaced=$(jq -r --arg m "$forge" '.Replace[]? | select(.Old.Path == $m) | "\(.New.Path) \(.New.Version // "")"' <<<"$mod")
if [ -n "$replaced" ]; then
  echo "::error file=go.mod::go.mod replaces $forge with ${replaced% } — CI cannot install that forge. Bridge a local forge checkout with an uncommitted go.work instead."
  exit 1
fi
if [ -n "$(jq -r --arg m "$forge" '.Require[]? | select(.Path == $m) | .Path' <<<"$mod")" ]; then
  v=$(GOWORK=off go list -m -f '{{.Version}}' "$forge")
else
  v=$(sed -n 's/^forge_version:[[:space:]]*//p' forge.yaml | tr -d "\"' ")
fi
case "$v" in
  v*+*|""|dev|0.0.0)
    echo "::error file=go.mod::no installable forge version (got '$v') — require $forge in go.mod, or set forge_version in forge.yaml to a published version."
    exit 1 ;;
esac
go install "$forge/cmd/forge@${v}"
`

// installForgeRun renders installForgeScript as a YAML `run: |` block whose
// key sits at `indent` spaces (the step's key column; body lines are indented
// two further).
func installForgeRun(indent int) string {
	key := strings.Repeat(" ", indent)
	body := key + "  "
	var b strings.Builder
	b.WriteString(key + "run: |\n")
	lines := strings.Split(strings.TrimRight(installForgeScript, "\n"), "\n")
	for i, line := range lines {
		b.WriteString(body + line)
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
