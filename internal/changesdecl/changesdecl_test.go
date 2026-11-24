package changesdecl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

const example = `version: 1
changes:
  new-checkout:
    description: Shorter checkout flow
    type: bool            # bool | string | number | json
    default: control      # a variant name
    variants:
      control: false
      treatment: true
  hero.copy:
    type: string
    default: a
    variants:
      a: Ship faster
      b: "Deploy in one command"
  max_items:
    type: number
    default: low
    variants:
      low: 10
      high: 2.5e2
  theme:
    type: json
    default: v1
    variants:
      v1: {accent: blue, radius: 4}
      v2: [1, "x"]
`

func TestParseTheExample(t *testing.T) {
	d, err := Parse(FileName, []byte(example))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, c := range d.Changes {
		keys = append(keys, c.Key)
	}
	if want := []string{"new-checkout", "hero.copy", "max_items", "theme"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v (file order)", keys, want)
	}
	checkout := d.Changes[0]
	if checkout.Description != "Shorter checkout flow" || checkout.Type != release.ChangeTypeBool || checkout.Default != "control" || checkout.Line != 3 {
		t.Errorf("new-checkout = %+v", checkout)
	}
	if want := []Variant{{"control", json.RawMessage(`false`)}, {"treatment", json.RawMessage(`true`)}}; !reflect.DeepEqual(checkout.Variants, want) {
		t.Errorf("variants = %s, want them in file order", checkout.Variants)
	}
	if got := string(d.Changes[3].DefaultValue()); got != `{"accent":"blue","radius":4}` {
		t.Errorf("theme default = %s", got)
	}
	if got := string(d.Changes[2].Variants[1].Value); got != `250` {
		t.Errorf("max_items high = %s, want 250", got)
	}

	// The shape it projects is one every reader validates.
	shape := release.Shape{Kind: release.EnvSelfManaged, Changes: d.Shape()}
	if err := shape.Validate(); err != nil {
		t.Fatalf("the projected shape does not validate: %v", err)
	}
	raw, err := json.Marshal(shape.Canonical().Changes["new-checkout"])
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"bool","default":"control","variants":{"control":false,"treatment":true},"description":"Shorter checkout flow"}`; string(raw) != want {
		t.Errorf("shape change = %s\nwant          %s", raw, want)
	}
}

// TestParseRefusesWithFileAndLine: every problem is reported, each with the
// line it is on.
func TestParseRefusesWithFileAndLine(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want []string
	}{
		"no version":              {"changes: {}\n", []string{"changes.yaml:1: version is missing"}},
		"wrong version":           {"version: 2\n", []string{`changes.yaml:1: version "2" is not supported`}},
		"unknown top-level field": {"version: 1\nflags: {}\n", []string{`changes.yaml:2: unknown field "flags"`}},
		"bad key": {"version: 1\nchanges:\n  New_Checkout:\n    type: bool\n    default: a\n    variants: {a: true}\n",
			[]string{`changes.yaml:3: change key "New_Checkout" is invalid`}},
		"bad type": {"version: 1\nchanges:\n  k:\n    type: int\n    default: a\n    variants: {a: 1}\n",
			[]string{`changes.yaml:4: change "k": type "int" is not bool, string, number or json`}},
		"missing type": {"version: 1\nchanges:\n  k:\n    default: a\n    variants: {a: 1}\n",
			[]string{`changes.yaml:4: change "k": type is missing`}},
		"default not a variant": {"version: 1\nchanges:\n  k:\n    type: bool\n    default: on\n    variants:\n      control: false\n      treatment: true\n",
			[]string{`changes.yaml:5: change "k": default "on" is not one of its variants (control, treatment)`}},
		"missing default": {"version: 1\nchanges:\n  k:\n    type: bool\n    variants: {a: true}\n",
			[]string{`change "k": default is missing`}},
		"value of the wrong type": {"version: 1\nchanges:\n  k:\n    type: bool\n    default: a\n    variants:\n      a: false\n      b: \"yes\"\n",
			[]string{`changes.yaml:8: change "k": variant "b" is "yes", not a bool (the change's type is bool)`}},
		"null value": {"version: 1\nchanges:\n  k:\n    type: json\n    default: a\n    variants:\n      a: ~\n",
			[]string{`changes.yaml:7: change "k": variant "a" is null`}},
		"infinite number": {"version: 1\nchanges:\n  k:\n    type: number\n    default: a\n    variants:\n      a: .inf\n",
			[]string{`changes.yaml:7: change "k": variant "a" is not a JSON value`}},
		"bad variant name": {"version: 1\nchanges:\n  k:\n    type: bool\n    default: a\n    variants:\n      a: true\n      B: false\n",
			[]string{`changes.yaml:8: change "k": variant name "B" is invalid`}},
		"no variants": {"version: 1\nchanges:\n  k:\n    type: bool\n    default: a\n    variants: {}\n",
			[]string{`changes.yaml:6: change "k": variants must be a non-empty mapping`}},
		"unknown change field": {"version: 1\nchanges:\n  k:\n    type: bool\n    default: a\n    variants: {a: true}\n    rollout: 50\n",
			[]string{`changes.yaml:7: change "k": unknown field "rollout"`}},
		"duplicate key": {"version: 1\nchanges:\n  k: {type: bool, default: a, variants: {a: true}}\n  k: {type: bool, default: a, variants: {a: true}}\n",
			[]string{`changes.yaml:4: changes declares "k" twice`}},
		"colliding names": {"version: 1\nchanges:\n  new-checkout: {type: bool, default: a, variants: {a: true}}\n  new_checkout: {type: bool, default: a, variants: {a: true}}\n",
			[]string{`changes.yaml:4: change "new_checkout": its Go name NewCheckout is also "new-checkout"'s`}},
		"two problems": {"version: 1\nchanges:\n  a: {type: int, default: x, variants: {x: 1}}\n  b: {type: bool, default: y, variants: {x: true}}\n",
			[]string{`changes.yaml:3: change "a": type "int"`, `changes.yaml:4: change "b": default "y" is not one of its variants (x)`}},
		"not yaml": {"version: [1\n", []string{"changes.yaml: yaml:"}},
		"a second document": {"version: 1\nchanges: {}\n---\nversion: 1\nchanges:\n  k: {type: bool, default: a, variants: {a: true}}\n",
			[]string{"changes.yaml:3: a second YAML document"}},
	}
	for name, tc := range cases {
		_, err := Parse(FileName, []byte(tc.yaml))
		if err == nil {
			t.Errorf("%s: parsed, want a refusal", name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error does not contain %q:\n%v", name, w, err)
			}
		}
	}
}

