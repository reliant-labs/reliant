package cli

// `forge release record` — write down a deploy someone else did.
//
// A team whose own CI builds and deploys adds ONE step after its deploy job:
//
//	forge release record --env prod --version "$TAG" \
//	    --workload api --image "ghcr.io/acme/api@$DIGEST"
//
// and forge's monitoring then has what it keys on: a release in the ledger and
// a promotion of it into the env — the deploy marker `obs changes` lists and
// `obs check` judges. forge builds nothing and applies nothing here. The
// release is RECORD-ONLY (release.Release.RecordOnly): it may name no
// artifacts at all (a version and a commit), and it never binds to an env
// forge itself deploys, so a record can never pose as a forge deploy. That is
// enforced twice: the control plane refuses the promotion, and before any cut
// this command refuses an env declared in deploy/kcl/<env>/main.k — an env
// forge's KCL declares is one forge applies, and the machine ledger (or a
// control plane that predates record-only) has nothing else to refuse it.
//
// IT NEEDS NO deploy/kcl. forge.yaml `name:` is the project; the env is
// ensured on the control plane as SELF_MANAGED (forge keeps its ledger, the
// team's pipeline deploys it). Where the record goes:
//
//   - signed in (forge login, FORGE_CONTROL_PLANE_TOKEN, a credential
//     helper), and FORGE_LEDGER is not machine: Reliant cloud;
//   - otherwise: this machine's ledger, with a note saying monitoring cannot
//     see it until it is recorded signed in (`forge ledger import`).
//
// IDEMPOTENT. A CI retry of the same step re-cuts an identical release
// (created=false) and re-promotes the env's current release (a no-op), so it
// records once. The same version with different digests — or, version-only,
// at a different commit — is a conflict, never an overwrite.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// defaultForgeConsole is where the forge console lives for Reliant cloud.
// FORGE_CONSOLE_URL points the printed links at another one (a staging
// console in front of a staging control plane).
const defaultForgeConsole = "https://forge.reliantlabs.io"

// releaseRecordActorCI is the promotion actor a record made inside CI
// carries: a named automation, as release.Actor documents.
const releaseRecordActorCI = "ci"

type releaseRecordOptions struct {
	env       string
	version   string
	commit    string
	workloads []string
	images    []string
	run       runOptions
	token     string
	jsonOut   bool
}

