// File: internal/codegen/crud_ownership.go
//
// Projecting a table's `forge:owner=` declaration into the CRUD ops.
//
// The declaration (schemadef.TableMarkerOwner) is resolved against the
// applied schema in BuildSchemaEntities, onto EntityDef.Ownership. This file
// turns it into the two things the ops template renders for an owned
// entity: a package-level crud.Ownership literal, and the Create stamp that
// fills a direct owner column from the caller. The ops then route every
// read and write through forge/pkg/crud's Owned* helpers, which resolve the
// caller through the service's `Owners crud.OwnerResolver` dep.
//
// An entity with no declaration gets none of it, so every existing project
// renders byte for byte what it rendered before.

package codegen

import (
	"bytes"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	"github.com/reliant-labs/forge/internal/naming"
)

// OwnershipTemplateData is one owned entity's crud.Ownership declaration in
// the ops file.
type OwnershipTemplateData struct {
	// Var is the package-level variable the ops name ("planOwnership").
	Var string
	// Table is the owned table, for the comment above the variable.
	Table string
	// Spec is the declaration as written, for the same comment.
	Spec string
	// Literal is the crud.Ownership composite literal.
	Literal string
}

// ownershipVar names the package-level crud.Ownership of an entity.
func ownershipVar(entity EntityDef) string {
	return strings.ToLower(entity.Name) + "Ownership"
}

// ownershipTemplateData renders an owned entity's declaration; ok is false
// for an entity that declares no owner.
func ownershipTemplateData(entity EntityDef) (OwnershipTemplateData, bool) {
	if len(entity.Ownership) == 0 {
		return OwnershipTemplateData{}, false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "crud.Ownership{\n\tEntity: %q,\n\tTerms: []crud.OwnerTerm{\n", strings.ToLower(entity.Name))
	var spec []string
	for _, t := range entity.Ownership {
		path := []string{t.Column}
		fmt.Fprintf(&b, "\t\t{Kind: %q, Column: %q", t.Kind, t.Column)
		if t.Constraint != "" {
			fmt.Fprintf(&b, ", Constraint: %q", t.Constraint)
		}
		if len(t.Hops) > 0 {
			b.WriteString(", Hops: []crud.OwnerHop{")
			for i, h := range t.Hops {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString("{")
				if h.Schema != "" && h.Schema != "public" {
					fmt.Fprintf(&b, "Schema: %q, ", h.Schema)
				}
				fmt.Fprintf(&b, "Table: %q, Key: %q, Column: %q}", h.Table, h.Key, h.Column)
				path = append(path, h.Column)
			}
			b.WriteString("}")
		}
		b.WriteString("},\n")
		spec = append(spec, t.Kind+":"+strings.Join(path, ">"))
	}
	b.WriteString("\t},\n}")
	return OwnershipTemplateData{
		Var:     ownershipVar(entity),
		Table:   entity.TableName,
		Spec:    strings.Join(spec, ","),
		Literal: b.String(),
	}, true
}

// ownerStampColumn is the column a Create stamps from the caller: the
// first owner term's column when it is a direct owner column. A path's
// first column is a foreign key the client chooses, so there is nothing to
// stamp; "" then, and for an entity that declares no owner.
func ownerStampColumn(entity EntityDef) string {
	if len(entity.Ownership) == 0 || len(entity.Ownership[0].Hops) > 0 {
		return ""
	}
	return entity.Ownership[0].Column
}

// ownerStampFunc renders the stamp closure crud.OwnedCreate calls with the
// caller's one value for the first term's kind: it fills that column only
// when the row names no direct owner at all, so a client never needs to
// send its own user id, and a row filed under an org is not also filed
// under its creator. "nil" when there is nothing to stamp, or when an owner
// column is not text (owner values are strings). qual is the identifier the
// ops file qualifies the ORM package with (CRUDTemplateData.ORMQualifier).
func ownerStampFunc(entity EntityDef, qual string) string {
	stamp := ownerStampColumn(entity)
	if stamp == "" {
		return "nil"
	}
	cols := map[string]EntityColumn{}
	for _, c := range entity.Columns {
		cols[c.Name] = c
	}
	var empty []string
	assign := ""
	for _, t := range entity.Ownership {
		if len(t.Hops) > 0 {
			continue
		}
		col, ok := cols[t.Column]
		if !ok || dbBaseGoType(col) != "string" {
			return "nil"
		}
		field := "e." + naming.ColumnGoName(col.Name)
		nullable := dbNullable(col)
		if nullable {
			empty = append(empty, fmt.Sprintf("(%s == nil || *%s == \"\")", field, field))
		} else {
			empty = append(empty, field+" == \"\"")
		}
		if col.Name == stamp {
			if nullable {
				assign = field + " = &owner"
			} else {
				assign = field + " = owner"
			}
		}
	}
	return fmt.Sprintf("func(e *%s.%s, owner string) {\n\t\t\t\tif %s {\n\t\t\t\t\t%s\n\t\t\t\t}\n\t\t\t}",
		ormQualOrDefault(qual), entity.Name, strings.Join(empty, " && "), assign)
}

