// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

type telemetryShutdown struct {
	calls int
	err   error
}

func (s *telemetryShutdown) Shutdown() error {
	s.calls++
	return s.err
}

func TestTelemetryFlushSharedByBootFailureAndSupervisor(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		t.Run(map[bool]string{false: "boot failure", true: "running root"}[supervised], func(t *testing.T) {
			want := errors.New("export failed")
			provider := &telemetryShutdown{err: want}
			flush := flushTelemetry(provider)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			func() {
				defer func() { require.ErrorIs(t, flush(ctx), want) }()
				if supervised {
					require.ErrorIs(t, flush(ctx), want)
				}
			}()
			require.Equal(t, 1, provider.calls, "deferred cleanup must also cover a root that never ran")
		})
	}
}

// Keep cleanup registration ahead of the first fallible boot operation after
// Init. Exercising runRoot with a real exporter/database would turn this small
// lifetime invariant into an integration fixture; inspect its owned wiring
// instead, and exercise the shared cleanup's behavior above.
func TestRunRootDefersTelemetryBeforeMigrations(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	require.NoError(t, err)
	file, err := parser.ParseFile(token.NewFileSet(), "serve.go", src, 0)
	require.NoError(t, err)
	var root *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "runRoot" {
			root = fn
		}
	}
	require.NotNil(t, root)
	var deferred, migration, supervisorFlush token.Pos
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.DeferStmt:
			ast.Inspect(n, func(child ast.Node) bool {
				if call, ok := child.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "flush" {
						deferred = n.Pos()
					}
				}
				return true
			})
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "migrateSchema" {
				migration = n.Pos()
			}
		case *ast.KeyValueExpr:
			if key, ok := n.Key.(*ast.Ident); ok && key.Name == "Flush" {
				value, ok := n.Value.(*ast.Ident)
				require.True(t, ok, "supervisor must share the deferred cleanup")
				require.Equal(t, "flush", value.Name)
				supervisorFlush = n.Pos()
			}
		}
		return true
	})
	require.NotZero(t, deferred)
	require.Greater(t, migration, deferred)
	require.Greater(t, supervisorFlush, migration)
}