// A bare trailing `---` is no second document.
func TestParseAcceptsATrailingDocumentMarker(t *testing.T) {
	if _, err := Parse(FileName, []byte("version: 1\nchanges: {}\n---\n")); err != nil {
		t.Fatal(err)
	}
}

func TestParseAcceptsAnEmptyChangesList(t *testing.T) {
	for _, src := range []string{"version: 1\n", "version: 1\nchanges:\n", "version: 1\nchanges: {}\n"} {
		d, err := Parse(FileName, []byte(src))
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		if d.Shape() != nil {
			t.Errorf("%q: shape = %v, want nil", src, d.Shape())
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	d, err := Load(dir)
	if d != nil || err != nil {
		t.Fatalf("absent file: got %v, %v; want nil, nil", d, err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, err := Load(dir); err != nil || len(d.Changes) != 4 {
		t.Fatalf("Load = %+v, %v", d, err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("version: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "changes.yaml:1:") {
		t.Fatalf("Load of an invalid file = %v", err)
	}
}

func TestNames(t *testing.T) {
	for key, want := range map[string][2]string{
		"new-checkout":  {"NewCheckout", "newCheckout"},
		"hero.copy":     {"HeroCopy", "heroCopy"},
		"max_items":     {"MaxItems", "maxItems"},
		"api.url":       {"APIURL", "apiUrl"},
		"checkout-v2":   {"CheckoutV2", "checkoutV2"},
		"delete":        {"Delete", "deleteChange"},
		"new":           {"New", "newChange"},
		"x":             {"X", "x"},
		"a--b":          {"AB", "aB"},
		"user-id-label": {"UserIDLabel", "userIdLabel"},
	} {
		if got := GoName(key); got != want[0] {
			t.Errorf("GoName(%q) = %q, want %q", key, got, want[0])
		}
		if got := TSName(key); got != want[1] {
			t.Errorf("TSName(%q) = %q, want %q", key, got, want[1])
		}
	}
}
