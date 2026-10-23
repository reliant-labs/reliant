package cloud

import (
	"fmt"
	"strings"
)

// DefaultEndpoint is Reliant's production control plane: the one a plain
// `go install`'d forge targets with no configuration.
//
// It is the Go copy of KCL's forge.RELIANT_CLOUD_ENDPOINT, which is what
// `forge.ControlPlane {}` resolves to. KCL owns the default for every env; this
// copy exists for the one command that runs with no env to render — `forge
// login` outside a project (or in one that declares no control plane).
// TestControlPlaneDefault_KCLMatchesGo renders the schema and fails if the two
// drift.
//
// It is the PUBLIC origin serving forge's whole hosted surface: the
// controlplane.v1.* RPCs and the CLI OAuth endpoints (/oauth/authorize,
// /oauth/token). reliant's API host is a different server and answers 404 for
// both, so pointing this at it would break login for every user.
const DefaultEndpoint = "https://admin.reliantapi.com"

// Endpoint is one environment's resolved hosted control plane: where to
// send requests, and which env var holds the credential.
//
// There is deliberately no package-level "current endpoint" and no way
// to set one. An Endpoint only ever comes from a specific environment's
// declaration, which is what makes two environments resolvable to two
// different endpoints in the same process — and what removes the class
// of bug where a command silently targets whatever was selected last.
type Endpoint struct {
	// Env is the environment this endpoint was declared by. Carried so
	// error messages can name the KCL file to edit.
	Env string
	// URL is the base URL, trailing slash trimmed.
	URL string
	// TokenEnv is the env var NAME the credential is read from.
	TokenEnv string
}

// Declaration is the per-environment control-plane declaration, as read
// from the environment's rendered KCL.
//
// Declared HERE, at the consumer, rather than importing the CLI's entity
// type: this package needs two strings, and taking a dependency on the
// renderer's struct would point the dependency the wrong way (cli
// depends on cloud, not the reverse) and drag the whole entity graph in
// behind it.
type Declaration struct {
	Endpoint string
	TokenEnv string
}

// ResolveEndpoint turns one environment's declaration into an Endpoint.
//
// decl == nil means the environment declares no hosted control plane.
// That is the overwhelmingly common case, so it returns a plain error
// naming the exact file to edit rather than falling back to
// DefaultEndpoint. An env that declares nothing is not hosted; defaulting
// here would mean a project with no hosted anything could send a request
// somewhere by accident, which is the one outcome this signature is shaped
// to prevent. The Reliant-cloud default belongs to the DECLARATION
// (`forge.ControlPlane {}` fills endpoint in KCL), never to its absence.
func ResolveEndpoint(env string, decl *Declaration) (Endpoint, error) {
	if decl == nil || strings.TrimSpace(decl.Endpoint) == "" {
		return Endpoint{}, fmt.Errorf(
			"env %q declares no hosted control plane\n"+
				"fix: add to the Bundle in deploy/kcl/%s/main.k\n"+
				"    control_plane = forge.ControlPlane {}    # Reliant cloud\n"+
				"or name another control plane with endpoint = \"https://…\" inside it. "+
				"The endpoint is declared per environment, so envs differ "+
				"in their KCL rather than in machine-local CLI state",
			env, env)
	}
	tokenEnv := strings.TrimSpace(decl.TokenEnv)
	if tokenEnv == "" {
		tokenEnv = DefaultTokenEnv
	}
	return Endpoint{
		Env:      env,
		URL:      strings.TrimRight(strings.TrimSpace(decl.Endpoint), "/"),
		TokenEnv: tokenEnv,
	}, nil
}
