// Copyright (c) 2026 Reliant Labs

package credsandbox

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The control-plane pipeline's credentialed steps fail closed on anything but
// exactly this name with a version of at least 2: changing either stops them.
func TestSignal_NameAndVersionAreTheContract(t *testing.T) {
	assert.Equal(t, "RELIANT_CREDSANDBOX_VERSION", SignalEnv)
	assert.Equal(t, 2, SignalVersion)
	assert.Equal(t, ".reliant/cred-scratch", filepath.ToSlash(CredScratchRel))
}

func TestWithSignal_SetsItOnlyWhenTheSandboxIsInForce(t *testing.T) {
	env := []string{"A=1", SignalEnv + "=7", "B=2"}

	assert.Equal(t, []string{"A=1", "B=2", SignalEnv + "=2"}, withSignal(env, true))
	assert.Equal(t, []string{"A=1", "B=2"}, withSignal(env, false),
		"an inherited value says nothing about this daemon's sandbox")
}

func TestDefaultPlan_HidesAndSealsTheCredentialedScratch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := DefaultPlan()
	scratch := filepath.Join(home, ".reliant", "cred-scratch")
	assert.Contains(t, p.Hide, scratch)
	assert.Contains(t, p.Seal, scratch)
}
