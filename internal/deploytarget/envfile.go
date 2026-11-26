package deploytarget

import (
	"fmt"
	"os"
	"strings"

	"github.com/reliant-labs/forge/internal/envutil"
)

// ComposeProcessEnv resolves the HOST-side Compose launcher overlay, not a
// container's environment: secrets < env_file < declared runtime env. Callers
// merge this over their inherited environment. No container DNS or port rewrite
// is possible for arbitrary application values; declare host-reachable values.
func ComposeProcessEnv(spec *ComposeSpec, secrets map[string]string) (map[string]string, error) {
	fileEnv, err := loadEnvFile(composeEnvFile(spec))
	if err != nil {
		return nil, err
	}
	return mergeComposeEnv(mergeSecretsUnderEnvFile(secrets, fileEnv), spec.Env), nil
}

// mergeSecretsUnderEnvFile layers resolved secrets (from a dotenv
// secret_provider) as the BASE env map, then lets envFile entries
// override on conflict — the explicit file wins. It is a no-op (returns
// envFile unchanged) when secrets is nil/empty (the common case for
// external/none providers), preserving the pre-secrets behaviour.
func mergeSecretsUnderEnvFile(secrets, envFile map[string]string) map[string]string {
	if len(secrets) == 0 {
		return envFile
	}
	merged := make(map[string]string, len(secrets)+len(envFile))
	for k, v := range secrets {
		merged[k] = v
	}
	for k, v := range envFile {
		merged[k] = v // env_file wins
	}
	return merged
}

// loadEnvFile parses a dotenv file into the env overlay the runner merges
// onto os.Environ() before exec. Empty path means "no overlay" (returns
// nil). Missing-file is a warning rather than a hard error, so a committed
// env_file path may be optional on some dev machines.
//
// File-format errors (permission denied, malformed line) DO propagate —
// silently dropping a misconfigured file would let the deploy proceed with
// the wrong env, which is exactly the failure mode env_file is meant to
// prevent.
func loadEnvFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	path = expandHomePath(path)
	m, err := envutil.ParseDotEnv(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("  Warning: env_file %s not found — skipping (no env overlay applied).\n", path)
			return nil, nil
		}
		return nil, err
	}
	return m, nil
}

// expandHomePath expands a leading ~ or ~/ (and $HOME) to the user's home
// directory. env_file paths in KCL are commonly written with ~ (e.g.
// "~/src/app/.env"); os.ReadFile does NOT expand it (the shell normally
// would), so without this the file silently isn't found and the secret
// overlay is dropped. Non-tilde paths pass through unchanged.
func expandHomePath(path string) string {
	if path == "" {
		return path
	}
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "$HOME") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	switch {
	case path == "~":
		return home
	case strings.HasPrefix(path, "~/"):
		return home + path[1:]
	case strings.HasPrefix(path, "$HOME/"):
		return home + path[len("$HOME"):]
	case path == "$HOME":
		return home
	}
	return path
}
