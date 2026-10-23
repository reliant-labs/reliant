package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// envProbeConfigImport is how a probe written by writeEnvProbe imports its
// env's config.k: the probe sits one directory below the env's.
const envProbeConfigImport = "..config"

// writeEnvProbe writes a throwaway KCL probe for one env and returns its path
// and the cleanup that removes it.
//
// The probe goes in a private subdirectory of the env's directory
// (deploy/kcl/<env>/.forge-probe-*/<name>.k), never into the env's directory
// itself. A KCL package is the .k files of ONE directory, so a probe beside
// main.k was, for as long as it existed, a file of the env's own package:
// a concurrent `forge env render <env>` in the same checkout listed it,
// the probe's owner removed it, and that render failed with "Failed to
// load KCL file '.../zz_forge_config_probe_<pid>.k'" (control-plane's gate,
// one render-test package racing another), or evaluated the probe's
// top-level output into its own. A subdirectory is a package nothing else
// loads. It stays under deploy/kcl/<env>, so the project's kcl.mod and every
// package-root import (`import config_gen`) resolve exactly as before; the
// env's config.k is `import ..config` from there.
func writeEnvProbe(envDir, name, src string) (string, func(), error) {
	dir, err := os.MkdirTemp(envDir, ".forge-probe-")
	if err != nil {
		return "", nil, fmt.Errorf("create probe dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, name+".k")
	if err := atomicfile.WriteFile(path, []byte(src), 0o644); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}
