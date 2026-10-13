// Copyright (c) 2025 Reliant Labs
//
// Package dockerengine reads and trims what docker stores on this machine,
// through the engine API on docker's local unix socket.
//
// forge:outbound-io
//
// It owns the engine's wire format (GET /system/df, POST /build/prune) and the
// docker CLI's accounting of it, and serves nothing inbound. It talks to the
// socket directly rather than through the docker CLI because on a cloud
// machine the CLI is a lazy-start shim that boots dockerd on first use, and a
// disk scan must never start a 3 GiB daemon. A socket nobody answers on is
// "docker is not running": every call fails, and the caller has nothing to
// measure.
//
//forge:not-a-component: daemon-only — built by internal/diskwatch for the daemon's disk watcher; the server never builds it
package dockerengine

import (
	"context"
	"time"
)

// Service is the slice of the engine API the daemon's disk watcher uses.
type Service interface {
	// DiskUsage is `docker system df`: what images, containers, volumes and
	// the build cache hold, and how much of each a prune could free.
	DiskUsage(ctx context.Context) (Usage, error)
	// PruneBuildCache removes build cache records unused for olderThan and
	// returns the bytes the engine reports reclaiming.
	PruneBuildCache(ctx context.Context, olderThan time.Duration) (uint64, error)
}

// Usage is docker's disk use by category.
type Usage struct {
	Images     Category
	Containers Category
	Volumes    Category
	BuildCache Category
}

// Category is what one kind of docker data holds. Reclaimable is never more
// than Bytes.
type Category struct {
	Bytes       uint64
	Reclaimable uint64
}

// Deps configures the adapter.
type Deps struct {
	// SocketPath is the engine's unix socket, e.g. /var/run/docker.sock.
	SocketPath string
}
