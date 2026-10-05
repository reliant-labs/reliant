// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: wire/validation helpers for custom model endpoints; the credential seam is declared here at its consumer
package modelendpoints

import (
	"context"
	"errors"
)

// ErrCredentialStoreUnavailable is returned when an endpoint needs a stored
// credential and this deployment has no sealed credential store wired in.
var ErrCredentialStoreUnavailable = errors.New("API keys for custom endpoints need the sealed credential store; coming with the next sync")

// EndpointCredentials is where an endpoint's API key and header VALUES live.
// They have exactly one home: the sealed connection store, never the
// model_endpoints table (which holds only credential_connection_id and header
// NAMES).
//
// MAIN-SIDE IMPLEMENTATION SPEC (origin/main's `connections` +
// `connection_secrets`, vault-sealed):
//
//   - Put creates (or updates, when the endpoint already has a connection) ONE
//     connection for the endpoint with auth_kind = 'api_key', owned by userID,
//     named after the endpoint. The API key is sealed into connection_secrets
//     via the vault under the user's DEK; each header value is sealed the same
//     way, keyed by header name. apiKey == nil keeps the stored key, a pointer
//     to "" removes it; a header mapped to "" is removed, an absent header is
//     kept. Put returns the connection id, which the caller stores in
//     model_endpoints.credential_connection_id. When nothing remains (no key
//     and no headers) Put deletes the connection and returns "".
//   - Get unseals and returns the key and header values. It MUST verify the
//     connection belongs to userID: userID is the authorization boundary, not
//     an optimization. An unknown or foreign connectionID returns an error
//     indistinguishable from "not found".
//   - Delete removes the connection and its sealed secrets (purge, not
//     soft-delete). Deleting an already-missing connection is not an error.
//
// Plaintext must never be logged and never reach a table in this schema.
type EndpointCredentials interface {
	Put(ctx context.Context, userID, endpointID string, apiKey *string, headers map[string]string) (connectionID string, err error)
	Get(ctx context.Context, userID, connectionID string) (apiKey string, headers map[string]string, err error)
	Delete(ctx context.Context, userID, connectionID string) error
}

// NotAvailableCredentials is the only implementation in a branch that has no
// sealed store. It refuses to hold a secret rather than hold it somewhere
// else: Put with a key or any header value fails, so nothing a user typed is
// silently dropped or stored in plaintext.
type NotAvailableCredentials struct{}

// Put fails whenever there is a secret to store. Clearing (empty key, empty
// values) is accepted: there is nothing stored to clear.
func (NotAvailableCredentials) Put(_ context.Context, _, _ string, apiKey *string, headers map[string]string) (string, error) {
	if apiKey != nil && *apiKey != "" {
		return "", ErrCredentialStoreUnavailable
	}
	for _, v := range headers {
		if v != "" {
			return "", ErrCredentialStoreUnavailable
		}
	}
	return "", nil
}

// Get always fails: nothing is ever stored.
func (NotAvailableCredentials) Get(context.Context, string, string) (string, map[string]string, error) {
	return "", nil, ErrCredentialStoreUnavailable
}

// Delete is a no-op: nothing is ever stored.
func (NotAvailableCredentials) Delete(context.Context, string, string) error { return nil }
