// File: internal/cli/lint/lint_config_reach.go
//
// config-reach — `forge lint --config-reach`.
//
// A config field that NO PROCESS LOADS. Since configs became per-binary
// ((forge.v1.binary_config) = {binary: "<name>"}), a config message is
// loaded by exactly the binary it names — so a message annotated for a
// binary that was renamed or deleted, or a message nothing composes and
// nothing binds, still exists in config.proto, still generates Go, KCL
// and env projections, and is read by nobody.
//
// Before the split this could not happen: one AppConfig went to every
// binary, so "unused" meant "unread by Go code", never "unloaded". The
// motivating project shipped 69 fields to every binary; after splitting,
// a field that falls out of every binary's surface is invisible, because
// nothing errors — the generated code for it is simply never loaded.
//
// ── What it reports, and what it deliberately does NOT ────────────────
//
// It reports UNREACHABLE fields: fields on a config message that no
// binary and no frontend can load. Reachability is a graph question with
// an exact answer, which is what makes the check safe to run everywhere.
//
// It does NOT report "composed but never read in Go". That was the other
// candidate, and it was rejected on purpose: proving a config field is
// never read requires whole-program analysis through a generated typed
// struct that is passed by value into Deps, embedded in other structs,
// and reflected over by the flag registrar. Any tractable approximation
// (grep the field's Go name) is wrong in both directions — it misses
// reflective reads and flags fields read through a local alias — and a
// noisy answer to "is this field used?" is worse than no answer, because
// the fix it suggests is DELETING A FIELD. A narrow check with an exact
// answer beats a clever one that is sometimes right.
//
// ── Reachability model ────────────────────────────────────────────────
//
// A config message is REACHABLE when any of these holds:
//
//	1. It names a binary that exists in cmd/            (binary-bound)
//	2. It names a frontend the project declares         (frontend-bound)
//	3. Some reachable message COMPOSES it, transitively (a shared block)
//	4. It is the PROJECT-GLOBAL ROOT config             (see below)
//
// Rule 4 is the one that matters most, and it is why a naive version of
// this check is dangerous. forge picks the FIRST unbound, uncomposed
// message as the project-global config — the `Config` alias that
// pkg/config.Load emits for (codegen: RootConfigMessage). The motivating
// project's AppConfig carries NO binary annotation for exactly this
// reason: it is in substance one binary's config, but it is also the
// root every subcommand and test helper calls. It has 75 fields. A check
// that flagged "composed by no binary" would report all 75 on the very
// project it was built for. Rule 4 is modeled after the generator's own
// selection so the check and the codegen agree by construction.
//
// ── Severity ──────────────────────────────────────────────────────────
//
// WARNING. An unreachable field is dead weight, not a defect: nothing
// misbehaves, and the remediation is a DELETION, which is a judgment the
// author has to make (a field may be unreachable because the binary that
// loads it is being written this week). A lint that gates on "delete
// this" invites the wrong reflex.
//
// ── False positives deliberately avoided ──────────────────────────────
//
//   - THE UNANNOTATED ROOT AppConfig (rule 4 above).
//   - SHARED DEFINITION BLOCKS (BaseConfig) — reachable transitively;
//     this is the documented shared-config pattern.
//   - FRONTEND-BOUND messages, loaded by a bundle rather than a process.
//   - AN UNKNOWN BINARY/FRONTEND SET. If cmd/ cannot be enumerated the
//     check reports NOTHING rather than concluding every binary is
//     missing. "I could not tell" must never render as "it is dead" —
//     that is the same silent-wrongness this whole exercise is against.
//   - A project with no config messages, or no descriptor yet.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// Reasons a config message is unreachable. These are the user-facing
// explanations, so each names the concrete thing that is missing.
const (
	configReachNoSuchBinary   = "no-such-binary"
	configReachNoSuchFrontend = "no-such-frontend"
	configReachOrphaned       = "orphaned"
)

