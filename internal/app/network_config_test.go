// Copyright (c) 2025 Reliant Labs
package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// An empty control-plane URL is what a misconfigured hosted deployment looks
// like, so it must never open the private network. Only the named opt-in does.
func TestAllowPrivateEgress_FailsClosedInEveryServerRoleShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *configv1.ServerProcessConfig
		want bool
	}{
		{"nothing configured", &configv1.ServerProcessConfig{}, false},
		{"empty control plane", &configv1.ServerProcessConfig{ControlPlane: &configv1.ControlPlaneConfig{}}, false},
		{"empty control plane, empty network", &configv1.ServerProcessConfig{ControlPlane: &configv1.ControlPlaneConfig{}, Network: &configv1.NetworkConfig{}}, false},
		{"hosted", &configv1.ServerProcessConfig{ControlPlane: &configv1.ControlPlaneConfig{Url: "http://cp.test:8090"}}, false},
		{"explicit opt-in, no control plane", &configv1.ServerProcessConfig{Network: &configv1.NetworkConfig{AllowPrivateEgress: true}}, true},
		{"explicit opt-in, hosted", &configv1.ServerProcessConfig{ControlPlane: &configv1.ControlPlaneConfig{Url: "http://cp.test:8090"}, Network: &configv1.NetworkConfig{AllowPrivateEgress: true}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AllowPrivateEgress(tc.cfg.GetNetwork())
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.want, netguard.ForDeployment(got).AllowPrivate)
		})
	}
}

func TestWarnPrivateEgress_LogsOnlyWhenOn(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	warnPrivateEgress(nil, log)
	warnPrivateEgress(&configv1.NetworkConfig{}, log)
	assert.Empty(t, buf.String(), "a closed policy is the default and says nothing")

	warnPrivateEgress(&configv1.NetworkConfig{AllowPrivateEgress: true}, log)
	out := buf.String()
	assert.Contains(t, out, "level=WARN")
	assert.Contains(t, out, "allow_private_egress is ON")
	assert.Equal(t, 1, strings.Count(out, "\n"))
}
