// File: internal/cli/scaffold/entity_quintet_scalars_test.go
//
// The quintet's proto-field renderer classifies a field as scalar-or-type-
// name before it can render it. That classification used to be a private
// copy of the fifteen proto scalar names living in this package; it now
// routes to codegen.IsProtoScalarKind, which reads the one table forge
// writes those names down in.
//
// The obligation below is DERIVED from codegen.ProtoScalarKinds() — the key
// set of that table — rather than restated here, because a test carrying
// its own list checks the kinds someone remembered.

package scaffold

import (
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// TestProtoFieldDecl_RendersEveryScalarKind walks the derived vocabulary
// and requires the renderer to accept every kind in it.
//
// A kind the classifier does not recognise is not an error at the call
// site: protoFieldDecl returns ok=false and the field is silently dropped
// from the injected message, which is the same answer a genuinely
// unrenderable field (a message with no type name) gets. The proto still
// compiles, so nothing downstream reports the missing field.
func TestProtoFieldDecl_RendersEveryScalarKind(t *testing.T) {
	kinds := codegen.ProtoScalarKinds()
	if len(kinds) == 0 {
		t.Fatal("codegen.ProtoScalarKinds() is EMPTY — every obligation below is " +
			"derived from it, so an empty set would loop zero times and pass " +
			"while proving nothing")
	}
	if len(kinds) < 15 {
		t.Fatalf("codegen.ProtoScalarKinds() has %d members, expected 15: %v — the "+
			"vocabulary shrank, which silently narrows this check", len(kinds), kinds)
	}

	const pkg = "services.item.v1"
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			decl, ok := protoFieldDecl(codegen.SchemaFieldDef{Name: "f", Kind: kind}, pkg)
			if !ok {
				t.Fatalf("protoFieldDecl(kind=%q) refused — a field of this kind would "+
					"be dropped from the injected message with nothing reporting it", kind)
			}
			if decl != kind {
				t.Errorf("protoFieldDecl(kind=%q) = %q, want the kind verbatim", kind, decl)
			}
			// The repeated form is what a scalar list column injects.
			rep, ok := protoFieldDecl(codegen.SchemaFieldDef{Name: "f", Kind: kind, Repeated: true}, pkg)
			if !ok {
				t.Fatalf("protoFieldDecl(repeated %q) refused", kind)
			}
			if rep != "repeated "+kind {
				t.Errorf("protoFieldDecl(repeated %q) = %q", kind, rep)
			}
		})
	}
}

// TestProtoFieldDecl_RefusesNonScalars pins the other half: a kind outside
// the vocabulary, with no type name to fall back on, must be refused rather
// than rendered as a bare token. A bare unknown token in the injected proto
// is a buf compile failure in a file the author did not write.
func TestProtoFieldDecl_RefusesNonScalars(t *testing.T) {
	for _, kind := range []string{"int128", "decimal", "", "unknown-kind"} {
		if decl, ok := protoFieldDecl(codegen.SchemaFieldDef{Name: "f", Kind: kind}, "services.item.v1"); ok {
			t.Errorf("protoFieldDecl(kind=%q) rendered %q; %q is not a proto scalar",
				kind, decl, kind)
		}
	}
}
