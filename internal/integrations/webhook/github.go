// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
)

// GitHubProvider receives the GitHub App's webhook at
// POST <PUBLIC_URL>/integrations/github/events.
//
// There is one App (control-plane's) and one webhook URL for every
// installation; its secret is this deployment's RELIANT_GITHUB_WEBHOOK_SECRET.
// Verification is X-Hub-Signature-256; the dedupe key is X-GitHub-Delivery,
// which redeliveries reuse; `ping` is answered and records nothing. What a
// delivery means — event type, routing keys, trimmed payload, revocations —
// is the github package's.
type GitHubProvider struct {
	secret []byte
}

// gitHubSignature is how GitHub signs: HMAC-SHA256 over the raw body, hex,
// behind "sha256=".
var gitHubSignature = &core.WebhookHMACConfig{
	Header: github.SignatureHeader, Algorithm: "sha256", Prefix: github.SignaturePrefix, Encoding: "hex",
}

// NewGitHubProvider builds the provider over the App's webhook secret.
func NewGitHubProvider(secret string) *GitHubProvider {
	return &GitHubProvider{secret: []byte(secret)}
}

// ID implements Provider.
func (p *GitHubProvider) ID() string { return github.ProviderID }

// Verify implements Provider: X-Hub-Signature-256 over the raw body, in
// constant time. The legacy SHA-1 X-Hub-Signature is never accepted.
func (p *GitHubProvider) Verify(_ context.Context, req *Request) error {
	sig := req.Header.Get(github.SignatureHeader)
	if sig == "" {
		return fmt.Errorf("%w: no %s", ErrUnauthorized, github.SignatureHeader)
	}
	if !VerifyHMAC(gitHubSignature, p.secret, req.Body, sig) {
		return fmt.Errorf("%w: %s does not match", ErrUnauthorized, github.SignatureHeader)
	}
	return nil
}

// Parse implements Provider.
func (p *GitHubProvider) Parse(_ context.Context, req *Request) (*Delivery, error) {
	parsed, err := github.Parse(req.Header.Get(github.EventHeader), req.Body, req.ReceivedAt)
	if err != nil {
		return nil, err
	}
	if parsed.Ping {
		return &Delivery{Respond: &Response{Body: []byte("pong\n")}}, nil
	}
	delivery := strings.TrimSpace(req.Header.Get(github.DeliveryHeader))
	if delivery == "" {
		// The delivery id is the dedupe key: without it a redelivery would
		// fire twice, so nothing is accepted that lacks one.
		return nil, fmt.Errorf("github: no %s header", github.DeliveryHeader)
	}
	out := &Delivery{Revocations: parsed.Revocations}
	for _, ev := range parsed.Events {
		out.Events = append(out.Events, Event{
			Type:        ev.Type,
			AccountKey:  ev.AccountKey,
			ResourceKey: ev.ResourceKey,
			DeliveryID:  delivery,
			OccurredAt:  ev.OccurredAt,
			Attributes:  ev.Attributes,
			Data:        ev.Data,
			Sender:      ev.Sender,
		})
	}
	return out, nil
}

var _ Provider = (*GitHubProvider)(nil)
