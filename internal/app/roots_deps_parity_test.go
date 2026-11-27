// Copyright (c) 2025 Reliant Labs
package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rootDepsOnlySomeRoots names the Deps keys a service is deliberately built
// with in some of its constructions in this package and not in others, as
// "<package>.<Key>". Every other key one construction sets, every
// construction of that service must set too.
//
// The rule exists because a service's validateDeps can make a key required
// only together with another ("trigger: Deps.AutomationKeys is required with
// Deps.ControlPlane"), and only a hosted process config sets that other key.
// reliant#3330 wired AutomationKeys into the api-server's trigger service and
// not into the worker's (NewWorkerTrigger), no test built a hosted worker, and
// v1.32.0-rc.5's worker refused to boot in prod. A key added to one root is
// now a decision for every root that builds the same service: wire it, or
// list it here.
//
// The entries below are the differences that already existed when this test
// was added (2026-11-24). Each should be reviewed and either wired or given
// its reason; the trigger ones are the api-only refresh loop, input-option
// check and alerting named in NewWorkerTrigger's doc comment.
var rootDepsOnlySomeRoots = map[string]string{
	"trigger.Alerting":             "api-only (NewWorkerTrigger: alerting, refresh loop and input-option check stay api-only)",
	"trigger.Drafter":              "api-only (NewWorkerTrigger)",
	"trigger.Options":              "api-only: the input-option reachability check (NewWorkerTrigger)",
	"trigger.OrgRollout":           "api-only (NewWorkerTrigger); pre-existing, to review",
	"trigger.Orgs":                 "api-only (NewWorkerTrigger); pre-existing, to review",
	"trigger.Senders":              "api-only (NewWorkerTrigger); pre-existing, to review",
	"daemon_registry.IdleTimeouts": "pre-existing difference between roots, to review",
	"daemon_registry.Names":        "pre-existing difference between roots, to review",
	"daemon_registry.Router":       "pre-existing difference between roots, to review",
	"workflowcheck.GitHub":         "pre-existing difference between roots, to review",
	"workflowcheck.RunURL":         "pre-existing difference between roots, to review",
	"run.Orgs":                     "pre-existing difference between roots, to review",
	"chat.Orgs":                    "pre-existing difference between roots, to review",
	"gtmsvc.Analytics":             "worker-only: product analytics is read by the agent's read_product_analytics tool; no GtmService rpc reads it (providers_gtm.go)",
	"gtmsvc.Attachments":           "worker-only: only attach_gtm_asset copies a chat attachment, behind GTM_ATTACH_FROM_CHAT_ENABLED; no GtmService rpc reads one (providers_gtm.go)",
	"gtmsvc.Email":                 "worker-only: only send_gtm_email sends, through the worker's integration credentials; no GtmService rpc sends (providers_gtm.go)",
	"controlplane.Sign":            "only the clients that call the control plane as this process sign; the per-caller (bearer) clients do not",
}

// Every construction of one service's Deps in this package's wiring sets the
// same keys, but for the listed exceptions. Read from source in this test
// process (no child process), so Go's test cache sees every file it judges.
func TestEveryRootBuildsAServiceWithTheSameDeps(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	type site struct {
		pos  string
		keys map[string]bool
	}
	built := map[string][]site{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		af, err := parser.ParseFile(fset, f, src, 0)
		require.NoError(t, err)
		ast.Inspect(af, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := cl.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Deps" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			keys := map[string]bool{}
			for _, e := range cl.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok {
						keys[id.Name] = true
					}
				}
			}
			built[pkg.Name] = append(built[pkg.Name], site{pos: fset.Position(cl.Pos()).String(), keys: keys})
			return true
		})
	}

	used := map[string]bool{}
	var problems []string
	for pkg, sites := range built {
		if len(sites) < 2 {
			continue
		}
		all := map[string]bool{}
		for _, s := range sites {
			for k := range s.keys {
				all[k] = true
			}
		}
		for k := range all {
			name := pkg + "." + k
			if _, ok := rootDepsOnlySomeRoots[name]; ok {
				used[name] = true
				continue
			}
			for _, s := range sites {
				if !s.keys[k] {
					problems = append(problems, s.pos+": "+pkg+".Deps is built without "+k+", which another construction of it sets: wire it here, or list "+name+" in rootDepsOnlySomeRoots with the reason")
				}
			}
		}
	}
	for name := range rootDepsOnlySomeRoots {
		if !used[name] {
			problems = append(problems, name+" is listed in rootDepsOnlySomeRoots but every construction now agrees on it: remove the entry")
		}
	}
	sort.Strings(problems)
	require.Empty(t, problems)
}
