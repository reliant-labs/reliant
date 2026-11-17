package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

var (
	sealGen = release.RenderedGeneratedSecret{Kind: "random", Bytes: 32, Encoding: "hex"}
	pwGen   = release.RenderedGeneratedSecret{Kind: "password", Length: 32, Symbols: "!-_."}
)

func newMinter(g *fakeGSM) (bootstrapMinter, *bytes.Buffer) {
	var out bytes.Buffer
	return bootstrapMinter{api: g, project: "acme-prod", out: &out}, &out
}

func (g *fakeGSM) container(project, id string) { g.secrets[project+"/"+id] = nil }

func TestBootstrapMint_CreatesAnAbsentValueOnce(t *testing.T) {
	g := newFakeGSM()
	g.container("acme-prod", "openbao-unseal-prod")
	m, out := newMinter(g)

	res, err := m.mintBootstrap(context.Background(), map[string]release.RenderedGeneratedSecret{"openbao-unseal-prod": sealGen})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !res[0].Created || res[0].Version != 1 || res[0].Raced {
		t.Fatalf("result = %+v", res)
	}
	vs := g.secrets["acme-prod/openbao-unseal-prod"]
	if len(vs) != 1 || len(vs[0]) != 64 {
		t.Fatalf("want one 32-byte hex value, got %d version(s)", len(vs))
	}
	for _, c := range vs[0] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("seal key is not lowercase hex: %q", vs[0])
		}
	}
	m.report(res)
	if !strings.Contains(out.String(), "minted (version 1") {
		t.Errorf("report = %q", out.String())
	}

	// A second deploy finds the value and leaves it alone.
	res, err = m.mintBootstrap(context.Background(), map[string]release.RenderedGeneratedSecret{"openbao-unseal-prod": sealGen})
	if err != nil || res[0].Created || res[0].Version != 1 {
		t.Fatalf("second run: %+v %v", res, err)
	}
	if len(g.secrets["acme-prod/openbao-unseal-prod"]) != 1 {
		t.Error("a second deploy added a version")
	}
}

// A container somebody already filled is never overwritten, whatever it holds.
func TestBootstrapMint_NeverOverwrites(t *testing.T) {
	g := newFakeGSM()
	g.secrets["acme-prod/openbao-unseal-prod"] = []string{"hand-provisioned"}
	m, _ := newMinter(g)
	res, err := m.mintBootstrap(context.Background(), map[string]release.RenderedGeneratedSecret{"openbao-unseal-prod": sealGen})
	if err != nil || res[0].Created {
		t.Fatalf("%+v %v", res, err)
	}
	if got := g.secrets["acme-prod/openbao-unseal-prod"]; len(got) != 1 || got[0] != "hand-provisioned" {
		t.Errorf("the existing value was touched: %v", got)
	}
}

// forge does not create a container: its IAM is provisioned outside, and a
// container forge made would have none.
func TestBootstrapMint_RefusesToCreateTheContainer(t *testing.T) {
	g := newFakeGSM()
	m, _ := newMinter(g)
	_, err := m.mintBootstrap(context.Background(), map[string]release.RenderedGeneratedSecret{"openbao-unseal-prod": sealGen})
	if err == nil || !strings.Contains(err.Error(), "does not exist in project acme-prod") {
		t.Fatalf("got %v", err)
	}
	if len(g.created) != 0 {
		t.Errorf("forge created %v", g.created)
	}
}

// A disabled or destroyed version was taken out of service on purpose.
func TestBootstrapMint_DoesNotMintOverADisabledVersion(t *testing.T) {
	g := newFakeGSM()
	g.secrets["acme-prod/openbao-unseal-prod"] = []string{"old"}
	g.disabled["acme-prod/openbao-unseal-prod"] = true
	m, _ := newMinter(g)
	_, err := m.mintBootstrap(context.Background(), map[string]release.RenderedGeneratedSecret{"openbao-unseal-prod": sealGen})
	if err == nil || !strings.Contains(err.Error(), "disabled or destroyed") {
		t.Fatalf("got %v", err)
	}
	if len(g.secrets["acme-prod/openbao-unseal-prod"]) != 1 {
		t.Error("a version was added over a disabled one")
	}
}

// raceGSM lets a second writer slip a version in between the look and the add.
type raceGSM struct {
	*fakeGSM
	once sync.Once
}

