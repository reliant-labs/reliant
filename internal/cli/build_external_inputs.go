// build_external_inputs.go decides whether a ShellBuild that declares its
// `inputs` can be skipped: its fingerprint (command, cwd, env, platform,
// destination and the content of every input) equals the one its last
// SUCCESSFUL build recorded, and the registry still serves the digest that
// build pushed at that tag. Anything forge cannot prove runs the command.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/buildtarget"
)

// rebuildFlagHelp is --rebuild's help on every command that builds an env.
const rebuildFlagHelp = "Run every ShellBuild's command, even one whose declared inputs are unchanged since its last successful build and whose image is still in the registry"

// shellInputsVerdict is what a ShellBuild's declared inputs say about running
// its command.
type shellInputsVerdict struct {
	// fp is the inputs' fingerprint before the command runs; nil when the
	// build declares no inputs or they could not be fingerprinted. A
	// successful run records it.
	fp *buildtarget.Fingerprint
	// reuse is the state of the last successful build when this one is up
	// to date; nil means run the command.
	reuse *buildtarget.State
	// reason says why the command runs despite declared inputs.
	reason string
	// hashed is the time spent fingerprinting; took adds the state read and
	// the registry check.
	hashed, took time.Duration
}

// externalBuildInputs is the ShellBuild's declared inputs, nil for a build
// that declares none (or is not a ShellBuild).
func externalBuildInputs(svc WorkloadEntity) []string {
	if sh := svc.shell(); sh != nil && len(sh.Inputs) > 0 {
		return sh.Inputs
	}
	return nil
}

// shellBuildFingerprint fingerprints the inputs together with everything
// else the command's output depends on. It is a SUPERSET of the artifact
// identity (externalArtifactIdentity, embedded whole), plus what only an
// execution knows: the pushed reference, the effective environment
// (spec.BuildEnv, which must already carry the built_images overlay, so a
// parent rebuilt to a new digest changes every child's fingerprint), the
// recipe name and output name, and the CONTENT of a declared inputs_file. A
// field that cannot be hashed — an unreadable inputs_file — is an error,
// which refuses reuse and runs the command.
func shellBuildFingerprint(svc WorkloadEntity, spec buildtarget.Spec, cwd, projectDir string, opts buildOptions) (buildtarget.Fingerprint, error) {
	canonical, err := filepath.Abs(cwd)
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	if canonical, err = filepath.EvalSymlinks(canonical); err != nil {
		return buildtarget.Fingerprint{}, err
	}
	env := spec.BuildEnv
	if env == nil {
		env = map[string]string{}
	}
	inputsFileDigest := ""
	if f := svc.Build.InputsFile; f != "" {
		data, err := os.ReadFile(resolveProjectPath(projectDir, f))
		if err != nil {
			return buildtarget.Fingerprint{}, fmt.Errorf("inputs_file: %w", err)
		}
		sum := sha256.Sum256(data)
		inputsFileDigest = "sha256:" + hex.EncodeToString(sum[:])
	}
	outputName := ""
	if sh := svc.shell(); sh != nil {
		outputName = sh.OutputName
	}
	recipe, err := json.Marshal(struct {
		Identity         externalArtifactIdentity `json:"identity"`
		Ref              string                   `json:"ref"`
		Env              map[string]string        `json:"env"`
		Unit             string                   `json:"unit"`
		OutputName       string                   `json:"output_name"`
		InputsFileDigest string                   `json:"inputs_file_digest"`
	}{externalBuildIdentity(svc, canonical, opts), externalPushedRef(svc, spec.Tag, opts.pushPlan.pushBase), env,
		svc.Build.Unit, outputName, inputsFileDigest})
	if err != nil {
		return buildtarget.Fingerprint{}, err
	}
	return buildtarget.FingerprintInputs(shellBuildInputScope(canonical, projectDir), externalBuildInputs(svc), recipe)
}

// shellBuildInputScope is where a ShellBuild's inputs may come from: its cwd,
// and the directory holding the project — the workspace of sibling
// checkouts a ShellBuild's own cwd already reaches (`cwd = "../reliant"`).
// Nothing beyond it is read, and no symlink carries a read out of it.
func shellBuildInputScope(cwd, projectDir string) buildtarget.InputScope {
	scope := buildtarget.InputScope{Cwd: cwd}
	if abs, err := filepath.Abs(projectDir); err == nil {
		scope.Roots = []string{filepath.Dir(abs)}
	}
	return scope
}

