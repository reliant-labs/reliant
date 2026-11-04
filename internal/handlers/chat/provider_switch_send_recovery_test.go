// Copyright (c) 2025 Reliant Labs
package chat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// A send can recover an unservable model onto a connected provider after
// building inputs. Recompute an existing binding from those final inputs, not
// from the stale selected model. Unbound sends must remain unbound.
func TestProviderSwitchRebindingUsesFinalSendInputs(t *testing.T) {
	ctx, _, _, _, svc := selectionFixture(t, db.Active())
	inputs := map[string]interface{}{
		"model":                   map[string]interface{}{"id": sonnetOnClaude},
		v2.InputKeyProviderSwitch: "codex",
	}
	svc.launcher().RefreshSelectedProvider(ctx, "test-user", inputs)
	require.Equal(t, "codex", inputs[v2.InputKeyProviderSwitch], "ordinary recovery cannot override an explicit provider binding")
	unbound := map[string]interface{}{"model": map[string]interface{}{"id": sonnetOnClaude}}
	svc.launcher().RefreshSelectedProvider(ctx, "test-user", unbound)
	require.NotContains(t, unbound, v2.InputKeyProviderSwitch)
}
