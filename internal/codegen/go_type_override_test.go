package codegen

import (
	"strings"
	"testing"
)

func TestGoIntType(t *testing.T) {
	for decl, want := range map[string]string{
		"INT4": "int32", "INTEGER": "int32", "SERIAL": "int32", "INT4[]": "int32",
		"INT2": "int16", "SMALLINT": "int16",
		"INT8": "int64", "BIGINT": "int64", "BIGSERIAL": "int64", "": "int64",
	} {
		if got := GoIntType(decl); got != want {
			t.Errorf("GoIntType(%q) = %q, want %q", decl, got, want)
		}
	}
	if got := ColumnGoType("int64", "INT4", false); got != "int32" {
		t.Errorf("an integer column is int32, got %s", got)
	}
	if got := ColumnGoType("json", "JSONB", false); got != "json.RawMessage" {
		t.Errorf("a jsonb column is json.RawMessage, got %s", got)
	}
}

func TestParseGoTypeOverride(t *testing.T) {
	cases := []struct {
		spec, goType, spec2 string
	}{
		{"int32", "int32", ""},
		{"map[string]any", "map[string]any", ""},
		{"[]string", "[]string", ""},
		{"time.Duration", "time.Duration", `"time"`},
		{"encoding/json.Number", "json.Number", `"encoding/json"`},
		{"github.com/acme/app/gen/app/v1;appv1.ContentBlockType", "appv1.ContentBlockType", `appv1 "github.com/acme/app/gen/app/v1"`},
		{"github.com/acme/app/internal/kinds.Kind", "kinds.Kind", `"github.com/acme/app/internal/kinds"`},
		{"*github.com/acme/app/internal/kinds.Kind", "*kinds.Kind", `"github.com/acme/app/internal/kinds"`},
	}
	for _, c := range cases {
		o, err := ParseGoTypeOverride(c.spec)
		if err != nil {
			t.Errorf("%s: %v", c.spec, err)
			continue
		}
		if o.GoType != c.goType || o.ImportSpec() != c.spec2 {
			t.Errorf("%s: GoType=%q import=%s, want %q / %s", c.spec, o.GoType, o.ImportSpec(), c.goType, c.spec2)
		}
	}
	for _, bad := range []string{"", "map[string]github.com/x.T", "github.com/x.", "github.com/x;1bad.T"} {
		if _, err := ParseGoTypeOverride(bad); err == nil {
			t.Errorf("%q: want a refusal", bad)
		}
	}
}

// A column with a forge:go-type has no generated wire conversion: refused
// by name rather than guessed at.
func TestGoTypeOverride_IsNotConvertedToTheWire(t *testing.T) {
	col := EntityColumn{Name: "kind", Type: "int64", DeclType: "INT4", NotNull: true, GoType: "github.com/acme/x;appv1.Kind"}
	wf := EntityField{Name: "kind", GoName: "Kind", ProtoType: "int32", GoType: "int32", Kind: FieldKindScalar}
	if _, why := assignToDB("e", "m", wf, col); !strings.Contains(why, "forge:go-type") {
		t.Errorf("assignToDB: why = %q", why)
	}
	if _, why := assignToProto("m", "e", wf, col); !strings.Contains(why, "forge:go-type") {
		t.Errorf("assignToProto: why = %q", why)
	}
}
