// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// vendorSDK is a provider SDK whose client constructor defaults to an
// http.Client with no idle timeout on the response body. A driver that calls it
// directly streams with no protection against a provider that sends response
// headers and then goes silent.
type vendorSDK struct {
	importPath string
	pkgIdent   string // default identifier; Go package names are not derivable from the path
	ctor       string
	instead    string
}

var vendorSDKs = []vendorSDK{
	{"github.com/openai/openai-go/v3", "openai", "NewClient", "llm.NewOpenAISDKClient"},
	{"github.com/anthropics/anthropic-sdk-go", "anthropic", "NewClient", "llm.NewAnthropicSDKClient"},
	{"google.golang.org/genai", "genai", "NewClient", "llm.NewGenAISDKClient"},
}

// TestDriversUseSanctionedSDKConstructors makes "a streaming driver without an
// idle timeout" impossible to add.
//
// This exists because six of the drivers under this tree forgot to pass an
// http.Client, which is the signature of an opt-in that should have been an
// opt-out. Rather than fixing six call sites and hoping the seventh driver
// remembers, no driver may construct a vendor SDK client at all: the only route
// is llm.New*SDKClient, which installs llm.StreamingHTTPClient unconditionally.
//
// The check resolves each file's imports, so the internal driver packages that
// are themselves named `openai` (openrouter, copilot and azure all call
// openai.NewClient meaning internal/llm/drivers/openai) are correctly ignored.
func TestDriversUseSanctionedSDKConstructors(t *testing.T) {
	byPath := make(map[string]vendorSDK, len(vendorSDKs))
	for _, sdk := range vendorSDKs {
		byPath[sdk.importPath] = sdk
	}

	var offenders []string

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}

		// local identifier -> vendor SDK, for the watched SDKs only.
		local := map[string]vendorSDK{}
		for _, imp := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(imp.Path.Value)
			if unquoteErr != nil {
				continue
			}
			sdk, watched := byPath[importPath]
			if !watched {
				continue
			}
			ident := sdk.pkgIdent
			if imp.Name != nil {
				ident = imp.Name.Name
			}
			local[ident] = sdk
		}
		if len(local) == 0 {
			return nil
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			sdk, watched := local[pkgIdent.Name]
			if !watched || sel.Sel.Name != sdk.ctor {
				return true
			}
			offenders = append(offenders, fmt.Sprintf(
				"%s:%d: %s.%s(...) — use %s instead",
				path, fset.Position(sel.Pos()).Line, pkgIdent.Name, sel.Sel.Name, sdk.instead,
			))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking driver sources: %v", err)
	}

	sort.Strings(offenders)
	assert.Empty(t, offenders,
		"drivers must build SDK clients through internal/llm so every stream gets an idle timeout:\n  %s",
		strings.Join(offenders, "\n  "))
}

// TestVendorSDKIdentifiersAreCurrent guards the guard. The check above matches
// on identifier + import path, so a wrong pkgIdent would make it silently pass
// on everything. These identifiers are how the driver sources actually refer to
// each SDK today.
func TestVendorSDKIdentifiersAreCurrent(t *testing.T) {
	seen := map[string]bool{}

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, imp := range file.Imports {
			if importPath, unquoteErr := strconv.Unquote(imp.Path.Value); unquoteErr == nil {
				seen[importPath] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking driver sources: %v", err)
	}

	for _, sdk := range vendorSDKs {
		assert.True(t, seen[sdk.importPath],
			"%s is no longer imported by any driver — drop it from vendorSDKs or fix the path", sdk.importPath)
	}
}