// ownerStampRenderer is ownerStampFunc deferred until the ops file's ORM
// qualifier is known; nil when the op stamps nothing.
func ownerStampRenderer(stamps bool, entity EntityDef) func(qual string) string {
	if !stamps {
		return nil
	}
	return func(qual string) string { return ownerStampFunc(entity, qual) }
}

// ownedEntityID renders the expression crud.OwnedCreate reads a created
// row's key with.
func ownedEntityID(entity EntityDef) string {
	field := "e." + naming.ColumnGoName(entity.PkField)
	if entity.PkGoType == "string" {
		return field
	}
	return "fmt.Sprint(" + field + ")"
}

// ensureDepsOwnersField adds `Owners crud.OwnerResolver` to the service's
// Deps, and its validateDeps check, when the service's ops route an owned
// entity through crud's Owned* helpers — the twin of ensureDepsDBField, for
// the same reason: the ops just started dereferencing s.deps.Owners, so the
// field is required for the package to compile.
//
// It is the ONE place forge learns who the caller is, and the app supplies
// it: composition fills it by type from a provider in
// internal/app/providers.go (generate names the missing provider, with a
// ready-made crud.OwnerKinds, until there is one). A nil resolver is refused
// at construction rather than at the first request.
//
// Idempotent and additive: a Deps that already declares `Owners` of any
// type is left alone (a foreign type is reported, never rewritten).
func ensureDepsOwnersField(serviceDir string) error {
	fields, err := ParseServiceDeps(serviceDir)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if f.Name == "Owners" {
			if !strings.HasSuffix(strings.TrimSpace(f.Type), "OwnerResolver") {
				fmt.Printf("⚠️  %s declares Deps.Owners as %s — the generated owner-scoped CRUD ops call s.deps.Owners as a crud.OwnerResolver. Retype the field.\n",
					filepath.Join(serviceDir, "service.go"), f.Type)
			}
			return nil
		}
	}
	servicePath := filepath.Join(serviceDir, "service.go")
	data, err := os.ReadFile(servicePath)
	if err != nil {
		return err
	}
	content := string(data)
	const open = "type Deps struct {"
	idx := strings.Index(content, open)
	if idx < 0 {
		return nil // no recognizable Deps struct: the compile error names the field
	}
	nl := strings.Index(content[idx:], "\n")
	if nl < 0 {
		return nil
	}
	insert := idx + nl + 1
	field := "\t// Owners tells the generated CRUD ops who the caller is, for the\n" +
		"\t// tables whose migration declares `forge:owner=` (see the forge\n" +
		"\t// `db/crud-overrides` skill).\n" +
		"\tOwners crud.OwnerResolver\n"
	content = content[:insert] + field + content[insert:]
	content = addGoImport(content, "github.com/reliant-labs/forge/pkg/crud")
	if !strings.Contains(content, "d.Owners == nil") {
		const marker = "// Add checks for your required Deps fields here."
		if i := strings.Index(content, marker); i >= 0 {
			check := "if d.Owners == nil {\n" +
				"\t\treturn fmt.Errorf(\"Deps.Owners is required by the owner-scoped CRUD ops — provide a crud.OwnerResolver in internal/app/providers.go\")\n" +
				"\t}\n\t"
			content = content[:i] + check + content[i:]
		}
	}
	fmt.Printf("  ✅ Added `Owners crud.OwnerResolver` to Deps in %s (required by the owner-scoped CRUD ops)\n", servicePath)
	return writeUserScaffold(servicePath, []byte(content))
}

// addGoImport adds path to a Go source's imports, whatever shape its import
// declaration has (a block, a single line, none). Source that does not parse
// is returned unchanged: the compile error then names the missing import.
func addGoImport(content, path string) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", content, parser.ParseComments)
	if err != nil {
		return content
	}
	if !astutil.AddImport(fset, f, path) {
		return content
	}
	var b bytes.Buffer
	if err := format.Node(&b, fset, f); err != nil {
		return content
	}
	return b.String()
}
