package cli

import (
	"strings"
	"testing"
)

// TestValidateForgePluginMode pins the plugin's mode contract: descriptor
// is the only mode; anything else is rejected.
func TestValidateForgePluginMode(t *testing.T) {
	t.Run("descriptor is accepted", func(t *testing.T) {
		got, err := validateForgePluginMode("descriptor")
		if err != nil {
			t.Fatalf("validateForgePluginMode(descriptor) error = %v", err)
		}
		if got != forgePluginModeDescriptor {
			t.Fatalf("validateForgePluginMode(descriptor) = %q, want %q", got, forgePluginModeDescriptor)
		}
	})

	t.Run("unknown mode is rejected", func(t *testing.T) {
		_, err := validateForgePluginMode("bogus")
		if err == nil || !strings.Contains(err.Error(), "unknown mode") {
			t.Fatalf("validateForgePluginMode(bogus) error = %v, want unknown-mode error", err)
		}
	})
}
