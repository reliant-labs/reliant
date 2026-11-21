package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	lazypath "kcl-lang.io/lib/go/path"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
	"github.com/reliant-labs/forge/internal/kclplugin"
)

// kclPluginCheckName is a CONTRACT, not a label: reliant's daemon reads
// `forge doctor --json` and matches this exact name to decide whether a
// machine's forge can render (reliant internal/toolexec/daemonruntime/
// cmd_forge_capability.go, forgeKCLPluginCheckName). Renaming it silently
// turns that answer into "undetermined".
const kclPluginCheckName = "forge: kcl-plugin"

// runKCLPluginDoctorCheck reports whether this forge can render an
// environment at all, by rendering the smallest possible one
// (kclplugin.Probe).
//
// The tool checks can all pass on a machine that cannot render: KCL's native
// library is extracted into a cache dir and loaded at runtime, and that can
// fail independently of every binary on PATH — antivirus quarantining
// kcl.dll, a read-only or redirected TEMP, a KCL_LIB_HOME that points
// nowhere. When it does, every `env render`/`env up`/`env deploy` fails, and
// this check is where that surfaces with a fix rather than as a KCL error.
//
// Statuses keep their established meaning for consumers: pass = renders,
// fail = cannot, skip = not applicable to this project (deploy disabled) —
// skip is never a claim that rendering works.
func runKCLPluginDoctorCheck(_ context.Context, cfg *config.ProjectConfig, projectDir, signal string) []doctor.CheckResult {
	if signal != "" {
		// `--signal` selects among deploy/metrics/traces/logs/profiles;
		// none of them is a binary self-check.
		return nil
	}
	return []doctor.CheckResult{kclPluginCheckResult(cfg, projectDir, kclplugin.Probe)}
}

// kclPluginCheckResult is the decision core, with the probe injected so the
// fail branch is testable on a machine where KCL works.
func kclPluginCheckResult(cfg *config.ProjectConfig, projectDir string, probe func() error) doctor.CheckResult {
	start := time.Now()
	result := doctor.CheckResult{Name: kclPluginCheckName}

	if !requiredWhen(func(f config.FeaturesConfig) bool { return f.DeployEnabled() })(cfg, projectDir) {
		result.Status = doctor.StatusSkip
		result.Message = "not required for this project"
		result.Duration = time.Since(start)
		return result
	}
	if err := probe(); err != nil {
		result.Status = doctor.StatusFail
		result.Message = "KCL cannot evaluate here — no environment can render"
		result.Evidence = kclPluginFailureEvidence(err)
		result.Duration = time.Since(start)
		return result
	}
	result.Status = doctor.StatusPass
	result.Message = "KCL runtime loaded and kcl_plugin.forge answered"
	result.Evidence = "evaluated a one-line program calling kcl_plugin.forge.probe"
	result.Duration = time.Since(start)
	return result
}

// kclPluginFailureEvidence is the runbook for a failed probe: what was
// attempted, where the native library lives, and how to move it.
func kclPluginFailureEvidence(err error) string {
	return strings.Join([]string{
		"expected: a one-line KCL program calling kcl_plugin.forge.probe evaluates",
		"found:    " + strings.TrimSpace(err.Error()),
		"cause:    usually KCL's native library could not be extracted or loaded from " + kclLibDir(),
		"          (antivirus quarantine, a read-only or redirected temp dir, or a bad KCL_LIB_HOME)",
		"fix:      allow the library through antivirus, or point KCL_LIB_HOME at a writable directory",
		"          forge may load native code from, then re-run `forge doctor`",
	}, "\n")
}

// kclLibDir is where kcl-lang.io/lib extracts its native library:
// KCL_LIB_HOME when set, else <CacheHome>/kcl (lib/go/native/loader.go).
func kclLibDir() string {
	if v := os.Getenv("KCL_LIB_HOME"); v != "" {
		return v + " (KCL_LIB_HOME)"
	}
	home, err := lazypath.CacheHomeWithError()
	if err != nil || home == "" {
		return "the KCL cache directory (could not be determined)"
	}
	return filepath.Join(home, "kcl")
}
