package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/kclrender"
)

// Environment discovery walks `deploy/kcl/<env>/main.k`. This is the
// deploy-target-architecture source-of-truth: per-env deploy config
// lives in KCL (a `forge.K8sCluster` ref attached to each service),
// and the env list is the set of directories under `deploy/kcl/` that
// contain a `main.k`. forge.yaml no longer declares environments.

// ListEnvs returns the names of every environment declared via a
// `deploy/kcl/<env>/main.k` file. The list is sorted alphabetically
// for deterministic output. An absent `deploy/kcl/` directory yields
// an empty list (and no error) — that's the shape of a brand-new
// project, not a problem.
//
// projectDir is the project root (the directory containing
// forge.yaml). Callers can pass either projectDir or kclDir directly
// — see ListEnvsFromKCLDir for the lower-level variant.
func ListEnvs(projectDir string) ([]string, error) {
	return ListEnvsFromKCLDir(filepath.Join(projectDir, "deploy", "kcl"))
}

// ListEnvsFromKCLDir is the lower-level discovery walker. It exists
// so callers that already have the kcl root (e.g. a forge.yaml-
// configured cfg.K8s.KCLDir) can skip the projectDir join.
func ListEnvsFromKCLDir(kclDir string) ([]string, error) {
	entries, err := os.ReadDir(kclDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list envs in %s: %w", kclDir, err)
	}
	var envs []string
	for _, e := range entries {
		// A dot directory is never an env: `forge env preview create` stages
		// an instance in one before swapping it in.
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		mainK := filepath.Join(kclDir, e.Name(), "main.k")
		if _, err := os.Stat(mainK); err == nil {
			envs = append(envs, e.Name())
		}
	}
	sort.Strings(envs)
	return envs, nil
}

// EnvExists reports whether `deploy/kcl/<env>/main.k` exists. Returns
// false (with no error) for the absent-env / missing-kcl-dir cases —
// callers typically convert that into a friendly "env not configured"
// error themselves.
func EnvExists(projectDir, env string) (bool, error) {
	mainK := filepath.Join(projectDir, "deploy", "kcl", env, "main.k")
	if _, err := os.Stat(mainK); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, fmt.Errorf("stat %s: %w", mainK, err)
	}
}

// ListCommittedEnvs is ListEnvs without preview instances (`forge env preview
// create`): the envs a generated workflow names. An instance is a transient
// copy its own pipeline creates and deletes, so a workflow generated while one
// happened to exist on disk must not start deploying it.
func ListCommittedEnvs(projectDir string) ([]string, error) {
	envs, err := ListEnvs(projectDir)
	if err != nil {
		return nil, err
	}
	out := envs[:0]
	for _, env := range envs {
		if _, isPreview, _ := kclrender.ReadPreviewMarker(filepath.Join(projectDir, "deploy", "kcl", env)); !isPreview {
			out = append(out, env)
		}
	}
	return out, nil
}
