package launch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveResetsOnlyTouchesResetKeys(t *testing.T) {
	baseline := map[string]interface{}{"mode": "manual", "model": "preset-model", "tools": []string{"preset-tool"}, "agent": map[string]interface{}{"mode": "auto", "limit": float64(4)}}
	update := map[string]interface{}{"mode": nil, "temperature": 0.2}
	require.NoError(t, ResolveResetsFromBaseline(update, baseline))
	assert.Equal(t, map[string]interface{}{"mode": "manual", "temperature": 0.2}, update)
	group := map[string]interface{}{"agent": nil}
	require.NoError(t, ResolveResetsFromBaseline(group, baseline))
	assert.Equal(t, map[string]interface{}{"agent": baseline["agent"]}, group)
}

func TestResolveResetWithoutDefaultFails(t *testing.T) {
	for _, baseline := range []map[string]interface{}{nil, {"mode": nil}} {
		update := map[string]interface{}{"mode": nil}
		require.Error(t, ResolveResetsFromBaseline(update, baseline))
	}
}
