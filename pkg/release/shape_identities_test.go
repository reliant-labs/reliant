package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// usersIdentity is a second hosted identity, "users", delivered to the same
// namespace as hostedIdentity under its own Secret.
func usersIdentity() ShapeIdentity {
	return ShapeIdentity{
		Name: "users", Runtime: IdentityRuntimeHosted, ProjectName: "acme-app",
		Login:        ShapeIdentityLogin{Mode: IdentityLoginNative},
		Roles:        []ShapeIdentityRole{{Key: "member", MembersFrom: IdentityMembersConsole}},
		Applications: []ShapeIdentityApplication{{Name: "app", Workload: "app-api", Kind: IdentityAppNativeBroker}},
		Consumers:    []ShapeIdentityConsumer{{Workload: "app-api", Application: "app"}},
		Deliveries:   []ShapeIdentityDelivery{usersDelivery()},
	}
}

func usersDelivery() ShapeIdentityDelivery {
	d := idCluster()
	d.Name = IdentitySecretPrefix + "users"
	return d
}

func usersSecret() ShapeControlPlaneSecret {
	c := idSecret()
	c.Name = usersDelivery().Name
	return c
}

// twoIdentities is a shape declaring the operators' identity and the users'.
func twoIdentities() Shape {
	return Shape{
		Kind:                EnvSelfManaged,
		Identities:          []ShapeIdentity{usersIdentity(), *hostedIdentity()},
		ControlPlaneSecrets: []ShapeControlPlaneSecret{idSecret(), usersSecret()},
	}
}

func topLevelKeys(t *testing.T, s Shape) map[string]json.RawMessage {
	t.Helper()
	b, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

// ONE identity is `identity` and nothing else, whatever its name: it encodes
// (and hashes) exactly as a shape recorded before Identities existed, and
// every control plane keeps converging it. SEVERAL are `identities` alone.
func TestShape_IdentitiesKeys(t *testing.T) {
	for _, name := range []string{DefaultIdentityName, "ops"} {
		id := hostedIdentity()
		id.Name = name
		keys := topLevelKeys(t, shapeWithIdentity(id))
		if _, ok := keys["identity"]; !ok {
			t.Errorf("one identity named %q: no `identity` key", name)
		}
		if _, ok := keys["identities"]; ok {
			t.Errorf("one identity named %q also emits `identities`, which changes the bytes of every recorded shape and an older strict decoder refuses", name)
		}
	}
	keys := topLevelKeys(t, twoIdentities())
	if _, ok := keys["identity"]; ok {
		t.Error("two identities also emit `identity`: an older control plane would converge one of them and drop the other")
	}
	if _, ok := keys["identities"]; !ok {
		t.Error("two identities: no `identities` key")
	}
}

// Several identities round-trip through the canonical encoding, sorted by
// name, each canonical.
func TestShape_IdentitiesRoundTrip(t *testing.T) {
	s := twoIdentities()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var got Shape
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("a round-tripped shape is invalid: %v", err)
	}
	if names := []string{got.Identities[0].Name, got.Identities[1].Name}; !reflect.DeepEqual(names, []string{"identity", "users"}) {
		t.Errorf("identities = %v, want sorted by name", names)
	}
	if !reflect.DeepEqual(got.Identities, s.Canonical().Identities) {
		t.Errorf("round trip changed the identities:\n got %+v\nwant %+v", got.Identities, s.Canonical().Identities)
	}
	again, _ := got.Encode()
	if string(again) != string(b) {
		t.Error("re-encoding a decoded shape changed its bytes")
	}
}

