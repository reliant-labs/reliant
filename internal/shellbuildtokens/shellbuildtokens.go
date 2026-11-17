// Package shellbuildtokens finds retired forge substitution tokens left in a
// ShellBuild `cmd`.
//
// Forge used to substitute a fixed set of ${NAME} tokens into a ShellBuild
// command before running it. It no longer does: the command is plain KCL and
// forge runs the rendered string byte-for-byte. So a command that still writes
// `${TARGETARCH}` is no longer a forge token — it is a SHELL variable, and the
// shell will resolve it from an environment where nothing ever set it.
//
// That fails in two very different ways, and the quiet one is why this package
// exists rather than a note in a changelog:
//
//   - Most of them fail loudly. `docker push ${REGISTRY}/${IMAGE}:${TAG}`
//     becomes `docker push /:` and docker rejects it.
//   - `GOARCH=${TARGETARCH}` becomes `GOARCH=`, which is not an error. Go
//     reads an empty GOARCH as "unset" and builds for the HOST arch, so an
//     amd64 cluster silently receives an arm64 binary that crash-loops with
//     `exec format error` — a failure whose message names nothing about the
//     build that caused it.
//
// A token is only reported when the key is NOT declared in that build's `env`
// map. A user who writes `env = {"TARGETARCH" = forge.target_arch()}` and then
// uses `${TARGETARCH}` in the command is correct: forge merges the declared env
// onto the process environment, so the shell resolves it. That is the migration
// path for a long command that names a token many times, and the rule must not
// flag it.
//
// The check is used by `forge lint` (as a gating rule) and refused by
// `forge generate`. It is deliberately NOT a KCL schema check: a KCL `check:`
// block cannot see whether the string it is judging came from a raw string, it
// would fire on a project's own legitimately-named env key, and the remediation
// it needs to print is per-token prose that does not belong in a schema.
//
// surface is package-level pure functions over strings and a directory tree
// (Check/ScanSource/ScanKCLTree/Error) plus two DISPLAY methods on the Finding
// data record (Message/Remediation) — which are the only reason the
// require-contract rule fires at all. There is no constructor, no Deps, no
// state and no I/O beyond reading the KCL files it is pointed at, so there is
// nothing a caller would substitute: a contract.go here could only restate the
// free functions as an interface with exactly one implementation, which the
// architecture rules call indirection rather than abstraction. Unexporting is
// not available either — internal/cli/lint and internal/cli/generate_pipeline
// are cross-package consumers of this surface, and Message/Remediation are how
// a finding renders itself in both the text and JSON lint arms. Tests are the
// consumer of the behaviour, exactly as in internal/pkgguard.
//
//forge:exclude-contract: analyzer-shaped check, not a service. The exported
package shellbuildtokens

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Replacements maps each retired token to what to write in KCL instead.
//
// Every entry names a KCL expression that reads the value from where it
// actually lives, which is the whole point of the change: the command is
// composed in a language that can already see these, so forge does not need to
// reach into the string afterwards.
var Replacements = map[string]string{
	"IMAGE":        `the workload's own image reference — write it in KCL (e.g. "ghcr.io/acme/api"), the same string the workload's image field names`,
	"TAG":          `forge.image_tag("<env>") — the tag forge resolved for this build, bound as the image_tag input before the render`,
	"CODE_VERSION": `forge.image_tag("<env>") — CODE_VERSION was always the same value as TAG`,
	"SERVICE":      `a KCL literal — the workload's name is written right there in the same file`,
	"TARGETARCH":   `forge.target_arch() — the GOARCH forge resolved for this build`,
	"REGISTRY":     `write the registry into the image reference itself (e.g. "ghcr.io/acme/api"); a registry belongs to an image, not to an env`,
	"PROJECT_DIR":  `file.workdir() — forge renders with the project root as the working directory`,
	"ENV":          `forge.env() — the environment being built`,
	"BUILD_CWD":    `a KCL literal, or "." — the command already runs in the cwd this build declares`,
}

// tokenRef matches a ${NAME} reference. Only the braced form is matched: a
// bare `$TARGETARCH` was never substituted as a word on its own in a way that
// can be told apart from an ordinary shell variable, and flagging every `$FOO`
// in a shell script would bury the real finding.
var tokenRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Finding is one retired token found in one ShellBuild command.
type Finding struct {
	// Workload is the name of the workload whose build declares the command.
	Workload string
	// Token is the bare token name, without the `${}`.
	Token string
	// Replacement is the KCL expression to write instead.
	Replacement string
}

// Message is the one-line description of the finding.
func (f Finding) Message() string {
	return fmt.Sprintf("workload %q: ShellBuild cmd contains ${%s}, which forge no longer substitutes — the shell will see it as an unset variable",
		f.Workload, f.Token)
}

// Remediation is the actionable half: what to write instead, and the escape
// hatch for keeping the token spelling.
func (f Finding) Remediation() string {
	return fmt.Sprintf("use %s; or, to keep the ${%s} spelling, declare it in this build's env map (env = {%q = <the KCL value>}) so the shell resolves it",
		f.Replacement, f.Token, f.Token)
}

// Check reports every retired token in cmd that is not declared in env.
//
// workload names the workload for the message. env is the ShellBuild's
// declared `env` map; a token whose key appears there is NOT reported, because
// forge merges that map onto the process environment and the shell resolves it
// correctly.
//
// Findings are sorted by token so output is stable across runs, and each token
// is reported once however many times the command names it — the fix is the
// same edit regardless.
func Check(workload, cmd string, env map[string]string) []Finding {
	if cmd == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, m := range tokenRef.FindAllStringSubmatch(cmd, -1) {
		token := m[1]
		if _, retired := Replacements[token]; !retired || seen[token] {
			continue
		}
		if _, declared := env[token]; declared {
			continue
		}
		seen[token] = true
	}
	out := make([]Finding, 0, len(seen))
	for token := range seen {
		out = append(out, Finding{Workload: workload, Token: token, Replacement: Replacements[token]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Token < out[j].Token })
	return out
}

// Error renders findings as the refusal `forge generate` returns. nil for no
// findings, so a caller can `if err := ...; err != nil`.
//
// Generate refuses rather than warns because it is the step that produces the
// artifacts a build then runs: letting it pass would hand the user a rendered
// command that is going to build for the wrong architecture, having just had
// the chance to say so.
func Error(findings []Finding) error {
	if len(findings) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString("ShellBuild commands contain retired forge substitution tokens.\n")
	sb.WriteString("forge runs a ShellBuild cmd verbatim — it substitutes nothing — so these reach the shell as unset variables.\n")
	for _, f := range findings {
		fmt.Fprintf(&sb, "\n  %s\n      → %s\n", f.Message(), f.Remediation())
	}
	return fmt.Errorf("%s", sb.String())
}
