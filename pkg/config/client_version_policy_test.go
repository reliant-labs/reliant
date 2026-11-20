package config_test

import (
	"testing"

	forgeconfig "github.com/reliant-labs/forge/pkg/config"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/pkg/config"
)

// A client floor is semver without a leading v, or empty (no floor). The
// loader refuses anything else as the config loads, before a server role
// serves a floor its clients cannot compare against.
func TestClientVersionPolicyValidatesFloors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		minVersion string
		upgradeURL string
		commitTime int64
		wantErr    bool
	}{
		{name: "unset", wantErr: false},
		{name: "release", minVersion: "1.9.0"},
		{name: "prerelease", minVersion: "2.0.0-rc.1"},
		{name: "dev build with metadata", minVersion: "1.8.2-dev+abc123"},
		{name: "upgrade url", minVersion: "1.9.0", upgradeURL: "https://reliantlabs.io/download"},
		{name: "commit-time floor", commitTime: 1_763_000_000},
		{name: "leading v", minVersion: "v1.9.0", wantErr: true},
		{name: "two components", minVersion: "1.9", wantErr: true},
		{name: "leading zero", minVersion: "01.9.0", wantErr: true},
		{name: "garbage", minVersion: "latest", wantErr: true},
		{name: "relative upgrade url", upgradeURL: "reliantlabs.io/download", wantErr: true},
		{name: "negative commit time", commitTime: -1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.ServerProcessConfig{ClientVersions: &configv1.ClientVersionPolicy{
				DaemonMinVersion:        tc.minVersion,
				DaemonUpgradeUrl:        tc.upgradeURL,
				DaemonMinCommitTimeUnix: tc.commitTime,
				SdkTsMinVersion:         tc.minVersion,
			}}
			err := forgeconfig.Validate(cfg)
			if tc.wantErr && err == nil {
				t.Fatalf("Validate(%q, %q, %d) = nil, want an error", tc.minVersion, tc.upgradeURL, tc.commitTime)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate(%q, %q, %d) = %v, want nil", tc.minVersion, tc.upgradeURL, tc.commitTime, err)
			}
		})
	}
}
