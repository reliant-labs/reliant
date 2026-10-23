package codegen

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// GoTypeOverride is a parsed `forge:go-type=<spec>` column declaration
// (schemadef.ColumnMarkerGoType): the Go type to spell on the entity
// struct and the import it needs.
type GoTypeOverride struct {
	GoType     string // as written in Go source, e.g. "appv1.ContentBlockType"
	ImportPath string // "" for a builtin or composite of builtins
	ImportName string // the package name GoType qualifies with
}

// IsReference reports whether the type already has a nil (a pointer,
// slice or map), so a nullable column needs no extra pointer.
func (o GoTypeOverride) IsReference() bool {
	return strings.HasPrefix(o.GoType, "*") || strings.HasPrefix(o.GoType, "[]") || strings.HasPrefix(o.GoType, "map[")
}

// ImportSpec is the override's import as an import-block line, "" for none.
func (o GoTypeOverride) ImportSpec() string {
	if o.ImportPath == "" {
		return ""
	}
	if path.Base(o.ImportPath) == o.ImportName {
		return fmt.Sprintf("%q", o.ImportPath)
	}
	return fmt.Sprintf("%s %q", o.ImportName, o.ImportPath)
}

var (
	goIdentRE      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	goBuiltinTypRE = regexp.MustCompile(`^[A-Za-z0-9_\[\]\.{}, ]+$`)
)

// ParseGoTypeOverride parses a `forge:go-type` spec:
//
//	int32, map[string]any, []string            builtin (no import)
//	time.Duration                              a standard-library package
//	github.com/acme/app/gen/app/v1;appv1.Kind  import path ; package name . type
//	github.com/acme/app/internal/kinds.Kind    package name = last path element
//
// with an optional leading `*` or `[]`. A composite (`map[…]`) must be
// spelled with builtins only: it has no import to carry.
func ParseGoTypeOverride(spec string) (GoTypeOverride, error) {
	spec = strings.TrimSpace(spec)
	prefix := ""
	for {
		switch {
		case strings.HasPrefix(spec, "*"):
			prefix, spec = prefix+"*", spec[1:]
			continue
		case strings.HasPrefix(spec, "[]"):
			prefix, spec = prefix+"[]", spec[2:]
			continue
		}
		break
	}
	if spec == "" {
		return GoTypeOverride{}, fmt.Errorf("empty forge:go-type")
	}
	if strings.ContainsAny(spec, "[") || !strings.Contains(spec, ".") {
		if strings.Contains(spec, "/") || !goBuiltinTypRE.MatchString(spec) {
			return GoTypeOverride{}, fmt.Errorf("forge:go-type=%s: a composite type must be spelled with builtin types only", prefix+spec)
		}
		return GoTypeOverride{GoType: prefix + spec}, nil
	}
	dot := strings.LastIndex(spec, ".")
	imp, typ := spec[:dot], spec[dot+1:]
	name := path.Base(imp)
	if p, n, ok := strings.Cut(imp, ";"); ok {
		imp, name = p, n
	}
	if !goIdentRE.MatchString(typ) || !goIdentRE.MatchString(name) || imp == "" {
		return GoTypeOverride{}, fmt.Errorf("forge:go-type=%s: want <import path>[;<package>].<Type>", prefix+spec)
	}
	return GoTypeOverride{GoType: prefix + name + "." + typ, ImportPath: imp, ImportName: name}, nil
}
