// Copyright (c) 2025 Reliant Labs
package system

import (
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// clientMinimums projects the configured client version policy onto the
// public ClientMinimums Version and Info serve. The config type stays out of
// the API: the daemon's commit-time key is the gateway's alone. A kind with
// nothing configured is served an empty ClientMinimum (no floor), so a
// client never has to tell an absent message from an empty one.
func clientMinimums(p *configv1.ClientVersionPolicy) *reliantv1.ClientMinimums {
	return &reliantv1.ClientMinimums{
		Web:      clientMinimum(p.GetWebMinVersion(), p.GetWebUpgradeUrl(), p.GetWebUpgradeCommand()),
		Desktop:  clientMinimum(p.GetDesktopMinVersion(), p.GetDesktopUpgradeUrl(), p.GetDesktopUpgradeCommand()),
		Daemon:   clientMinimum(p.GetDaemonMinVersion(), p.GetDaemonUpgradeUrl(), p.GetDaemonUpgradeCommand()),
		Cli:      clientMinimum(p.GetCliMinVersion(), p.GetCliUpgradeUrl(), p.GetCliUpgradeCommand()),
		SdkGo:    clientMinimum(p.GetSdkGoMinVersion(), p.GetSdkGoUpgradeUrl(), p.GetSdkGoUpgradeCommand()),
		SdkTs:    clientMinimum(p.GetSdkTsMinVersion(), p.GetSdkTsUpgradeUrl(), p.GetSdkTsUpgradeCommand()),
		Enforced: p.GetEnforce(),
	}
}

func clientMinimum(minVersion, upgradeURL, upgradeCommand string) *reliantv1.ClientMinimum {
	return &reliantv1.ClientMinimum{
		MinVersion:     minVersion,
		UpgradeUrl:     upgradeURL,
		UpgradeCommand: upgradeCommand,
	}
}
