package schemagen

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func marshal(t *testing.T, s *Schema) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func generate(t *testing.T, v any, opts Options) string {
	t.Helper()
	s, err := Generate(reflect.TypeOf(v), opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return marshal(t, s)
}

func propNames(s *Schema) string {
	names := make([]string, len(s.Properties))
	for i, p := range s.Properties {
		names[i] = p.Name
	}
	return strings.Join(names, ",")
}

type scalars struct {
	B   bool      `json:"b"`
	I   int32     `json:"i"`
	U   uint8     `json:"u"`
	F   float64   `json:"f"`
	S   string    `json:"s"`
	Any any       `json:"any"`
	T   time.Time `json:"t"`
	Raw []byte    `json:"raw"`
}

// Each Go kind maps to the JSON type encoding/json would accept for it, and
// every non-required property additionally accepts null.
func TestGenerateMapsGoKindsToJSONTypes(t *testing.T) {
	got := generate(t, scalars{}, Options{Required: map[reflect.Type][]string{reflect.TypeOf(scalars{}): {"b", "u"}}})
	want := `{"$schema":"` + Draft + `","type":"object",` +
		`"properties":{` +
		`"b":{"type":"boolean"},` +
		`"i":{"type":["integer","null"]},` +
		`"u":{"type":"integer","minimum":0},` +
		`"f":{"type":["number","null"]},` +
		`"s":{"type":["string","null"]},` +
		`"any":{},` +
		`"t":{"type":["string","null"],"format":"date-time"},` +
		`"raw":{"description":"base64","type":["string","null"]}},` +
		`"required":["b","u"],"additionalProperties":false}`
	if got != want {
		t.Errorf("schema:\n got %s\nwant %s", got, want)
	}
}

func TestGenerateRootMustBeAStruct(t *testing.T) {
	for name, v := range map[string]any{"string": "", "slice": []int{}, "map": map[string]int{}} {
		_, err := Generate(reflect.TypeOf(v), Options{})
		if err == nil || !strings.Contains(err.Error(), "is not a struct") {
			t.Errorf("%s root: err = %v, want a not-a-struct error", name, err)
		}
	}
	// A pointer root is the struct it points at.
	s, err := Generate(reflect.TypeOf(&hostRT{}), Options{ID: "id", Title: "T", Description: "D"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "id" || s.Title != "T" || s.Description != "D" || s.Dialect != Draft {
		t.Errorf("root metadata = %+v", s)
	}
}

type unrepresentable struct {
	Ch chan int `json:"ch"`
}

type badKey struct {
	M map[int]string `json:"m"`
}

type chanSlice struct {
	L []chan int `json:"l"`
}

type chanMap struct {
	M map[string]chan int `json:"m"`
}

func TestGenerateErrorsNameTheOffendingField(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"channel field", unrepresentable{}, "schemagen.unrepresentable.Ch: schemagen: chan int (chan) has no JSON Schema"},
		{"non-string map key", badKey{}, "map key int is not a string"},
		{"unrepresentable slice element", chanSlice{}, "chan int (chan) has no JSON Schema"},
		{"unrepresentable map value", chanMap{}, "chan int (chan) has no JSON Schema"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Generate(reflect.TypeOf(tc.v), Options{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

type nested struct {
	Hosts   []hostRT          `json:"hosts"`
	ByName  map[string]hostRT `json:"byName"`
	Primary *hostRT           `json:"primary"`
	Grid    [2]int            `json:"grid"`
}

// Named structs are described once under $defs and referenced everywhere.
func TestGenerateSharesNamedStructsThroughDefs(t *testing.T) {
	got := generate(t, nested{}, Options{})
	if n := strings.Count(got, `"$ref":"#/$defs/hostRT"`); n != 3 {
		t.Errorf("hostRT referenced %d times, want 3 (slice item, map value, pointer):\n%s", n, got)
	}
	if n := strings.Count(got, `"hostRT":{`); n != 1 {
		t.Errorf("hostRT defined %d times, want once:\n%s", n, got)
	}
	if !strings.Contains(got, `"grid":{"type":["array","null"],"items":{"type":"integer"}}`) {
		t.Errorf("array should be an array of integers:\n%s", got)
	}
	if !strings.Contains(got, `"byName":{"type":["object","null"],"additionalProperties":{"$ref":"#/$defs/hostRT"}}`) {
		t.Errorf("map should constrain its values:\n%s", got)
	}
}

// Buffer collides by name with bytes.Buffer.
type Buffer struct {
	N int `json:"n"`
}

type clash struct {
	A Buffer       `json:"a"`
	B bytes.Buffer `json:"b"`
}

// Two packages declaring the same type name get distinct $defs keys.
func TestGenerateQualifiesCollidingTypeNames(t *testing.T) {
	got := generate(t, clash{}, Options{})
	for _, want := range []string{`"Buffer":{`, `"bytes.Buffer":{`, `"$ref":"#/$defs/bytes.Buffer"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

type left struct {
	V int `json:"v"`
}

// dupDepthType and bothSidesType repeat a JSON name at one depth, which go
// vet's structtag check refuses in source, so they are built by reflection.
func dupDepthType() reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeOf(""), Tag: `json:"x"`},
		{Name: "B", Type: reflect.TypeOf(""), Tag: `json:"x"`},
	})
}

func bothSidesType() reflect.Type {
	type right struct {
		V string `json:"v"`
	}
	return reflect.StructOf([]reflect.StructField{
		{Name: "Left", Type: reflect.TypeOf(left{}), Anonymous: true},
		{Name: "Right", Type: reflect.TypeOf(right{}), Anonymous: true},
	})
}

type embeds struct {
	*left
	Named  left `json:"named"`
	Tagged left `json:",omitempty"`
}

func TestGenerateFollowsEncodingJSONEmbeddingRules(t *testing.T) {
	if _, err := Generate(dupDepthType(), Options{}); err == nil || !strings.Contains(err.Error(), `declares JSON property "x" twice at one depth`) {
		t.Errorf("duplicate names: err = %v", err)
	}
	if _, err := Generate(bothSidesType(), Options{}); err == nil || !strings.Contains(err.Error(), `"v" twice at one depth`) {
		t.Errorf("same name from two embedded structs: err = %v", err)
	}
	s, err := Generate(reflect.TypeOf(embeds{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// *left is flattened into v; a tag with no name falls back to the Go name.
	if got := propNames(s); got != "v,named,Tagged" {
		t.Errorf("properties = %q, want v,named,Tagged", got)
	}
}

func TestGenerateRequiredMustNameARealField(t *testing.T) {
	_, err := Generate(reflect.TypeOf(hostRT{}), Options{Required: map[reflect.Type][]string{reflect.TypeOf(hostRT{}): {"prot"}}})
	if err == nil || !strings.Contains(err.Error(), `required property "prot" is not a field`) {
		t.Errorf("err = %v", err)
	}
}

func TestGenerateAttachesDescriptions(t *testing.T) {
	describe := func(typ reflect.Type, field string) string {
		switch {
		case typ == reflect.TypeOf(hostRT{}) && field == "":
			return "A host."
		case typ == reflect.TypeOf(hostRT{}) && field == "Port":
			return "Listen port."
		case typ == reflect.TypeOf(nested{}) && field == "Primary":
			return "The primary host."
		case typ == reflect.TypeOf(nested{}) && field == "Hosts":
			return "All hosts."
		}
		return ""
	}
	got := generate(t, nested{}, Options{Describe: describe})
	for _, want := range []string{
		`"hostRT":{"description":"A host.","type":"object"`,
		`"port":{"description":"Listen port.","type":["integer","null"]}`,
		// A $ref property keeps its description beside the nullable wrapper.
		`"primary":{"description":"The primary host.","anyOf":[{"$ref":"#/$defs/hostRT"},{"type":"null"}]}`,
		`"hosts":{"description":"All hosts.","type":["array","null"],"items":{"$ref":"#/$defs/hostRT"}}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

type enumString string

func (e *enumString) UnmarshalJSON([]byte) error { return nil }

type wireDoc struct {
	W custom `json:"w"`
}

func TestGenerateStringKindedUnmarshalerIsAString(t *testing.T) {
	got := generate(t, struct {
		E enumString `json:"e"`
	}{}, Options{})
	if !strings.Contains(got, `"e":{"type":["string","null"]}`) {
		t.Errorf("enum string: %s", got)
	}
}

func TestGenerateHonoursOptionsTypes(t *testing.T) {
	customType := reflect.TypeOf(custom{})
	got := generate(t, wireDoc{}, Options{Types: map[reflect.Type]func(*Generator) (*Schema, error){
		customType: func(g *Generator) (*Schema, error) {
			// A custom wire shape that references another type.
			ref, err := g.Ref(reflect.TypeOf(&hostRT{}))
			if err != nil {
				return nil, err
			}
			return &Schema{Types: []string{"object"}, Properties: []Property{{Name: "host", Schema: ref}}}, nil
		},
	}})
	for _, want := range []string{
		`"w":{"anyOf":[{"$ref":"#/$defs/custom"},{"type":"null"}]}`,
		`"custom":{"type":"object","properties":{"host":{"$ref":"#/$defs/hostRT"}}}`,
		`"hostRT":{`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}

	// An unnamed type's override is inlined rather than put under $defs.
	got = generate(t, struct {
		L []custom `json:"l"`
	}{}, Options{Types: map[reflect.Type]func(*Generator) (*Schema, error){
		reflect.TypeOf([]custom(nil)): func(*Generator) (*Schema, error) { return &Schema{Types: []string{"string"}}, nil },
	}})
	if !strings.Contains(got, `"l":{"type":["string","null"]}`) || strings.Contains(got, "$defs") {
		t.Errorf("unnamed override: %s", got)
	}
}

func TestGenerateSurfacesFailingOverrides(t *testing.T) {
	boom := errors.New("boom")
	_, err := Generate(reflect.TypeOf(wireDoc{}), Options{Types: map[reflect.Type]func(*Generator) (*Schema, error){
		reflect.TypeOf(custom{}): func(*Generator) (*Schema, error) { return nil, boom },
	}})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "wireDoc.W") {
		t.Errorf("type override: err = %v, want boom wrapped with the field", err)
	}
	_, err = Generate(reflect.TypeOf(wireDoc{}), Options{Fields: map[FieldKey]func(*Generator) (*Schema, error){
		{Type: reflect.TypeOf(wireDoc{}), Name: "w"}: func(*Generator) (*Schema, error) { return nil, boom },
	}})
	if !errors.Is(err, boom) {
		t.Errorf("field override: err = %v, want boom", err)
	}
}

type variantA struct {
	Name string `json:"name"`
	A    int    `json:"a"`
}

type variantB struct {
	B bool `json:"b"`
}

func newGenerator() *Generator {
	return &Generator{opts: Options{}, defs: map[string]*Schema{}, names: map[reflect.Type]string{}, taken: map[string]reflect.Type{}, active: map[reflect.Type]bool{}}
}

func TestTaggedUnionPrependsTagAndSharedProperties(t *testing.T) {
	g := newGenerator()
	sharedProp := Property{Name: "name", Schema: &Schema{Types: []string{"string"}}}
	u, err := TaggedUnion(g, "kind", map[string]reflect.Type{
		"b":     reflect.TypeOf(&variantB{}),
		"a":     reflect.TypeOf(variantA{}),
		"empty": nil,
	}, sharedProp)
	if err != nil {
		t.Fatal(err)
	}
	// Variants come out in tag order, whatever the map's order.
	want := `{"oneOf":[{"$ref":"#/$defs/variantA"},{"$ref":"#/$defs/variantB"},` +
		`{"type":"object","properties":{"kind":{"const":"empty"},"name":{"type":"string"}},"required":["kind"],"additionalProperties":false}]}`
	if got := marshal(t, u); got != want {
		t.Errorf("union:\n got %s\nwant %s", got, want)
	}
	a := g.defs["variantA"]
	// variantA declares name itself, so the shared one is not repeated.
	if got := propNames(a); got != "kind,name,a" || strings.Join(a.Required, ",") != "kind" {
		t.Errorf("variantA properties = %s required = %v", got, a.Required)
	}
	if got := propNames(g.defs["variantB"]); got != "kind,name,b" {
		t.Errorf("variantB properties = %s, want the shared name added after the tag", got)
	}
}

func TestTaggedUnionRefusesVariantUsedOutsideTheUnion(t *testing.T) {
	g := newGenerator()
	if _, err := g.Ref(reflect.TypeOf(variantB{})); err != nil {
		t.Fatal(err)
	}
	_, err := TaggedUnion(g, "kind", map[string]reflect.Type{"b": reflect.TypeOf(variantB{})})
	if err == nil || !strings.Contains(err.Error(), "also referenced outside its union") {
		t.Errorf("err = %v", err)
	}
}

func TestTaggedUnionVariantOverridesAndErrors(t *testing.T) {
	boom := errors.New("boom")
	g := newGenerator()
	g.opts.Types = map[reflect.Type]func(*Generator) (*Schema, error){
		reflect.TypeOf(variantB{}): func(*Generator) (*Schema, error) { return nil, boom },
	}
	if _, err := TaggedUnion(g, "kind", map[string]reflect.Type{"b": reflect.TypeOf(variantB{})}); !errors.Is(err, boom) {
		t.Errorf("override error not surfaced: %v", err)
	}

	g = newGenerator()
	g.opts.Types = map[reflect.Type]func(*Generator) (*Schema, error){
		reflect.TypeOf(variantB{}): func(*Generator) (*Schema, error) {
			return &Schema{Types: []string{"object"}, Properties: []Property{{Name: "custom", Schema: &Schema{}}}}, nil
		},
	}
	if _, err := TaggedUnion(g, "kind", map[string]reflect.Type{"b": reflect.TypeOf(variantB{})}); err != nil {
		t.Fatal(err)
	}
	if got := marshal(t, g.defs["variantB"]); !strings.Contains(got, `"kind":{"const":"b"},"custom":{}`) {
		t.Errorf("an overridden variant still gets the tag first: %s", got)
	}

	// A variant whose struct cannot be described reports its own error.
	if _, err := TaggedUnion(newGenerator(), "kind", map[string]reflect.Type{"x": reflect.TypeOf(unrepresentable{})}); err == nil {
		t.Error("expected the variant's own error")
	}
}

func TestStructAndSetProperty(t *testing.T) {
	g := newGenerator()
	s, err := g.Struct(reflect.TypeOf(&hostRT{}))
	if err != nil {
		t.Fatal(err)
	}
	// Struct is inline: nothing is registered under $defs.
	if len(g.defs) != 0 || len(s.Properties) != 1 {
		t.Fatalf("Struct = %+v defs=%v", s, g.defs)
	}
	s.Properties[0].Schema.Description = "old note"
	if !s.SetProperty("port", &Schema{Types: []string{"string"}}) {
		t.Fatal("SetProperty missed an existing property")
	}
	if got := s.Properties[0].Schema; got.Types[0] != "string" || got.Description != "old note" {
		t.Errorf("replaced property = %+v, want the new type with the old description", got)
	}
	if !s.SetProperty("port", &Schema{Types: []string{"integer"}, Description: "new note"}) || s.Properties[0].Schema.Description != "new note" {
		t.Errorf("an explicit description should win: %+v", s.Properties[0].Schema)
	}
	if s.SetProperty("missing", &Schema{}) {
		t.Error("SetProperty reported success for a missing property")
	}
}

func TestNullableWrapsEachShapeAppropriately(t *testing.T) {
	cases := []struct {
		name string
		in   *Schema
		want string
	}{
		{"type list gains null", &Schema{Types: []string{"string"}}, `{"type":["string","null"]}`},
		{"already accepts anything", &Schema{}, `{}`},
		{"ref becomes anyOf", &Schema{Ref: "#/$defs/x"}, `{"anyOf":[{"$ref":"#/$defs/x"},{"type":"null"}]}`},
		{"description moves to the wrapper", &Schema{Ref: "#/$defs/x", Description: "d"}, `{"description":"d","anyOf":[{"$ref":"#/$defs/x"},{"type":"null"}]}`},
		{"const becomes anyOf", &Schema{Const: "a"}, `{"anyOf":[{"const":"a"},{"type":"null"}]}`},
		{"oneOf becomes anyOf", &Schema{OneOf: []*Schema{{Const: "a"}}}, `{"anyOf":[{"oneOf":[{"const":"a"}]},{"type":"null"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := marshal(t, tc.in)
			if got := marshal(t, Nullable(tc.in)); got != tc.want {
				t.Errorf("Nullable = %s, want %s", got, tc.want)
			}
			if marshal(t, tc.in) != orig {
				t.Error("Nullable mutated its input")
			}
		})
	}
}

func TestSchemaMarshalCoversEveryKeywordInFixedOrder(t *testing.T) {
	min := 1.5
	s := &Schema{
		Dialect: "d", ID: "i", Title: "t", Description: "desc", Types: []string{"string", "null"}, Const: "c", Format: "f", Minimum: &min,
		Properties: []Property{{Name: "z", Schema: &Schema{}}, {Name: "a", Schema: &Schema{}}},
		Required:   []string{"z"}, AdditionalProperties: &Schema{Types: []string{"integer"}},
		Items: &Schema{}, OneOf: []*Schema{{}}, AnyOf: []*Schema{{}},
		Defs: map[string]*Schema{"b/c": {}, "a": {}},
	}
	want := `{"$schema":"d","$id":"i","title":"t","description":"desc","type":["string","null"],"const":"c","format":"f","minimum":1.5,` +
		`"properties":{"z":{},"a":{}},"required":["z"],"additionalProperties":{"type":"integer"},"items":{},"oneOf":[{}],"anyOf":[{}],"$defs":{"a":{},"b/c":{}}}`
	if got := marshal(t, s); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// NoAdditional wins over an additionalProperties schema.
	if got := marshal(t, &Schema{NoAdditional: true, AdditionalProperties: &Schema{}}); got != `{"additionalProperties":false}` {
		t.Errorf("NoAdditional = %s", got)
	}
}

func TestSchemaMarshalPropagatesUnmarshalableValues(t *testing.T) {
	if _, err := json.Marshal(&Schema{Const: make(chan int)}); err == nil {
		t.Error("expected the const's marshal error")
	}
	bad := &Schema{Properties: []Property{{Name: "p", Schema: &Schema{Const: make(chan int)}}}}
	if _, err := json.Marshal(bad); err == nil {
		t.Error("expected the nested property's marshal error")
	}
}

func TestEscapePointerRoundTripsThroughValidate(t *testing.T) {
	if got := escapePointer("a/b~c"); got != "a~1b~0c" {
		t.Errorf("escapePointer = %q", got)
	}
	c, err := Compile([]byte(`{"$ref":"#/$defs/a~1b~0c","$defs":{"a/b~c":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := validate(t, c, `1`); !strings.Contains(got, "must be string") {
		t.Errorf("a $ref with escaped pointer characters did not resolve: %q", got)
	}
}
