package cli

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// SOURCE_DATE_EPOCH — the timestamp every docker build forge runs is stamped
// with, so the same inputs build the same image digest.
//
// WHY. An image's config carries a `created` time and one per history entry.
// Left alone, BuildKit writes the wall clock, so a rebuild of byte-identical
// inputs on a cold cache (a fresh CI runner, a pruned host, a release cut)
// produces a NEW digest. The deploy ledger binds digests, so every such cut
// rolled the workload for nothing — the prod otel-gateway rolled on each cold
// release cut with no change to tools/otelcol-reliant.
//
// WHAT. BuildKit treats the SOURCE_DATE_EPOCH build arg specially (0.11+): it
// stamps the config's `created` and every history entry with it. forge sets it
// to the commit time of the LAST CHANGE TO THE BUILD'S INPUTS — `git log -1
// --format=%ct -- <context> <dockerfile>` — not of HEAD, because HEAD's time
// moves on every commit and would change the digest of an image whose inputs
// did not. An unrelated commit therefore leaves the digest alone.
//
// PROVENANCE. On the containerd image store (docker 29's default on a fresh
// install, and every non-docker buildx driver) buildx attaches a provenance
// attestation by default, and its build start/finish times are NOT clamped by
// SOURCE_DATE_EPOCH: two otherwise byte-identical builds get different index
// digests (measured on docker 29.9.2, 2026-10-25). forge therefore passes
// --provenance=false. Nothing forge ships reads that attestation (forge's own
// build receipt is what a release records), and the classic store never
// produced one, so this makes both stores build the same thing.
//
// WHAT IT DOES NOT DO. On docker's classic image store (the default docker
// driver without the containerd snapshotter) BuildKit cannot rewrite FILE
// mtimes inside layers (`rewrite-timestamp` needs the containerd store). A
// layer that copies a file built in an earlier stage keeps that file's mtime,
// the build's wall clock — so a Dockerfile clamps the files its final stage
// copies (`touch -d "@${SOURCE_DATE_EPOCH}" …`). tools/otelcol-reliant in
// control-plane is the worked example.
//
// PRECEDENCE, so the 20% stay in charge: a DockerBuild's own build_args entry
// wins, then $SOURCE_DATE_EPOCH in forge's environment (the convention every
// reproducible-builds tool honours), then the git commit time. Outside a git
// checkout, or for inputs no commit touched, forge passes nothing and the
// Dockerfile's own `ARG SOURCE_DATE_EPOCH=<default>` (if any) applies.

// sourceDateEpochArg is the build arg BuildKit reads.
const sourceDateEpochArg = "SOURCE_DATE_EPOCH"

// gitInputsCommitTime runs `git log -1 --format=%ct -- <inputs>` in dir. A var
// so tests can pin it without a repository.
var gitInputsCommitTime = func(ctx context.Context, dir string, inputs []string) (string, error) {
	args := append([]string{"log", "-1", "--format=%ct", "--"}, inputs...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveSourceDateEpoch is the SOURCE_DATE_EPOCH for a build whose inputs are
// the given paths (relative to dir), or "" when there is none to pass. See the
// file comment for the precedence; the DockerBuild build_args case is handled
// by appendSourceDateEpoch, which never overrides an explicit arg.
func resolveSourceDateEpoch(ctx context.Context, dir string, inputs ...string) string {
	if v := strings.TrimSpace(os.Getenv(sourceDateEpochArg)); isEpoch(v) { //nolint:forbidigo // SOURCE_DATE_EPOCH is a cross-tool convention read from the caller's environment by design, like buildx itself does.
		return v
	}
	var paths []string
	for _, p := range inputs {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	v, err := gitInputsCommitTime(ctx, dir, paths)
	if err != nil || !isEpoch(v) {
		return ""
	}
	return v
}

// appendSourceDateEpoch makes a docker argv reproducible: right after its
// leading "build" it inserts `--provenance=false` (unless the argv already says
// --provenance or --attest) and `--build-arg SOURCE_DATE_EPOCH=<epoch>` (unless
// epoch is empty or the argv already passes the arg — a DockerBuild's
// build_args entry is the user's explicit choice and wins).
func appendSourceDateEpoch(dockerArgs []string, epoch string) []string {
	if len(dockerArgs) == 0 {
		return dockerArgs
	}
	var extra []string
	if !hasFlag(dockerArgs, "--provenance") && !hasFlag(dockerArgs, "--attest") {
		extra = append(extra, "--provenance=false")
	}
	if epoch != "" && !hasBuildArg(dockerArgs, sourceDateEpochArg) {
		extra = append(extra, "--build-arg", sourceDateEpochArg+"="+epoch)
	}
	if len(extra) == 0 {
		return dockerArgs
	}
	out := make([]string, 0, len(dockerArgs)+len(extra))
	out = append(out, dockerArgs[0])
	out = append(out, extra...)
	return append(out, dockerArgs[1:]...)
}

// hasFlag reports whether argv carries flag, as `flag`, `flag=v` or `flag v`.
func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// hasBuildArg reports whether argv already passes --build-arg name=… (either
// the two-token or the --build-arg=name=… spelling).
func hasBuildArg(argv []string, name string) bool {
	for i, a := range argv {
		v, ok := "", false
		switch {
		case a == "--build-arg" && i+1 < len(argv):
			v, ok = argv[i+1], true
		case strings.HasPrefix(a, "--build-arg="):
			v, ok = strings.TrimPrefix(a, "--build-arg="), true
		}
		if ok && (v == name || strings.HasPrefix(v, name+"=")) {
			return true
		}
	}
	return false
}

// isEpoch reports whether s is a non-negative integer number of seconds.
func isEpoch(s string) bool {
	if s == "" {
		return false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n >= 0
}
