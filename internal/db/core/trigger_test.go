// Copyright (c) 2025 Reliant Labs
package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Unattended decides whether a run may wake its machine with the delegated
// automation token, so each kind's answer is pinned here by name.
func TestTriggerEventKindUnattended(t *testing.T) {
	t.Parallel()

	for _, kind := range []TriggerEventKind{
		TriggerEventKindSchedule, TriggerEventKindWebhook, TriggerEventKindIntegration, TriggerEventKindWorkflowEvent,
	} {
		if !kind.Unattended() {
			t.Errorf("%s is launched by a stored trigger with nobody signed in; it must be unattended", kind)
		}
	}
	for _, kind := range []TriggerEventKind{
		TriggerEventKindChatStart, TriggerEventKindAgentStartRun, TriggerEventKindBuilderTest,
	} {
		if kind.Unattended() {
			t.Errorf("%s is started by a human (or an agent working for one); it must not borrow the automation token", kind)
		}
	}
}

// An unknown kind fails closed: "unclassified" must never read as "may use the
// stored token".
func TestTriggerEventKindUnattended_UnknownKindIsAttended(t *testing.T) {
	t.Parallel()
	if TriggerEventKind("no.such.kind").Unattended() {
		t.Fatal("an unknown event kind must read as attended")
	}
}

// Every declared TriggerEventKind is classified on purpose: a newly added kind
// has to take a position in eventKindUnattended, and this test is what asks.
func TestTriggerEventKindUnattended_EveryDeclaredKindIsClassified(t *testing.T) {
	t.Parallel()

	declared := declaredStringConsts(t, "TriggerEventKind")
	if len(declared) == 0 {
		t.Fatal("found no TriggerEventKind constants; the scan is broken")
	}
	for name, value := range declared {
		if _, classified := eventKindUnattended[TriggerEventKind(value)]; !classified {
			t.Errorf("%s (%q) is not in eventKindUnattended; decide whether a run it launches has a human behind it", name, value)
		}
	}
	if len(eventKindUnattended) != len(declared) {
		t.Errorf("eventKindUnattended classifies %d kinds but %d are declared; remove entries for kinds that no longer exist",
			len(eventKindUnattended), len(declared))
	}
}

// A stored trigger always fires with nobody watching, so every kind of stored
// trigger must record an unattended event — otherwise its runs could never
// wake a suspended machine once the owner signs out.
func TestTriggerKind_EveryStoredTriggerKindFiresAnUnattendedEvent(t *testing.T) {
	t.Parallel()

	declared := declaredStringConsts(t, "TriggerKind")
	if len(declared) == 0 {
		t.Fatal("found no TriggerKind constants; the scan is broken")
	}
	for name, value := range declared {
		if !TriggerKind(value).EventKind().Unattended() {
			t.Errorf("%s fires event kind %q, which is not unattended", name, value)
		}
	}
}

// declaredStringConsts returns name → value for every constant of the named
// type declared in this package's non-test sources. Parsing the source, rather
// than listing the constants again, is what makes a newly added one visible.
func declaredStringConsts(t *testing.T, typeName string) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				ident, ok := vs.Type.(*ast.Ident)
				if !ok || ident.Name != typeName {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						t.Fatalf("%s %s has no explicit value; extend declaredStringConsts", typeName, name.Name)
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("%s %s is not a string literal; extend declaredStringConsts", typeName, name.Name)
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("unquote %s: %v", name.Name, err)
					}
					out[name.Name] = value
				}
			}
		}
	}
	return out
}