func TestShape_AllIdentities(t *testing.T) {
	if got := (Shape{}).AllIdentities(); got != nil {
		t.Errorf("no identity: %v", got)
	}
	one := shapeWithIdentity(hostedIdentity())
	if got := one.AllIdentities(); len(got) != 1 || got[0].Name != "identity" {
		t.Errorf("one: %+v", got)
	}
	two := twoIdentities()
	if got := two.AllIdentities(); len(got) != 2 {
		t.Errorf("two: %+v", got)
	}
	if u, ok := two.IdentityNamed("users"); !ok || u.ProjectName != "acme-app" {
		t.Errorf("IdentityNamed(users) = %+v, %v", u, ok)
	}
	if _, ok := two.IdentityNamed("staff"); ok {
		t.Error("IdentityNamed found an identity the shape does not declare")
	}
}

func TestShape_IdentitiesValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*Shape)
		want   string
	}{
		"duplicate name": {func(s *Shape) {
			u := usersIdentity()
			u.Deliveries = nil
			s.Identities = append(s.Identities, u)
		}, `declares identity "users" twice`},
		"bad name": {func(s *Shape) {
			s.Identities[0].Name = "9users"
			s.Identities[0].Deliveries = nil
		}, `identities name "9users" must match`},
		"one managed-secret namespace, two identities": {func(s *Shape) {
			u := usersIdentity()
			u.Name, u.Deliveries = "a-b", nil
			v := usersIdentity()
			v.Name, v.Deliveries = "a--b", nil
			s.Identities = []ShapeIdentity{u, v}
		}, `identities "a-b" and "a--b" both name their hosted managed secrets RELIANT_IDENTITY_A_B_*`},
		"one Secret, two identities": {func(s *Shape) {
			s.Identities[0].Deliveries = []ShapeIdentityDelivery{idCluster()}
		}, `both deliver gke_acme_prod/acme/forge-identity-identity`},
		"both keys, different": {func(s *Shape) {
			id := hostedIdentity()
			id.ProjectName = "other"
			s.Identity = id
		}, "sets identity \"identity\" and identities"},
		"each validates": {func(s *Shape) {
			s.Identities[0].Runtime = "somewhere"
		}, `shape identity "users": runtime "somewhere"`},
	} {
		t.Run(name, func(t *testing.T) {
			s := twoIdentities()
			tc.mutate(&s)
			err := s.Validate()
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
	// Both keys are accepted when they say the same thing.
	s := twoIdentities()
	s.Identity = hostedIdentity()
	if err := s.Validate(); err != nil {
		t.Errorf("identity mirrored in identities: %v", err)
	}
	// An identity-kind Secret with no identity at all is still refused.
	none := Shape{Kind: EnvSelfManaged, ControlPlaneSecrets: []ShapeControlPlaneSecret{idSecret()}}
	if err := none.Validate(); err == nil || !strings.Contains(err.Error(), "declares no identity") {
		t.Errorf("identity Secret with no identity: %v", err)
	}
}

// A shape with several identities is refused to a reader that does not
// converge them, before anything is written; one identity never is.
func TestBundleReader_CheckIdentities(t *testing.T) {
	two := twoIdentities()
	for name, r := range map[string]*BundleReader{
		"no features":           {},
		"decodes, not converge": {Features: BundleDocFeatures()},
	} {
		err := r.CheckIdentities(two)
		if err == nil || !strings.Contains(err.Error(), "declares 2 managed identities (users, identity)") || !strings.Contains(err.Error(), CapabilityConvergesSeveralIdentities) {
			t.Errorf("%s: CheckIdentities = %v", name, err)
		}
	}
	for name, r := range map[string]*BundleReader{
		"converges": {Features: append(BundleDocFeatures(), CapabilityConvergesSeveralIdentities)},
		"nil":       nil,
	} {
		if err := r.CheckIdentities(two); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (&BundleReader{}).CheckIdentities(shapeWithIdentity(hostedIdentity())); err != nil {
		t.Errorf("one identity refused to an old reader: %v", err)
	}
}

// The capability is hand-advertised, never implied by a pin bump.
func TestCapabilityConvergesSeveralIdentitiesIsNotAFeature(t *testing.T) {
	for _, f := range BundleDocFeatures() {
		if f == CapabilityConvergesSeveralIdentities {
			t.Fatal("BundleDocFeatures advertises the convergence capability: every control plane that bumps its forge pin would claim to converge several identities")
		}
	}
}

// LimitShape applies the sign-in limits to every identity, and strips the
// identities key for a reader that predates it (which CheckIdentities refused
// first).
func TestLimitShape_Identities(t *testing.T) {
	two := twoIdentities()
	two.Identities[0].Owners, two.Identities[0].OwnerRole = []string{"a@acme.test"}, "member"
	two.Identities[1].CodeLifetime = "24h0m0s"
	noSignIn := (&BundleReader{Features: []string{BundleFeatureIdentities}}).LimitShape(two)
	for _, id := range noSignIn.Identities {
		if id.Owners != nil || id.CodeLifetime != "" {
			t.Errorf("identity %q kept a part the reader does not decode: %+v", id.Name, id)
		}
	}
	if two.Identities[0].Owners == nil || two.Identities[1].CodeLifetime == "" {
		t.Error("LimitShape modified the shape it was given")
	}
	if got := (&BundleReader{}).LimitShape(two); got.Identities != nil {
		t.Errorf("a reader without %s was sent identities", BundleFeatureIdentities)
	}
	if got := (&BundleReader{Features: append(BundleDocFeatures(), CapabilityConvergesSeveralIdentities)}).LimitShape(two); !reflect.DeepEqual(got, two) {
		t.Error("a reader of every feature did not get the shape whole")
	}
	if !two.HasOptionalParts() {
		t.Error("a shape with several identities reports no optional parts, so the environment declaration would not ask the control plane what it reads")
	}
}

func TestIdentitySlugFor(t *testing.T) {
	const envID = "3b1f0c2e-8a7d-4e55-9a10-6c2d9e4b7f01"
	if got, want := IdentitySlugFor(envID, "Acme Corp", "prod", DefaultIdentityName, 6), IdentitySlug(envID, "Acme Corp", "prod", 6); got != want {
		t.Errorf("the default identity's slug = %q, want IdentitySlug's %q: its issuer must not move", got, want)
	}
	if got, want := IdentitySlugFor(envID, "Acme Corp", "prod", "", 6), IdentitySlug(envID, "Acme Corp", "prod", 6); got != want {
		t.Errorf("an unnamed identity's slug = %q, want %q", got, want)
	}
	hash := IdentitySlug(envID, "a", "b", IdentitySlugMaxHash)[len("a-b-"):]
	users := IdentitySlugFor(envID, "Acme Corp", "prod", "users", 6)
	if users != "acme-corp-prod-users-"+hash[:6] {
		t.Errorf("IdentitySlugFor(users) = %q", users)
	}
	if users == IdentitySlugFor(envID, "Acme Corp", "prod", "staff", 6) {
		t.Error("two names of one env share a slug")
	}
	if longer := IdentitySlugFor(envID, "Acme Corp", "prod", "users", 9); !strings.HasPrefix(longer, users) || longer != users+hash[6:9] {
		t.Errorf("a retry with a longer hash = %q, want %q extended", longer, users)
	}
	// The longest inputs still make a DNS label, name and hash intact.
	long := strings.Repeat("p", 40)
	name := "a" + strings.Repeat("b", 23)
	s := IdentitySlugFor(envID, long, strings.Repeat("e", 40), name, IdentitySlugMaxHash)
	if !ValidIdentitySlug(s) || len(s) > 63 || !strings.Contains(s, "-"+name+"-") || !strings.HasSuffix(s, hash) {
		t.Errorf("longest slug %q (%d characters) is not a DNS label carrying the name and the hash", s, len(s))
	}
	for _, in := range [][3]string{{"", "", "x"}, {"---", "Prod!", "ops"}, {"a", "b", strings.Repeat("n", 40)}} {
		if s := IdentitySlugFor(envID, in[0], in[1], in[2], IdentitySlugMaxHash); !ValidIdentitySlug(s) {
			t.Errorf("IdentitySlugFor%v = %q, not a valid slug", in, s)
		}
	}
}
