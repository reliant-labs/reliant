package cli

import (
	"strings"
	"testing"
)

// TestValidateForgePluginMode pins the plugin's mode contract after the
// entity-proto retirement: descriptor is the only mode; mode=orm fails
// with the dedicated removal message (legacy buf templates must learn
// the migration path, not get a generic "unknown mode"); anything else
// is rejected.
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

	t.Run("orm errors with the removal message", func(t *testing.T) {
		_, err := validateForgePluginMode("orm")
		if err == nil {
			t.Fatal("validateForgePluginMode(orm) must error — mode=orm was removed")
		}
		for _, want := range []string{"mode=orm was removed", "db/migrations", "forge generate"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("mode=orm error %q must mention %q", err.Error(), want)
			}
		}
	})

	t.Run("unknown mode is rejected", func(t *testing.T) {
		_, err := validateForgePluginMode("bogus")
		if err == nil || !strings.Contains(err.Error(), "unknown mode") {
			t.Fatalf("validateForgePluginMode(bogus) error = %v, want unknown-mode error", err)
		}
	})
}