// configReachFinding is one config field that no process loads.
type configReachFinding struct {
	// Message is the config message the field is declared on.
	Message string
	// Field is the proto field name.
	Field string
	// EnvVar is the field's env binding, when it has one — the most
	// recognizable handle for a config field in a deploy manifest.
	EnvVar string
	// Binary / Frontend is the name the message named, when it named one
	// that does not exist.
	Binary   string
	Frontend string
	// Reason is one of the configReach* constants.
	Reason string
}

// configReachFixHint renders the remediation. Each reason has a
// different literal fix, so the hint branches rather than emitting one
// vague sentence.
func configReachFixHint(f configReachFinding) string {
	switch f.Reason {
	case configReachNoSuchBinary:
		return fmt.Sprintf(
			"message %s is annotated `option (forge.v1.binary_config) = {binary: %q}`, but there is no cmd/%s — "+
				"so no process loads %s, and its generated loader, KCL schema and env projection go to nobody. "+
				"Either create the binary (`forge scaffold binary %s`), point the annotation at the binary that "+
				"should load it, compose %s onto a binary's config as a shared block, or delete the message.",
			f.Message, f.Binary, f.Binary, f.Field, f.Binary, f.Message)
	case configReachNoSuchFrontend:
		return fmt.Sprintf(
			"message %s is annotated `option (forge.v1.frontend_config) = {frontend: %q}`, but no forge.yaml "+
				"frontend is named %s — so no bundle loads %s. Point the annotation at a declared frontend, "+
				"or delete the message.",
			f.Message, f.Frontend, f.Frontend, f.Field)
	default:
		return fmt.Sprintf(
			"message %s is bound to no binary and no frontend, and no reachable config message composes it — "+
				"so nothing loads %s. Either bind it "+
				"(`option (forge.v1.binary_config) = {binary: \"<cmd/name>\"};`), compose it as a block on a config "+
				"that IS loaded (`%s %s = <next tag>;`), or delete the message.",
			f.Message, f.Field, f.Message, strings.ToLower(f.Message))
	}
}

// collectConfigReachFindingsForProject is the project-level entry: it
// resolves the config messages, the cmd/ binary set and the declared
// frontends, then delegates to the pure reachability pass.
//
// A project with no descriptor yet (never generated) yields nothing
// rather than an error — `forge lint` runs in that state routinely.
func collectConfigReachFindingsForProject(projectDir string, cfg *config.ProjectConfig) ([]configReachFinding, error) {
	messages, err := codegen.ParseConfigProtosFromDir(filepath.Join(projectDir, "proto", "config"))
	if err != nil || len(messages) == 0 {
		// No descriptor / no config protos: nothing to say. An error here
		// is a missing or stale descriptor, which the generate path
		// reports far better than a lint can.
		return nil, nil
	}
	return collectConfigReachFindings(messages, discoverBinaries(projectDir), declaredFrontends(cfg)), nil
}

