package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// renderFixture is the `output` of a Bundle that declares a hosted store, an
// OpenBao, and two namespaces: the JSON the populate function reads, by the
// render's own field names.
const renderFixture = `{
  "required_secrets": [
    {"type": "secret", "name": "reliant-db-role", "namespace": "app", "keys": ["password", "proxy-database-url"],
     "source": {"store": {"type": "hosted"},
       "data": {"password": "RELIANT_DB_ROLE_PASSWORD"},
       "templates": {"proxy-database-url": "postgres://reliant:{{ .password | urlquery }}@127.0.0.1/db"},
       "generate": {"RELIANT_DB_ROLE_PASSWORD": {"kind": "password", "bytes": 32, "encoding": "base64", "length": 24}},
       "pin": {"RELIANT_DB_ROLE_PASSWORD": 1},
       "immutable": ["RELIANT_DB_ROLE_PASSWORD"]}},
    {"type": "secret", "name": "stripe", "namespace": "app", "keys": ["key"],
     "source": {"store": {"type": "hosted"}, "data": {"key": "STRIPE_SECRET_KEY"}, "pin": {"STRIPE_SECRET_KEY": 3}}},
    {"type": "secret", "name": "signing", "namespace": "auth", "keys": ["private", "public"],
     "source": {"store": {"type": "openbao"}, "data": {"private": "SIGNING_KEY", "public": "SIGNING_PUB"},
       "generate": {"SIGNING_KEY": {"kind": "rsa_2048", "bytes": 32, "encoding": "base64", "companion": "SIGNING_PUB"}}}},
    {"type": "secret", "name": "seed", "namespace": "auth", "keys": ["seed"],
     "source": {"store": {"type": "gcpsm", "project": "acme-prod"}, "data": {"seed": "bootstrap-seed"}}},
    {"type": "secret", "name": "cloudflare", "namespace": "cert-manager", "keys": ["api-token"], "reason": "dns-01"}
  ],
  "openbao": {"name": "openbao", "namespace": "openbao", "image": "quay.io/openbao/openbao:2.6.2@sha256:aa", "cluster": "gke_hub", "connected_cluster": "prod-hub"},
  "external_secrets_operators": [
    {"cluster": "gke_daemon", "connected_cluster": "prod-daemon", "namespace": "external-secrets", "version": "2.13.0"},
    {"cluster": "gke_hub", "connected_cluster": "prod-hub", "namespace": "external-secrets", "version": "2.13.0"}
  ]
}`

var (
	hub    = []StoreCluster{{Cluster: "gke_hub", ConnectedCluster: "prod-hub"}}
	daemon = StoreCluster{Cluster: "gke_daemon", ConnectedCluster: "prod-daemon"}
)

func fixtureRender(t *testing.T) StoreRender {
	t.Helper()
	var raw struct {
		Secrets []RenderedStoreSecret             `json:"required_secrets"`
		OpenBao *RenderedOpenBao                  `json:"openbao"`
		ESO     []RenderedExternalSecretsOperator `json:"external_secrets_operators"`
	}
	if err := json.Unmarshal([]byte(renderFixture), &raw); err != nil {
		t.Fatal(err)
	}
	// The deploy router's answer: which clusters each namespace's Secret lands on.
	clusters := map[string][]StoreCluster{"app": append(append([]StoreCluster{}, hub...), daemon), "auth": hub, "cert-manager": hub}
	for i := range raw.Secrets {
		raw.Secrets[i].Clusters = clusters[raw.Secrets[i].Namespace]
	}
	return StoreRender{Secrets: raw.Secrets, OpenBao: raw.OpenBao, ExternalSecretsOperators: raw.ESO}
}

