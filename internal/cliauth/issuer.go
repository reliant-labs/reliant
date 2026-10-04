// Copyright (c) 2025 Reliant Labs
package cliauth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/oauth2"
)

// ErrNoControlPlane reports that a server publishes no RFC 8414 metadata, so
// it names no control plane. A self-hosted reliant with no hosted control
// plane is the ordinary case, not a misconfiguration — callers that deposit a
// forge credential treat it as "nothing to deposit" rather than a failure.
var ErrNoControlPlane = errors.New("cliauth: server names no control plane")

// DiscoverIssuer returns the origin of the control plane that issues server's
// tokens, discovered from server's RFC 8414 metadata.
//
// ── WHY THIS IS SEPARATE FROM Login.Run ───────────────────────────────
//
// Login.Run already discovers this, and stamps it onto the credential as
// Credential.Issuer. But a browser login is only ONE of the ways a token
// reaches a machine, and it is the way an Electron user never takes. Electron
// mints its own daemon PAT (electron/src/daemon-creds.js), a managed daemon
// receives one through a mounted Kubernetes Secret, and `--token` takes one
// pasted from the web UI. All three produce a bare token plus the --server URL
// it is presented to, and NO issuer — so the deposit those paths need has
// nothing to key by unless the issuer is discovered on its own.
//
// That gap is the bug this exists to close: the deposit was reachable only
// from `reliant auth login` and interactive `daemon register`, so a laptop
// signed in to prod through the app had no prod entry in forge's store at all.
//
// ── WHY DISCOVERY AND NOT A HOSTNAME RULE ─────────────────────────────
//
// The control plane is a DIFFERENT origin from the API server in prod
// (admin.reliantapi.com versus api.reliantapi.com), and the relationship
// between the two is a property of the DEPLOYMENT, not of the hostnames. A
// derive-by-string rule ("s/^api\\./admin./") is how the gateway URL was once
// derived, and it produced gateway-api.reliantapi.com — an NXDOMAIN — for
// prod. Asking the server is what the deployment actually said.
func DiscoverIssuer(ctx context.Context, client oauth2.HTTPDoer, server string) (string, error) {
	if strings.TrimSpace(server) == "" {
		return "", errors.New("cliauth: cannot discover a control plane without a server URL")
	}
	meta, err := oauth2.DiscoverAuthorizationServer(ctx, client, server)
	if err != nil {
		if errors.Is(err, oauth2.ErrNoAuthorizationServer) {
			return "", fmt.Errorf("%w: %s publishes no %s", ErrNoControlPlane, server, oauth2.AuthorizationServerMetadataPath)
		}
		return "", err
	}
	issuer := strings.TrimRight(strings.TrimSpace(meta.Issuer), "/")
	if issuer == "" {
		return "", fmt.Errorf("%w: %s declares an empty issuer", ErrNoControlPlane, server)
	}
	return issuer, nil
}
