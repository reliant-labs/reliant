// File: internal/linter/forgeconv/activity_declared.go
//
// forgeconv-activity-declared — every mutating rpc declares the activity
// event it records, or declares that it records none.
//
// Console v3 decision D3: the activity stream is DECLARED on the rpc
// (`(forge.v1.method).activity`) and written by one interceptor
// (forge/pkg/activity), never hand-written per call site. A declaration
// that is optional is a stream with holes nobody can see: the rpc that
// forgot is exactly the one missing from a user's history. So a mutation
// without a declaration is a lint error, and the escape hatch is a stated
// reason in the proto — `activity: { none: "<why>" }` — where a reviewer
// reads it, rather than silence.
//
// ── Source of truth ──────────────────────────────────────────────────────
//
// gen/forge_descriptor.json, whose methods carry Activity: the verdict of
// activity.IsMutating and activity.Resolve, recorded by `forge generate`
// from the COMPILED descriptors. Those are the functions the runtime
// interceptor calls, so this rule cannot disagree with what the
// interceptor does — the CRUD implication, the read-verb prefixes, the
// NO_SIDE_EFFECTS marker and every path check are decided once, in
// pkg/activity. The raw source scan is used only to place each finding on
// its rpc's line.
//
// A descriptor that predates the field (Activity nil) cannot be judged and
// yields one finding asking for `forge generate`; a missing descriptor
// (nothing generated yet) yields nothing — every other lane already says
// generate first. A stale descriptor is refused by codegen and reported
// the same way. Those findings name no file: the descriptor sits outside
// proto/, the frame every proto finding is reported in.
//
// ── What it reports (ERROR) ──────────────────────────────────────────────
//
//   - a mutating rpc with no declaration: no option, no CRUD implication,
//     no `none`;
//   - a malformed declaration on ANY rpc (a path that does not resolve,
//     a non-token verb, `none` mixed with other fields, a streaming rpc
//     left to the interceptor), with Resolve's message.
//
// Unmanaged services are in scope: activity describes the rpc, whoever
// implements it.

package forgeconv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

// ruleActivityDeclared is the stable rule ID.
const ruleActivityDeclared = "forgeconv-activity-declared"

// activityRemediation is the fix every undeclared-mutation finding names.
const activityRemediation = "declare the event: option (forge.v1.method) = { activity: { verb: \"<verb>\", target: { kind: \"<kind>\", id: \"<field path>\" } } }; " +
	"or, if it changes nothing worth recording, activity: { none: \"<why>\" }; " +
	"a read rpc can also be marked `option idempotency_level = NO_SIDE_EFFECTS;` (forge skill load activity)"

// checkActivityDeclared runs the rule over the tree rooted at rootDir (the
// project's proto/ directory) and returns findings keyed by each file's
// path relative to rootDir.
func checkActivityDeclared(rootDir string, protoFiles []string) map[string][]Finding {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil
	}
	projectDir := filepath.Dir(absRoot)
	if _, err := os.Stat(filepath.Join(projectDir, "gen", "forge_descriptor.json")); err != nil {
		return nil
	}
	services, err := codegen.ParseDeclaredServices(projectDir)
	if err != nil {
		return map[string][]Finding{"": {{
			Rule:        ruleActivityDeclared,
			Severity:    SeverityError,
			Message:     "cannot check activity declarations: " + firstLine(err.Error()),
			Remediation: "run `forge generate`",
		}}}
	}

	// rpc lines from the raw source, keyed "<rel file>|<Service>|<Method>".
	lines := map[string]int{}
	for _, f := range protoFiles {
		content, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, absPath(f))
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, svc := range parseProtoFile(f, string(content)).Services {
			for _, m := range svc.Methods {
				lines[rel+"|"+svc.Name+"|"+m.Name] = m.Line
			}
		}
	}

	out := map[string][]Finding{}
	stale := false
	for _, svc := range services {
		file := filepath.ToSlash(svc.ProtoFile)
		for _, m := range svc.Methods {
			a := m.Activity
			if a == nil {
				stale = true
				continue
			}
			var msg, fix string
			switch {
			case a.Error != "":
				// Resolve's error already names the rpc.
				msg, fix = a.Error, "fix the declaration: every path is a dotted field name in the request (or response.<path>) ending on a scalar or enum (forge skill load activity)"
			case a.Mutating && !a.Declared:
				msg = fmt.Sprintf("rpc %s.%s mutates (its name is not a read verb and it is not NO_SIDE_EFFECTS) but declares no activity event", svc.Name, m.Name)
				fix = activityRemediation
			default:
				continue
			}
			out[filepath.FromSlash(file)] = append(out[filepath.FromSlash(file)], Finding{
				Rule:        ruleActivityDeclared,
				Severity:    SeverityError,
				File:        filepath.FromSlash(file),
				Line:        lines[file+"|"+svc.Name+"|"+m.Name],
				Message:     msg,
				Remediation: fix,
			})
		}
	}
	if stale {
		out[""] = append(out[""], Finding{
			Rule:        ruleActivityDeclared,
			Severity:    SeverityError,
			Message:     "gen/forge_descriptor.json predates activity declarations, so they cannot be checked",
			Remediation: "run `forge generate`",
		})
	}
	return out
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
