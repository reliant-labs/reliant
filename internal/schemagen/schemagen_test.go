package schemagen

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type hostRT struct {
	Port int `json:"port,omitempty"`
}

type clusterRT struct {
	Namespace string `json:"namespace"`
}

type item struct {
	Name    string          `json:"name"`
	Runtime json.RawMessage `json:"runtime"`
	Tags    []string        `json:"tags,omitempty"`
	Hidden  string          `json:"-"`
	private string          //nolint:unused // an unexported field: the schema must skip it
}

type base struct {
	Project string `json:"project"`
	Shadow  int    `json:"shadow"`
}

type doc struct {
	base
	Shadow string            `json:"shadow,omitempty"`
	Items  []item            `json:"items"`
	Labels map[string]string `json:"labels,omitempty"`
	Next   *doc              `json:"next,omitempty"`
}

func compileDoc(t *testing.T) *Compiled {
	t.Helper()
	s, err := Generate(reflect.TypeOf(doc{}), Options{
		ID:       "https://example.test/doc.json",
		Required: map[reflect.Type][]string{reflect.TypeOf(doc{}): {"project"}, reflect.TypeOf(item{}): {"name", "runtime"}},
		Fields: map[FieldKey]func(*Generator) (*Schema, error){
			{Type: reflect.TypeOf(item{}), Name: "runtime"}: func(g *Generator) (*Schema, error) {
				return TaggedUnion(g, "type", map[string]reflect.Type{"host": reflect.TypeOf(hostRT{}), "cluster": reflect.TypeOf(clusterRT{}), "external": nil})
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compile(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func validate(t *testing.T, c *Compiled, src string) string {
	t.Helper()
	d, err := DecodeJSON([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if errs := c.Validate(d); len(errs) > 0 {
		return errs.Error()
	}
	return ""
}

func TestValidateAcceptsWhatTheDecoderAccepts(t *testing.T) {
	c := compileDoc(t)
	ok := `{"project":"p","shadow":"s","items":[{"name":"a","runtime":{"type":"host","port":8080},"tags":null},
		{"name":"b","runtime":{"type":"external"}}],"labels":{"x":"y"},"next":{"project":"q","items":null}}`
	if got := validate(t, c, ok); got != "" {
		t.Fatalf("valid document rejected:\n%s", got)
	}
}

func TestValidateErrorsNameThePathAndTheField(t *testing.T) {
	c := compileDoc(t)
	cases := []struct{ doc, want string }{
		{`{"items":[]}`, "project: required, and missing"},
		{`{"project":"p","itmes":[]}`, `itmes: unknown field (did you mean "items"?)`},
		{`{"project":"p","items":[{"name":"a","runtime":{"type":"kubernetes"}}]}`, `items[0].runtime.type: "kubernetes" is not one of: cluster, external, host`},
		{`{"project":"p","items":[{"name":"a","runtime":{}}]}`, "items[0].runtime.type: required, and missing (one of: cluster, external, host)"},
		{`{"project":"p","items":[{"name":"a","runtime":{"type":"host","prot":1}}]}`, `items[0].runtime.prot: unknown field (did you mean "port"?)`},
		{`{"project":"p","items":[{"name":"a","runtime":{"type":"host","port":"80"}}]}`, "items[0].runtime.port: must be integer (or null), not string"},
		{`{"project":"p","items":[{"name":"a","runtime":null}]}`, "items[0].runtime: required, and null"},
		{`{"project":"p","next":{"project":7}}`, "next.project: must be string, not integer"},
	}
	for _, tc := range cases {
		got := validate(t, c, tc.doc)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s\n got: %s\nwant: %s", tc.doc, got, tc.want)
		}
	}
}

func TestShallowerFieldShadowsEmbedded(t *testing.T) {
	c := compileDoc(t)
	if got := validate(t, c, `{"project":"p","shadow":1}`); !strings.Contains(got, "shadow: must be string") {
		t.Fatalf("embedded int field was not shadowed by the outer string: %q", got)
	}
}

type custom struct{ X int }

func (c *custom) UnmarshalJSON([]byte) error { return nil }

type hasCustom struct {
	C custom `json:"c"`
}

func TestCustomUnmarshalerMustBeDeclared(t *testing.T) {
	_, err := Generate(reflect.TypeOf(hasCustom{}), Options{})
	if err == nil || !strings.Contains(err.Error(), "Options.Types") {
		t.Fatalf("a struct with its own UnmarshalJSON was described by its fields: %v", err)
	}
}

func TestCompileRefusesUnimplementedKeywords(t *testing.T) {
	if _, err := Compile([]byte(`{"type":"object","patternProperties":{"^x":{}}}`)); err == nil {
		t.Fatal("Compile accepted a keyword Validate would ignore")
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	gen := func() string {
		s, err := Generate(reflect.TypeOf(doc{}), Options{Fields: map[FieldKey]func(*Generator) (*Schema, error){
			{Type: reflect.TypeOf(item{}), Name: "runtime"}: func(g *Generator) (*Schema, error) {
				return TaggedUnion(g, "type", map[string]reflect.Type{"host": reflect.TypeOf(hostRT{}), "cluster": reflect.TypeOf(clusterRT{})})
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(s)
		return string(b)
	}
	if a, b := gen(), gen(); a != b {
		t.Fatal("two generations differ")
	}
}

func TestDecodeJSONTakesExactlyOneDocument(t *testing.T) {
	for _, ok := range []string{`{"a":1}`, "{\"a\":1}\n", " {\"a\":1} \n\t "} {
		if _, err := DecodeJSON([]byte(ok)); err != nil {
			t.Errorf("DecodeJSON(%q) = %v, want the document", ok, err)
		}
	}
	for _, bad := range []string{`{"a":1}{"b":2}`, `{"a":1} 2`, `{"a":1} garbage`, `{"a":1}]`} {
		if _, err := DecodeJSON([]byte(bad)); err == nil {
			t.Errorf("DecodeJSON(%q) accepted trailing data", bad)
		}
	}
}
