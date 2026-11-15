package cloud

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/credentials"
)

// TestResolveEndpoint_IsPerEnvironmentNotProcessState is THE test for
// the design constraint: the endpoint is declared per environment, so
// two environments resolve to two different endpoints IN THE SAME
// PROCESS, with no state carried between the calls.
//
// This is what rules out a `forge context use <name>` implementation.
// Under a stateful current-context, resolving staging and then prod
// would either return the same endpoint twice or depend on the order
// the calls were made in. Here neither can happen, because the only
// input is the declaration passed in.
func TestResolveEndpoint_IsPerEnvironmentNotProcessState(t *testing.T) {
	stagingDecl := &Declaration{Endpoint: "https://api.staging.example.com"}
	prodDecl := &Declaration{Endpoint: "https://api.example.com", TokenEnv: "ACME_PROD_TOKEN"}

	staging, err := ResolveEndpoint("staging", stagingDecl)
	if err != nil {
		t.Fatalf("staging: %v", err)
	}
	prod, err := ResolveEndpoint("prod", prodDecl)
	if err != nil {
		t.Fatalf("prod: %v", err)
	}

	if staging.URL == prod.URL {
		t.Fatalf("two environments must resolve DIFFERENT endpoints in one process; both got %q", staging.URL)
	}
	if staging.URL != "https://api.staging.example.com" {
		t.Errorf("staging endpoint: got %q", staging.URL)
	}
	if prod.URL != "https://api.example.com" {
		t.Errorf("prod endpoint: got %q", prod.URL)
	}

	// Resolving prod must not have disturbed staging: re-resolve and
	// confirm it is unchanged. A stateful implementation fails here.
	stagingAgain, err := ResolveEndpoint("staging", stagingDecl)
	if err != nil {
		t.Fatalf("staging re-resolve: %v", err)
	}
	if stagingAgain.URL != staging.URL {
		t.Errorf("resolving another env changed this one: %q then %q — endpoint resolution is carrying state",
			staging.URL, stagingAgain.URL)
	}

	// Each env's token_env is its own, too.
	if staging.TokenEnv != DefaultTokenEnv {
		t.Errorf("staging declared no token_env, so it should default to %s; got %q", DefaultTokenEnv, staging.TokenEnv)
	}
	if prod.TokenEnv != "ACME_PROD_TOKEN" {
		t.Errorf("prod declared ACME_PROD_TOKEN; got %q", prod.TokenEnv)
	}
}

// TestResolveEndpoint_NoDeclarationIsAClearError — the common case for
// every forge user with no hosted anything. It must name the file to
// edit rather than invent a default endpoint.
func TestResolveEndpoint_NoDeclarationIsAClearError(t *testing.T) {
	_, err := ResolveEndpoint("dev", nil)
	if err == nil {
		t.Fatal("an env with no control_plane declaration must be an error, not a default endpoint")
	}
	msg := err.Error()
	for _, want := range []string{"dev", "deploy/kcl/dev/main.k", "forge.ControlPlane"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should contain %q so the user knows what to edit; got:\n%s", want, msg)
		}
	}
}

// TestDefaultEndpoint_IsHTTPSAndCanonical — Reliant cloud is where every
// unconfigured forge sends a bearer credential, so it must never be plaintext,
// and it must already be the normalized form credentials are keyed by (else a
// login and a later lookup could land on two different keys).
func TestDefaultEndpoint_IsHTTPSAndCanonical(t *testing.T) {
	if !strings.HasPrefix(DefaultEndpoint, "https://") {
		t.Fatalf("DefaultEndpoint must be https: %q", DefaultEndpoint)
	}
	key, err := credentials.Normalize(DefaultEndpoint)
	if err != nil || key != DefaultEndpoint {
		t.Fatalf("DefaultEndpoint must be canonical: Normalize(%q) = %q, %v", DefaultEndpoint, key, err)
	}
	ep, err := ResolveEndpoint("", &Declaration{Endpoint: DefaultEndpoint})
	if err != nil || ep.URL != DefaultEndpoint || ep.TokenEnv != DefaultTokenEnv {
		t.Fatalf("resolve default: %+v %v", ep, err)
	}
}

// TestResolveEndpoint_NoDeclarationNeverDefaults — the Reliant-cloud default
// belongs to a DECLARATION (`forge.ControlPlane {}`), never to its absence: an
// env that declares nothing is not hosted and must not send anything anywhere.
func TestResolveEndpoint_NoDeclarationNeverDefaults(t *testing.T) {
	ep, err := ResolveEndpoint("dev", nil)
	if err == nil || ep.URL != "" {
		t.Fatalf("no declaration must not resolve to %s; got %+v %v", DefaultEndpoint, ep, err)
	}
}

// TestResolveEndpoint_EmptyEndpointIsTreatedAsAbsent — a declaration
// present but blank must not produce a client pointed at "".
func TestResolveEndpoint_EmptyEndpointIsTreatedAsAbsent(t *testing.T) {
	if _, err := ResolveEndpoint("staging", &Declaration{Endpoint: "   "}); err == nil {
		t.Fatal("a blank endpoint must be rejected")
	}
}

// TestResolveEndpoint_TrimsTrailingSlash so joining a procedure path
// never produces a double slash the server may route differently.
func TestResolveEndpoint_TrimsTrailingSlash(t *testing.T) {
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: "https://api.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if ep.URL != "https://api.example.com" {
		t.Errorf("trailing slash should be trimmed; got %q", ep.URL)
	}
}
