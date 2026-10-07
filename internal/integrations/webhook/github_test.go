// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // G505: builds the legacy SHA-1 header a test proves is NOT accepted
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

const gitHubTestSecret = "whsec-test"

func gitHubFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("github", "testdata", name+".json"))
	require.NoError(t, err)
	return b
}

// SignGitHub is what GitHub puts in X-Hub-Signature-256.
func signGitHub(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func gitHubRequest(event, delivery string, body []byte, signature string) *Request {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if event != "" {
		h.Set("X-GitHub-Event", event)
	}
	if delivery != "" {
		h.Set("X-GitHub-Delivery", delivery)
	}
	if signature != "" {
		h.Set("X-Hub-Signature-256", signature)
	}
	u, _ := url.Parse("https://reliant.example.com/integrations/github/events")
	return &Request{PublicURL: u, Method: http.MethodPost, Header: h, Body: body, ReceivedAt: time.Now()}
}

func TestGitHubVerify(t *testing.T) {
	p := NewGitHubProvider(gitHubTestSecret)
	body := gitHubFixture(t, "issues.opened")
	ctx := context.Background()
	assert.Equal(t, "github", p.ID())

	assert.NoError(t, p.Verify(ctx, gitHubRequest("issues", "d1", body, signGitHub(gitHubTestSecret, body))))

	for name, sig := range map[string]string{
		"missing":       "",
		"wrong secret":  signGitHub("other", body),
		"no prefix":     strings.TrimPrefix(signGitHub(gitHubTestSecret, body), "sha256="),
		"sha1 prefix":   "sha1=" + strings.Repeat("a", 40),
		"not hex":       "sha256=zz",
		"other body":    signGitHub(gitHubTestSecret, []byte(`{"action":"opened"}`)),
		"truncated mac": signGitHub(gitHubTestSecret, body)[:20],
	} {
		t.Run(name, func(t *testing.T) {
			err := p.Verify(ctx, gitHubRequest("issues", "d1", body, sig))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUnauthorized)
		})
	}

	// The legacy SHA-1 X-Hub-Signature, correctly computed, is not enough.
	req := gitHubRequest("issues", "d1", body, "")
	mac := hmac.New(sha1.New, []byte(gitHubTestSecret))
	mac.Write(body)
	req.Header.Set("X-Hub-Signature", "sha1="+hex.EncodeToString(mac.Sum(nil)))
	assert.ErrorIs(t, p.Verify(ctx, req), ErrUnauthorized)

	// An empty secret verifies nothing.
	assert.ErrorIs(t, NewGitHubProvider("").Verify(ctx, gitHubRequest("issues", "d1", body, signGitHub("", body))), ErrUnauthorized)
}

func TestGitHubParse(t *testing.T) {
	p := NewGitHubProvider(gitHubTestSecret)
	ctx := context.Background()

	ping, err := p.Parse(ctx, gitHubRequest("ping", "d-ping", gitHubFixture(t, "ping"), ""))
	require.NoError(t, err)
	require.NotNil(t, ping.Respond, "ping is a handshake")
	assert.Empty(t, ping.Events)

	d, err := p.Parse(ctx, gitHubRequest("issues", "72d3162e-cc78-11e3-81ab-4c9367dc0958", gitHubFixture(t, "issues.opened"), ""))
	require.NoError(t, err)
	require.Len(t, d.Events, 1)
	ev := d.Events[0]
	assert.Equal(t, "issues.opened", ev.Type)
	assert.Equal(t, "98765", ev.AccountKey)
	assert.Equal(t, "123456", ev.ResourceKey)
	assert.Equal(t, "72d3162e-cc78-11e3-81ab-4c9367dc0958", ev.DeliveryID, "X-GitHub-Delivery is the dedupe key")

	_, err = p.Parse(ctx, gitHubRequest("issues", "", gitHubFixture(t, "issues.opened"), ""))
	assert.Error(t, err, "without a delivery id a redelivery would fire twice")
	_, err = p.Parse(ctx, gitHubRequest("", "d1", gitHubFixture(t, "issues.opened"), ""))
	assert.Error(t, err)

	rev, err := p.Parse(ctx, gitHubRequest("installation", "d-x", gitHubFixture(t, "installation.deleted"), ""))
	require.NoError(t, err)
	assert.Len(t, rev.Revocations, 1)
}

func TestRegistryFromEnvRegistersGitHubOnItsSecret(t *testing.T) {
	r, err := RegistryFromEnv(func(string) string { return "" })
	require.NoError(t, err)
	assert.False(t, r.HasInboundSource("github"), "no secret, no provider: a delivery could not be verified")

	r, err = RegistryFromEnv(func(k string) string {
		if k == "RELIANT_GITHUB_WEBHOOK_SECRET" {
			return " s3cret "
		}
		return ""
	})
	require.NoError(t, err)
	p, ok := r.Provider("github")
	require.True(t, ok)
	body := []byte(`{}`)
	assert.NoError(t, p.Verify(context.Background(), gitHubRequest("issues", "d", body, signGitHub("s3cret", body))),
		"the secret is trimmed")
}

// Every event GitHub's manifest declares, recorded from its fixture,
// validates against the trigger.payload schema the manifest declares for it.
// Workflow validation type-checks a trigger's filter, inputs and prompt
// against that schema (issue.labels is a list of label NAMES, so `l.name` is
// rejected), so a normalizer that drifted from it would make validation
// reject expressions that run, or accept ones that cannot.
func TestGitHubPayloadsMatchTheManifestTriggers(t *testing.T) {
	var m *reliantv1.IntegrationManifest
	for _, cand := range catalog.MustBuiltin().Manifests() {
		if cand.GetId() == "github" {
			m = cand
		}
	}
	require.NotNil(t, m, "the catalog ships the github manifest")

	p := NewGitHubProvider(gitHubTestSecret)
	for _, tr := range m.GetTriggers() {
		for _, evType := range tr.GetEvents() {
			t.Run(evType, func(t *testing.T) {
				header, _, _ := strings.Cut(evType, ".")
				d, err := p.Parse(context.Background(), gitHubRequest(header, "d-"+evType, gitHubFixture(t, evType), ""))
				require.NoError(t, err)
				require.Len(t, d.Events, 1)
				require.Equal(t, evType, d.Events[0].Type)

				payload := toInbound("github", &core.Trigger{ID: "t"}, d.Events[0]).Payload
				schemaMap := manifest.TriggerPayloadSchema(m, tr)
				// This pins TYPES, which is what validation checks against. Not
				// attribute presence: the envelope requires every declared
				// attribute, but a push sets branch or tag, never both.
				delete(schemaMap["properties"].(map[string]any)["attributes"].(map[string]any), "required")
				rawSchema, err := json.Marshal(schemaMap)
				require.NoError(t, err)
				var schema jsonschema.Schema
				require.NoError(t, json.Unmarshal(rawSchema, &schema))
				resolved, err := schema.Resolve(nil)
				require.NoError(t, err)
				raw, err := json.Marshal(payload)
				require.NoError(t, err)
				var asJSON any
				require.NoError(t, json.Unmarshal(raw, &asJSON))
				assert.NoError(t, resolved.Validate(asJSON), "payload of %s against %s's schema", evType, tr.GetId())
			})
		}
	}
}
