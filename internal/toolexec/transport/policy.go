package transport

import "time"

const (
	DaemonHeartbeatInterval = 30 * time.Second
	ReconnectMinDelay       = 1 * time.Second
	ReconnectMaxDelay       = 15 * time.Second
	WatchPollInterval       = 2 * time.Second

	ServerReadHeaderTimeout = 10 * time.Second
	ServerIdleTimeout       = 120 * time.Second
)
