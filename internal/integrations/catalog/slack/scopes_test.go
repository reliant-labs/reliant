package slack_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
)

// historyScopeFor is the bot scope Slack requires before it will deliver each
// message event type. Slack refuses to SAVE an event subscription whose scope
// the app lacks, so an event declared here without its scope is not a missing
// feature — it is an Events page the operator cannot save.
var historyScopeFor = map[string]string{
	"message.channels": "channels:history",
	"message.groups":   "groups:history",
	"message.im":       "im:history",
	"message.mpim":     "mpim:history",
}

// Every message event a trigger declares must come with the scope Slack
// requires for it, and every declared message event must have a known scope.
func TestEveryDeclaredMessageEventHasItsScope(t *testing.T) {
	slack := shippedManifest(t, "slack")
	scopes := map[string]bool{}
	for _, method := range slack.GetConnection().GetAuth() {
		for _, s := range method.GetOauth2().GetScopes() {
			scopes[s] = true
		}
	}
	for _, trig := range slack.GetTriggers() {
		for _, event := range trig.GetEvents() {
			if !strings.HasPrefix(event, "message.") {
				continue
			}
			scope, known := historyScopeFor[event]
			require.Truef(t, known, "trigger %s declares %s, which has no known history scope; add it to historyScopeFor", trig.GetId(), event)
			require.Truef(t, scopes[scope], "trigger %s declares %s but the manifest does not request %s, so Slack will refuse the event subscription", trig.GetId(), event, scope)
		}
	}
}

// conversations.list accepts every conversation type in its `types` param, and
// Slack answers missing_scope for a type whose read scope the app lacks.
var readScopeFor = map[string]string{
	"public_channel":  "channels:read",
	"private_channel": "groups:read",
	"im":              "im:read",
	"mpim":            "mpim:read",
}

func TestEveryListableConversationTypeHasItsScope(t *testing.T) {
	slack := shippedManifest(t, "slack")
	scopes := map[string]bool{}
	for _, method := range slack.GetConnection().GetAuth() {
		for _, s := range method.GetOauth2().GetScopes() {
			scopes[s] = true
		}
	}
	for kind, scope := range readScopeFor {
		require.Truef(t, scopes[scope], "conversations.list offers types=%s but the manifest does not request %s", kind, scope)
	}
}

func shippedManifest(t *testing.T, id string) *reliantv1.IntegrationManifest {
	t.Helper()
	for _, m := range catalog.MustBuiltin().Manifests() {
		if m.GetId() == id {
			return m
		}
	}
	t.Fatalf("manifest %q is not in the built-in catalog", id)
	return nil
}
