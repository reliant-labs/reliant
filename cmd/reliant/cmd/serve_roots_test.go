// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// The api-server is the ONE process that applies migrations; the worker and
// the gateway wait for them (db.MigrationPolicy). A second migrator would
// reintroduce the startup race that policy removes, so the roots' generated
// specs (roots_gen.go, from each //forge:root's migrate= option) are pinned:
// runRoot migrates exactly when its spec says Migrate.
func TestOnlyTheAPIMigrates(t *testing.T) {
	src, err := os.ReadFile("roots_gen.go")
	require.NoError(t, err)
	file, err := parser.ParseFile(token.NewFileSet(), "roots_gen.go", src, 0)
	require.NoError(t, err)

	migrate := map[string]bool{}
	await := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if id, ok := lit.Type.(*ast.Ident); !ok || id.Name != "rootSpec" {
			return true
		}
		fields := map[string]ast.Expr{}
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				fields[kv.Key.(*ast.Ident).Name] = kv.Value
			}
		}
		name, err := strconv.Unquote(fields["Name"].(*ast.BasicLit).Value)
		require.NoError(t, err)
		migrate[name] = isTrue(fields["Migrate"])
		await[name] = isTrue(fields["AwaitMigrations"])
		return true
	})

	require.Equal(t, map[string]bool{"api": true, "worker": false, "gateway": false}, migrate)
	require.Equal(t, map[string]bool{"api": false, "worker": true, "gateway": true}, await)
}

func isTrue(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "true"
}

// Each root's telemetry identity and log file are what the hand-written
// commands used: dashboards and alerts key on the service name.
func TestRootPoliciesKeepEachRolesIdentity(t *testing.T) {
	require.Equal(t, map[string]rootPolicy{
		"api":     {serviceName: "reliant-api-server", logFile: "reliant.log"},
		"gateway": {serviceName: "daemon-gateway", logFile: "daemon-gateway.log"},
		"worker":  {serviceName: "reliant-temporal-worker", logFile: "temporal-worker.log"},
	}, rootPolicies)
}