func TestProjectStoreSecrets_FromRender(t *testing.T) {
	p, err := ProjectStoreSecrets(fixtureRender(t))
	if err != nil {
		t.Fatal(err)
	}

	wantBindings := []ShapeStoreSecret{
		{Cluster: "gke_daemon", ConnectedCluster: "prod-daemon", Namespace: "app", Store: StoreHosted,
			Names: []string{"RELIANT_DB_ROLE_PASSWORD", "STRIPE_SECRET_KEY"},
			Pins:  []ShapeStorePin{{Name: "RELIANT_DB_ROLE_PASSWORD", Version: 1}, {Name: "STRIPE_SECRET_KEY", Version: 3}}, Immutable: []string{"RELIANT_DB_ROLE_PASSWORD"}},
		{Cluster: "gke_hub", ConnectedCluster: "prod-hub", Namespace: "app", Store: StoreHosted,
			Names: []string{"RELIANT_DB_ROLE_PASSWORD", "STRIPE_SECRET_KEY"},
			Pins:  []ShapeStorePin{{Name: "RELIANT_DB_ROLE_PASSWORD", Version: 1}, {Name: "STRIPE_SECRET_KEY", Version: 3}}, Immutable: []string{"RELIANT_DB_ROLE_PASSWORD"}},
		{Cluster: "gke_hub", ConnectedCluster: "prod-hub", Namespace: "auth", Store: StoreGCPSecretManager, Project: "acme-prod", Names: []string{"bootstrap-seed"}},
		{Cluster: "gke_hub", ConnectedCluster: "prod-hub", Namespace: "auth", Store: StoreOpenBao, Names: []string{"SIGNING_KEY", "SIGNING_PUB"}},
	}
	if !reflect.DeepEqual(p.StoreSecrets, wantBindings) {
		t.Errorf("store secrets:\n got %+v\nwant %+v", p.StoreSecrets, wantBindings)
	}

	wantGen := []ShapeGeneratedSecret{
		{Store: StoreHosted, Kind: "password", Names: []string{"RELIANT_DB_ROLE_PASSWORD"}, Length: 24, Symbols: "!-_."},
		{Store: StoreOpenBao, Kind: "rsa_2048", Names: []string{"SIGNING_KEY", "SIGNING_PUB"}},
	}
	if !reflect.DeepEqual(p.GeneratedSecrets, wantGen) {
		t.Errorf("generated secrets:\n got %+v\nwant %+v", p.GeneratedSecrets, wantGen)
	}

	if p.OpenBao == nil || p.OpenBao.Cluster != "gke_hub" || p.OpenBao.Image == "" {
		t.Errorf("openbao = %+v", p.OpenBao)
	}
	if len(p.ExternalSecretsOperators) != 2 || p.ExternalSecretsOperators[0].Cluster != "gke_daemon" {
		t.Errorf("external secrets operators = %+v", p.ExternalSecretsOperators)
	}

	shape := Shape{Kind: EnvSelfManaged}.WithStoreSecrets(p)
	if err := shape.Validate(); err != nil {
		t.Fatalf("the projection of a valid render is not a valid shape: %v", err)
	}
	// The out-of-band Secret contributes nothing, and no generated value or
	// template leaks into the encoding.
	b, _ := shape.Encode()
	for _, leak := range []string{"cloudflare", "api-token", "urlquery", "postgres://"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("the shape carries %q: %s", leak, b)
		}
	}
}

// Two runs over the same declaration, in any record order, give one shape.
func TestProjectStoreSecrets_Deterministic(t *testing.T) {
	in := fixtureRender(t)
	want, err := ProjectStoreSecrets(in)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, _ := Shape{Kind: EnvSelfManaged}.WithStoreSecrets(want).Encode()

	rev := fixtureRender(t)
	for i, j := 0, len(rev.Secrets)-1; i < j; i, j = i+1, j-1 {
		rev.Secrets[i], rev.Secrets[j] = rev.Secrets[j], rev.Secrets[i]
	}
	for i, j := 0, len(rev.ExternalSecretsOperators)-1; i < j; i, j = i+1, j-1 {
		rev.ExternalSecretsOperators[i], rev.ExternalSecretsOperators[j] = rev.ExternalSecretsOperators[j], rev.ExternalSecretsOperators[i]
	}
	for i := range rev.Secrets {
		c := rev.Secrets[i].Clusters
		for a, b := 0, len(c)-1; a < b; a, b = a+1, b-1 {
			c[a], c[b] = c[b], c[a]
		}
	}
	got, err := ProjectStoreSecrets(rev)
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, _ := Shape{Kind: EnvSelfManaged}.WithStoreSecrets(got).Encode()
	if string(gotBytes) != string(wantBytes) {
		t.Errorf("record order changed the shape:\n%s\n%s", gotBytes, wantBytes)
	}
}