func (r *raceGSM) AddVersion(ctx context.Context, project, id string, value []byte, create bool) (uint32, error) {
	r.once.Do(func() { _, _ = r.fakeGSM.AddVersion(ctx, project, id, []byte("the-winner"), false) })
	return r.fakeGSM.AddVersion(ctx, project, id, value, create)
}

// Secret Manager has no check-and-set. The loser's version is disabled, so
// "latest" and version 1 stay one value, and the deploy says so.
func TestBootstrapMint_ARaceLeavesOneUsableValue(t *testing.T) {
	g := newFakeGSM()
	g.container("acme-prod", "openbao-pg-password-prod")
	var out bytes.Buffer
	m := bootstrapMinter{api: &raceGSM{fakeGSM: g}, project: "acme-prod", out: &out}
	res, err := m.mintBootstrap(context.Background(), map[string]release.RenderedGeneratedSecret{"openbao-pg-password-prod": pwGen})
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Raced || !res[0].Disabled || res[0].Version != 2 {
		t.Fatalf("result = %+v", res)
	}
	vs := g.secrets["acme-prod/openbao-pg-password-prod"]
	if len(vs) != 2 || vs[0] != "the-winner" {
		t.Errorf("version 1 = %q: the first writer must keep it", vs)
	}
	if !g.disabledVersions["acme-prod/openbao-pg-password-prod"][2] || g.disabledVersions["acme-prod/openbao-pg-password-prod"][1] {
		t.Errorf("the loser's version 2 must be disabled and the winner's version 1 left alone: %v", g.disabledVersions)
	}
	m.report(res)
	if !strings.Contains(out.String(), "another writer minted first") || !strings.Contains(out.String(), "disabled it") {
		t.Errorf("report = %q", out.String())
	}
}

// noDisableGSM models a deployer holding versions.add without versions.disable.
type noDisableGSM struct{ *raceGSM }

func (noDisableGSM) Disable(context.Context, string, string, uint32) error {
	return errors.New("secret manager disable version: HTTP 403 PERMISSION_DENIED: Permission 'secretmanager.versions.disable' denied")
}

// A loser that cannot disable says exactly what to run: for a password read as
// "latest", leaving version 2 enabled would split the database from the Secret.
func TestBootstrapMint_AFailedDisableNamesTheFix(t *testing.T) {
	g := newFakeGSM()
	g.container("acme-prod", "openbao-pg-password-prod")
	m := bootstrapMinter{api: noDisableGSM{&raceGSM{fakeGSM: g}}, project: "acme-prod", out: &bytes.Buffer{}}
	_, err := m.mintBootstrap(context.Background(), map[string]release.RenderedGeneratedSecret{"openbao-pg-password-prod": pwGen})
	if err == nil {
		t.Fatal("a loser that cannot disable its version must fail the deploy")
	}
	for _, want := range []string{"gcloud secrets versions disable 2 --secret openbao-pg-password-prod --project acme-prod", "could not disable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	for _, v := range g.secrets["acme-prod/openbao-pg-password-prod"] {
		if strings.Contains(err.Error(), v) && v != "the-winner" {
			t.Error("the error carries a generated value")
		}
	}
}

func TestBootstrapMint_PasswordHasAllClassesAndHyphenatedIDsWork(t *testing.T) {
	g := newFakeGSM()
	g.container("acme-prod", "openbao-pg-password-prod")
	m, _ := newMinter(g)
	if _, err := m.mintBootstrap(context.Background(), map[string]release.RenderedGeneratedSecret{"openbao-pg-password-prod": pwGen}); err != nil {
		t.Fatal(err)
	}
	pw := g.secrets["acme-prod/openbao-pg-password-prod"][0]
	if len(pw) != 32 {
		t.Fatalf("length %d", len(pw))
	}
	var up, lo, dg, sy bool
	for _, c := range pw {
		switch {
		case c >= 'A' && c <= 'Z':
			up = true
		case c >= 'a' && c <= 'z':
			lo = true
		case c >= '0' && c <= '9':
			dg = true
		default:
			sy = true
		}
	}
	if !(up && lo && dg && sy) {
		t.Errorf("password lacks a class: %q", pw)
	}
}

