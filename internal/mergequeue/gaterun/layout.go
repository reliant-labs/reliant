// Copyright (c) 2025 Reliant Labs
package gaterun

// The boundary's filesystem layout, the same on every backend. The work
// volume is mounted at WorkMount; the stage script finds it through
// EnvGateRoot. They are wire facts (GateEnv names them), so they live with
// the wire; the backends that mount them are the daemon's (package gatehost).
const (
	WorkMount    = "/gate"
	SrcDir       = WorkMount + "/src"
	OutDir       = WorkMount + "/out"
	ArtifactsDir = WorkMount + "/artifacts"
	HomeDir      = WorkMount + "/home"
	TmpDir       = WorkMount + "/tmp"
	// VulnDBMount is where the machine's grype database is mounted,
	// read-only, for the sbom_scan stage.
	VulnDBMount = "/vulndb"
	// CacheMount is where a gate's toolchain caches live (the gate image
	// points GOCACHE, GOMODCACHE, the pnpm store and XDG_CACHE_HOME under
	// it). With Spec.CacheVolume it is the repository's warm cache volume;
	// without, a directory on the per-gate work volume.
	CacheMount = WorkMount + "/cache"
	// TrustedGoCacheMount is where the machine's trusted Go build cache is
	// mounted, read-only. The gate image's GOCACHEPROG
	// (`forge cache prog --trusted /gate/trusted-go-build`) reads it before
	// GOCACHE and writes only to GOCACHE; with nothing mounted there it runs
	// on GOCACHE alone.
	TrustedGoCacheMount = WorkMount + "/trusted-go-build"
)
