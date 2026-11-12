package blobstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"cloud.google.com/go/compute/metadata"
	"google.golang.org/api/option"
	"google.golang.org/api/option/internaloption"
)

// defaultSigner replaces storage v1.68.0 bucket.go's context-less metadata
// discovery and defaultSignBytesFunc (which uses context.Background). Keep its
// credential precedence: service-account JSON, impersonated/external account
// target, then instance metadata. Local private-key signing remains local.
func (g *GCS) defaultSigner(ctx context.Context) (email string, privateKey []byte, err error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	opts := []option.ClientOption{internaloption.EnableNewAuthLibrary(), option.WithScopes("https://www.googleapis.com/auth/cloud-platform")}
	opts = append(opts, g.clientOptions...)
	creds, err := internaloption.AuthCreds(ctx, opts)
	if err != nil {
		return "", nil, fmt.Errorf("blobstore: resolving signer credentials: %w", err)
	}
	if creds != nil && len(creds.JSON()) > 0 {
		var sa struct {
			Type             string `json:"type"`
			Email            string `json:"client_email"`
			Key              string `json:"private_key"`
			ImpersonationURL string `json:"service_account_impersonation_url"`
		}
		if err := json.Unmarshal(creds.JSON(), &sa); err != nil {
			return "", nil, fmt.Errorf("blobstore: signer credential JSON: %w", err)
		}
		switch sa.Type {
		case "service_account":
			if sa.Email != "" {
				if sa.Key != "" {
					return sa.Email, []byte(sa.Key), nil
				}
				return sa.Email, nil, nil
			}
		case "external_account", "impersonated_service_account":
			start, end := strings.LastIndex(sa.ImpersonationURL, "/"), strings.LastIndex(sa.ImpersonationURL, ":")
			if start >= 0 && end > start+1 {
				return sa.ImpersonationURL[start+1 : end], nil, nil
			}
		}
	}
	// Do not gate on metadata.OnGCE: its probe has no caller context. The
	// context-aware email request itself decides availability and is cancellable.
	email, err = metadata.EmailWithContext(ctx, "default")
	if err != nil {
		return "", nil, fmt.Errorf("blobstore: discovering default signer: %w", err)
	}
	if strings.TrimSpace(email) == "" {
		return "", nil, fmt.Errorf("blobstore: metadata returned an empty signer email")
	}
	return email, nil, nil
}
