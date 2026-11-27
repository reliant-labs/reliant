// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// The stage script is the gate's command inside the boundary (E9f, owner
// decision D13). It runs the planned stages in order from the tree's root,
// each to its own log, maps each exit onto 0 pass / 1 fail / 2 unproven,
// stops at the first stage that does not pass (later ones are skipped: a
// broken build has nothing to scan), writes gate-result.json, and exits with
// the gate's class.
//
// Everything in it is fixed text or a single-quoted value: a stage command
// from pipeline.yaml is passed to `sh -c` as one argument, never spliced into
// the script. The script runs inside the boundary, beside the code it gates,
// so its output is read back as data and checked against the plan and the
// exit code (report.go); it is not trusted on its own.
const scriptPrelude = `set -u
root="${RELIANT_GATE_ROOT:-/gate}"
db="${RELIANT_GATE_VULNDB:-/vulndb}"
out="$root/out"
art="$root/artifacts"
logs="$root/logs"
scan="$root/scan"
skips="$root/skips"
mkdir -p "$out/logs" "$out/scan" "$logs" "$scan" "$skips" "$art" "$HOME" "$TMPDIR" || exit 2
export RELIANT_GATE_ARTIFACTS="$art"
stages=""
stop=0
overall=0
emit() {
	if [ -n "$stages" ]; then stages="$stages,"; fi
	stages="$stages{\"name\":\"$1\",\"state\":\"$2\",\"exit_code\":$3,\"seconds\":$4,\"note\":\"$5\",\"docker_skips\":${6:-0}}"
}
judge() {
	t1=$(date +%s)
	case "$2" in
	0) st=pass ;;
	2) st=unproven; stop=1; overall=2 ;;
	*) st=fail; stop=1; overall=1 ;;
	esac
	emit "$1" "$st" "$2" "$((t1 - $3))" "$4" "${5:-0}"
}
run_cmd() {
	if [ "$stop" = 1 ]; then emit "$1" skipped -1 0 ""; return 0; fi
	echo "reliant-gate: stage $1"
	t0=$(date +%s)
	skiplog="$skips/$1.${RELIANT_RUN_ID:-run}"
	rm -f "$skiplog"
	(cd "$root/src" && RELIANT_GATE_STAGE="$1" RELIANT_GATE_DOCKER_SKIPS="$skiplog" exec /bin/sh -c "$2") >"$logs/$1.log" 2>&1 </dev/null
	code=$?
	tail -c LOGTAIL "$logs/$1.log" >"$out/logs/$1.log" 2>/dev/null
	nskip=$(grep -c '' "$skiplog" 2>/dev/null) || nskip=0
	case "$nskip" in ''|*[!0-9]*) nskip=0 ;; esac
	if [ "$nskip" -gt 0 ]; then echo "reliant-gate: stage $1: $nskip container test(s) skipped: docker unavailable (gate)"; fi
	judge "$1" "$code" "$t0" "" "$nskip"
}
run_scan() {
	if [ "$stop" = 1 ]; then emit sbom_scan skipped -1 0 ""; return 0; fi
	echo "reliant-gate: stage sbom_scan"
	t0=$(date +%s)
	if ! command -v forge >/dev/null 2>&1 || ! forge vuln gate --help >/dev/null 2>&1; then
		echo "forge vuln gate is not available in the gate image" >"$out/logs/sbom_scan.log"
		judge sbom_scan 2 "$t0" forge_vuln_gate_missing
		return 0
	fi
	tgt="$root/src"
	if [ -n "$(ls -A "$art" 2>/dev/null)" ]; then tgt="$art"; fi
	(cd "$root/src" && RELIANT_GATE_STAGE=sbom_scan exec forge vuln gate "$tgt" --db "$db" --out "$scan" --json "$@") >"$scan/stdout.json" 2>"$logs/sbom_scan.log" </dev/null
	code=$?
	tail -c LOGTAIL "$logs/sbom_scan.log" >"$out/logs/sbom_scan.log" 2>/dev/null
	for f in vuln-gate.json stdout.json; do
		if [ -f "$scan/$f" ] && [ ! -L "$scan/$f" ]; then head -c SCANCAP "$scan/$f" >"$out/scan/$f"; fi
	done
	judge sbom_scan "$code" "$t0" ""
}
`

const scriptEpilogue = `printf '{"version":1,"exit_code":%d,"stages":[%s]}\n' "$overall" "$stages" >"$out/gate-result.json.tmp" || exit 2
mv -f "$out/gate-result.json.tmp" "$out/gate-result.json" || exit 2
repo="$root/src/.reliant/gate-result.json"
if [ -f "$repo" ] && [ ! -L "$repo" ]; then head -c REPOCAP "$repo" >"$out/repo-gate-result.json" 2>/dev/null || true; fi
exit "$overall"
`

// Script is the stage script for stages, a plan [validatePlan] accepted.
//
// Only what report reads is under the output directory: each stage's log
// tail, the scan's verdict documents and gate-result.json. Full logs and the
// SBOMs stay in the boundary and go with its teardown.
func Script(stages []PlanStage) string {
	var b strings.Builder
	b.WriteString(strings.NewReplacer("LOGTAIL", strconv.Itoa(logTailBytes), "SCANCAP", strconv.Itoa(scanDocMaxBytes)).Replace(scriptPrelude))
	for _, s := range stages {
		switch {
		case s.State == config.StageOff:
			b.WriteString("emit " + string(s.Name) + " off -1 0 \"\"\n")
		case s.State != config.StageOn:
			b.WriteString("emit " + string(s.Name) + " unavailable -1 0 \"\"\n")
		case s.Kind == config.StageBuiltinScan:
			b.WriteString("run_scan")
			if s.ScanEnv != "" {
				b.WriteString(" --env " + shellQuote(s.ScanEnv))
			}
			b.WriteString("\n")
		default:
			b.WriteString("run_cmd " + string(s.Name) + " " + shellQuote(s.Command) + "\n")
		}
	}
	b.WriteString(strings.Replace(scriptEpilogue, "REPOCAP", strconv.Itoa(repoResultMaxBytes), 1))
	return b.String()
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
