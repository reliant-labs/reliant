package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func idCluster() ShapeIdentityDelivery {
	return ShapeIdentityDelivery{
		Kind: ControlPlaneSecretIdentity, Name: "forge-identity-identity", Namespace: "acme",
		Cluster: "gke_acme_prod", ConnectedCluster: "acme-prod",
	}
}

// hostedIdentity is a fully populated hosted declaration, in non-canonical
// order throughout.
func hostedIdentity() *ShapeIdentity {
	return &ShapeIdentity{
		Name: "identity", Runtime: IdentityRuntimeHosted, ProjectName: "acme",
		Login: ShapeIdentityLogin{Mode: IdentityLoginNative},
		Roles: []ShapeIdentityRole{
			{Key: "member", MembersFrom: IdentityMembersConsole},
			{Key: "admin", DisplayName: "Administrators", MembersFrom: IdentityMembersDeclared, Members: []string{"b@acme.test", "a@acme.test"}},
		},
		Applications: []ShapeIdentityApplication{
			{Name: "prod:web", Workload: "web", Kind: IdentityAppOIDCWeb, RedirectURIs: []string{"https://b/cb", "https://a/cb"}},
			{Name: "prod:api", Workload: "api", Kind: IdentityAppNativeBroker},
		},
		Consumers: []ShapeIdentityConsumer{
			{Workload: "worker"},
			{Workload: "api", Application: "prod:api", Fields: map[string]string{"issuer": "zitadel_issuer"}},
		},
		MachineUsers: []ShapeIdentityMachineUser{{
			Name: "ops", Roles: []string{"admin"},
			KeySecret: &ShapeIdentityKeySecret{Name: "acme-ops-key", Namespace: "acme", Cluster: "gke_acme_prod", ConnectedCluster: "acme-prod"},
		}},
		SMTP:           &ShapeIdentitySMTP{Host: "smtp.acme.test", Port: 587, User: "postmaster", Sender: "no-reply@acme.test", PasswordSecret: "SMTP_PASSWORD"},
		Deliveries:     []ShapeIdentityDelivery{idCluster()},
		ManagedSecrets: []string{"RELIANT_IDENTITY_IDENTITY_ISSUER", "RELIANT_IDENTITY_IDENTITY_AUDIENCE"},
	}
}

// the control-plane secret the delivery above corresponds to.
func idSecret() ShapeControlPlaneSecret {
	d := idCluster()
	return ShapeControlPlaneSecret{
		Name: d.Name, Namespace: d.Namespace, Cluster: d.Cluster, ConnectedCluster: d.ConnectedCluster,
		Kind: ControlPlaneSecretIdentity, Keys: []string{"ISSUER", "AUDIENCE"}, Required: true,
	}
}

func shapeWithIdentity(id *ShapeIdentity) Shape {
	s := Shape{Kind: EnvSelfManaged, Identity: id}
	if id != nil {
		s.ControlPlaneSecrets = []ShapeControlPlaneSecret{idSecret()}
	}
	return s
}

// A shape that declares no identity encodes without the field.
func TestShape_IdentityOmittedWhenAbsent(t *testing.T) {
	b, err := Shape{Kind: EnvLocal}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "identity") {
		t.Errorf("an absent identity must not be encoded: %s", b)
	}
}