func TestBootstrapMint_IDMapping(t *testing.T) {
	for in, want := range map[string]string{
		"openbao-unseal-prod": "openbao_unseal_prod",
		"9lives":              "_9lives",
		"plain":               "plain",
	} {
		if got := secretNameFor(in); got != want {
			t.Errorf("secretNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// No value is printed, and a remote error that echoes the request is redacted.
type echoGSM struct {
	*fakeGSM
	seen []byte
}

func (e *echoGSM) AddVersion(_ context.Context, _, _ string, value []byte, _ bool) (uint32, error) {
	e.seen = append([]byte(nil), value...)
	return 0, errors.New("secret manager add version: HTTP 400 INVALID_ARGUMENT: bad payload " + string(value))
}

func TestBootstrapMint_NeverPrintsAValue(t *testing.T) {
	g := newFakeGSM()
	g.container("acme-prod", "openbao-unseal-prod")
	e := &echoGSM{fakeGSM: g}
	var out bytes.Buffer
	m := bootstrapMinter{api: e, project: "acme-prod", out: &out}
	res, err := m.mintBootstrap(context.Background(), map[string]release.RenderedGeneratedSecret{"openbao-unseal-prod": sealGen})
	if err == nil {
		t.Fatal("want the remote's refusal")
	}
	if len(e.seen) == 0 {
		t.Fatal("the fake never saw a value")
	}
	if strings.Contains(err.Error(), string(e.seen)) {
		t.Errorf("the error carries the generated value: %v", err)
	}
	m.report(res)
	if strings.Contains(out.String(), string(e.seen)) {
		t.Error("the report carries the generated value")
	}
}

func bootstrapEnv(generate map[string]release.RenderedGeneratedSecret, source *BootstrapSourceEntity) *KCLEntities {
	return &KCLEntities{
		OpenBao:         &OpenBaoEntity{Name: "openbao", Namespace: "openbao", BootstrapGenerate: generate},
		BootstrapSource: source,
	}
}

func TestEnsureBootstrapSecrets(t *testing.T) {
	gsm := &BootstrapSourceEntity{Type: "gcpsm", Project: "acme-prod"}
	gen := map[string]release.RenderedGeneratedSecret{"openbao-unseal-prod": sealGen}

	t.Run("nothing to mint costs nothing and needs no credentials", func(t *testing.T) {
		prev := newGSMAPI
		newGSMAPI = func(context.Context) (gsmAPI, error) {
			t.Fatal("opened Secret Manager with nothing to mint")
			return nil, nil
		}
		t.Cleanup(func() { newGSMAPI = prev })
		for _, e := range []*KCLEntities{
			nil, {}, bootstrapEnv(nil, gsm), bootstrapEnv(gen, nil),
			bootstrapEnv(gen, &BootstrapSourceEntity{Type: "out_of_band"}),
		} {
			if err := ensureBootstrapSecrets(context.Background(), e, false, &bytes.Buffer{}); err != nil {
				t.Errorf("%+v: %v", e, err)
			}
		}
	})

	t.Run("mints through the deployer's credentials", func(t *testing.T) {
		g := newFakeGSM()
		g.container("acme-prod", "openbao-unseal-prod")
		prev := newGSMAPI
		newGSMAPI = func(context.Context) (gsmAPI, error) { return g, nil }
		t.Cleanup(func() { newGSMAPI = prev })
		var out bytes.Buffer
		if err := ensureBootstrapSecrets(context.Background(), bootstrapEnv(gen, gsm), false, &out); err != nil {
			t.Fatal(err)
		}
		if len(g.secrets["acme-prod/openbao-unseal-prod"]) != 1 {
			t.Error("not minted")
		}
	})

	t.Run("a dry run writes nothing", func(t *testing.T) {
		g := newFakeGSM()
		g.container("acme-prod", "openbao-unseal-prod")
		prev := newGSMAPI
		newGSMAPI = func(context.Context) (gsmAPI, error) { return g, nil }
		t.Cleanup(func() { newGSMAPI = prev })
		var out bytes.Buffer
		if err := ensureBootstrapSecrets(context.Background(), bootstrapEnv(gen, gsm), true, &out); err != nil {
			t.Fatal(err)
		}
		if len(g.secrets["acme-prod/openbao-unseal-prod"]) != 0 {
			t.Error("a dry run added a version")
		}
		if !strings.Contains(out.String(), "would be minted") {
			t.Errorf("out = %q", out.String())
		}
	})

	t.Run("no credentials is an actionable error", func(t *testing.T) {
		prev := newGSMAPI
		newGSMAPI = func(context.Context) (gsmAPI, error) {
			return nil, errors.New("no Google Application Default Credentials")
		}
		t.Cleanup(func() { newGSMAPI = prev })
		err := ensureBootstrapSecrets(context.Background(), bootstrapEnv(gen, gsm), false, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "forge secret import --to-bootstrap") {
			t.Fatalf("got %v", err)
		}
	})
}
