package cli

import (
	"context"
	"io"
	"os"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/pathshadow"
)

// forgeOnPathScan lists the Reliant Forge binaries on PATH and the versions
// they report (internal/pathshadow). A seam: TestMain replaces it, so no unit
// test runs whatever forge the developer's PATH holds.
var forgeOnPathScan = func(ctx context.Context, running string) pathshadow.Report {
	return pathshadow.New().Scan(ctx, running)
}

// warnForgeOnPath writes, to w, a warning naming every forge on PATH when one
// reports a version other than this forge's: which one a bare `forge` runs,
// and how to remove or update the stale one. It writes nothing when they all
// agree, when there is no other forge, or when FORGE_NO_PATH_CHECK is set
// (every probe sets it on the forge it asks, so a probed forge never scans in
// turn). `forge version` and `forge start` call it; both print to stdout, so
// the warning goes to stderr and their output stays parseable.
func warnForgeOnPath(ctx context.Context, w io.Writer) {
	if pathshadow.Disabled() || forgeOnPathScan == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	self, _ := os.Executable()
	rep := forgeOnPathScan(ctx, buildinfo.Current().Version)
	if msg := rep.Warning(Name(), self); msg != "" {
		_, _ = io.WriteString(w, msg)
	}
}
