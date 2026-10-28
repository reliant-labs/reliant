package automationpolicy

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestSafeDefinitionWithholdsArbitrarySensitiveLiterals(t *testing.T) {
	secret := "secret-in-untyped-field"
	row := &core.Trigger{ID: "activation", Message: secret, Name: secret, Params: map[string]any{"innocent_name": secret, "password": secret}, Presets: map[string]string{"prompt": secret}, Filter: secret, Config: []byte(`{"literal":"` + secret + `"}`)}
	result, err := SafeJSON(row)
	require.NoError(t, err)
	require.NotContains(t, result, secret)
	require.Contains(t, result, "activation")
	require.LessOrEqual(t, len(result), MaxDisplayBytes)
	row.Workflow = strings.Repeat("x", MaxDisplayBytes*2)
	result, err = SafeJSON(row)
	require.NoError(t, err)
	require.LessOrEqual(t, len(result), MaxDisplayBytes)
	require.Contains(t, result, "limit")
}
