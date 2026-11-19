package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/secretaccess"
	"github.com/reliant-labs/forge/pkg/secretgen"
)

func storeShape() Shape {
	return Shape{
		Kind: EnvSelfManaged,
		StoreSecrets: []ShapeStoreSecret{
			{Cluster: "gke_hub", ConnectedCluster: "prod-hub", Namespace: "control-plane", Store: StoreHosted,
				Names: []string{"STRIPE_SECRET_KEY", "DB_PASSWORD"}, Pins: []ShapeStorePin{{Name: "STRIPE_SECRET_KEY", Version: 2}},
				Immutable: []string{"DB_PASSWORD"}},
			{Cluster: "gke_daemon", ConnectedCluster: "prod-daemon", Namespace: "daemon", Store: StoreHosted, Names: []string{"DAEMON_TOKEN"}},
		},
		GeneratedSecrets: []ShapeGeneratedSecret{
			{Store: StoreHosted, Kind: "password", Names: []string{"DB_PASSWORD"}},
		},
		ExternalSecretsOperators: []ShapeExternalSecretsOperator{
			{Cluster: "gke_hub", ConnectedCluster: "prod-hub", Namespace: "external-secrets", Version: "2.13.0"},
		},
	}
}

func TestShape_StoreSecretsOmittedWhenEmpty(t *testing.T) {
	b, err := Shape{Kind: EnvLocal}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"store_secrets", "generated_secrets", "openbao", "external_secrets_operators"} {
		if strings.Contains(string(b), key) {
			t.Errorf("an empty %s must not be encoded: %s", key, b)
		}
	}
}

