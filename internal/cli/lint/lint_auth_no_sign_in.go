// File: internal/cli/lint/lint_auth_no_sign_in.go
//
// forge-auth-required-no-sign-in — an rpc that requires a signed-in caller in
// a project nobody can sign in to.
//
// `forge project new --auth-mode none` scaffolds a project with a frontend
// and no identity: no deploy/kcl/identity.k, no dev identity provider, no
// sign-in pages (codegen.ProjectHasNoSignIn reads that back off the tree).
// The API still fails closed, so an rpc whose proto does not declare
// `auth_required: false` answers 401 to every caller the frontend has, and
// the app is unusable with no hint why beyond the 401. forge scaffold births
// such a project's CRUD rpcs public; this lane finds the ones that are not:
// rpcs born before that, and custom rpcs the author declared without the
// option.
//
// ADVISORY: a warning at any pin, never a gate by default (lint.rules can
// raise it). An rpc meant for callers that present an API key, or a token
// from an issuer the project trusts, IS satisfiable without sign-in; the
// lane cannot see that, so the author says so with a reasoned
// `// forge:lint-disable-next-line` above the rpc.
//
// It reads the same projection of auth_required the generated open-procedure
// set (pkg/middleware/procedures_gen.go) is built from, so what it reports is
// exactly what the interceptor will refuse.
package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// ruleAuthRequiredNoSignIn is the rule ID this lane emits.
const ruleAuthRequiredNoSignIn = "forge-auth-required-no-sign-in"

// authNoSignInRPC is one rpc that requires a caller.
type authNoSignInRPC struct {
	Service string
	Name    string
	// File is the declaring .proto, relative to the project root; Line the
	// rpc keyword's line in it (0 when the source could not be read).
	File string
	Line int
}

// authNoSignInFindingsFor is the pure rule: every rpc given requires a
// caller in a project that cannot produce one.
func authNoSignInFindingsFor(rpcs []authNoSignInRPC) []finding.Finding {
	out := make([]finding.Finding, 0, len(rpcs))
	for _, r := range rpcs {
		out = append(out, finding.Finding{
			Rule: ruleAuthRequiredNoSignIn, Severity: finding.SeverityWarning, File: r.File, Line: r.Line,
			Message: fmt.Sprintf("rpc %s.%s requires a signed-in caller (its proto does not declare auth_required: false), "+
				"but this project declares no sign-in (no deploy/kcl/identity.k and no dev identity provider — `forge project new --auth-mode none`): "+
				"nothing issues the credential its frontend would present, so it answers 401 to every browser caller", r.Service, r.Name),
			Remediation: "if anyone may call it, publish it in the rpc's body: `option (forge.v1.method) = {auth_required: false};`. " +
				"If its callers present an API key or a token from an issuer you trust, say so on the line above: " +
				"`// forge:lint-disable-next-line " + ruleAuthRequiredNoSignIn + ": <who calls it>`. " +
				"To give the project users instead, declare an identity (`forge skill load auth`)",
		})
	}
	return out
}

// collectAuthNoSignInFindings reads the declared services under root and
// returns the findings, suppression directives applied, sorted by (file,
// line). A project that declares a sign-in, has no frontend, or has no
// descriptor yet yields nothing.
func collectAuthNoSignInFindings(root string) ([]finding.Finding, error) {
	if root == "" || !codegen.ProjectHasNoSignIn(root) {
		return nil, nil
	}
	services, err := codegen.ParseDeclaredServices(root)
	if err != nil {
		// The idempotency and conventions lanes report an unreadable
		// descriptor; a second finding here would only restate it.
		return nil, nil
	}
	lines := map[string]map[string]int{}
	var rpcs []authNoSignInRPC
	for _, svc := range services {
		file := protoSourcePath(root, svc.ProtoFile)
		l, ok := lines[file]
		if !ok {
			if content, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(file))); rerr == nil {
				l = codegen.ScanRPCCommentMarkers(string(content)).Lines
			}
			lines[file] = l
		}
		for _, m := range svc.Methods {
			if !m.AuthRequired {
				continue
			}
			rpcs = append(rpcs, authNoSignInRPC{Service: svc.Name, Name: m.Name, File: file, Line: l[m.Name]})
		}
	}
	res := suppress.ApplyFiles(root, authNoSignInFindingsFor(rpcs), os.ReadFile)
	out := slices.Concat(res.Kept, res.Violations)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// collectAuthNoSignInJSON maps the findings onto the JSON contract. The lane
// is advisory, so it never reports a gating verdict of its own.
func collectAuthNoSignInJSON(root string) ([]lintJSONFinding, error) {
	findings, err := collectAuthNoSignInFindings(root)
	if err != nil {
		return nil, fmt.Errorf("auth-no-sign-in lint failed: %w", err)
	}
	return findingsToJSON(findings), nil
}