// A render with nothing store-backed projects to nothing, so a shape that
// predates the feature is unchanged.
func TestProjectStoreSecrets_NothingDeclared(t *testing.T) {
	p, err := ProjectStoreSecrets(StoreRender{Secrets: []RenderedStoreSecret{{Name: "cf", Namespace: "cert-manager"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, StoreProjection{}) {
		t.Errorf("projection = %+v, want none", p)
	}
	before, _ := Shape{Kind: EnvLocal}.Encode()
	after, _ := Shape{Kind: EnvLocal}.WithStoreSecrets(p).Encode()
	if string(before) != string(after) {
		t.Errorf("an empty projection changed the encoding:\n%s\n%s", before, after)
	}
}

// The same generation declared by two sources is one entry; a conflicting
// one is refused, because the first writer would win and the other source would
// read a value it did not ask for.
func TestProjectStoreSecrets_SharedGeneration(t *testing.T) {
	src := func(g RenderedGeneratedSecret) *RenderedStoreSource {
		return &RenderedStoreSource{Store: RenderedStore{Type: "hosted"}, Data: map[string]string{"k": "SHARED"}, Generate: map[string]RenderedGeneratedSecret{"SHARED": g}}
	}
	same := RenderedGeneratedSecret{Kind: "random"}
	p, err := ProjectStoreSecrets(StoreRender{Secrets: []RenderedStoreSecret{
		{Name: "a", Namespace: "x", Source: src(same), Clusters: hub},
		{Name: "b", Namespace: "y", Source: src(RenderedGeneratedSecret{Kind: "random", Bytes: 32, Encoding: "base64"}), Clusters: hub},
	}})
	if err != nil {
		t.Fatalf("one generation spelled two ways was refused: %v", err)
	}
	if len(p.GeneratedSecrets) != 1 {
		t.Errorf("generated = %+v, want one entry", p.GeneratedSecrets)
	}

	_, err = ProjectStoreSecrets(StoreRender{Secrets: []RenderedStoreSecret{
		{Name: "a", Namespace: "x", Source: src(same), Clusters: hub},
		{Name: "b", Namespace: "y", Source: src(RenderedGeneratedSecret{Kind: "random", Bytes: 16}), Clusters: hub},
	}})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("conflicting generations: want ErrInvalid, got %v", err)
	}
}

func TestProjectStoreSecrets_Refusals(t *testing.T) {
	hosted := func(mut func(*RenderedStoreSource)) RenderedStoreSecret {
		s := &RenderedStoreSource{Store: RenderedStore{Type: "hosted"}, Data: map[string]string{"k": "NAME"}}
		if mut != nil {
			mut(s)
		}
		return RenderedStoreSecret{Name: "s", Namespace: "app", Source: s, Clusters: hub}
	}
	cases := map[string]StoreRender{
		"reaches no cluster": {Secrets: []RenderedStoreSecret{func() RenderedStoreSecret { s := hosted(nil); s.Clusters = nil; return s }()}},
		"unknown store":      {Secrets: []RenderedStoreSecret{hosted(func(s *RenderedStoreSource) { s.Store.Type = "vault" })}},
		"generate into gsm": {Secrets: []RenderedStoreSecret{hosted(func(s *RenderedStoreSource) {
			s.Store = RenderedStore{Type: "gcpsm", Project: "acme-prod"}
			s.Generate = map[string]RenderedGeneratedSecret{"NAME": {Kind: "random"}}
		})}},
		"unknown kind": {Secrets: []RenderedStoreSecret{hosted(func(s *RenderedStoreSource) { s.Generate = map[string]RenderedGeneratedSecret{"NAME": {Kind: "uuid"}} })}},
		"pair without companion": {Secrets: []RenderedStoreSecret{hosted(func(s *RenderedStoreSource) {
			s.Generate = map[string]RenderedGeneratedSecret{"NAME": {Kind: "rsa_2048"}}
		})}},
		"two pins for one name": {Secrets: []RenderedStoreSecret{
			hosted(func(s *RenderedStoreSource) { s.Pin = map[string]int{"NAME": 1} }),
			hosted(func(s *RenderedStoreSource) { s.Pin = map[string]int{"NAME": 2} }),
		}},
		"one namespace, two connected names": {Secrets: []RenderedStoreSecret{
			hosted(nil),
			func() RenderedStoreSecret {
				s := hosted(nil)
				s.Clusters = []StoreCluster{{Cluster: "gke_hub", ConnectedCluster: "other"}}
				return s
			}(),
		}},
	}
	for name, in := range cases {
		if _, err := ProjectStoreSecrets(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

// A pin or immutable mark on a name the source does not read is dropped, not
// attributed to the binding: the schema refuses it, and the shape would too.
func TestProjectStoreSecrets_IgnoresUnreadPins(t *testing.T) {
	in := StoreRender{Secrets: []RenderedStoreSecret{{
		Name: "s", Namespace: "app", Clusters: hub,
		Source: &RenderedStoreSource{Store: RenderedStore{Type: "hosted"}, Data: map[string]string{"k": "A"}, Pin: map[string]int{"A": 2, "COMPANION": 1}, Immutable: []string{"COMPANION"}},
	}}}
	p, err := ProjectStoreSecrets(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Shape{Kind: EnvSelfManaged}).WithStoreSecrets(p).Validate(); err != nil {
		t.Errorf("projection invalid: %v", err)
	}
	if got := p.StoreSecrets[0]; len(got.Pins) != 1 || got.Pins[0].Name != "A" || len(got.Immutable) != 0 {
		t.Errorf("binding = %+v", got)
	}
}
