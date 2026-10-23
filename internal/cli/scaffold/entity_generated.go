// File: internal/cli/scaffold/entity_generated.go
//
// The birth pre-flight for `// forge:generated <expr>` fields: every reason
// the marker cannot become the GENERATED column its author wrote is found
// HERE, before a birth writes anything, and reported against the marker's
// file and line.
//
// Two layers, because the reasons come in two kinds:
//
//   - forge's own rules, checked statically: a marker with no expression,
//     two markers on one field, a field birth gives no single column
//     (entityscaffold.GeneratedColumnRefusal).
//   - postgres's rules, which forge does not reimplement. The expression is
//     SQL and only postgres can say whether it is valid for this table: the
//     columns it names, their types, whether every function in it is
//     IMMUTABLE, whether it reads another generated column. So the rendered
//     migration is applied to the shadow database — the same server and the
//     same replay of db/migrations that `forge generate` uses — inside a
//     transaction that is always rolled back.
//
// A rejected migration is then attributed by asking postgres again, not by
// parsing its message: the same migration with every generated column
// rendered plain must apply (otherwise the failure is not the expressions'),
// and the field whose expression alone fails is the one named. Errors are
// rare and the questions are cheap — one rolled-back transaction each on a
// shadow that is already open.

package scaffold

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	entityscaffold "github.com/reliant-labs/forge/internal/scaffold"
	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// generatedPreflight refuses a birth whose `// forge:generated` fields cannot
// be born as written. spec is the exact spec the birth will render; m is the
// raw-scanned message, which carries the markers' sites (the zero value when
// the scan never saw the message — sites then fall back to the field name).
//
// A spec with no generated field returns nil without opening anything: the
// shadow is paid for only by the births that use the marker.
func generatedPreflight(migDir string, spec entityscaffold.EntityFromProtoSpec, m codegen.RawProtoMessage) error {
	if err := generatedMarkerRefusal(spec, m); err != nil {
		return err
	}
	if !hasGeneratedField(spec.Fields) {
		return nil
	}
	projectDir := filepath.Dir(filepath.Dir(migDir))
	shadow, err := schemadef.OpenShadowAt(migDir, shadowdb.Resolve(projectDir))
	if err != nil {
		// Not a verdict on the expression: postgres was never asked. The
		// birth proceeds, and `forge generate` — which needs the same
		// shadow — applies the migration and fails on a bad expression.
		fmt.Printf("  ⚠️  forge:generated expressions on %s NOT verified (%v); `forge generate` applies them next and fails if postgres rejects one\n",
			messageLabel(spec), err)
		return nil
	}
	defer shadow.Close()
	return verifyGeneratedColumns(context.Background(), shadow, spec, m)
}

