// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
)

// cloudPlatformScope is the OAuth scope Vertex AI calls need.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// googleTokenURI is the only token endpoint a connected key may name.
// #nosec G101 -- OAuth endpoint URL, not a credential
const googleTokenURI = "https://oauth2.googleapis.com/token"

// ErrNoVertexCredential is returned for a Vertex AI driver with no credential
// of the account's. Vertex AI calls run on the multi-tenant server roles, so
// there is deliberately no fallback to the server's own Google credentials
// (Application Default Credentials, the metadata server, a key file).
var ErrNoVertexCredential = errors.New("vertex ai needs a service-account key: connect Vertex AI from Settings")

// accountCredential is a connected Vertex AI account: the project its
// service-account key belongs to, and the credentials that sign its calls.
type accountCredential struct {
	project string
	creds   *auth.Credentials
}

// serviceAccountKey is the part of a Google service-account key file this
// package checks before handing the key to the auth library.
type serviceAccountKey struct {
	Type           string `json:"type"`
	ProjectID      string `json:"project_id"`
	ClientEmail    string `json:"client_email"`
	PrivateKey     string `json:"private_key"`
	TokenURI       string `json:"token_uri"`
	UniverseDomain string `json:"universe_domain"`
}

// parseAccountCredential turns the credential a user or org connected (the
// JSON of a Google service-account key) into signing credentials.
//
// Only a service-account key is accepted. A workload-identity
// (external_account) configuration gets its subject token from a file, a URL
// or a program named in the config, which on a server role would be the
// server's own files, metadata server or processes: a credential of the
// process by another route. An authorized_user file is a person's gcloud
// login, not an account to share. The key's token_uri and universe_domain are
// pinned to Google's, as Google advises for externally sourced credentials,
// so a key cannot make the server send a signed assertion elsewhere. Tokens
// are self-signed JWTs, so no token endpoint is called at all.
func parseAccountCredential(raw string) (*accountCredential, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrNoVertexCredential
	}
	var key serviceAccountKey
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return nil, fmt.Errorf("vertex ai credential is not a service-account key (JSON): %w", err)
	}
	switch {
	case key.Type != string(credentials.ServiceAccount):
		return nil, fmt.Errorf("vertex ai credential must be a service-account key (type %q), got type %q", credentials.ServiceAccount, key.Type)
	case strings.TrimSpace(key.ProjectID) == "":
		return nil, errors.New("vertex ai service-account key has no project_id")
	case key.ClientEmail == "" || key.PrivateKey == "":
		return nil, errors.New("vertex ai service-account key has no client_email or private_key")
	case key.TokenURI != "" && key.TokenURI != googleTokenURI:
		return nil, fmt.Errorf("vertex ai service-account key names token_uri %q; only %s is accepted", key.TokenURI, googleTokenURI)
	case key.UniverseDomain != "" && key.UniverseDomain != "googleapis.com":
		return nil, fmt.Errorf("vertex ai service-account key names universe_domain %q; only googleapis.com is supported", key.UniverseDomain)
	}

	parsed, err := credentials.NewCredentialsFromJSON(credentials.ServiceAccount, []byte(raw), &credentials.DetectOptions{
		Scopes:           []string{cloudPlatformScope},
		UseSelfSignedJWT: true,
	})
	if err != nil {
		return nil, fmt.Errorf("vertex ai service-account key: %w", err)
	}
	project := strings.TrimSpace(key.ProjectID)
	// Rebuilt with every property fixed: auth.Credentials otherwise lets
	// GOOGLE_CLOUD_PROJECT and GOOGLE_CLOUD_QUOTA_PROJECT in the server's
	// environment override the key's project.
	return &accountCredential{
		project: project,
		creds: auth.NewCredentials(&auth.CredentialsOptions{
			TokenProvider:          parsed.TokenProvider,
			ProjectIDProvider:      fixedProperty(project),
			QuotaProjectIDProvider: fixedProperty(project),
			UniverseDomainProvider: fixedProperty("googleapis.com"),
		}),
	}, nil
}

// fixedProperty is an auth.CredentialsPropertyProvider that always answers v.
func fixedProperty(v string) auth.CredentialsPropertyProvider {
	return auth.CredentialsPropertyFunc(func(context.Context) (string, error) { return v, nil })
}
