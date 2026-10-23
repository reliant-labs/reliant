package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func cpSecret() ShapeControlPlaneSecret {
	return ShapeControlPlaneSecret{
		Name: "reliant-otel-auth", Namespace: "control-plane-prod", Cluster: "gke_p_prod",
		ConnectedCluster: "prod-control-plane", Kind: ControlPlaneSecretTelemetryToken,
		Keys: []string{"token", "OTEL_EXPORTER_OTLP_HEADERS"},
	}
}

// A shape that declares no control-plane secret encodes without the field, so
// every shape recorded before it existed hashes and diffs exactly as it did.
func TestShape_ControlPlaneSecretsOmittedWhenEmpty(t *testing.T) {
	b, err := Shape{Kind: EnvLocal}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "control_plane_secrets") {
		t.Errorf("an empty list must not be encoded: %s", b)
	}
}

// The round trip a recorded shape takes: encode, decode, same value. The wire
// names are the contract the control plane reads, so they are asserted too.
func TestShape_ControlPlaneSecretsRoundTrip(t *testing.T) {
	in := Shape{Kind: EnvSelfManaged, ControlPlaneSecrets: []ShapeControlPlaneSecret{cpSecret()}}
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"control_plane_secrets"`, `"connected_cluster":"prod-control-plane"`, `"kind":"telemetry_token"`, `"namespace":"control-plane-prod"`, `"cluster":"gke_p_prod"`, `"name":"reliant-otel-auth"`} {
		if !strings.Contains(string(b), field) {
			t.Errorf("encoded shape lacks %s: %s", field, b)
		}
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("a round-tripped shape is invalid: %v", err)
	}
	if want := in.Canonical(); !reflect.DeepEqual(out.ControlPlaneSecrets, want.ControlPlaneSecrets) {
		t.Errorf("round trip changed the secrets:\n got %+v\nwant %+v", out.ControlPlaneSecrets, want.ControlPlaneSecrets)
	}
	// It names a destination and a kind. There is no value field to carry one.
	typ := reflect.TypeOf(ShapeControlPlaneSecret{})
	for i := 0; i < typ.NumField(); i++ {
		switch n := strings.ToLower(typ.Field(i).Name); n {
		case "value", "data", "token", "password":
			t.Errorf("ShapeControlPlaneSecret has a %q field: a shape must never be able to carry a value", n)
		}
	}
}

func TestShape_ControlPlaneSecretsCanonicalOrder(t *testing.T) {
	a, b := cpSecret(), cpSecret()
	b.Name = "another"
	x, _ := Shape{Kind: EnvLocal, ControlPlaneSecrets: []ShapeControlPlaneSecret{a, b}}.Encode()
	y, _ := Shape{Kind: EnvLocal, ControlPlaneSecrets: []ShapeControlPlaneSecret{b, a}}.Encode()
	if string(x) != string(y) {
		t.Errorf("declaration order changed the encoding:\n%s\n%s", x, y)
	}
}

func TestShape_ControlPlaneSecretsValidate(t *testing.T) {
	ok := Shape{Kind: EnvSelfManaged, ControlPlaneSecrets: []ShapeControlPlaneSecret{cpSecret()}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid shape refused: %v", err)
	}
	mut := func(f func(*ShapeControlPlaneSecret)) Shape {
		c := cpSecret()
		f(&c)
		return Shape{Kind: EnvSelfManaged, ControlPlaneSecrets: []ShapeControlPlaneSecret{c}}
	}
	cases := map[string]Shape{
		"no name":      mut(func(c *ShapeControlPlaneSecret) { c.Name = "" }),
		"no namespace": mut(func(c *ShapeControlPlaneSecret) { c.Namespace = "" }),
		"no cluster":   mut(func(c *ShapeControlPlaneSecret) { c.Cluster = "" }),
		// The control plane has no credential to deliver with.
		"no connected cluster": mut(func(c *ShapeControlPlaneSecret) { c.ConnectedCluster = "" }),
		"unknown kind":         mut(func(c *ShapeControlPlaneSecret) { c.Kind = "password" }),
		"duplicate":            {Kind: EnvSelfManaged, ControlPlaneSecrets: []ShapeControlPlaneSecret{cpSecret(), cpSecret()}},
	}
	for name, s := range cases {
		if err := s.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}
