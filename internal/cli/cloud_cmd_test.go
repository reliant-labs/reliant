package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// TestDeclarationFromEntities_PerEnvironment proves the CLI's bridge
// keeps the endpoint a per-ENVIRONMENT fact: two rendered environments
// produce two declarations, resolved in the same process, with nothing
// shared between them.
//
// Paired with cloud.TestResolveEndpoint_IsPerEnvironmentNotProcessState:
// that one pins the resolver, this one pins the wiring from rendered
// KCL into it. A `forge context use` implementation could not satisfy
// both.
func TestDeclarationFromEntities_PerEnvironment(t *testing.T) {
	staging := &KCLEntities{ControlPlane: &ControlPlaneEntity{
		Type:     "control_plane",
		Endpoint: "https://api.staging.example.com",
	}}
	prod := &KCLEntities{ControlPlane: &ControlPlaneEntity{
		Type:     "control_plane",
		Endpoint: "https://api.example.com",
		TokenEnv: "ACME_PROD_TOKEN",
	}}

	stagingEP, err := cloud.ResolveEndpoint("staging", declarationFromEntities(staging))
	if err != nil {
		t.Fatalf("staging: %v", err)
	}
	prodEP, err := cloud.ResolveEndpoint("prod", declarationFromEntities(prod))
	if err != nil {
		t.Fatalf("prod: %v", err)
	}

	if stagingEP.URL == prodEP.URL {
		t.Fatalf("two envs must resolve different endpoints in one process; both got %q", stagingEP.URL)
	}
	if stagingEP.URL != "https://api.staging.example.com" || prodEP.URL != "https://api.example.com" {
		t.Errorf("endpoints crossed: staging=%q prod=%q", stagingEP.URL, prodEP.URL)
	}
	if prodEP.TokenEnv != "ACME_PROD_TOKEN" {
		t.Errorf("prod's declared token_env should be carried through; got %q", prodEP.TokenEnv)
	}
	if stagingEP.TokenEnv != cloud.DefaultTokenEnv {
		t.Errorf("staging declared none, so it defaults; got %q", stagingEP.TokenEnv)
	}
}

// TestDeclarationFromEntities_AbsentIsNil is the common case: a forge
// project with no hosted anything. It must produce no declaration, so
// nothing downstream looks for a credential.
func TestDeclarationFromEntities_AbsentIsNil(t *testing.T) {
	cases := map[string]*KCLEntities{
		"nil entities":     nil,
		"no control_plane": {},
		"blank endpoint":   {ControlPlane: &ControlPlaneEntity{Type: "control_plane", Endpoint: "  "}},
	}
	for name, entities := range cases {
		if got := declarationFromEntities(entities); got != nil {
			t.Errorf("%s: expected nil declaration, got %+v", name, got)
		}
	}
}

// TestForgeStillWorksWithNoHostedEndpoint is the regression guard for
// every existing forge user, who has no hosted anything and must be
// entirely unaffected.
//
// Two properties: the new commands are registered (so the binary shape
// is stable), and resolving an env that declares no control plane fails
// with a message about the DECLARATION rather than about credentials.
// Sending that user to `forge login` would be the wrong hint — logging
// in cannot fix an absent declaration.
func TestForgeStillWorksWithNoHostedEndpoint(t *testing.T) {
	_, err := cloud.ResolveEndpoint("dev", declarationFromEntities(&KCLEntities{}))
	if err == nil {
		t.Fatal("an env with no control_plane must not silently resolve an endpoint")
	}
	if strings.Contains(err.Error(), "forge login") {
		t.Errorf("a missing DECLARATION must not be reported as a missing credential; got:\n%s", err)
	}
	if !strings.Contains(err.Error(), "declares no hosted control plane") {
		t.Errorf("want a declaration-shaped message; got:\n%s", err)
	}

	// The commands exist and are wired, without any hosted config present.
	root := newLoginCmd()
	if root.Name() != "login" {
		t.Errorf("login command name: got %q", root.Name())
	}
	if root.Flags().Lookup("token") == nil {
		t.Error("`forge login` must offer --token: CI has no browser")
	}
	cloudCmd := newCloudCmd()
	var names []string
	for _, c := range cloudCmd.Commands() {
		names = append(names, c.Name())
	}
	if len(names) == 0 {
		t.Fatal("`forge cloud` should have subcommands")
	}
}