// generatedMarkerRefusal is the static half: the reasons forge itself knows a
// marker cannot be honoured, none of which needs a database to see.
func generatedMarkerRefusal(spec entityscaffold.EntityFromProtoSpec, m codegen.RawProtoMessage) error {
	var problems []string
	perField := map[string][]codegen.GeneratedFieldMarker{}
	for _, gm := range m.GeneratedMarkers {
		perField[gm.Field] = append(perField[gm.Field], gm)
		if gm.Expr == "" {
			problems = append(problems, fmt.Sprintf("%s\n      carries no expression — write the SQL after the marker, separated by a space, on the same line", gm.Site))
		}
	}
	for _, f := range spec.Fields {
		if sites := perField[f.Name]; len(sites) > 1 {
			lines := make([]string, 0, len(sites))
			for _, s := range sites {
				lines = append(lines, s.Site.String())
			}
			problems = append(problems, fmt.Sprintf("%s\n      field %s carries %d forge:generated markers — keep exactly one", strings.Join(lines, "\n    "), f.Name, len(sites)))
		}
		if f.Generated == "" {
			continue
		}
		if reason := entityscaffold.GeneratedColumnRefusal(f, spec); reason != "" {
			problems = append(problems, fmt.Sprintf("%s\n      forge:generated %s", generatedSite(f.Name, spec, m), reason))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s carries a forge:generated marker birth cannot honour (nothing was written):\n    %s",
		messageLabel(spec), strings.Join(problems, "\n    "))
}

// verifyGeneratedColumns applies the rendered migration to the shadow and,
// when postgres rejects it, names the generated field(s) responsible.
func verifyGeneratedColumns(ctx context.Context, shadow *schemadef.Shadow, spec entityscaffold.EntityFromProtoSpec, m codegen.RawProtoMessage) error {
	full := shadow.TryApply(ctx, entityscaffold.RenderEntityMigrationFromProto(spec).UpSQL)
	if full == nil {
		return nil
	}

	// Every generated column rendered as the plain column it replaces. If
	// even that is refused, the expressions are not the problem, and blaming
	// one would send the author to fix the wrong line.
	plain := withGenerated(spec, nil)
	if err := shadow.TryApply(ctx, entityscaffold.RenderEntityMigrationFromProto(plain).UpSQL); err != nil {
		return fmt.Errorf("the migration born for %s does not apply to the shadow database: %w", messageLabel(spec), err)
	}

	type culprit struct {
		field string
		err   error
	}
	var culprits []culprit
	var generated []string
	for _, f := range spec.Fields {
		if f.Generated == "" {
			continue
		}
		generated = append(generated, f.Name)
		only := withGenerated(spec, map[string]bool{f.Name: true})
		if err := shadow.TryApply(ctx, entityscaffold.RenderEntityMigrationFromProto(only).UpSQL); err != nil {
			culprits = append(culprits, culprit{field: f.Name, err: err})
		}
	}
	// Every expression applies alone but not together: they interact, and
	// postgres's own message (e.g. a generated column reading another one)
	// names how. Report the whole set against that message.
	if len(culprits) == 0 {
		for _, name := range generated {
			culprits = append(culprits, culprit{field: name, err: full})
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "postgres rejected the forge:generated expression on %s (nothing was written):", messageLabel(spec))
	for _, c := range culprits {
		fmt.Fprintf(&b, "\n    %s\n      postgres: %v", generatedSite(c.field, spec, m), c.err)
	}
	b.WriteString("\n  the expression is copied verbatim into `<column> <type> GENERATED ALWAYS AS (<expr>) STORED` — fix it in the proto and re-run")
	return errors.New(b.String())
}

// withGenerated returns spec with forge:generated kept only on the fields
// named in keep (none when keep is nil). A cleared field renders as the plain
// column it would have been born as without the marker.
func withGenerated(spec entityscaffold.EntityFromProtoSpec, keep map[string]bool) entityscaffold.EntityFromProtoSpec {
	fields := make([]codegen.SchemaFieldDef, len(spec.Fields))
	copy(fields, spec.Fields)
	for i := range fields {
		if !keep[fields[i].Name] {
			fields[i].Generated = ""
		}
	}
	spec.Fields = fields
	return spec
}

// generatedSite names where a field's forge:generated marker was written: the
// raw scan's file:line when it has one, the message and field otherwise (a
// message declared outside the service's proto directory).
func generatedSite(field string, spec entityscaffold.EntityFromProtoSpec, m codegen.RawProtoMessage) string {
	for _, gm := range m.GeneratedMarkers {
		if gm.Field == field {
			return gm.Site.String()
		}
	}
	return fmt.Sprintf("%s.%s", spec.MessageFQ, field)
}

func hasGeneratedField(fields []codegen.SchemaFieldDef) bool {
	for _, f := range fields {
		if f.Generated != "" {
			return true
		}
	}
	return false
}

// messageLabel is the message a refusal is about, as the author declared it.
func messageLabel(spec entityscaffold.EntityFromProtoSpec) string {
	if i := strings.LastIndex(spec.MessageFQ, "."); i >= 0 {
		return "message " + spec.MessageFQ[i+1:]
	}
	return "message " + spec.MessageFQ
}