// HASH STABILITY. The bytes below were produced by the encoder BEFORE the
// identity field existed, for a shape that uses every other optional field.
// They must not move: a shape recorded last month has to hash and diff the
// same today, or every env reads as changed on the next ensure.
func TestShape_IdentityFieldLeavesExistingShapesByteIdentical(t *testing.T) {
	const pinned = `{"kind":"self_managed","workloads":[{"name":"api","runtime":"cluster","cluster":"ctx","artifact":"api","replicas":2}],"secrets":[{"name":"DB","provider":"external","declared_by":["api"]}],"domains":["a.test"],"clusters":["ctx"],"objects":[],"control_plane_secrets":[{"name":"t","namespace":"n","cluster":"ctx","connected_cluster":"c","kind":"telemetry_token","keys":["token"]}],"observability_policy":{"enabled":true,"disabled":["logs"]},"applier":"forge"}`
	rep := 2
	s := Shape{
		Kind:                EnvSelfManaged,
		Workloads:           []ShapeWorkload{{Name: "api", Runtime: "cluster", Cluster: "ctx", Artifact: "api", Replicas: &rep}},
		Secrets:             []ShapeSecret{{Name: "DB", Provider: "external", DeclaredBy: []string{"api"}}},
		Domains:             []string{"a.test"},
		Clusters:            []string{"ctx"},
		ControlPlaneSecrets: []ShapeControlPlaneSecret{{Name: "t", Namespace: "n", Cluster: "ctx", ConnectedCluster: "c", Kind: ControlPlaneSecretTelemetryToken, Keys: []string{"token"}}},
		ObservabilityPolicy: &ShapeObservabilityPolicy{Enabled: true, Disabled: []string{"logs"}},
		Applier:             ApplierForge,
	}
	b, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != pinned {
		t.Errorf("the encoding of a shape without an identity moved:\n got %s\nwant %s", b, pinned)
	}
	sum := sha256.Sum256(b)
	if got, want := hex.EncodeToString(sum[:]), hex.EncodeToString(func() []byte { h := sha256.Sum256([]byte(pinned)); return h[:] }()); got != want {
		t.Errorf("hash moved: %s != %s", got, want)
	}
	// And Required, which this change also added, is invisible when false.
	if strings.Contains(string(b), `"required"`) {
		t.Errorf("an optional control-plane secret must not encode required: %s", b)
	}
}

func TestShape_IdentityRoundTrip(t *testing.T) {
	in := shapeWithIdentity(hostedIdentity())
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"identity":{"name":"identity","runtime":"hosted"`, `"project_name":"acme"`, `"login":{"mode":"native"}`,
		`"members_from":"declared"`, `"kind":"native_broker"`, `"password_secret":"SMTP_PASSWORD"`,
		`"kind":"identity"`, `"required":true`, `"managed_secrets":["RELIANT_IDENTITY_IDENTITY_AUDIENCE"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("encoded shape lacks %s:\n%s", want, b)
		}
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("a round-tripped shape is invalid: %v", err)
	}
	if want := in.Canonical(); !reflect.DeepEqual(out.Identity, want.Identity) {
		t.Errorf("round trip changed the identity:\n got %+v\nwant %+v", out.Identity, want.Identity)
	}
	again, err := out.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(b) {
		t.Errorf("encode(decode(encode(x))) != encode(x):\n%s\n%s", again, b)
	}
}

// Two projections of one declaration, listed in different orders, are one
// shape: same bytes, same digest.
func TestShape_IdentityCanonicalisesLists(t *testing.T) {
	a := hostedIdentity()
	b := hostedIdentity()
	reverse := func(n int, swap func(i, j int)) {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			swap(i, j)
		}
	}
	reverse(len(b.Roles), func(i, j int) { b.Roles[i], b.Roles[j] = b.Roles[j], b.Roles[i] })
	reverse(len(b.Applications), func(i, j int) { b.Applications[i], b.Applications[j] = b.Applications[j], b.Applications[i] })
	reverse(len(b.Consumers), func(i, j int) { b.Consumers[i], b.Consumers[j] = b.Consumers[j], b.Consumers[i] })
	reverse(len(b.ManagedSecrets), func(i, j int) { b.ManagedSecrets[i], b.ManagedSecrets[j] = b.ManagedSecrets[j], b.ManagedSecrets[i] })
	reverse(len(b.Roles[0].Members), func(i, j int) {
		b.Roles[0].Members[i], b.Roles[0].Members[j] = b.Roles[0].Members[j], b.Roles[0].Members[i]
	})

	ea, err := shapeWithIdentity(a).Encode()
	if err != nil {
		t.Fatal(err)
	}
	eb, err := shapeWithIdentity(b).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(ea) != string(eb) {
		t.Errorf("list order changed the encoding:\n%s\n%s", ea, eb)
	}
	got := shapeWithIdentity(a).Canonical().Identity
	if got.Roles[0].Key != "admin" || got.Applications[0].Name != "prod:api" || got.Consumers[0].Workload != "api" {
		t.Errorf("lists are not sorted: %+v", got)
	}
	// An empty list is nil, so "no machine users" is one encoding.
	empty := &ShapeIdentity{Name: "identity", Runtime: IdentityRuntimeHosted, Login: ShapeIdentityLogin{Mode: IdentityLoginNative}, Roles: []ShapeIdentityRole{}}
	e1, _ := Shape{Kind: EnvLocal, Identity: empty}.Encode()
	e2, _ := Shape{Kind: EnvLocal, Identity: &ShapeIdentity{Name: "identity", Runtime: IdentityRuntimeHosted, Login: ShapeIdentityLogin{Mode: IdentityLoginNative}}}.Encode()
	if string(e1) != string(e2) {
		t.Errorf("empty and nil lists encode differently:\n%s\n%s", e1, e2)
	}
}