// checkShellBuildInputs fingerprints a ShellBuild's declared inputs and
// decides whether its last successful build can stand in for running it.
func checkShellBuildInputs(ctx context.Context, svc WorkloadEntity, spec buildtarget.Spec, cwd string, opts buildOptions, projectDir string) shellInputsVerdict {
	if externalBuildInputs(svc) == nil {
		return shellInputsVerdict{}
	}
	if opts.composition != nil {
		// A release records the builder's receipt, which only a run produces,
		// and its tag is new by construction.
		return shellInputsVerdict{reason: "a release build always runs its command"}
	}
	start := time.Now()
	v := shellInputsVerdict{}
	fp, err := shellBuildFingerprint(svc, spec, cwd, projectDir, opts)
	if err != nil {
		v.reason = fmt.Sprintf("could not fingerprint its inputs (%v)", err)
		v.took = time.Since(start)
		return v
	}
	v.fp = &fp
	v.hashed = time.Since(start)
	if opts.rebuild {
		v.reason = "forced by --rebuild"
		v.took = time.Since(start)
		return v
	}
	prev, err := buildtarget.ReadState(projectDir, opts.env, svc.Name)
	if err != nil {
		v.reason = fmt.Sprintf("its build state is unreadable (%v)", err)
		v.took = time.Since(start)
		return v
	}
	if prev != nil && !buildtarget.StateBelongsTo(prev, opts.env, svc.Name) {
		prev = nil
	}
	repo := externalPushedRepository(svc, opts.pushPlan.pushBase)
	ok, why := shellBuildReusable(ctx, prev, fp.Digest, svc.Image != "", repo, spec.Tag, externalImageDigestResolver)
	if ok {
		v.reuse = prev
	} else {
		v.reason = why
	}
	v.took = time.Since(start)
	return v
}

// shellBuildReusable reports whether prev — the state the last successful
// build of this workload recorded — can stand in for running the command
// again, and if not, why. The registry is consulted last, and only when
// everything recorded already agrees.
func shellBuildReusable(ctx context.Context, prev *buildtarget.State, fingerprint string, hasImage bool, repo, tag string,
	resolve func(context.Context, string) (string, []string, error)) (bool, string) {
	switch {
	case !hasImage:
		return false, "the workload declares no image, so nothing proves the last build's output still exists"
	case prev == nil:
		return false, "no successful build recorded"
	case prev.InputsFingerprint == "":
		return false, "the last successful build recorded no inputs fingerprint"
	case prev.InputsFingerprint != fingerprint:
		return false, "inputs changed since the last successful build (" + shortFingerprint(prev.InputsFingerprint) + " → " + shortFingerprint(fingerprint) + ")"
	case prev.Image != repo || prev.Tag != tag:
		return false, fmt.Sprintf("the last build pushed %s:%s, this one pushes %s:%s", prev.Image, prev.Tag, repo, tag)
	case prev.Digest == "":
		return false, "the last build's pushed digest was never resolved"
	}
	ref := repo + ":" + tag
	digest, _, err := resolve(ctx, ref)
	if err != nil {
		return false, fmt.Sprintf("could not confirm %s is still in the registry (%v)", ref, err)
	}
	if digest != prev.Digest {
		return false, fmt.Sprintf("the registry serves %s at %s, not the %s the last build pushed", digest, ref, prev.Digest)
	}
	return true, ""
}

// shortFingerprint abbreviates a "sha256:<hex>" fingerprint for a log line.
func shortFingerprint(fp string) string {
	hex := strings.TrimPrefix(fp, "sha256:")
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return hex
}

// describeFingerprint is the "<n> files, <size> in <took>" a log line shows,
// so the cost of the check is visible beside what it saved.
func describeFingerprint(fp *buildtarget.Fingerprint, took time.Duration) string {
	if fp == nil {
		return ""
	}
	return fmt.Sprintf("%s, %d files, %.1f MB hashed in %s", shortFingerprint(fp.Digest), fp.Files, float64(fp.Bytes)/(1<<20), took.Truncate(time.Millisecond))
}

// warnSelfModifiedInputs re-fingerprints after a successful run and warns when
// the command changed its own declared inputs. Such a build can never be
// skipped: the next run sees inputs that differ from the ones it recorded.
func warnSelfModifiedInputs(svc WorkloadEntity, spec buildtarget.Spec, cwd, projectDir string, opts buildOptions, before *buildtarget.Fingerprint) {
	if before == nil {
		return
	}
	after, err := shellBuildFingerprint(svc, spec, cwd, projectDir, opts)
	if err != nil || after.Digest == before.Digest {
		return
	}
	changed := after.Changed(*before)
	more := ""
	if len(changed) > 5 {
		more = fmt.Sprintf(" and %d more", len(changed)-5)
		changed = changed[:5]
	}
	fmt.Printf("[build] %s: warning: the command changed its own declared inputs (%s%s), so it will run again next time; "+
		"exclude its outputs from inputs with `!<pattern>`\n", svc.Name, strings.Join(changed, ", "), more)
}