func TestShape_StoreSecretsRoundTrip(t *testing.T) {
	in := storeShape()
	in.OpenBao = &ShapeOpenBao{Name: "openbao", Namespace: "openbao", Cluster: "gke_hub", Image: "quay.io/openbao/openbao:2.6.2"}
	in.GeneratedSecrets = append(in.GeneratedSecrets, ShapeGeneratedSecret{Store: StoreOpenBao, Kind: "rsa_2048", Names: []string{"SIGNING_KEY", "SIGNING_PUB"}})
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"store_secrets"`, `"generated_secrets"`, `"openbao"`, `"external_secrets_operators"`,
		`"connected_cluster":"prod-hub"`, `"store":"hosted"`, `"version":2`, `"kind":"password"`} {
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
	want := in.Canonical()
	if !reflect.DeepEqual(out.StoreSecrets, want.StoreSecrets) || !reflect.DeepEqual(out.GeneratedSecrets, want.GeneratedSecrets) ||
		!reflect.DeepEqual(out.OpenBao, want.OpenBao) || !reflect.DeepEqual(out.ExternalSecretsOperators, want.ExternalSecretsOperators) {
		t.Errorf("round trip changed the store secrets:\n got %+v\nwant %+v", out, want)
	}
}

// A shape can describe where a value comes from and how to mint it, but has no
// place to hold one.
func TestShape_StoreSecretTypesCarryNoValue(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(ShapeStoreSecret{}), reflect.TypeOf(ShapeStorePin{}), reflect.TypeOf(ShapeGeneratedSecret{}),
		reflect.TypeOf(ShapeOpenBao{}), reflect.TypeOf(ShapeExternalSecretsOperator{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			switch n := strings.ToLower(typ.Field(i).Name); n {
			case "value", "values", "data", "token", "password", "secret", "key", "material":
				t.Errorf("%s has a %q field: a shape must never be able to carry a value", typ.Name(), n)
			}
		}
	}
}

// Declaration order, name order and spelled-out defaults do not change the
// bytes: two renders of one declaration hash alike.
func TestShape_StoreSecretsCanonical(t *testing.T) {
	a := storeShape()
	b := storeShape()
	b.StoreSecrets[0], b.StoreSecrets[1] = b.StoreSecrets[1], b.StoreSecrets[0]
	b.StoreSecrets[1].Names = []string{"DB_PASSWORD", "STRIPE_SECRET_KEY"}
	x, _ := a.Encode()
	y, _ := b.Encode()
	if string(x) != string(y) {
		t.Errorf("declaration order changed the encoding:\n%s\n%s", x, y)
	}

	bare := Shape{Kind: EnvLocal, GeneratedSecrets: []ShapeGeneratedSecret{{Store: StoreHosted, Kind: "random", Names: []string{"K"}}}}
	spelled := Shape{Kind: EnvLocal, GeneratedSecrets: []ShapeGeneratedSecret{{Store: StoreHosted, Kind: "random", Names: []string{"K"}, Bytes: 32, Encoding: "base64"}}}
	x, _ = bare.Encode()
	y, _ = spelled.Encode()
	if string(x) != string(y) {
		t.Errorf("spelling out the defaults changed the encoding:\n%s\n%s", x, y)
	}

	// A pair's names are in the kind's order (key, then companion) and keep it.
	pair := Shape{Kind: EnvLocal, GeneratedSecrets: []ShapeGeneratedSecret{{Store: StoreHosted, Kind: "rsa_2048", Names: []string{"Z_PRIV", "A_PUB"}}}}
	if got := pair.Canonical().GeneratedSecrets[0].Names; !reflect.DeepEqual(got, []string{"Z_PRIV", "A_PUB"}) {
		t.Errorf("canonicalizing reordered a pair: %v", got)
	}

	// Parameters that belong to another kind are not carried.
	stray := Shape{Kind: EnvLocal, GeneratedSecrets: []ShapeGeneratedSecret{{Store: StoreHosted, Kind: "ec_p256_pkcs8", Names: []string{"K"}, Bytes: 9, Length: 9, Prefix: "x"}}}
	if g := stray.Canonical().GeneratedSecrets[0]; g.Bytes != 0 || g.Length != 0 || g.Prefix != "" {
		t.Errorf("a kind carried another kind's parameters: %+v", g)
	}
}

func TestShape_GeneratedSecretSpec(t *testing.T) {
	g := ShapeGeneratedSecret{Store: StoreOpenBao, Kind: "ec_p256_selfsigned", Names: []string{"TLS_KEY", "TLS_CERT"}, CommonName: "signing.example.com"}.canonical()
	spec := g.Spec()
	if spec.Kind != secretgen.KindECP256SelfSigned || !reflect.DeepEqual(spec.Names, []string{"TLS_KEY", "TLS_CERT"}) || spec.CommonName != "signing.example.com" {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.ValidFor != secretgen.DefaultValidFor {
		t.Errorf("valid for = %v, want the default %v", spec.ValidFor, secretgen.DefaultValidFor)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("a canonical entry must be a valid spec: %v", err)
	}
	// Mutating the spec must not reach the shape.
	spec.Names[0] = "X"
	if g.Names[0] != "TLS_KEY" {
		t.Error("Spec shares its Names with the shape")
	}
}

func TestShape_StoreSecretsValidate(t *testing.T) {
	if err := storeShape().Validate(); err != nil {
		t.Fatalf("valid shape refused: %v", err)
	}
	withBao := func(s Shape) Shape {
		s.OpenBao = &ShapeOpenBao{Name: "openbao", Namespace: "openbao", Cluster: "gke_hub", Image: "img"}
		return s
	}
	mut := func(f func(*Shape)) Shape {
		s := storeShape()
		f(&s)
		return s
	}
	cases := map[string]Shape{
		"no cluster":             mut(func(s *Shape) { s.StoreSecrets[0].Cluster = "" }),
		"no namespace":           mut(func(s *Shape) { s.StoreSecrets[0].Namespace = "" }),
		"unknown store":          mut(func(s *Shape) { s.StoreSecrets[0].Store = "vault" }),
		"no names":               mut(func(s *Shape) { s.StoreSecrets[0].Names = nil }),
		"bad name":               mut(func(s *Shape) { s.StoreSecrets[0].Names = []string{"has space"} }),
		"name twice":             mut(func(s *Shape) { s.StoreSecrets[0].Names = []string{"A", "A"} }),
		"project on hosted":      mut(func(s *Shape) { s.StoreSecrets[0].Project = "acme" }),
		"gcpsm without project":  mut(func(s *Shape) { s.StoreSecrets[0].Store = StoreGCPSecretManager }),
		"binding twice":          mut(func(s *Shape) { s.StoreSecrets = append(s.StoreSecrets, s.StoreSecrets[0]) }),
		"openbao store, no bao":  mut(func(s *Shape) { s.StoreSecrets[0].Store = StoreOpenBao }),
		"pin of unread name":     mut(func(s *Shape) { s.StoreSecrets[0].Pins = []ShapeStorePin{{Name: "OTHER", Version: 1}} }),
		"pin below 1":            mut(func(s *Shape) { s.StoreSecrets[0].Pins = []ShapeStorePin{{Name: "STRIPE_SECRET_KEY", Version: 0}} }),
		"pin twice":              mut(func(s *Shape) { s.StoreSecrets[0].Pins = append(s.StoreSecrets[0].Pins, s.StoreSecrets[0].Pins[0]) }),
		"immutable of unread":    mut(func(s *Shape) { s.StoreSecrets[0].Immutable = []string{"OTHER"} }),
		"immutable pinned above": mut(func(s *Shape) { s.StoreSecrets[0].Pins = []ShapeStorePin{{Name: "DB_PASSWORD", Version: 2}} }),
		"unknown generate store": mut(func(s *Shape) { s.GeneratedSecrets[0].Store = StoreGCPSecretManager }),
		"unknown kind":           mut(func(s *Shape) { s.GeneratedSecrets[0].Kind = "uuid" }),
		"pair with one name":     mut(func(s *Shape) { s.GeneratedSecrets[0].Kind = "rsa_2048" }),
		"generated twice": mut(func(s *Shape) {
			s.GeneratedSecrets = append(s.GeneratedSecrets, ShapeGeneratedSecret{Store: StoreHosted, Kind: "random", Names: []string{"DB_PASSWORD"}})
		}),
		"generate into bao, no bao": mut(func(s *Shape) { s.GeneratedSecrets[0].Store = StoreOpenBao }),
		"eso twice on a cluster": mut(func(s *Shape) {
			s.ExternalSecretsOperators = append(s.ExternalSecretsOperators, s.ExternalSecretsOperators[0])
		}),
		"eso without version":   mut(func(s *Shape) { s.ExternalSecretsOperators[0].Version = "" }),
		"openbao without image": withBaoNoImage(storeShape()),
	}
	for name, s := range cases {
		if err := s.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}

	// The same name may be generated into two different stores, and an OpenBao
	// binding is valid once the component is declared.
	ok := withBao(mut(func(s *Shape) {
		s.StoreSecrets[0].Store = StoreOpenBao
		s.GeneratedSecrets = append(s.GeneratedSecrets, ShapeGeneratedSecret{Store: StoreOpenBao, Kind: "random", Names: []string{"DB_PASSWORD"}})
	}))
	if err := ok.Validate(); err != nil {
		t.Errorf("valid openbao shape refused: %v", err)
	}
	gcp := Shape{Kind: EnvSelfManaged, StoreSecrets: []ShapeStoreSecret{{Cluster: "c", Namespace: "n", Store: StoreGCPSecretManager, Project: "acme-prod", Names: []string{"secret-with-dashes"}}}}
	if err := gcp.Validate(); err != nil {
		t.Errorf("a GSM secret id with dashes refused: %v", err)
	}
}

func withBaoNoImage(s Shape) Shape {
	s.OpenBao = &ShapeOpenBao{Name: "openbao", Namespace: "openbao", Cluster: "gke_hub"}
	return s
}

// StoreBindings is what a control plane feeds secretaccess: it must derive a
// real document, with exact paths and no more than the shape named.
func TestShape_StoreBindingsDeriveExactPolicies(t *testing.T) {
	s := storeShape()
	s.StoreSecrets = append(s.StoreSecrets,
		// A second source in the same namespace merges into one binding.
		ShapeStoreSecret{Cluster: "gke_hub", ConnectedCluster: "prod-hub", Namespace: "control-plane", Store: StoreHosted, Names: []string{"DB_PASSWORD", "SMTP_PASSWORD"}},
		// Another store's names are not this store's.
		ShapeStoreSecret{Cluster: "gke_hub", ConnectedCluster: "prod-hub", Namespace: "control-plane", Store: StoreGCPSecretManager, Project: "acme", Names: []string{"bootstrap-seed"}},
	)
	got := s.StoreBindings(StoreHosted)
	want := []secretaccess.Binding{
		{Cluster: "prod-daemon", Namespace: "daemon", Names: []string{"DAEMON_TOKEN"}},
		{Cluster: "prod-hub", Namespace: "control-plane", Names: []string{"DB_PASSWORD", "SMTP_PASSWORD", "STRIPE_SECRET_KEY"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bindings:\n got %+v\nwant %+v", got, want)
	}
	if b := s.StoreBindings(StoreGCPSecretManager); b != nil {
		t.Errorf("a GSM store derives no policy, got %+v", b)
	}
	if b := s.StoreBindings(StoreOpenBao); len(b) != 0 {
		t.Errorf("no openbao names were declared, got %+v", b)
	}

	doc, err := secretaccess.Derive(secretaccess.Request{
		Scope: secretaccess.Scope{Org: "acme", Env: "prod"},
		Clusters: []secretaccess.Cluster{
			{ID: "prod-hub", Audience: "https://secrets.example.com/c/prod-hub"},
			{ID: "prod-daemon", Audience: "https://secrets.example.com/c/prod-daemon"},
		},
		Bindings: got,
	})
	if err != nil {
		t.Fatalf("the shape's bindings do not derive: %v", err)
	}
	if len(doc.Roles) != 2 || len(doc.Policies) != 2 {
		t.Fatalf("want a role and a policy per namespace, got %d roles, %d policies", len(doc.Roles), len(doc.Policies))
	}
	var hcl string
	for _, p := range doc.Policies {
		hcl += p.HCL()
	}
	for _, path := range []string{"secret/data/tenants/acme/prod/STRIPE_SECRET_KEY", "secret/data/tenants/acme/prod/DAEMON_TOKEN"} {
		if !strings.Contains(hcl, path) {
			t.Errorf("policy lacks %s:\n%s", path, hcl)
		}
	}
	if strings.Contains(hcl, "bootstrap-seed") || strings.Contains(hcl, "*") {
		t.Errorf("policy grants more than the shape named:\n%s", hcl)
	}
}

// Without a connected cluster the binding falls back to the kube context, so a
// self-run store's converge Job can still address it.
func TestShape_StoreBindingsFallBackToContext(t *testing.T) {
	s := Shape{Kind: EnvLocal, OpenBao: &ShapeOpenBao{Name: "b", Namespace: "b", Cluster: "k3d-dev", Image: "i"},
		StoreSecrets: []ShapeStoreSecret{{Cluster: "k3d-dev", Namespace: "app", Store: StoreOpenBao, Names: []string{"A"}}}}
	got := s.StoreBindings(StoreOpenBao)
	if len(got) != 1 || got[0].Cluster != "k3d-dev" {
		t.Errorf("bindings = %+v", got)
	}
}

func TestBundleReader_LimitShapeWithholdsStoreSecrets(t *testing.T) {
	full := storeShape()
	full.OpenBao = &ShapeOpenBao{Name: "openbao", Namespace: "openbao", Cluster: "gke_hub", Image: "img"}
	old := (&BundleReader{}).LimitShape(full)
	if old.StoreSecrets != nil || old.GeneratedSecrets != nil || old.OpenBao != nil || old.ExternalSecretsOperators != nil {
		t.Errorf("an old reader's shape = %+v", old)
	}
	if full.StoreSecrets == nil || full.OpenBao == nil {
		t.Error("LimitShape modified its argument")
	}
	if got := (&BundleReader{Features: BundleDocFeatures()}).LimitShape(full); !reflect.DeepEqual(got, full) {
		t.Error("a reader of every feature must get the shape whole")
	}
	if got := (*BundleReader)(nil).LimitShape(full); !reflect.DeepEqual(got, full) {
		t.Error("the nil reader must get the shape whole")
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"store_secrets", "generated_secrets", "openbao", "external_secrets_operators"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("a limited shape still carries %q, even empty; an empty key is still an unknown key", key)
		}
	}
}

// Store-secret fields are part of the shape's identity: changing a pin moves
// the encoding, so a pin bump is a visible change.
func TestShape_PinBumpChangesEncoding(t *testing.T) {
	a, b := storeShape(), storeShape()
	b.StoreSecrets[0].Pins[0].Version = 3
	x, _ := a.Encode()
	y, _ := b.Encode()
	if string(x) == string(y) {
		t.Error("a pin bump did not change the encoding")
	}
}
