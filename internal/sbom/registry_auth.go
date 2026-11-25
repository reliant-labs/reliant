package sbom

import (
	"context"
	"strings"
)

// Registry credentials for syft's registry reads.
//
// syft reads the ambient docker credential store natively, the way the docker
// CLI does, so a registry the author `docker login`ed to (or whose credential
// helper is configured) needs nothing from forge. A registry forge
// authenticates to with ITS OWN identity is different: the Reliant platform
// registry takes the env's control-plane credential, which forge presents in
// memory and never writes to the docker store (bundle_registry_auth.go). For
// those hosts a [RegistryAuth] hands syft the same credential.
//
// HOW IT REACHES SYFT: syft's own registry-auth environment variables,
// scoped to the one authority, on the syft process alone:
//
//	SYFT_REGISTRY_AUTH_AUTHORITY=<host>
//	SYFT_REGISTRY_AUTH_USERNAME=<user>
//	SYFT_REGISTRY_AUTH_PASSWORD=<secret>
//
// Chosen over a temporary DOCKER_CONFIG because:
//
//   - nothing touches disk, so there is nothing to remove afterwards and
//     nothing left behind by a crash or a kill between write and cleanup;
//   - syft presents an auth entry only to its authority and falls back to the
//     ambient docker keychain for every other host, so the author's own
//     logins and credential helpers keep working unchanged. A replacement
//     DOCKER_CONFIG would hide them, or forge would have to copy the user's
//     whole config (and its helpers) into the temp file to avoid that;
//   - the variables are set on the child's environment only (exec.Cmd.Env),
//     never in forge's own process environment and never on a command line,
//     which any user on the machine can read from the process table.
//
// The password is the long-form credential, exchanged by syft at the
// registry's token realm (basic auth on the token request), exactly as
// `docker login` would present it.

// RegistryCredential is what syft presents to one registry.
type RegistryCredential struct {
	Username string
	Password string
}

// RegistryAuth returns forge's own credential for a registry host. ok is
// false when forge has no identity of its own for that host: syft then reads
// the ambient docker credential store, as `docker pull` would. An error means
// forge has an identity for the host but could not resolve it now.
type RegistryAuth func(ctx context.Context, host string) (cred RegistryCredential, ok bool, err error)

// Environment variable names syft reads its single registry-auth entry from.
const (
	envAuthority = "SYFT_REGISTRY_AUTH_AUTHORITY"
	envUsername  = "SYFT_REGISTRY_AUTH_USERNAME"
	envPassword  = "SYFT_REGISTRY_AUTH_PASSWORD"
)

// registryEnv is the environment a registry read of ref runs with, and a note
// for the failure reason when forge's credential could not be resolved (syft
// then tries the ambient store, which may still hold one).
func (g *Generator) registryEnv(ctx context.Context, ref string) (env []string, note string) {
	if g.RegistryAuth == nil {
		return nil, ""
	}
	host := registryHost(ref)
	cred, ok, err := g.RegistryAuth(ctx, host)
	if err != nil {
		return nil, "forge's own credential for " + host + " could not be resolved: " + oneLine(err.Error())
	}
	if !ok || cred.Password == "" {
		return nil, ""
	}
	return []string{envAuthority + "=" + host, envUsername + "=" + cred.Username, envPassword + "=" + cred.Password}, ""
}

// registryHost is the registry an image reference names: its first path
// segment when that is a host (a dot, a port, or localhost), else Docker Hub.
func registryHost(ref string) string {
	first, rest, found := strings.Cut(ref, "/")
	if found && rest != "" && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return strings.ToLower(first)
	}
	return "docker.io"
}