func TestShape_IdentityValidate(t *testing.T) {
	if err := shapeWithIdentity(hostedIdentity()).Canonical().Validate(); err != nil {
		t.Fatalf("the reference declaration is valid: %v", err)
	}
	host := func() *ShapeIdentity {
		return &ShapeIdentity{
			Name: "identity", Runtime: IdentityRuntimeHost, Login: ShapeIdentityLogin{Mode: IdentityLoginNative},
			Roles:     []ShapeIdentityRole{{Key: "admin", MembersFrom: IdentityMembersConsole}},
			SeedUsers: []ShapeIdentitySeedUser{{Email: "dev@acme.test", Roles: []string{"admin"}, PasswordSecret: "DEV_PASSWORD"}},
		}
	}
	if err := (Shape{Kind: EnvLocal, Identity: host()}).Canonical().Validate(); err != nil {
		t.Fatalf("a host-run declaration is valid: %v", err)
	}

	cases := map[string]func(i *ShapeIdentity){
		"bad name":                                  func(i *ShapeIdentity) { i.Name = "Identity!" },
		"unknown runtime":                           func(i *ShapeIdentity) { i.Runtime = "moon" },
		"unknown login mode":                        func(i *ShapeIdentity) { i.Login.Mode = "magic" },
		"custom domain not public":                  func(i *ShapeIdentity) { i.Domain = "login.acme.test" },
		"domain not a name":                         func(i *ShapeIdentity) { i.Domain = "no spaces"; i.Public = true },
		"bad role key":                              func(i *ShapeIdentity) { i.Roles[0].Key = "Admin" },
		"duplicate role":                            func(i *ShapeIdentity) { i.Roles[1].Key = i.Roles[0].Key },
		"members on a console role":                 func(i *ShapeIdentity) { i.Roles[0].Members = []string{"a@acme.test"} },
		"member not an email":                       func(i *ShapeIdentity) { i.Roles[1].Members = []string{"nobody"} },
		"member twice":                              func(i *ShapeIdentity) { i.Roles[1].Members = []string{"a@acme.test", "a@acme.test"} },
		"unknown members_from":                      func(i *ShapeIdentity) { i.Roles[0].MembersFrom = "wishful" },
		"unknown app kind":                          func(i *ShapeIdentity) { i.Applications[0].Kind = "spa" },
		"duplicate app":                             func(i *ShapeIdentity) { i.Applications[1].Name = i.Applications[0].Name },
		"app without workload":                      func(i *ShapeIdentity) { i.Applications[0].Workload = "" },
		"consumer of unknown app":                   func(i *ShapeIdentity) { i.Consumers[1].Application = "prod:nope" },
		"consumer maps unknown output":              func(i *ShapeIdentity) { i.Consumers[1].Fields = map[string]string{"password": "x"} },
		"consumer maps to bad field":                func(i *ShapeIdentity) { i.Consumers[1].Fields = map[string]string{"issuer": "Not A Field"} },
		"duplicate consumer":                        func(i *ShapeIdentity) { i.Consumers = append(i.Consumers, i.Consumers[0]) },
		"machine user with unknown role":            func(i *ShapeIdentity) { i.MachineUsers[0].Roles = []string{"root"} },
		"hosted machine user without key_secret":    func(i *ShapeIdentity) { i.MachineUsers[0].KeySecret = nil },
		"smtp password is not a name":               func(i *ShapeIdentity) { i.SMTP.PasswordSecret = "hunter 2" },
		"smtp port":                                 func(i *ShapeIdentity) { i.SMTP.Port = 0 },
		"delivery of a non-identity kind":           func(i *ShapeIdentity) { i.Deliveries[0].Kind = ControlPlaneSecretTelemetryToken },
		"delivery the shape does not carry":         func(i *ShapeIdentity) { i.Deliveries[0].Name = "elsewhere" },
		"managed secret outside the reserved names": func(i *ShapeIdentity) { i.ManagedSecrets = []string{"DATABASE_URL"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			id := hostedIdentity()
			mutate(id)
			err := shapeWithIdentity(id).Canonical().Validate()
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}

	t.Run("seed user with a bad password name", func(t *testing.T) {
		id := host()
		id.SeedUsers[0].PasswordSecret = "not a name"
		if err := (Shape{Kind: EnvLocal, Identity: id}).Canonical().Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("want ErrInvalid, got %v", err)
		}
	})
	t.Run("managed secrets on a host runtime", func(t *testing.T) {
		id := host()
		id.ManagedSecrets = []string{"RELIANT_IDENTITY_X"}
		if err := (Shape{Kind: EnvLocal, Identity: id}).Canonical().Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("want ErrInvalid, got %v", err)
		}
	})
	t.Run("an identity secret with no identity", func(t *testing.T) {
		s := Shape{Kind: EnvSelfManaged, ControlPlaneSecrets: []ShapeControlPlaneSecret{idSecret()}}
		if err := s.Canonical().Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("want ErrInvalid, got %v", err)
		}
	})
	t.Run("an identity secret that names no keys", func(t *testing.T) {
		s := shapeWithIdentity(hostedIdentity())
		s.ControlPlaneSecrets[0].Keys = nil
		if err := s.Canonical().Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("want ErrInvalid, got %v", err)
		}
	})
}

