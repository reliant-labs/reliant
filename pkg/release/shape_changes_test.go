package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func checkoutChange() ShapeChange {
	return ShapeChange{
		Type: ChangeTypeBool, Default: "control", Description: "Shorter checkout flow",
		Variants: map[string]json.RawMessage{"control": json.RawMessage(`false`), "treatment": json.RawMessage(`true`)},
	}
}

// TestShapeChangesValidate: a declared change has a valid key, a known type,
// 1..32 well-named variants whose values all have that type, and a default
// that is one of them.
func TestShapeChangesValidate(t *testing.T) {
	ok := map[string]ShapeChange{
		"new-checkout": checkoutChange(),
		"hero.copy":    {Type: ChangeTypeString, Default: "a", Variants: map[string]json.RawMessage{"a": json.RawMessage(`"Ship it"`)}},
		"max_items":    {Type: ChangeTypeNumber, Default: "low", Variants: map[string]json.RawMessage{"low": json.RawMessage(`10`), "high": json.RawMessage(`2.5e2`)}},
		"theme":        {Type: ChangeTypeJSON, Default: "v1", Variants: map[string]json.RawMessage{"v1": json.RawMessage(`{"accent": "blue"}`), "v2": json.RawMessage(`[1, "x"]`)}},
	}
	if err := (Shape{Kind: EnvSelfManaged, Changes: ok}).Validate(); err != nil {
		t.Fatalf("valid changes refused: %v", err)
	}

	with := func(edit func(*ShapeChange)) ShapeChange { c := checkoutChange(); edit(&c); return c }
	bad := map[string]struct {
		key string
		c   ShapeChange
	}{
		"key with a capital":        {"New-checkout", checkoutChange()},
		"key with a separator":      {"a|b", checkoutChange()},
		"key starting with a digit": {"1x", checkoutChange()},
		"unknown type":              {"k", with(func(c *ShapeChange) { c.Type = "int" })},
		"no variants":               {"k", with(func(c *ShapeChange) { c.Variants = nil })},
		"default not a variant":     {"k", with(func(c *ShapeChange) { c.Default = "off" })},
		"variant name invalid":      {"k", with(func(c *ShapeChange) { c.Variants["Treatment"] = json.RawMessage(`true`) })},
		"bool given a string":       {"k", with(func(c *ShapeChange) { c.Variants["treatment"] = json.RawMessage(`"true"`) })},
		"null value":                {"k", with(func(c *ShapeChange) { c.Variants["treatment"] = json.RawMessage(`null`) })},
		"malformed value":           {"k", with(func(c *ShapeChange) { c.Variants["treatment"] = json.RawMessage(`{`) })},
		"number overflows":          {"k", ShapeChange{Type: ChangeTypeNumber, Default: "a", Variants: map[string]json.RawMessage{"a": json.RawMessage(`1e999`)}}},
		"number given a string":     {"k", ShapeChange{Type: ChangeTypeNumber, Default: "a", Variants: map[string]json.RawMessage{"a": json.RawMessage(`"1"`)}}},
		"string given a number":     {"k", ShapeChange{Type: ChangeTypeString, Default: "a", Variants: map[string]json.RawMessage{"a": json.RawMessage(`1`)}}},
		"description too long":      {"k", with(func(c *ShapeChange) { c.Description = strings.Repeat("x", MaxChangeDescription+1) })},
		"value too large":           {"k", ShapeChange{Type: ChangeTypeString, Default: "a", Variants: map[string]json.RawMessage{"a": json.RawMessage(`"` + strings.Repeat("x", MaxChangeValueBytes) + `"`)}}},
	}
	for name, tc := range bad {
		err := (Shape{Kind: EnvSelfManaged, Changes: map[string]ShapeChange{tc.key: tc.c}}).Validate()
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}

	many := map[string]ShapeChange{}
	for i := 0; i <= MaxShapeChanges; i++ {
		many["k"+strings.Repeat("x", i%60)+string(rune('a'+i%26))+string(rune('a'+i/26))] = checkoutChange()
	}
	if len(many) <= MaxShapeChanges {
		t.Fatalf("fixture built %d changes, want more than %d", len(many), MaxShapeChanges)
	}
	if err := (Shape{Kind: EnvSelfManaged, Changes: many}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("%d changes: got %v, want ErrInvalid", len(many), err)
	}
}

// TestShapeChangesCanonical: values are compacted, so two spellings of one
// declaration hash the same, and no changes is the absence of the key.
func TestShapeChangesCanonical(t *testing.T) {
	spaced := Shape{Kind: EnvSelfManaged, Changes: map[string]ShapeChange{"theme": {
		Type: ChangeTypeJSON, Default: "v1", Variants: map[string]json.RawMessage{"v1": json.RawMessage("{ \"accent\" : \"blue\" }")},
	}}}
	got := spaced.Canonical().Changes["theme"].Variants["v1"]
	if string(got) != `{"accent":"blue"}` {
		t.Errorf("canonical value = %s, want it compacted", got)
	}
	if string(spaced.Changes["theme"].Variants["v1"]) == string(got) {
		t.Fatal("Canonical modified the shape it was given")
	}
	empty := Shape{Kind: EnvSelfManaged, Changes: map[string]ShapeChange{}}.Canonical()
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Changes != nil || strings.Contains(string(raw), `"changes"`) {
		t.Errorf("a shape declaring no change carries a changes key: %s", raw)
	}
}

// TestChangesAreAFeature: a reader that predates Shape.Changes is sent the
// shape without them (its strict decoder would refuse the whole shape), a
// reader of the feature gets them whole, and the shape given is unmodified.
func TestChangesAreAFeature(t *testing.T) {
	s := Shape{Kind: EnvSelfManaged, Changes: map[string]ShapeChange{"new-checkout": checkoutChange()}}
	if old := (&BundleReader{Features: []string{BundleFeaturePreview}}).LimitShape(s); old.Changes != nil {
		t.Errorf("a reader without the feature was sent the changes: %+v", old.Changes)
	}
	if s.Changes == nil {
		t.Fatal("LimitShape modified the shape it was given")
	}
	if got := (&BundleReader{Features: []string{BundleFeatureChanges}}).LimitShape(s); !reflect.DeepEqual(got.Changes, s.Changes) {
		t.Errorf("a reader of the feature lost changes: %+v", got.Changes)
	}
	if !s.HasOptionalParts() {
		t.Error("a shape declaring changes reports no optional parts")
	}
	if (Shape{Kind: EnvSelfManaged}).HasOptionalParts() {
		t.Error("a shape declaring no change reports optional parts")
	}
}

// TestShapeChangesRoundTripTheBundleDocument: the documented JSON of a
// declared change, through the strict bundle decoder and back.
func TestShapeChangesRoundTripTheBundleDocument(t *testing.T) {
	doc := fullBundleDoc()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	const want = `"changes":{"new-checkout":{"type":"bool","default":"control","variants":{"control":false,"treatment":true},"description":"Shorter checkout flow"}}`
	if !strings.Contains(string(raw), want) {
		t.Errorf("the document does not carry the change as documented; want %s in\n%s", want, raw)
	}
	back, err := DecodeBundleDoc(raw)
	if err != nil {
		t.Fatalf("the document no longer decodes: %v", err)
	}
	if !reflect.DeepEqual(back.Shape.Changes, doc.Shape.Changes) {
		t.Errorf("round trip changed the changes:\n got %+v\nwant %+v", back.Shape.Changes, doc.Shape.Changes)
	}
}
