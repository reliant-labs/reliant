package trigger

import (
	"strings"
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
)

func TestSecretDeclarationDisclosure(t *testing.T) {
	wf := &pb.Workflow{Secrets: map[string]*pb.SecretDeclaration{
		"TRADER_KEY": {Environment: lit("trader/staging")},
		"FROM_INPUT": {Environment: expr("{{ inputs.trader_env }}")},
		"FROM_EVENT": {Environment: expr("{{ inputs.event_env }}")},
		"UNRESOLVED": {Environment: expr("{{ inputs.missing }}")},
		"ODD":        {Environment: lit("has a {{ weird")},
	}}
	values := map[string]any{"trader_env": "trader/prod", "event_env": "attacker/env"}
	summary, caps := secretDeclarationDisclosure(wf, values, "wf", map[string]bool{"event_env": true})

	assert.Equal(t, []string{
		"secret:access_list/dynamic/FROM_EVENT",
		"secret:access_list/trader/prod/FROM_INPUT",
		"secret:access_list/dynamic/ODD",
		"secret:access_list/trader/staging/TRADER_KEY",
		"secret:access_list/dynamic/UNRESOLVED",
	}, caps)
	text := strings.Join(summary, "\n")
	assert.Contains(t, text, "Secret TRADER_KEY:")
	assert.Contains(t, text, "environment=trader/staging")
	assert.Contains(t, text, "environment=trader/prod")
	assert.NotContains(t, text, "attacker/env", "an event-steered environment is never printed")
	assert.Len(t, summary, 5)
}

func TestNoSecretsDiscloseNothing(t *testing.T) {
	summary, caps := secretDeclarationDisclosure(&pb.Workflow{}, nil, "wf", nil)
	assert.Empty(t, summary)
	assert.Empty(t, caps)
}
