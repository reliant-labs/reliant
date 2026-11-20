// Copyright (c) 2025 Reliant Labs
package system

import (
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// clientVersionFloor is what every per-kind floor of ClientVersionPolicy
// offers: one message per kind (their env vars differ), the same getters.
type clientVersionFloor interface {
	GetMinVersion() string
	GetUpgradeUrl() string
	GetUpgradeCommand() string
}

// clientMinimums projects the configured client version policy onto the
// public ClientMinimums Version and Info serve. The config type stays out of
// the API: the daemon's commit-time key is the gateway's alone. A kind with
// nothing configured is served an empty ClientMinimum (no floor), so a
// client never has to tell an absent message from an empty one.
func clientMinimums(policy *configv1.ClientVersionPolicy) *reliantv1.ClientMinimums {
	return &reliantv1.ClientMinimums{
		Web:      clientMinimum(policy.GetWeb()),
		Desktop:  clientMinimum(policy.GetDesktop()),
		Daemon:   clientMinimum(policy.GetDaemon()),
		Cli:      clientMinimum(policy.GetCli()),
		SdkGo:    clientMinimum(policy.GetSdkGo()),
		SdkTs:    clientMinimum(policy.GetSdkTs()),
		Enforced: policy.GetEnforce(),
	}
}

func clientMinimum(floor clientVersionFloor) *reliantv1.ClientMinimum {
	return &reliantv1.ClientMinimum{
		MinVersion:     floor.GetMinVersion(),
		UpgradeUrl:     floor.GetUpgradeUrl(),
		UpgradeCommand: floor.GetUpgradeCommand(),
	}
}
