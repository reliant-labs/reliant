package settings

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/stretchr/testify/assert"
)

// automation.approvals is read by the server, so a value it would misread is
// refused at write time; every other key stays free-form.
func TestValidateSettingValue(t *testing.T) {
	for _, mode := range []string{"always", "on_capability_change", "never"} {
		assert.NoError(t, validateSettingValue(automationpolicy.ApprovalSettingKey, mode))
	}
	assert.Error(t, validateSettingValue(automationpolicy.ApprovalSettingKey, "nevr"))
	assert.Error(t, validateSettingValue(automationpolicy.ApprovalSettingKey, ""))
	assert.NoError(t, validateSettingValue("ui.theme", "anything"))
}
