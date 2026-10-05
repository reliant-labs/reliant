// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestProvider is the reference provider: what every real one does, in the
// smallest shape that exercises the registry end to end. It signs
// HMAC-SHA256 over the public URL plus the body, so a test proves the
// verifier sees the URL the sender addressed and not the internal one.
//
// It is registered only in tests and in deployments that set
// RELIANT_TEST_INTEGRATION_SECRET (an e2e stack); never in production.
type TestProvider struct {
	secret []byte
}

// TestProviderID is the test provider's integration id.
const TestProviderID = "test"

// TestSignatureHeader carries the test provider's signature.
const TestSignatureHeader = "X-Test-Signature"

// NewTestProvider builds the test provider with a signing secret.
func NewTestProvider(secret string) *TestProvider { return &TestProvider{secret: []byte(secret)} }

// TestDelivery is the test provider's body: a handshake when Challenge is
// set, otherwise one event.
type TestDelivery struct {
	Challenge  string            `json:"challenge,omitempty"`
	ID         string            `json:"id,omitempty"`
	Account    string            `json:"account,omitempty"`
	Type       string            `json:"type,omitempty"`
	At         time.Time         `json:"at,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Data       map[string]any    `json:"data,omitempty"`
	// Resource, when set, makes the event access-gated (Event.ResourceKey).
	Resource string `json:"resource,omitempty"`
	// Revoke are access revocations the delivery carries.
	Revoke []core.IntegrationAccessRevocation `json:"revoke,omitempty"`
}

// ID implements Provider.
func (p *TestProvider) ID() string { return TestProviderID }

// SignTestDelivery is the signature a sender of a test-provider delivery puts
// in TestSignatureHeader: HMAC-SHA256 under secret over the public URL, a NUL,
// and the body.
func SignTestDelivery(secret, publicURL string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(publicURL))
	mac.Write([]byte{0})
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify implements Provider.
func (p *TestProvider) Verify(_ context.Context, req *Request) error {
	got, err := hex.DecodeString(req.Header.Get(TestSignatureHeader))
	if err != nil || len(got) == 0 {
		return fmt.Errorf("%w: missing or malformed signature", ErrUnauthorized)
	}
	want, _ := hex.DecodeString(SignTestDelivery(string(p.secret), req.PublicURL.String(), req.Body))
	if !hmac.Equal(got, want) {
		return fmt.Errorf("%w: signature mismatch", ErrUnauthorized)
	}
	return nil
}

// Parse implements Provider.
func (p *TestProvider) Parse(_ context.Context, req *Request) (*Delivery, error) {
	var d TestDelivery
	if err := json.Unmarshal(req.Body, &d); err != nil {
		return nil, fmt.Errorf("test delivery: %w", err)
	}
	if d.Challenge != "" {
		return &Delivery{Respond: &Response{Body: []byte(d.Challenge)}}, nil
	}
	out := &Delivery{Revocations: d.Revoke}
	if d.Type != "" {
		out.Events = []Event{{
			Type:        d.Type,
			AccountKey:  d.Account,
			ResourceKey: d.Resource,
			DeliveryID:  d.ID,
			OccurredAt:  d.At,
			Attributes:  d.Attributes,
			Data:        d.Data,
		}}
	}
	return out, nil
}
