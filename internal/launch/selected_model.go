// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// ValidateSelectedModel checks an explicit picker edit without substituting a
// retired model or another provider. Tag selectors resolve normally; once a
// concrete model/provider is selected it must be exactly servable.
func (l *Launcher) ValidateSelectedModel(ctx context.Context, userID string, value interface{}) error {
	var selector models.ModelSelector
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &selector); err != nil {
		return err
	}
	available, err := drivers.LookupAvailableDrivers(ctx, userID)
	if err != nil {
		return err
	}
	resolved := resolveSelector(selector, available)
	if resolved == nil {
		return fmt.Errorf("the selected model/provider is not available on your connected accounts")
	}
	if selector.ID != "" {
		id, driver, _ := strings.Cut(selector.ID, "@")
		if id != resolved.Definition.ID || (driver != "" && driver != resolved.Provider.Driver) {
			return fmt.Errorf("the exact selected model/provider is unavailable; choose a replacement explicitly")
		}
	}
	return nil
}