// releaseRecordResult is what `--json` prints, and what the text output says.
type releaseRecordResult struct {
	Project string `json:"project"`
	Env     string `json:"env"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	// Workloads maps each recorded workload to the image it runs
	// (repo@sha256:…). Empty for a version-only record.
	Workloads map[string]string `json:"workloads,omitempty"`
	// Ledger is "hosted" (a control plane) or "machine" (this machine's
	// ledger); Location is the endpoint or directory.
	Ledger   string `json:"ledger"`
	Location string `json:"location"`
	// ReleaseCreated is false when the release was already recorded with the
	// same content (a retry).
	ReleaseCreated bool `json:"release_created"`
	// Promoted is false when the env already ran this release, so nothing
	// was appended.
	Promoted    bool   `json:"promoted"`
	PromotionID string `json:"promotion_id,omitempty"`
	RunURL      string `json:"run_url,omitempty"`
	// ConsoleURL is the release's page in the forge console; empty when the
	// record is not on Reliant cloud.
	ConsoleURL string `json:"console_url,omitempty"`
}

func newReleaseRecordCmd() *cobra.Command {
	var o releaseRecordOptions
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record a deploy your own CI did, so monitoring gets a deploy marker",
		Long: `Record that your own pipeline deployed a version to an environment.

forge builds and applies nothing. It writes a RECORD-ONLY release (the version,
the commit, and the image digest of each workload you name) and promotes it
into the env, which is the deploy marker ` + "`forge obs changes`" + ` lists,
` + "`forge obs check`" + ` judges, and the console's Releases page shows as
"recorded by CI".

No deploy/kcl is needed: forge.yaml's ` + "`name:`" + ` is the project, and the env is
created on the control plane as self-managed on first use. A version alone is
enough (--version, with the commit detected from CI); pass --workload/--image
pairs to record the digests your pipeline shipped.

Where it records: an env with deploy/kcl uses the ledger its KCL selects.
Otherwise, signed in (forge login, or a CI token in FORGE_CONTROL_PLANE_TOKEN
from ` + "`forge cloud token create --scopes deploy:write`" + `) it records on Reliant cloud;
not signed in, on this machine's ledger, which monitoring cannot see.

Idempotent: a CI retry records once. The same version with different digests
(or, version-only, a different commit) is refused as a conflict.

Examples:
  ` + Name() + ` release record --env prod --version v1.4.0
  ` + Name() + ` release record --env prod --version v1.4.0 --commit "$GITHUB_SHA" \
      --workload api --image ghcr.io/acme/api@sha256:<digest> \
      --workload web --image ghcr.io/acme/web@sha256:<digest>
  ` + Name() + ` release record --env staging --version "$CI_COMMIT_TAG" --url "$CI_PIPELINE_URL" --json`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReleaseRecord(cmd.Context(), o, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.env, "env", "", "Environment your pipeline deployed to (required)")
	f.StringVar(&o.version, "version", "", "Version label of what it deployed, e.g. v1.4.0 (required)")
	f.StringVar(&o.commit, "commit", "", "Commit it was built from (default: detected from CI — GITHUB_SHA, CI_COMMIT_SHA, BUILDKITE_COMMIT — or git HEAD)")
	f.StringArrayVar(&o.workloads, "workload", nil, "Workload it deployed; pair each with an --image, in order (repeatable)")
	f.StringArrayVar(&o.images, "image", nil, "Image the matching --workload runs, pinned by digest: <ref>@sha256:<hex> (repeatable)")
	f.StringVar(&o.run.URL, "url", "", "Link to the CI run (default: detected from the CI environment)")
	f.StringVar(&o.run.ID, "run-id", "", "CI run id, stable across retries (default: detected, or the --url)")
	f.BoolVar(&o.run.None, "no-run", false, "Record no CI run, even inside CI")
	f.StringVar(&o.token, "token", "", "Credential for the control plane, ahead of the env var and the credentials file")
	f.BoolVar(&o.jsonOut, "json", false, "Emit machine-readable JSON")
	_ = cmd.MarkFlagRequired("env")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

// recordLedger is where a record goes, and how to describe it.
type recordLedger struct {
	ledger envLedger
	// endpoint is the control plane's URL for a hosted ledger, "" for the
	// machine ledger.
	endpoint string
	// note, when set, is printed to stderr: why the record went where it did.
	note string
}

// openRecordLedger selects the ledger a record goes to. A seam: tests point it
// at a fake control plane or a temporary machine ledger.
var openRecordLedger = defaultRecordLedger

func defaultRecordLedger(ctx context.Context, projectDir, project, env, token string) (recordLedger, error) {
	selection, err := ledgerSelectionFromEnv()
	if err != nil {
		return recordLedger{}, err
	}
	if selection == ledgerSelectMachine {
		l, err := machineLedger(projectDir)
		return recordLedger{ledger: l}, err
	}
	ep, err := cloud.ResolveEndpoint(env, &cloud.Declaration{Endpoint: cloud.DefaultEndpoint})
	if err != nil {
		return recordLedger{}, err
	}
	ep.ProjectName = project
	cred, cerr := cloud.ResolveCredential(token, ep)
	if cerr != nil {
		if strings.TrimSpace(token) != "" {
			return recordLedger{}, cerr
		}
		l, err := machineLedger(projectDir)
		return recordLedger{ledger: l, note: fmt.Sprintf(
			"note: not signed in to %s (%v), so this is recorded on this machine only and monitoring cannot see it. "+
				"Sign in with `%s login`, or set %s to a token from `%s cloud token create --scopes deploy:write`, "+
				"then re-run, or move this machine's records with `%s ledger import --from-file-ledger`.",
			ep.URL, cerr, Name(), ep.TokenEnv, Name(), Name())}, err
	}
	return recordLedger{
		ledger:   hostedLedger(cloud.NewClient(ep, cred), ep.URL, project, deploytarget.HostedEnvSelfManaged),
		endpoint: ep.URL,
	}, nil
}

// reasonRecordOnlyRelease is the control plane's refusal of a record-only
// release bound to an env forge deploys; the client uses the same reason.
const reasonRecordOnlyRelease = "record_only_release"

// refuseForgeDeclaredEnv refuses, before anything is cut, a record into an env
// forge's own KCL declares: forge applies that env, so a record-only release
// must not become its current promotion, on any ledger. The control plane
// refuses it too, but the machine ledger has no such check, and a control
// plane that predates record-only would accept it.
func refuseForgeDeclaredEnv(projectDir, env string) error {
	decl := filepath.Join("deploy", "kcl", env, "main.k")
	if _, err := os.Stat(filepath.Join(projectDir, decl)); err != nil {
		return nil //nolint:nilerr // no declaration: an env forge does not deploy
	}
	return &promoteRefusedError{
		Reason: reasonRecordOnlyRelease,
		Detail: fmt.Sprintf("environment %q is declared in %s, so forge deploys it; `%s release record` records "+
			"deploys an outside pipeline makes. Deploy it with `%s env deploy %s`, or record into an env forge does not declare",
			env, decl, Name(), Name(), env),
	}
}

func runReleaseRecord(ctx context.Context, o releaseRecordOptions, out, errOut io.Writer) error {
	if o.jsonOut {
		defer reserveStdoutForJSON()()
	}
	res, err := recordRelease(ctx, o, errOut)
	if err != nil {
		return err
	}
	if o.jsonOut {
		return writeJSONDocument(out, res)
	}
	printReleaseRecord(out, res)
	return nil
}

func recordRelease(ctx context.Context, o releaseRecordOptions, errOut io.Writer) (releaseRecordResult, error) {
	cfgPath, err := findProjectConfigFile()
	if err != nil {
		return releaseRecordResult{}, fmt.Errorf("forge release record needs a forge.yaml with the project's `name:` "+
			"(the project its releases are filed under): %w", err)
	}
	project := hostedProjectName()
	if project == "" {
		return releaseRecordResult{}, fmt.Errorf("%s has no `name:`; add one — it is the project this release is filed under", cfgPath)
	}
	return recordReleaseIn(ctx, o, recordScope{projectDir: filepath.Dir(cfgPath), project: project, getenv: os.Getenv}, errOut)
}

// recordScope is what a record reads from its surroundings: the project and
// the process environment (CI detection). A struct so a test states it.
type recordScope struct {
	projectDir string
	project    string
	getenv     func(string) string
}

func recordReleaseIn(ctx context.Context, o releaseRecordOptions, sc recordScope, errOut io.Writer) (releaseRecordResult, error) {
	env := strings.TrimSpace(o.env)
	version := strings.TrimSpace(o.version)
	if env == "" || version == "" {
		return releaseRecordResult{}, errors.New("--env and --version are required")
	}
	artifacts, workloads, err := recordArtifacts(o.workloads, o.images)
	if err != nil {
		return releaseRecordResult{}, err
	}
	commit, err := recordCommit(o.commit, sc.getenv)
	if err != nil {
		return releaseRecordResult{}, err
	}
	if commit == "" {
		commit = recordGitHead(sc.projectDir)
	}
	run, err := recordRun(o.run, sc.getenv)
	if err != nil {
		return releaseRecordResult{}, err
	}
	project := sc.project
	if err := refuseForgeDeclaredEnv(sc.projectDir, env); err != nil {
		return releaseRecordResult{}, err
	}

	target, err := openRecordLedger(ctx, sc.projectDir, project, env, o.token)
	if err != nil {
		return releaseRecordResult{}, err
	}
	if target.note != "" {
		fmt.Fprintln(errOut, target.note)
	}

	rel := release.Release{
		Version:    version,
		Project:    project,
		Git:        release.Git{Commit: commit},
		CreatedAt:  time.Now().UTC(),
		Artifacts:  artifacts,
		Run:        run,
		RecordOnly: true,
	}
	if err := rel.Validate(); err != nil {
		return releaseRecordResult{}, err
	}
	created, err := target.ledger.Releases.Cut(ctx, rel)
	if err != nil {
		if errors.Is(err, release.ErrReleaseConflict) {
			return releaseRecordResult{}, fmt.Errorf("version %s is already recorded with different content "+
				"(other digests, or — version-only — another commit); a version names one release, so record this one under a new version: %w",
				version, err)
		}
		return releaseRecordResult{}, fmt.Errorf("record release %s: %w", version, err)
	}

	before, bound, err := target.ledger.Bindings.Current(ctx, env)
	if err != nil {
		return releaseRecordResult{}, fmt.Errorf("read env %s's current release: %w", env, err)
	}
	p := release.NewPromotion(env, rel, release.KindPromote)
	p.Run = run
	if runFromEnvironment(sc.getenv) != (release.Run{}) || sc.getenv("CI") != "" {
		p.PromotedBy.Actor = releaseRecordActorCI
	}
	written, err := target.ledger.Bindings.Append(ctx, p, appendGuard{})
	if err != nil {
		return releaseRecordResult{}, fmt.Errorf("record %s as deployed to %s: %w", version, env, err)
	}

	res := releaseRecordResult{
		Project:        project,
		Env:            env,
		Version:        version,
		Commit:         commit,
		Workloads:      workloads,
		Ledger:         "machine",
		Location:       target.ledger.Bindings.Location(),
		ReleaseCreated: created,
		Promoted:       !bound || before.ID != written.ID,
		PromotionID:    written.ID,
		RunURL:         run.URL,
	}
	if target.ledger.Hosted {
		res.Ledger = "hosted"
		res.ConsoleURL = releaseConsoleURL(target.endpoint, project, version)
	}
	return res, nil
}

func printReleaseRecord(w io.Writer, r releaseRecordResult) {
	what := "recorded"
	if !r.ReleaseCreated && !r.Promoted {
		what = "already recorded"
	}
	fmt.Fprintf(w, "✓ %s %s as deployed to %s (%s ledger: %s)\n", what, r.Version, r.Env, r.Ledger, r.Location)
	if r.Commit != "" {
		fmt.Fprintf(w, "  commit   %s\n", r.Commit)
	}
	names := make([]string, 0, len(r.Workloads))
	for name := range r.Workloads {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-8s %s\n", name, r.Workloads[name])
	}
	if len(names) == 0 {
		fmt.Fprintln(w, "  version-only: no image digests recorded")
	}
	if r.RunURL != "" {
		fmt.Fprintf(w, "  run      %s\n", r.RunURL)
	}
	if r.ConsoleURL != "" {
		fmt.Fprintf(w, "  console  %s\n", r.ConsoleURL)
	}
	if r.Ledger == "hosted" {
		fmt.Fprintf(w, "next: %s obs check %s --release %s --wait   # did this deploy make things worse?\n", Name(), r.Env, r.Version)
	}
}

// imageDigestRef is <ref>@sha256:<64 hex>: the only image reference a record
// accepts, because a tag names whatever was pushed last and a marker must name
// the bytes that were deployed.
var imageDigestRef = regexp.MustCompile(`^([^@\s]+)@(sha256:[0-9a-f]{64})$`)

// recordArtifacts pairs --workload with --image, in order, into the release's
// artifact set: one shared OCI image per workload, pinned by digest, with the
// repository kept as the artifact's URI.
func recordArtifacts(workloads, images []string) (map[string]release.Artifact, map[string]string, error) {
	if len(workloads) != len(images) {
		return nil, nil, fmt.Errorf("--workload and --image pair up in order: got %d --workload and %d --image", len(workloads), len(images))
	}
	if len(workloads) == 0 {
		return nil, nil, nil
	}
	artifacts := make(map[string]release.Artifact, len(workloads))
	named := make(map[string]string, len(workloads))
	for i, name := range workloads {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, nil, errors.New("--workload must not be empty")
		}
		if _, dup := artifacts[name]; dup {
			return nil, nil, fmt.Errorf("--workload %s is named twice", name)
		}
		m := imageDigestRef.FindStringSubmatch(strings.TrimSpace(images[i]))
		if m == nil {
			return nil, nil, fmt.Errorf("--image %q for workload %s must be pinned by digest: <ref>@sha256:<64 hex> "+
				"(a tag names whatever was pushed last, not what you deployed)", images[i], name)
		}
		artifacts[name] = release.Artifact{
			Kind:    release.KindOCI,
			Mode:    release.ModeShared,
			Digests: map[string]string{release.SharedVariant: m[2]},
			URI:     m[1],
		}
		named[name] = m[0]
	}
	return artifacts, named, nil
}

// commitPattern is a full or abbreviated git object name.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// recordCommit is --commit, else the commit the CI environment names. ""
// means none was given or detected; the caller then asks git.
func recordCommit(flag string, getenv func(string) string) (string, error) {
	if c := strings.ToLower(strings.TrimSpace(flag)); c != "" {
		if !commitPattern.MatchString(c) {
			return "", fmt.Errorf("--commit %q is not a git commit id (7–64 hex characters)", flag)
		}
		return c, nil
	}
	for _, key := range []string{"GITHUB_SHA", "CI_COMMIT_SHA", "BUILDKITE_COMMIT"} {
		if c := strings.ToLower(strings.TrimSpace(getenv(key))); commitPattern.MatchString(c) {
			return c, nil
		}
	}
	return "", nil
}

// recordGitHead is the checkout's full HEAD, or "" when dir is not a git checkout.
// Best-effort: a record without a commit is still a record.
func recordGitHead(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	raw, err := cmd.Output()
	if err != nil {
		return ""
	}
	if c := strings.TrimSpace(string(raw)); commitPattern.MatchString(c) {
		return c
	}
	return ""
}

// recordRun is the CI run a record belongs to: the run the environment names
// (GitHub Actions, GitLab CI, Buildkite), with --url/--run-id over it. A --url
// with no run id anywhere uses the url as the id: it is what a retry of the
// same run sends again, which is all a run id has to be.
func recordRun(o runOptions, getenv func(string) string) (release.Run, error) {
	if !o.None && o.ID == "" && o.URL != "" && runFromEnvironment(getenv) == (release.Run{}) {
		o.ID = o.URL
	}
	return o.resolve(getenv)
}

// releaseConsoleURL is the release's page in the forge console, for a record
// on Reliant cloud (or on another control plane when FORGE_CONSOLE_URL names
// its console). "" when no console is known for the endpoint.
func releaseConsoleURL(endpoint, project, version string) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("FORGE_CONSOLE_URL")), "/")
	if base == "" {
		if strings.TrimRight(endpoint, "/") != cloud.DefaultEndpoint {
			return ""
		}
		base = defaultForgeConsole
	}
	return base + "/forge/p/" + url.PathEscape(project) + "/releases/" + url.PathEscape(version)
}
