package doctor

import (
	"strings"
	"testing"
)

// The compose check told the reader to run `docker compose up -d`, which
// starts every service in the file outside the environment that declares
// which ones run and with what values.
func TestComposeStartHintGoesThroughForge(t *testing.T) {
	for _, env := range []string{"dev", ""} {
		got := composeStartHint(env)
		if strings.Contains(got, "docker compose up") {
			t.Errorf("hint routes around forge: %q", got)
		}
		if !strings.Contains(got, "forge env up") {
			t.Errorf("hint does not name the forge command: %q", got)
		}
	}
	if got := composeStartHint("dev"); !strings.Contains(got, "forge env up dev") {
		t.Errorf("hint should name the env being checked: %q", got)
	}
}
