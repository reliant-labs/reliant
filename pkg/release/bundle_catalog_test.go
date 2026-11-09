package release

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateDeclaredEnvs(t *testing.T) {
	ok := []DeclaredEnv{
		{Name: "dev", Lifecycle: DeclaredLifecycleLocal, Destination: DeclaredDestinationCompose},
		{Name: "e2e", Lifecycle: DeclaredLifecycleEphemeral, Destination: DeclaredDestinationCluster},
		{Name: "prod", Destination: DeclaredDestinationMixed, ControlPlaneKind: string(EnvSelfManaged)},
		{Name: "site.v2", Destination: DeclaredDestinationHosted, ControlPlaneKind: string(EnvPersistent)},
		{Name: "wip", RenderError: DeclaredEnvRenderFailed},
	}
	if err := ValidateDeclaredEnvs(ok); err != nil {
		t.Fatalf("a valid catalog was refused: %v", err)
	}
	if err := ValidateDeclaredEnvs(nil); err != nil {
		t.Fatalf("an absent catalog is valid: %v", err)
	}

	tooMany := make([]DeclaredEnv, MaxDeclaredEnvs+1)
	for i := range tooMany {
		tooMany[i] = DeclaredEnv{Name: fmt.Sprintf("env%03d", i)}
	}
	cases := map[string][]DeclaredEnv{
		"a path as a name":           {{Name: "/Users/me/proj/deploy/kcl/prod"}},
		"a relative path":            {{Name: "deploy/kcl/prod"}},
		"prose as a name":            {{Name: "prod (the real one)"}},
		"an upper-case name":         {{Name: "Prod"}},
		"an empty name":              {{Name: ""}},
		"unsorted":                   {{Name: "prod"}, {Name: "dev"}},
		"a repeat":                   {{Name: "prod"}, {Name: "prod"}},
		"an unknown lifecycle":       {{Name: "prod", Lifecycle: "forever"}},
		"an unknown destination":     {{Name: "prod", Destination: "moon"}},
		"an unknown kind":            {{Name: "prod", ControlPlaneKind: "preview"}},
		"render error text":          {{Name: "prod", RenderError: "open /Users/me/proj/deploy/kcl/prod/main.k: no such file"}},
		"facts beside a render fail": {{Name: "prod", RenderError: DeclaredEnvRenderTimeout, Destination: DeclaredDestinationHost}},
		"too many":                   tooMany,
	}
	for name, envs := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateDeclaredEnvs(envs)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

// TestValidateDeclaredEnvs_TheLargestValidCatalogFits: 100 envs at the
// longest name and every field set is accepted, so the byte bound is a
// backstop that never refuses a catalog the other rules allow.
func TestValidateDeclaredEnvs_TheLargestValidCatalogFits(t *testing.T) {
	envs := make([]DeclaredEnv, MaxDeclaredEnvs)
	for i := range envs {
		envs[i] = DeclaredEnv{
			Name:             fmt.Sprintf("e%03d", i) + strings.Repeat("x", 59),
			Lifecycle:        DeclaredLifecycleEphemeral,
			Destination:      DeclaredDestinationCompose,
			ControlPlaneKind: string(EnvSelfManaged),
		}
	}
	if err := ValidateDeclaredEnvs(envs); err != nil {
		t.Fatalf("the largest valid catalog was refused: %v", err)
	}
}

// TestBundleDocValidateCoversTheCatalog: the document's own Validate runs the
// catalog's, so neither producer nor decoder can carry an invalid one.
func TestBundleDocValidateCoversTheCatalog(t *testing.T) {
	d := fullBundleDoc()
	if err := d.Validate(); err != nil {
		t.Fatalf("the fixture must be valid: %v", err)
	}
	d.DeclaredEnvs = []DeclaredEnv{{Name: "../etc"}}
	if err := d.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a document carrying an invalid catalog must be refused, got %v", err)
	}
}