// discoverBinaries lists the cmd/<name> leaves. It returns nil — meaning
// UNKNOWN, not empty — when cmd/ cannot be read, which the reachability
// pass treats as "do not judge binary bindings".
func discoverBinaries(projectDir string) []string {
	entries, err := os.ReadDir(filepath.Join(projectDir, "cmd"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// declaredFrontends lists the frontends forge.yaml declares. As with
// discoverBinaries, nil means UNKNOWN — a nil cfg (no forge.yaml, or it
// failed to load) must not be read as "this project has no frontends",
// which would make every frontend-bound config look dead.
func declaredFrontends(cfg *config.ProjectConfig) []string {
	if cfg == nil {
		return nil
	}
	out := []string{}
	for _, f := range cfg.Frontends {
		if f.Name != "" {
			out = append(out, f.Name)
		}
	}
	return out
}

// collectConfigReachFindings is the pure reachability pass — the shared
// engine behind text mode and `forge lint --json`, and the unit the
// tests drive directly.
//
// binaries / frontends are the names that EXIST. A nil slice means the
// set could not be determined, and every binding of that kind is then
// treated as reachable (see the file header: "I could not tell" must
// never render as "it is dead").
func collectConfigReachFindings(messages []codegen.ConfigMessage, binaries, frontends []string) []configReachFinding {
	if len(messages) == 0 {
		return nil
	}

	byName := make(map[string]*codegen.ConfigMessage, len(messages))
	for i := range messages {
		byName[messages[i].Name] = &messages[i]
	}

	// A message is COMPOSED when some other config message declares a
	// message-typed field naming it — the shared-block shape. Mirrors the
	// generator's own isBlock computation so the two agree.
	composed := map[string]bool{}
	for i := range messages {
		m := &messages[i]
		for _, f := range m.Fields {
			if f.ProtoType != "message" || f.MessageType == "" || f.MessageType == m.Name {
				continue
			}
			if _, known := byName[f.MessageType]; known {
				composed[f.MessageType] = true
			}
		}
	}

	binaryKnown, frontendKnown := binaries != nil, frontends != nil
	hasBinary := sliceSet(binaries)
	hasFrontend := sliceSet(frontends)

	// Seed the reachable set with every message a live binary or frontend
	// binds, plus the project-global root.
	reachable := map[string]bool{}
	for i := range messages {
		m := &messages[i]
		switch {
		case m.Binary != "":
			if !binaryKnown || hasBinary[m.Binary] {
				reachable[m.Name] = true
			}
		case m.Frontend != "":
			if !frontendKnown || hasFrontend[m.Frontend] {
				reachable[m.Name] = true
			}
		}
	}
	if root := rootConfigMessage(messages, composed); root != "" {
		reachable[root] = true
	}

	// Propagate through composition until fixpoint: a block composed by a
	// reachable message is itself reachable.
	for changed := true; changed; {
		changed = false
		for i := range messages {
			m := &messages[i]
			if !reachable[m.Name] {
				continue
			}
			for _, f := range m.Fields {
				if f.ProtoType != "message" || f.MessageType == "" {
					continue
				}
				if _, known := byName[f.MessageType]; known && !reachable[f.MessageType] {
					reachable[f.MessageType] = true
					changed = true
				}
			}
		}
	}

	var findings []configReachFinding
	for i := range messages {
		m := &messages[i]
		if reachable[m.Name] {
			continue
		}
		reason := configReachOrphaned
		switch {
		case m.Binary != "":
			reason = configReachNoSuchBinary
		case m.Frontend != "":
			reason = configReachNoSuchFrontend
		}
		for _, f := range m.Fields {
			// Block references are not leaves; the composed message's own
			// fields are reported on their own message.
			if f.ProtoType == "message" && f.MessageType != "" {
				continue
			}
			findings = append(findings, configReachFinding{
				Message:  m.Name,
				Field:    f.Name,
				EnvVar:   f.EnvVar,
				Binary:   m.Binary,
				Frontend: m.Frontend,
				Reason:   reason,
			})
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Message != findings[j].Message {
			return findings[i].Message < findings[j].Message
		}
		return findings[i].Field < findings[j].Field
	})
	return findings
}

// rootConfigMessage returns the project-global config message — the
// first message bound to no binary and no frontend that nothing
// composes.
//
// This mirrors the generator's selection (codegen sets RootConfigMessage
// from the first unbound, non-block message) deliberately: the check must
// consider reachable exactly what forge actually emits a root loader for.
// Returns "" for a fully per-binary project, which legitimately has no
// root.
func rootConfigMessage(messages []codegen.ConfigMessage, composed map[string]bool) string {
	for i := range messages {
		m := &messages[i]
		if m.Binary == "" && m.Frontend == "" && !composed[m.Name] {
			return m.Name
		}
	}
	return ""
}

func sliceSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}
