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
// gen/forge_descriptor/, whose methods carry Activity: the verdict of
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
// ── The one exception: forge's own unwired stub (WARNING) ────────────────
//
// Right after `forge scaffold`, a custom rpc the author declared is served
// by forge's placeholder — a handler method stamped `// forge:gen
// unwired-stub` that returns Unimplemented. That rpc changes nothing and
// records nothing, so the stream has no hole yet, and failing the gate on
// forge's fresh output makes the scaffold phase's expected state illegal:
// an agent told to gate on lint goes and invents an event for an rpc nobody
// asked it to touch. So an undeclared mutation whose OWN handler is still
// that stub is a warning, "pending: implement <Rpc>", and it gates again
// the moment the stub is implemented (the marker removed) — the same act
// that writes the mutation is the one that must declare it. The grain is
// the rpc itself, not its service: the stub marker names the method, so
// no other rpc's verdict moves. A malformed declaration is never held: it
// is wrong whatever the handler does. The read-only-fields lane holds its
// findings the same way (internal/cli/lint/lint_pending_stubs.go).
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
	// The descriptor is derived on demand (codegen.LoadDescriptor extracts
	// it when this checkout has none), so a nil result means no protos.
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
		var stubs unwiredStubs // resolved on the first undeclared mutation
		for _, m := range svc.Methods {
			a := m.Activity
			if a == nil {
				stale = true
				continue
			}
			var msg, fix string
			sev := SeverityError
			switch {
			case a.Error != "":
				// Resolve's error already names the rpc.
				msg, fix = a.Error, "fix the declaration: every path is a dotted field name in the request (or response.<path>) ending on a scalar or enum (forge skill load activity)"
			case a.Mutating && !a.Declared:
				msg = fmt.Sprintf("rpc %s.%s mutates (its name is not a read verb and it is not NO_SIDE_EFFECTS) but declares no activity event", svc.Name, m.Name)
				fix = activityRemediation
				if stubs.dir == "" {
					stubs = resolveUnwiredStubs(projectDir, svc.Name)
				}
				if stubs.methods[m.Name] {
					sev = SeverityWarning
					msg = fmt.Sprintf("pending: implement %s — rpc %s.%s declares no activity event, but its handler is still forge's unwired stub in %s, "+
						"which returns Unimplemented and records nothing; this is a warning until the stub is implemented, then it fails the build",
						m.Name, svc.Name, m.Name, stubs.rel)
					fix = "when you implement it, " + activityRemediation
				}
			default:
				continue
			}
			out[filepath.FromSlash(file)] = append(out[filepath.FromSlash(file)], Finding{
				Rule:        ruleActivityDeclared,
				Severity:    sev,
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
			Message:     "gen/forge_descriptor/ predates activity declarations, so they cannot be checked",
			Remediation: "run `forge generate`",
		})
	}
	return out
}

// unwiredStubs is the rpcs of one service whose handler is still forge's
// `forge:gen unwired-stub` placeholder. dir is set once resolved (to "-"
// when the service has no handler directory on disk), so the handler
// package is scanned at most once per service.
type unwiredStubs struct {
	dir     string
	rel     string // project-relative handler dir, for the message
	methods map[string]bool
}

// resolveUnwiredStubs finds the service's handler package the way
// `forge generate` does (codegen.ResolveServiceComponent, disk first) and
// reads the stub markers in it. A service with no handler directory on
// disk has no forge placeholders, so its findings stay errors.
func resolveUnwiredStubs(projectDir, svcName string) unwiredStubs {
	comp, err := codegen.ResolveServiceComponent(projectDir, svcName)
	if err != nil || !comp.FromDisk {
		return unwiredStubs{dir: "-"}
	}
	rel, rerr := filepath.Rel(projectDir, comp.Dir)
	if rerr != nil {
		rel = comp.Dir
	}
	return unwiredStubs{
		dir:     comp.Dir,
		rel:     filepath.ToSlash(rel),
		methods: codegen.ScanUnwiredStubMethods(comp.Dir),
	}
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