func TestShape_IdentityShareWith(t *testing.T) {
	sharer := func() *ShapeIdentity {
		return &ShapeIdentity{
			Name: "identity", Runtime: IdentityRuntimeHosted, ShareWith: "staging", Login: ShapeIdentityLogin{Mode: IdentityLoginNative},
			Applications: []ShapeIdentityApplication{{Name: "preview:api", Workload: "api", Kind: IdentityAppNativeBroker}},
			Consumers:    []ShapeIdentityConsumer{{Workload: "api", Application: "preview:api"}},
		}
	}
	if err := (Shape{Kind: EnvPreview, Identity: sharer()}).Canonical().Validate(); err != nil {
		t.Fatalf("a sharer that declares only applications and consumers is valid: %v", err)
	}
	for name, mutate := range map[string]func(i *ShapeIdentity){
		"roles": func(i *ShapeIdentity) {
			i.Roles = []ShapeIdentityRole{{Key: "admin", MembersFrom: IdentityMembersConsole}}
		},
		"smtp": func(i *ShapeIdentity) {
			i.SMTP = &ShapeIdentitySMTP{Host: "h", Port: 25, Sender: "a@b.c", PasswordSecret: "P"}
		},
		"domain":       func(i *ShapeIdentity) { i.Domain = "login.acme.test"; i.Public = true },
		"project_name": func(i *ShapeIdentity) { i.ProjectName = "other" },
		"host runtime": func(i *ShapeIdentity) { i.Runtime = IdentityRuntimeHost },
		"a path":       func(i *ShapeIdentity) { i.ShareWith = "other/project" },
	} {
		t.Run(name, func(t *testing.T) {
			id := sharer()
			mutate(id)
			if err := (Shape{Kind: EnvPreview, Identity: id}).Canonical().Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

// A shape can carry a destination and a name, never a value.
func TestShapeIdentity_CarriesNoValue(t *testing.T) {
	var walk func(typ reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			switch n := strings.ToLower(f.Name); n {
			case "value", "data", "token", "password", "secret", "clientsecret", "privatekey", "keymaterial", "pem":
				t.Errorf("%s.%s: a shape must never be able to carry a value", path, f.Name)
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(reflect.TypeOf(ShapeIdentity{}), "ShapeIdentity")
}

func TestIdentitySlug(t *testing.T) {
	const envID = "7f3c2a10-5b1e-4d52-9a8c-0e6b4d2f1a77"
	sum := sha256.Sum256([]byte(envID))
	hash := hex.EncodeToString(sum[:])

	got := IdentitySlug(envID, "Acme Corp", "prod", IdentitySlugMinHash)
	if want := "acme-corp-prod-" + hash[:6]; got != want {
		t.Errorf("IdentitySlug = %q, want %q", got, want)
	}
	if IdentityIssuer(got) != "https://"+got+".reliantidentity.com" {
		t.Errorf("issuer = %q", IdentityIssuer(got))
	}

	// STABLE: a function of the env's immutable id, so renaming the env or the
	// project never moves it... as long as the slug is stored, which is the
	// control plane's job. The same inputs always give the same slug.
	if again := IdentitySlug(envID, "Acme Corp", "prod", 6); again != got {
		t.Errorf("not deterministic: %q vs %q", again, got)
	}
	// UNIQUE per env: a different id is a different slug even for the same names.
	if other := IdentitySlug("another-env-id", "Acme Corp", "prod", 6); other == got {
		t.Error("two envs with the same names share a slug")
	}
	// A collision retry lengthens the hash and nothing else.
	if longer := IdentitySlug(envID, "Acme Corp", "prod", 9); longer != "acme-corp-prod-"+hash[:9] || !strings.HasPrefix(longer, got) {
		t.Errorf("a longer hash must extend the slug: %q", longer)
	}
	if clamped := IdentitySlug(envID, "a", "b", 99); !strings.HasSuffix(clamped, hash[:IdentitySlugMaxHash]) {
		t.Errorf("hash length is not clamped: %q", clamped)
	}

	for name, in := range map[string][3]string{
		"empty labels":      {"", "", ""},
		"symbols only":      {"!!!", "---", ""},
		"unicode":           {"Café Ünï", "prod/eu", ""},
		"very long":         {strings.Repeat("p", 80), strings.Repeat("e", 80), ""},
		"leading separator": {"-acme-", "-prod-", ""},
	} {
		s := IdentitySlug(envID, in[0], in[1], IdentitySlugMaxHash)
		if !ValidIdentitySlug(s) {
			t.Errorf("%s: %q is not a valid DNS label", name, s)
		}
		if len(s) > 63 {
			t.Errorf("%s: %q is %d characters, over a DNS label", name, s, len(s))
		}
	}
	if ValidIdentitySlug("Has_Underscore") || ValidIdentitySlug("-lead") || ValidIdentitySlug(strings.Repeat("a", 64)) {
		t.Error("ValidIdentitySlug accepts an invalid label")
	}
}

// A consumer that omits nothing encodes exactly as before the field existed,
// so shapes recorded earlier hash and diff the same.
func TestShape_IdentityOmitLeavesExistingConsumersByteIdentical(t *testing.T) {
	id := hostedIdentity()
	before, err := shapeWithIdentity(id).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), `"omit"`) {
		t.Fatalf("a consumer with no omit must not encode the field: %s", before)
	}
	// An empty (non-nil) slice is the same as none.
	id2 := hostedIdentity()
	for n := range id2.Consumers {
		id2.Consumers[n].Omit = []string{}
	}
	after, err := shapeWithIdentity(id2).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("an empty omit changed the encoding:\n%s\n%s", before, after)
	}
}

func TestShape_IdentityOmitIsSortedRoundTripsAndValidates(t *testing.T) {
	id := hostedIdentity()
	id.Consumers[1].Omit = []string{"scopes", "login_uri"}
	s := shapeWithIdentity(id)
	b, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"omit":["login_uri","scopes"]`) {
		t.Errorf("omit is not recorded sorted: %s", b)
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("a shape with omit is invalid: %v", err)
	}
}

func TestShape_IdentityOmitRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		omit []string
		want string
	}{
		"the issuer defines the identity":     {[]string{"issuer"}, "may be omitted"},
		"the broker token defines the broker": {[]string{"broker_token"}, "may be omitted"},
		"not an output":                       {[]string{"nope"}, "may be omitted"},
		"twice":                               {[]string{"scopes", "scopes"}, "twice"},
		"both mapped in fields and omitted":   {[]string{"scopes"}, "both maps and omits"},
	} {
		t.Run(name, func(t *testing.T) {
			id := hostedIdentity()
			id.Consumers[1].Omit = tc.omit
			if name == "both mapped in fields and omitted" {
				id.Consumers[1].Fields = map[string]string{"scopes": "my_scopes"}
			}
			err := shapeWithIdentity(id).Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
